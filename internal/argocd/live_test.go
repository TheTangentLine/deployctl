package argocd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLiveRead exercises an explicitly configured Application without calling Sync.
func TestLiveRead(t *testing.T) {
	if os.Getenv("DEPLOYCTL_ARGOCD_SMOKE") != "1" {
		t.Skip("set DEPLOYCTL_ARGOCD_SMOKE=1; see docs/reference/argocd-client.md")
	}
	server := liveEnv(t, "DEPLOYCTL_ARGOCD_SERVER")
	token := liveEnv(t, "DEPLOYCTL_ARGOCD_TOKEN")
	app := liveEnv(t, "DEPLOYCTL_ARGOCD_SMOKE_APP")
	revision := liveEnv(t, "DEPLOYCTL_ARGOCD_SMOKE_REVISION")
	namespace := liveEnv(t, "DEPLOYCTL_ARGOCD_SMOKE_NAMESPACE")
	selector := liveEnv(t, "DEPLOYCTL_ARGOCD_SMOKE_SELECTOR")
	var expected map[string]string
	if err := json.Unmarshal([]byte(liveEnv(t, "DEPLOYCTL_ARGOCD_SMOKE_EXPECTED_IMAGES")), &expected); err != nil {
		t.Fatalf("decode expected images: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("expected images must contain at least one regular container")
	}
	for name, image := range expected {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(image) == "" {
			t.Fatal("expected images must have non-empty container names and images")
		}
	}
	httpClient := liveHTTPClient(t)
	t.Cleanup(httpClient.CloseIdleConnections)
	client, err := New(server, token, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	state, err := client.Get(ctx, app, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Logf("Get: sync=%s revision=%s health=%s autoSync=%t", state.SyncStatus, state.SyncRevision, state.Health, state.AutoSync)
	images, err := client.GetDesiredImages(ctx, app, revision, namespace, selector)
	if err != nil {
		t.Fatalf("GetDesiredImages: %v", err)
	}
	if !reflect.DeepEqual(images, expected) {
		t.Fatalf("desired images = %+v, want %+v", images, expected)
	}
	t.Logf("GetDesiredImages at %s: %+v", revision, images)
	state, err = client.Get(ctx, app, true)
	if err != nil {
		t.Fatalf("Get with refresh: %v", err)
	}
	t.Logf("Refresh: sync=%s revision=%s health=%s autoSync=%t", state.SyncStatus, state.SyncRevision, state.Health, state.AutoSync)
	if state.Operation != nil {
		t.Logf("Operation: phase=%s revision=%s", state.Operation.Phase, state.Operation.Revision)
	}
}

func liveEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("%s is required for the live smoke test", name)
	}
	return value
}

func liveHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		ServerName: os.Getenv("DEPLOYCTL_ARGOCD_SMOKE_TLS_SERVER_NAME"),
	}
	if path := os.Getenv("DEPLOYCTL_ARGOCD_SMOKE_CA_FILE"); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read trusted CA file: %v", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("trusted CA file contains no PEM certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

func TestLiveReadHarness(t *testing.T) {
	sha := strings.Repeat("a", 40)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer smoke-test-token" {
			t.Error("smoke test must use authenticated GET requests")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/v1/applications/backend-prod":
			wantQuery := ""
			if n == 4 {
				wantQuery = "refresh=normal"
			}
			if r.URL.RawQuery != wantQuery {
				t.Errorf("request %d: query = %q, want %q", n, r.URL.RawQuery, wantQuery)
			}
			_, _ = io.WriteString(w, `{"spec":`+gitSpecFixture+`,"status":{"sync":{"status":"Synced","revision":"`+sha+`"},"health":{"status":"Healthy"}}}`)
		case "/api/v1/applications/backend-prod/manifests":
			if n != 3 || r.URL.RawQuery != "revision="+sha {
				t.Errorf("unexpected manifests request %d: %s", n, r.URL)
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"manifests": []string{deploymentFixture}}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected smoke request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE", "1")
	t.Setenv("DEPLOYCTL_ARGOCD_SERVER", server.URL)
	t.Setenv("DEPLOYCTL_ARGOCD_TOKEN", "smoke-test-token")
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_APP", "backend-prod")
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_REVISION", sha)
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_NAMESPACE", "prod")
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_SELECTOR", "app=backend")
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_EXPECTED_IMAGES", `{"backend":"registry.example.com/backend:1.2.3","proxy":"registry.example.com/proxy@sha256:`+strings.Repeat("a", 64)+`"}`)
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_CA_FILE", "")
	t.Setenv("DEPLOYCTL_ARGOCD_SMOKE_TLS_SERVER_NAME", "")
	t.Run("configured smoke flow", TestLiveRead)
	if calls.Load() != 4 {
		t.Fatalf("smoke made %d requests, want four GETs", calls.Load())
	}
}
