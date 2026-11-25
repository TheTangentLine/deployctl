// Package kube provides read-only Kubernetes snapshots and diagnostics.
package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	requestTimeout       = 30 * time.Second
	pageSize       int64 = 500
)

// Client exposes reads only. Cluster credentials must separately enforce RBAC.
type Client struct {
	api     kubernetes.Interface
	timeout time.Duration
}

var _ steps.KubeReader = (*Client)(nil)

// LoadConfig uses only explicit KUBECONFIG paths or in-cluster credentials.
// It never silently falls back to the user's default ~/.kube/config.
func LoadConfig() (*rest.Config, error) {
	if paths := os.Getenv("KUBECONFIG"); paths != "" {
		rules := &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(paths)}
		// Loading rules otherwise ignore missing files in a path list. Explicit
		// configuration errors must not silently fall back to in-cluster auth.
		for _, path := range rules.Precedence {
			if _, err := os.Stat(path); err != nil {
				return nil, fmt.Errorf("kube KUBECONFIG: %w", err)
			}
		}
		raw, err := rules.Load()
		if err != nil {
			return nil, fmt.Errorf("kube KUBECONFIG: %w", err)
		}
		cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, raw.CurrentContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kube KUBECONFIG: %w", err)
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kube in-cluster config: %w", err)
	}
	return cfg, nil
}

// NewForConfig copies cfg so timeout/defaulting never modifies caller state.
func NewForConfig(cfg *rest.Config) (*Client, error) {
	if cfg == nil {
		return nil, errors.New("kube config must not be nil")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("kube config timeout must not be negative")
	}
	copy := rest.CopyConfig(cfg)
	if copy.Timeout == 0 || copy.Timeout > requestTimeout {
		copy.Timeout = requestTimeout
	}
	api, err := kubernetes.NewForConfig(copy)
	if err != nil {
		return nil, fmt.Errorf("kube client: %w", err)
	}
	c, err := New(api)
	if err != nil {
		return nil, err
	}
	c.timeout = copy.Timeout
	return c, nil
}

// New injects a Kubernetes client, including a client-go fake in tests.
func New(api kubernetes.Interface) (*Client, error) {
	if api == nil {
		return nil, errors.New("kube client must not be nil")
	}
	return &Client{api: api, timeout: requestTimeout}, nil
}

func requireValue(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("kube %s must not be empty", field)
	}
	return nil
}

// classify preserves the cause for errors.Is/As. Caller cancellation is never
// retryable; a timeout imposed by this client can be retried by the worker.
func classify(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("kube %s: %w", operation, ctx.Err())
	}
	wrapped := fmt.Errorf("kube %s: %w", operation, err)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %w", wrapped, steps.ErrNotFound)
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		if code == 429 || code >= 500 && code <= 599 {
			return steps.Retryable(wrapped)
		}
		return wrapped
	}
	if errors.Is(err, context.Canceled) {
		return wrapped
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return steps.Retryable(wrapped)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return wrapped
	}
	var op *net.OpError
	if errors.As(err, &op) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return steps.Retryable(wrapped)
	}
	return wrapped
}
