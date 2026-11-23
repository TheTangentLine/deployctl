package argocd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestGetRequest(t *testing.T) {
	for _, tc := range []struct {
		name, basePath, wantPath, wantQuery string
		refresh                             bool
	}{
		{"read", "", "/api/v1/applications/backend-prod", "", false},
		{"refresh", "", "/api/v1/applications/backend-prod", "refresh=normal", true},
		{"base path", "/argo%20cd/", "/argo%20cd/api/v1/applications/backend-prod", "", false},
		{"base path refresh", "/argo%20cd/", "/argo%20cd/api/v1/applications/backend-prod", "refresh=normal", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.wantPath || r.URL.RawQuery != tc.wantQuery {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				data, err := io.ReadAll(r.Body)
				if err != nil || len(data) != 0 {
					t.Errorf("GET body = %q, error = %v", data, err)
				}
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL+tc.basePath, server.Client())
			_, err := client.Get(context.Background(), "backend-prod", tc.refresh)
			if err != nil || calls.Load() != 1 {
				t.Fatalf("error = %v, calls = %d", err, calls.Load())
			}
		})
	}
}

func TestGetState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       steps.AppState
	}{
		{
			name: "full snapshot with independent comparison and operation revisions",
			body: `{
				"spec":{"syncPolicy":{"automated":{"enabled":true}}},
				"operation":{"sync":{"revision":"requested-sha"}},
				"status":{
					"sync":{"status":"OutOfSync","revision":"comparison-sha"},
					"health":{"status":"Degraded","message":"Deployment exceeded its progress deadline"},
					"operationState":{
						"phase":"Succeeded",
						"operation":{"sync":{"revision":"previous-sha"}},
						"syncResult":{"revision":"applied-sha"}
					}
				}
			}`,
			want: steps.AppState{
				SyncStatus:    "OutOfSync",
				SyncRevision:  "comparison-sha",
				Health:        "Degraded",
				HealthMessage: "Deployment exceeded its progress deadline",
				AutoSync:      true,
			},
		},
		{
			name: "missing fields",
			body: `{}`,
			want: steps.AppState{SyncStatus: "Unknown", Health: "Unknown"},
		},
		{
			name: "null fields",
			body: `{"spec":null,"status":{"sync":null,"health":null}}`,
			want: steps.AppState{SyncStatus: "Unknown", Health: "Unknown"},
		},
		{
			name: "empty statuses",
			body: `{"status":{"sync":{"status":"","revision":"comparison-sha"},"health":{"status":""}}}`,
			want: steps.AppState{SyncStatus: "Unknown", SyncRevision: "comparison-sha", Health: "Unknown"},
		},
		{
			name: "missing health",
			body: `{"status":{"sync":{"status":"Synced","revision":"comparison-sha"}}}`,
			want: steps.AppState{SyncStatus: "Synced", SyncRevision: "comparison-sha", Health: "Unknown"},
		},
		{
			name: "missing sync",
			body: `{"status":{"health":{"status":"Healthy"}}}`,
			want: steps.AppState{SyncStatus: "Unknown", Health: "Healthy"},
		},
		{
			name: "unrecognized statuses are preserved",
			body: `{"status":{"sync":{"status":"FutureSyncStatus"},"health":{"status":"FutureHealthStatus"}}}`,
			want: steps.AppState{SyncStatus: "FutureSyncStatus", Health: "FutureHealthStatus"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			got, err := client.Get(context.Background(), "backend-prod", false)
			if err != nil || got != tc.want {
				t.Fatalf("state = %+v, want %+v; error = %v", got, tc.want, err)
			}
		})
	}
}

func TestGetAutoSync(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		want       bool
	}{
		{"missing sync policy", `{}`, false},
		{"null sync policy", `{"syncPolicy":null}`, false},
		{"missing automated", `{"syncPolicy":{}}`, false},
		{"null automated", `{"syncPolicy":{"automated":null}}`, false},
		{"enabled", `{"syncPolicy":{"automated":{"enabled":true}}}`, true},
		{"disabled", `{"syncPolicy":{"automated":{"enabled":false}}}`, false},
		{"disabled with other options", `{"syncPolicy":{"automated":{"enabled":false,"prune":true,"selfHeal":true}}}`, false},
		{"enabled omitted", `{"syncPolicy":{"automated":{}}}`, true},
		{"enabled null", `{"syncPolicy":{"automated":{"enabled":null}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"spec":`+tc.spec+`}`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			got, err := client.Get(context.Background(), "backend-prod", false)
			if err != nil || got.AutoSync != tc.want {
				t.Fatalf("AutoSync = %v, want %v; error = %v", got.AutoSync, tc.want, err)
			}
		})
	}
}

func TestGetErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		status              int
		retryable, notFound bool
	}{
		{"not found", `{"message":"application missing"}`, "application missing", 404, false, true},
		{"forbidden", `{"message":"permission denied"}`, "permission denied", 403, false, false},
		{"rate limit", `{"message":"too many requests"}`, "too many requests", 429, true, false},
		{"unavailable", `{"message":"unavailable"}`, "unavailable", 503, true, false},
		{"invalid JSON", `{"status":`, "decode response", 200, false, false},
		{"null JSON", `null`, "response must not be null", 200, false, false},
		{"wrong field type", `{"status":{"sync":{"status":"Synced"},"health":{"status":42}}}`, "decode response", 200, false, false},
		{"wrong enabled type", `{"spec":{"syncPolicy":{"automated":{"enabled":"false"}}}}`, "decode response", 200, false, false},
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
			got, err := client.Get(context.Background(), "backend-prod", true)
			if err == nil || !strings.Contains(err.Error(), tc.message) || got != (steps.AppState{}) {
				t.Fatalf("state = %+v, error = %v; want zero state and error containing %q", got, err, tc.message)
			}
			if steps.IsRetryable(err) != tc.retryable || errors.Is(err, steps.ErrNotFound) != tc.notFound {
				t.Fatalf("error = %v, retryable = %v, not found = %v", err, steps.IsRetryable(err), errors.Is(err, steps.ErrNotFound))
			}
			if calls.Load() != 1 {
				t.Fatalf("request was retried: %d calls", calls.Load())
			}
		})
	}
}

func TestGetRejectsEmptyName(t *testing.T) {
	client := newTestClient(t, "https://argocd.example.com", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Error("empty application name reached HTTP transport")
			return nil, errors.New("unexpected HTTP call")
		}),
	})
	for _, app := range []string{"", " \t\r\n"} {
		got, err := client.Get(context.Background(), app, false)
		if err == nil || steps.IsRetryable(err) || errors.Is(err, steps.ErrNotFound) || got != (steps.AppState{}) {
			t.Fatalf("name = %q, state = %+v, error = %v", app, got, err)
		}
	}
}

func TestGetCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, server.Client())
	got, err := client.Get(ctx, "backend-prod", true)
	if !errors.Is(err, context.Canceled) || steps.IsRetryable(err) || got != (steps.AppState{}) {
		t.Fatalf("state = %+v, error = %v; want zero state and preserved cancellation", got, err)
	}
}
