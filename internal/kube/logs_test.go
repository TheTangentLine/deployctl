package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"k8s.io/client-go/rest"
)

func TestLogsParameters(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/demo/pods/backend/log" || q.Get("container") != "sidecar" || q.Get("tailLines") != "50" || q.Get("follow") == "true" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if q.Get("previous") == "true" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","code":404,"reason":"NotFound"}`)
			return
		}
		_, _ = io.WriteString(w, "line 1\nline 2\n")
	}))
	defer server.Close()
	c, err := NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	text, err := c.Logs(context.Background(), "demo", "backend", "sidecar", false, 50)
	if err != nil || text != "line 1\nline 2\n" {
		t.Fatalf("logs = %q, %v", text, err)
	}
	_, err = c.Logs(context.Background(), "demo", "backend", "sidecar", true, 50)
	if !errors.Is(err, steps.ErrNotFound) || calls.Load() != 2 {
		t.Fatalf("previous logs error=%v; calls=%d (must not fall back)", err, calls.Load())
	}
	for _, tc := range []struct {
		ns, pod, container string
		lines              int64
	}{
		{"", "pod", "c", 50}, {"demo", "", "c", 50}, {"demo", "pod", " ", 50}, {"demo", "pod", "c", 0}, {"demo", "pod", "c", -1},
	} {
		if _, err := c.Logs(context.Background(), tc.ns, tc.pod, tc.container, false, tc.lines); err == nil || steps.IsRetryable(err) {
			t.Fatalf("invalid input %+v: %v", tc, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid inputs reached API")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct {
	read   func([]byte) (int, error)
	closed bool
	bytes  int
}

func (b *observedBody) Read(p []byte) (int, error) { n, err := b.read(p); b.bytes += n; return n, err }
func (b *observedBody) Close() error               { b.closed = true; return nil }

func TestLogsStreamLifecycle(t *testing.T) {
	for _, mode := range []string{"ok", "limit", "read error", "client timeout", "caller deadline", "caller cancel"} {
		t.Run(mode, func(t *testing.T) {
			body := &observedBody{}
			ctx := context.Background()
			cancel := func() {}
			if mode == "caller deadline" {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
			}
			if mode == "caller cancel" {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			cfg := &rest.Config{Host: "https://cluster.example.com", Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				text := "logs\n"
				if mode == "limit" {
					text = strings.Repeat("a", logReadLimit+1000)
				}
				reader := strings.NewReader(text)
				body.read = reader.Read
				if mode == "read error" {
					body.read = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
				}
				if mode == "client timeout" || mode == "caller deadline" || mode == "caller cancel" {
					body.read = func([]byte) (int, error) {
						if mode == "caller cancel" {
							cancel()
						}
						<-r.Context().Done()
						return 0, r.Context().Err()
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: body}, nil
			})}
			c, err := NewForConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.timeout = 60 * time.Millisecond
			text, err := c.Logs(ctx, "demo", "pod", "container", true, 50)
			if !body.closed {
				t.Fatal("log stream was not closed")
			}
			switch mode {
			case "ok":
				if err != nil || text != "logs\n" {
					t.Fatalf("logs=%q, %v", text, err)
				}
			case "limit":
				if err != nil || len(text) != logReadLimit || body.bytes != logReadLimit {
					t.Fatalf("bytes=%d, read=%d, %v", len(text), body.bytes, err)
				}
			case "read error":
				if !errors.Is(err, io.ErrUnexpectedEOF) || !steps.IsRetryable(err) {
					t.Fatalf("read error=%v", err)
				}
			case "client timeout":
				if !errors.Is(err, context.DeadlineExceeded) || !steps.IsRetryable(err) {
					t.Fatalf("timeout=%v", err)
				}
			default:
				if !errors.Is(err, ctx.Err()) || steps.IsRetryable(err) {
					t.Fatalf("caller context=%v", err)
				}
			}
		})
	}
}
