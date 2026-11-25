package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestSyncRequest(t *testing.T) {
	for _, tc := range []struct {
		name, basePath, revision string
	}{
		{"SHA", "", strings.Repeat("a", 40)},
		{"branch", "", "refs/heads/feature/backend"},
		{"base path", "/argo%20cd/", strings.Repeat("b", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantPath := strings.TrimRight(tc.basePath, "/") + "/api/v1/applications/backend-prod/sync"
				if r.Method != http.MethodPost || r.URL.EscapedPath() != wantPath || r.URL.RawQuery != "" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				if len(body) != 1 || body["revision"] != tc.revision {
					t.Errorf("body = %+v, want only revision %q", body, tc.revision)
				}
				_, _ = io.WriteString(w, `{"status":{"health":{"status":"Progressing"}}}`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL+tc.basePath, server.Client())
			err := client.Sync(context.Background(), "backend-prod", tc.revision)
			if err != nil || calls.Load() != 1 {
				t.Fatalf("error = %v, calls = %d; want accepted sync and one POST", err, calls.Load())
			}
		})
	}
}

func TestSyncErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body          string
		status              int
		retryable, notFound bool
	}{
		{"busy", `{"code":9,"message":"another operation is already in progress"}`, 400, true, false},
		{"other precondition", `{"code":9,"message":"sync window is closed"}`, 400, false, false},
		{"missing code", `{"message":"another operation is already in progress"}`, 400, false, false},
		{"wrong code", `{"code":10,"message":"another operation is already in progress"}`, 400, false, false},
		{"invalid code type", `{"code":"9","message":"another operation is already in progress"}`, 400, false, false},
		{"different message", `{"code":9,"message":"another operation is already in progress elsewhere"}`, 400, false, false},
		{"plain text busy", "another operation is already in progress", 400, false, false},
		{"wrong status", `{"code":9,"message":"another operation is already in progress"}`, 409, false, false},
		{"forbidden busy message", `{"code":9,"message":"another operation is already in progress"}`, 403, false, false},
		{"not found", `{"code":5,"message":"application missing"}`, 404, false, true},
		{"unauthorized", `{"code":16,"message":"invalid token"}`, 401, false, false},
		{"forbidden", `{"code":7,"message":"permission denied"}`, 403, false, false},
		{"rate limit", `{"message":"too many requests"}`, 429, true, false},
		{"server error", `{"message":"internal failure"}`, 500, true, false},
		{"unavailable", `{"message":"unavailable"}`, 503, true, false},
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
			err := client.Sync(context.Background(), "backend-prod", "target-sha")
			if err == nil || steps.IsRetryable(err) != tc.retryable || errors.Is(err, steps.ErrNotFound) != tc.notFound {
				t.Fatalf("error = %v, retryable = %v, not found = %v", err, steps.IsRetryable(err), errors.Is(err, steps.ErrNotFound))
			}
			var responseErr *responseError
			if !errors.As(err, &responseErr) || responseErr.statusCode != tc.status {
				t.Fatalf("error lost HTTP response details: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("sync was retried or polled: %d calls", calls.Load())
			}
		})
	}
}

func TestSyncRejectsEmptyInputs(t *testing.T) {
	client := newTestClient(t, "https://argocd.example.com", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Error("invalid sync input reached HTTP transport")
			return nil, errors.New("unexpected HTTP call")
		}),
	})
	for _, tc := range []struct{ app, revision string }{
		{"", "target-sha"},
		{" \t\r\n", "target-sha"},
		{"backend-prod", ""},
		{"backend-prod", " \t\r\n"},
	} {
		err := client.Sync(context.Background(), tc.app, tc.revision)
		if err == nil || steps.IsRetryable(err) || errors.Is(err, steps.ErrNotFound) {
			t.Fatalf("app = %q, revision = %q, unexpected error = %v", tc.app, tc.revision, err)
		}
	}
}

func TestSyncCancellationAndTimeout(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cancel        bool
		deadline      bool
		clientTimeout time.Duration
		retryable     bool
	}{
		{name: "caller cancellation", cancel: true},
		{name: "caller deadline", deadline: true},
		{name: "client timeout", clientTimeout: 100 * time.Millisecond, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var cancel context.CancelFunc
			if tc.deadline {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			release := make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if tc.cancel {
					cancel()
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			httpClient := server.Client()
			httpClient.Timeout = tc.clientTimeout
			client := newTestClient(t, server.URL, httpClient)
			err := client.Sync(ctx, "backend-prod", "target-sha")
			want := context.DeadlineExceeded
			if tc.cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || steps.IsRetryable(err) != tc.retryable || calls.Load() != 1 {
				t.Fatalf("error = %v, retryable = %v, calls = %d", err, steps.IsRetryable(err), calls.Load())
			}
		})
	}
}

func TestSyncDoesNotResendAfterLostResponse(t *testing.T) {
	calls := 0
	client := newTestClient(t, "https://argocd.example.com", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return nil, io.EOF
		}),
	})
	err := client.Sync(context.Background(), "backend-prod", "target-sha")
	if !errors.Is(err, io.EOF) || !steps.IsRetryable(err) || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

func TestSyncThenGetOperation(t *testing.T) {
	snapshots := []string{
		`{"operation":{"sync":{"revision":"target-sha"}},"status":{"health":{"status":"Progressing"}}}`,
		`{"operation":{"sync":{"revision":"target-sha"}},"status":{"health":{"status":"Progressing"},"operationState":{"phase":"Running","operation":{"sync":{"revision":"target-sha"}},"syncResult":{"revision":"target-sha"}}}}`,
		`{"status":{"health":{"status":"Progressing"},"operationState":{"phase":"Succeeded","operation":{"sync":{"revision":"target-sha"}},"syncResult":{"revision":"target-sha"}}}}`,
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			if r.Method != http.MethodPost {
				t.Errorf("first request = %s, want POST", r.Method)
			}
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if r.Method != http.MethodGet || n > int32(len(snapshots)+1) {
			t.Errorf("unexpected request %d: %s %s", n, r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, snapshots[n-2])
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, server.Client())
	if err := client.Sync(context.Background(), "backend-prod", "target-sha"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("Sync performed extra calls: %d", calls.Load())
	}
	for _, phase := range []string{"Pending", "Running", "Succeeded"} {
		got, err := client.Get(context.Background(), "backend-prod", false)
		if err != nil || got.Operation == nil || got.Operation.Revision != "target-sha" || got.Operation.Phase != phase || got.Health != "Progressing" {
			t.Fatalf("state = %+v, operation = %+v, error = %v; want phase %s and Progressing health", got, got.Operation, err, phase)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("flow made %d requests, want one POST and three GETs", calls.Load())
	}
}
