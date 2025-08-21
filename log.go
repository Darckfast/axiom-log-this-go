package logthis

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	slogFields   string = "slog_fields"
	messageKey   string = "msg"
	levelKey     string = "level"
	timestampKey string = "timestamp"
)

type DoReq = func(req *http.Request) (*http.Response, error)

var (
	letters = []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	Do      DoReq
	wg      = &sync.WaitGroup{}
)

type Handler struct {
	startedAt time.Time
	slog.Handler
	l *log.Logger
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs())

	fields[messageKey] = record.Message
	fields[levelKey] = record.Level.String()
	fields[timestampKey] = record.Time.UnixMicro()

	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = attr.Value.Any()
		return true
	})

	if attrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		for _, attr := range attrs {
			fields[attr.Key] = attr.Value.Any()
		}
	}

	startedAt, ok := fields["startedAt"].(time.Time)
	if ok {
		duration := time.Since(startedAt).Nanoseconds()
		fields["duration"] = duration
	}

	jsonBytes, _ := json.Marshal(fields)
	h.l.Println(string(jsonBytes))
	body, _ := json.Marshal([]any{fields})

	if os.Getenv("AXIOM_API_KEY") == "" {
		log.Println("no axiom api key found, not sending logs")
	} else {
		sendLogOverHTTP(ctx, &body)
	}

	return nil
}

func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func sendLogOverHTTP(ctx context.Context, body *[]byte) {
	wg.Add(1)

	req, _ := http.NewRequestWithContext(ctx,
		"POST",
		"https://api.axiom.co/v1/datasets/main/ingest",
		bytes.NewBuffer(*body),
	)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+os.Getenv("AXIOM_API_KEY"))

	go func() {
		defer wg.Done()
		rs, err := Do(req)

		if err != nil {
			log.Println("error sending logs over http", err.Error())
		} else if rs.StatusCode > 399 {
			log.Println("axiom returned non 200 status", rs.StatusCode)
		}
	}()
}

func NewLogger(c DoReq) *slog.Logger {
	h := &Handler{
		startedAt: time.Now(),
		Handler:   slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}),
		l:         log.New(os.Stdout, "", 0),
	}

	Do = c
	return slog.New(h)
}

func AppendCtx(parent context.Context, attr slog.Attr) context.Context {
	if parent == nil {
		parent = context.Background()
	}

	if v, ok := parent.Value(slogFields).([]slog.Attr); ok {
		v = append(v, attr)
		return context.WithValue(parent, slogFields, v)
	}

	v := []slog.Attr{}
	v = append(v, attr)
	return context.WithValue(parent, slogFields, v)
}

func Flush() {
	wg.Wait()
}

func FromRequest(r *http.Request) (*http.Request, context.Context) {
	id := randSeq(24)
	ctx := AppendCtx(context.Background(), slog.String("id", id))
	ctx = AppendCtx(ctx, slog.String("service", os.Getenv("SERVICE_NAME")))
	ctx = AppendCtx(ctx, slog.Time("startedAt", time.Now()))

	if r != nil {
		ctx = AppendCtx(ctx, slog.String("query", r.URL.RawQuery))
		ctx = AppendCtx(ctx, slog.String("user-agent", r.UserAgent()))
		ctx = AppendCtx(ctx, slog.String("ip", r.RemoteAddr))
		ctx = AppendCtx(ctx, slog.String("host", r.Host))
		ctx = AppendCtx(ctx, slog.String("method", r.Method))

		requestIp := r.Header.Get("X-Forwarded-For")
		connectingIp := r.Header.Get("CF-Connecting-IP")
		if requestIp != "" && connectingIp != "" {
			requestIp += ","
		}
		requestIp += connectingIp

		ctx = AppendCtx(ctx, slog.String("x-forwarded-for", requestIp))
		ctx = AppendCtx(ctx, slog.String("country", r.Header.Get("CF-IPCountry")))
		ctx = AppendCtx(ctx, slog.String("content-type", r.Header.Get("content-type")))
		ctx = AppendCtx(ctx, slog.String("path", r.URL.Path))
		r = r.WithContext(ctx)
	}

	return r, ctx
}
