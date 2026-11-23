package argocd

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, token string
		valid                bool
	}{
		{"https", "https://argocd.example.com", "token", true},
		{"http", "http://localhost:8080", "token", true},
		{"base path", "https://example.com/argo%20cd/", "token", true},
		{"empty URL", "", "token", false},
		{"missing scheme", "argocd.example.com:443", "token", false},
		{"missing host", "https:///argocd", "token", false},
		{"unsupported scheme", "ftp://example.com", "token", false},
		{"invalid escape", "https://example.com/%zz", "token", false},
		{"invalid port", "https://example.com:invalid", "token", false},
		{"credentials", "https://user:password@example.com", "token", false},
		{"query", "https://example.com?project=default", "token", false},
		{"empty query", "https://example.com?", "token", false},
		{"fragment", "https://example.com#part", "token", false},
		{"empty fragment", "https://example.com#", "token", false},
		{"empty token", "https://example.com", "", false},
		{"spaces in token", "https://example.com", " token ", false},
		{"newline in token", "https://example.com", "token\r\nX-Test: value", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(tc.baseURL, tc.token, nil)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if client.httpClient.Timeout != 30*time.Second {
					t.Fatalf("default timeout = %v", client.httpClient.Timeout)
				}
			} else if err == nil || client != nil {
				t.Fatalf("New returned (%v, %v), want nil client and error", client, err)
			}
		})
	}
}

func TestJSONRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/argo%20cd/api/v1/applications/app%2Fwith%20space/sync" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.URL.Query().Get("revision") != "sha+value&other" || len(r.URL.Query()) != 1 {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected authentication or JSON headers")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != `{"revision":"commit-sha"}` {
			t.Errorf("request body = %q, error = %v", data, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":"accepted"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/argo%20cd/", server.Client())
	var got struct{ Message string }
	err := client.doJSON(context.Background(), http.MethodPost,
		[]string{"applications", "app/with space", "sync"},
		url.Values{"revision": {"sha+value&other"}},
		map[string]string{"revision": "commit-sha"}, &got)
	if err != nil || got.Message != "accepted" || calls.Load() != 1 {
		t.Fatalf("response = %+v, error = %v, calls = %d", got, err, calls.Load())
	}
}

func TestHTTPResponseErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		message   string
		retryable bool
		notFound  bool
	}{
		{"not found", 404, `{"code":5,"message":"application missing"}`, "application missing", false, true},
		{"unauthorized", 401, `{"message":"invalid token"}`, "invalid token", false, false},
		{"forbidden", 403, `{"message":"permission denied"}`, "permission denied", false, false},
		{"bad request", 400, `{"message":"invalid revision"}`, "invalid revision", false, false},
		{"conflict", 409, `{"message":"conflict"}`, "conflict", false, false},
		{"rate limit", 429, `{"message":"too many requests"}`, "too many requests", true, false},
		{"server error", 500, `{"message":"internal failure"}`, "internal failure", true, false},
		{"gateway", 502, "upstream unavailable", "upstream unavailable", true, false},
		{"unavailable", 503, "", "Service Unavailable", true, false},
		{"gateway timeout", 504, "", "Gateway Timeout", true, false},
		{"malformed error JSON", 400, `{"message":`, `{"message":`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			err := client.doJSON(context.Background(), http.MethodPost, []string{"applications", "app", "sync"}, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want message %q", err, tc.message)
			}
			if steps.IsRetryable(err) != tc.retryable || errors.Is(err, steps.ErrNotFound) != tc.notFound {
				t.Fatalf("error = %v, retryable = %v, not found = %v", err, steps.IsRetryable(err), errors.Is(err, steps.ErrNotFound))
			}
			var responseErr *responseError
			if !errors.As(err, &responseErr) || responseErr.statusCode != tc.status {
				t.Fatalf("error lost HTTP status: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("request was retried: %d calls", calls.Load())
			}
		})
	}
}

func TestResponseJSONAndBodyClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		decode bool
		fail   bool
	}{
		{"valid", 200, `{"message":"ok"}`, true, false},
		{"trailing whitespace", 200, "{\"message\":\"ok\"}\n ", true, false},
		{"invalid JSON", 200, "not JSON", true, true},
		{"incomplete JSON", 200, `{"message":`, true, true},
		{"empty JSON", 200, "", true, true},
		{"wrong type", 200, `{"message":42}`, true, true},
		{"multiple JSON values", 200, `{} {}`, true, true},
		{"trailing junk", 200, `{} invalid`, true, true},
		{"discard body", 200, `{"message":"ok"}`, false, false},
		{"no content", 204, "", false, false},
		{"API error", 403, `{"message":"forbidden"}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			client := newTestClient(t, "https://argocd.example.com", &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Content-Type") != "" || r.URL.RawQuery != "" {
						t.Error("bodyless request has Content-Type or query")
					}
					return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
				}),
			})
			var out any
			if tc.decode {
				out = &struct{ Message string }{}
			}
			err := client.doJSON(context.Background(), http.MethodGet, []string{"applications", "app"}, nil, nil, out)
			if (err != nil) != tc.fail || steps.IsRetryable(err) {
				t.Fatalf("unexpected error: %v", err)
			}
			if !body.closed {
				t.Error("response body was not closed")
			}
		})
	}
}

func TestRequestCancellationAndDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name          string
		clientTimeout time.Duration
		cancel        bool
		deadline      bool
		retryable     bool
	}{
		{"caller cancellation", 0, true, false, false},
		{"caller deadline", 0, false, true, false},
		{"client timeout", 20 * time.Millisecond, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			calls := 0
			httpClient := &http.Client{Timeout: tc.clientTimeout}
			httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > 30*time.Second {
					t.Error("request has no 30-second deadline cap")
				}
				if tc.cancel {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			client := newTestClient(t, "https://argocd.example.com", httpClient)
			err := client.doJSON(ctx, http.MethodGet, []string{"applications", "app"}, nil, nil, nil)
			want := context.DeadlineExceeded
			if tc.cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || steps.IsRetryable(err) != tc.retryable || calls != 1 {
				t.Fatalf("error = %v, retryable = %v, calls = %d", err, steps.IsRetryable(err), calls)
			}
			if httpClient.Timeout != tc.clientTimeout || httpClient.CheckRedirect != nil {
				t.Error("constructor mutated the supplied HTTP client")
			}
		})
	}
}

func TestRequestsFailBeforeTransport(t *testing.T) {
	client := newTestClient(t, "https://argocd.example.com", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Error("unexpected HTTP call")
			return nil, errors.New("unexpected HTTP call")
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.doJSON(ctx, http.MethodGet, []string{"applications", "app"}, nil, nil, nil)
	if !errors.Is(err, context.Canceled) || steps.IsRetryable(err) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	err = client.doJSON(context.Background(), http.MethodPost, []string{"applications"}, nil, make(chan int), nil)
	if err == nil || steps.IsRetryable(err) {
		t.Fatalf("unexpected request encoding error: %v", err)
	}
}

func TestTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"connection closed", io.EOF, true},
		{"temporary DNS failure", &net.DNSError{Err: "temporary failure", IsTemporary: true}, true},
		{"DNS timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true},
		{"unknown hostname", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"invalid certificate", x509.UnknownAuthorityError{}, false},
		{"other failure", errors.New("invalid transport configuration"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newTestClient(t, "https://argocd.example.com", &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					return nil, tc.err
				}),
			})
			err := client.doJSON(context.Background(), http.MethodPost, []string{"applications", "app", "sync"}, nil, nil, nil)
			if !errors.Is(err, tc.err) || steps.IsRetryable(err) != tc.retryable || calls != 1 {
				t.Fatalf("error = %v, retryable = %v, calls = %d", err, steps.IsRetryable(err), calls)
			}
		})
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/another-endpoint", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, server.Client())
	err := client.doJSON(context.Background(), http.MethodPost, []string{"applications", "app", "sync"}, nil, map[string]string{"revision": "sha"}, nil)
	if err == nil || steps.IsRetryable(err) || calls.Load() != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls.Load())
	}
}

func TestTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	client := newTestClient(t, server.URL, nil)
	err := client.doJSON(context.Background(), http.MethodGet, []string{"applications", "app"}, nil, nil, nil)
	if err == nil || steps.IsRetryable(err) {
		t.Fatalf("untrusted TLS certificate should be a permanent error: %v", err)
	}
	client = newTestClient(t, server.URL, server.Client())
	if err := client.doJSON(context.Background(), http.MethodGet, []string{"applications", "app"}, nil, nil, nil); err != nil {
		t.Fatalf("configured TLS trust failed: %v", err)
	}
}

func newTestClient(t *testing.T, baseURL string, httpClient *http.Client) *Client {
	t.Helper()
	client, err := New(baseURL, "test-token", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}
