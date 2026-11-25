package kube

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	valid := `apiVersion: v1
kind: Config
current-context: test
clusters:
- name: test
  cluster:
    server: https://cluster.example.com
contexts:
- name: test
  context:
    cluster: test
    user: test
users:
- name: test
  user:
    token: test-token
`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	cfg, err := LoadConfig()
	if err != nil || cfg.Host != "https://cluster.example.com" || cfg.BearerToken != "test-token" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	// Even a plausible in-cluster environment must not rescue invalid explicit config.
	t.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	for _, content := range []string{"", "not: [valid", "apiVersion: v1\nkind: Config\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "KUBECONFIG") {
			t.Fatalf("invalid explicit config: %v", err)
		}
	}
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	if _, err := LoadConfig(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	t.Setenv("KUBECONFIG", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := LoadConfig(); !errors.Is(err, rest.ErrNotInCluster) {
		t.Fatalf("in-cluster selection: %v", err)
	}
}

func TestConstructors(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("accepted nil client")
	}
	if _, err := NewForConfig(nil); err == nil {
		t.Fatal("accepted nil config")
	}
	cfg := &rest.Config{Host: "https://cluster.example.com", Timeout: time.Minute, TLSClientConfig: rest.TLSClientConfig{CAData: []byte{}}}
	before := rest.CopyConfig(cfg)
	c, err := NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.timeout != 30*time.Second || !reflect.DeepEqual(cfg, before) {
		t.Fatalf("constructor modified config: %+v", cfg)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		retry, missing bool
	}{
		{"404", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 404, Reason: metav1.StatusReasonNotFound}}, false, true},
		{"400", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 400}}, false, false},
		{"401", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 401}}, false, false},
		{"403", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 403}}, false, false},
		{"429", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 429}}, true, false},
		{"500", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 500}}, true, false},
		{"503", &apierrors.StatusError{ErrStatus: metav1.Status{Code: 503}}, true, false},
		{"client deadline", context.DeadlineExceeded, true, false},
		{"canceled", context.Canceled, false, false},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true, false},
		{"DNS permanent", &net.DNSError{Err: "no such host", IsNotFound: true}, false, false},
		{"DNS timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true, false},
		{"EOF", io.EOF, true, false},
		{"short read", io.ErrUnexpectedEOF, true, false},
		{"other", errors.New("invalid response"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(context.Background(), "read", tc.err)
			if !errors.Is(got, tc.err) || steps.IsRetryable(got) != tc.retry || errors.Is(got, steps.ErrNotFound) != tc.missing {
				t.Fatalf("error=%v; retry=%v; missing=%v", got, steps.IsRetryable(got), errors.Is(got, steps.ErrNotFound))
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		if !deadline {
			cancel()
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		got := classify(ctx, "read", io.EOF)
		cancel()
		if !errors.Is(got, ctx.Err()) || steps.IsRetryable(got) {
			t.Fatalf("caller context: %v", got)
		}
	}
}

func TestRequestErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 429, 500, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":`+strconv.Itoa(code)+`}`)
			}))
			defer server.Close()
			c, err := NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []func() error{
				func() error { _, err := c.Pods(context.Background(), "demo", "app=x"); return err },
				func() error { _, err := c.Events(context.Background(), "demo", []string{"pod"}, 20); return err },
				func() error { _, err := c.Logs(context.Background(), "demo", "pod", "container", true, 50); return err },
			} {
				err := operation()
				if err == nil || steps.IsRetryable(err) != (code == 429 || code >= 500) || errors.Is(err, steps.ErrNotFound) != (code == 404) {
					t.Fatalf("HTTP %d: %v", code, err)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("unexpected retry: HTTP %d, calls=%d", code, calls.Load())
			}
		})
	}
}

func TestPodRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	for _, callerDeadline := range []bool{false, true} {
		c, err := NewForConfig(&rest.Config{Host: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		c.timeout = 40 * time.Millisecond
		ctx := context.Background()
		cancel := func() {}
		if callerDeadline {
			ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
		}
		_, err = c.Pods(ctx, "demo", "app=x")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || steps.IsRetryable(err) == callerDeadline {
			t.Fatalf("caller=%v: %v", callerDeadline, err)
		}
	}
}
