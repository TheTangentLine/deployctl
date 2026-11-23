package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

const gitSpecFixture = `{"source":{"repoURL":"https://example.com/config.git","path":"charts/backend","helm":{"valueFiles":["values-prod.yaml"]}},"destination":{"namespace":"prod"}}`

func TestGetDesiredImages(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, spec, basePath, inputSHA, returnedRevision string
		sources                                          bool
	}{
		{name: "single source with omitted response revision", spec: gitSpecFixture, inputSHA: sha},
		{name: "base path and matching returned revision", spec: gitSpecFixture, basePath: "/argo%20cd/", inputSHA: sha, returnedRevision: sha},
		{name: "uppercase SHA", spec: gitSpecFixture, inputSHA: strings.ToUpper(sha), returnedRevision: strings.ToUpper(sha)},
		{name: "Git root source", spec: `{"source":{"repoURL":"https://example.com/config.git"}}`, inputSHA: sha},
		{name: "one source in sources array", spec: `{"sources":[{"repoURL":"https://example.com/config.git","path":"charts/backend","targetRevision":"main"}],"destination":{"namespace":"prod"}}`, inputSHA: sha, sources: true},
		{name: "sources array takes precedence", spec: `{"source":{"repoURL":"https://example.com/old-helm","chart":"old-chart"},"sources":[{"repoURL":"https://example.com/config.git","path":"charts/backend"}],"destination":{"namespace":"prod"}}`, inputSHA: sha, sources: true},
		{name: "null hydrator", spec: `{"source":{"repoURL":"https://example.com/config.git"},"sourceHydrator":null}`, inputSHA: sha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				basePath := strings.TrimRight(tc.basePath, "/") + "/api/v1/applications/backend-prod"
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
				}
				if n == 1 {
					if r.URL.EscapedPath() != basePath || r.URL.RawQuery != "" {
						t.Errorf("unexpected Application request: %s", r.URL)
					}
					_, _ = io.WriteString(w, `{"spec":`+tc.spec+`,"status":{"sync":{"revision":"moving-head"},"summary":{"images":["wrong:live-status"]}}}`)
					return
				}
				wantQuery := url.Values{"revision": {sha}}
				if tc.sources {
					wantQuery.Set("sourcePositions", "1")
					wantQuery.Set("revisions", sha)
				}
				if n != 2 || r.URL.EscapedPath() != basePath+"/manifests" || r.URL.RawQuery != wantQuery.Encode() {
					t.Errorf("unexpected manifests request %d: %s", n, r.URL)
				}
				body := map[string]any{"manifests": []string{deploymentFixture}}
				if tc.returnedRevision != "" {
					body["revision"] = tc.returnedRevision
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := newTestClient(t, server.URL+tc.basePath, server.Client())
			got, err := client.GetDesiredImages(context.Background(), "backend-prod", tc.inputSHA, "prod", "app=backend")
			want := map[string]string{
				"backend": "registry.example.com/backend:1.2.3",
				"proxy":   "registry.example.com/proxy@sha256:" + strings.Repeat("a", 64),
			}
			if err != nil || !reflect.DeepEqual(got, want) || calls.Load() != 2 {
				t.Fatalf("images = %+v, error = %v, calls = %d", got, err, calls.Load())
			}
		})
	}
}

func TestGetDesiredImagesNamespaceAndMapIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/api/v1/applications/backend-prod" {
			_, _ = io.WriteString(w, `{"spec":`+gitSpecFixture+`}`)
			return
		}
		if r.URL.Path != "/api/v1/applications/backend-prod/manifests" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		manifest := strings.Replace(deploymentFixture, "  namespace: prod\n", "", 1)
		if err := json.NewEncoder(w).Encode(map[string]any{"manifests": []string{manifest}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, server.Client())
	sha := strings.Repeat("a", 40)
	first, err := client.GetDesiredImages(context.Background(), "backend-prod", sha, "prod", "app=backend")
	if err != nil {
		t.Fatal(err)
	}
	first["backend"] = "changed"
	first["extra"] = "changed"
	second, err := client.GetDesiredImages(context.Background(), "backend-prod", sha, "prod", "app=backend")
	if err != nil || len(second) != 2 || second["backend"] != "registry.example.com/backend:1.2.3" || calls.Load() != 4 {
		t.Fatalf("images = %+v, error = %v, calls = %d", second, err, calls.Load())
	}
}

func TestGetDesiredImagesRejectsInputsBeforeHTTP(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client := newTestClient(t, "https://argocd.example.com", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Error("invalid input reached HTTP transport")
			return nil, errors.New("unexpected HTTP call")
		}),
	})
	for _, tc := range []struct{ app, revision, namespace, selector string }{
		{"backend-prod", "", "prod", "app=backend"},
		{"backend-prod", "main", "prod", "app=backend"},
		{"backend-prod", sha[:12], "prod", "app=backend"},
		{"backend-prod", strings.Repeat("g", 40), "prod", "app=backend"},
		{"backend-prod", sha + "a", "prod", "app=backend"},
		{"backend-prod", sha, "", "app=backend"},
		{"backend-prod", sha, " \t", "app=backend"},
		{"backend-prod", sha, "prod", "app in ("},
		{" \t", sha, "prod", "app=backend"},
	} {
		got, err := client.GetDesiredImages(context.Background(), tc.app, tc.revision, tc.namespace, tc.selector)
		if got != nil || err == nil || steps.IsRetryable(err) || errors.Is(err, steps.ErrNotFound) {
			t.Fatalf("input = %+v, images = %+v, error = %v", tc, got, err)
		}
	}
}

func TestGetDesiredImagesUnsupportedSources(t *testing.T) {
	for _, tc := range []struct{ name, spec, message string }{
		{"missing source", `{}`, "one Git source"},
		{"null source", `{"source":null}`, "one Git source"},
		{"missing repo", `{"source":{"repoURL":" "}}`, "repoURL"},
		{"empty sources", `{"sources":[]}`, "one Git source"},
		{"null source element", `{"sources":[null]}`, "repoURL"},
		{"multiple sources", `{"sources":[{"repoURL":"https://example.com/a.git"},{"repoURL":"https://example.com/b.git"}]}`, "found 2 sources"},
		{"Helm repository", `{"source":{"repoURL":"https://charts.example.com","chart":"backend"}}`, "Helm repository and OCI"},
		{"OCI source", `{"source":{"repoURL":"oci://registry.example.com/backend","path":"."}}`, "Helm repository and OCI"},
		{"Helm OCI chart", `{"source":{"repoURL":"registry.example.com/charts","chart":"backend"}}`, "Helm repository and OCI"},
		{"source hydrator", `{"source":{"repoURL":"https://example.com/config.git"},"sourceHydrator":{}}`, "sourceHydrator"},
		{"reference only", `{"sources":[{"repoURL":"https://example.com/values.git","ref":"values"}]}`, "reference-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/v1/applications/backend-prod" {
					t.Error("unsupported source triggered manifests request")
				}
				_, _ = io.WriteString(w, `{"spec":`+tc.spec+`}`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			got, err := client.GetDesiredImages(context.Background(), "backend-prod", strings.Repeat("a", 40), "prod", "app=backend")
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.message) || steps.IsRetryable(err) || errors.Is(err, steps.ErrNotFound) || calls.Load() != 1 {
				t.Fatalf("images = %+v, error = %v, calls = %d; want permanent error containing %q", got, err, calls.Load(), tc.message)
			}
		})
	}
}

func TestGetDesiredImagesErrors(t *testing.T) {
	encodedManifests, err := json.Marshal([]string{deploymentFixture})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, message string
		status              int
		application         bool
		retryable, notFound bool
	}{
		{"missing Application", `{"message":"app missing"}`, "app missing", 404, true, false, true},
		{"Application unavailable", `{"message":"unavailable"}`, "unavailable", 503, true, true, false},
		{"null Application", "null", "response must not be null", 200, true, false, false},
		{"manifests forbidden", `{"message":"permission denied"}`, "permission denied", 403, false, false, false},
		{"manifests rate limit", `{"message":"rate limit"}`, "rate limit", 429, false, true, false},
		{"manifests unavailable", `{"message":"unavailable"}`, "unavailable", 503, false, true, false},
		{"Application disappeared", `{"message":"app missing"}`, "app missing", 404, false, false, true},
		{"null manifests response", "null", "response must not be null", 200, false, false, false},
		{"invalid manifests JSON", `{"manifests":`, "decode response", 200, false, false, false},
		{"wrong manifests type", `{"manifests":[{}]}`, "decode response", 200, false, false, false},
		{"missing manifests", `{}`, "no Deployment matches", 200, false, false, false},
		{"empty manifests", `{"manifests":[]}`, "no Deployment matches", 200, false, false, false},
		{"mismatched revision", `{"revision":"` + strings.Repeat("b", 40) + `","manifests":` + string(encodedManifests) + `}`, "does not match requested SHA", 200, false, false, false},
		{"branch instead of resolved SHA", `{"revision":"main","manifests":` + string(encodedManifests) + `}`, "does not match requested SHA", 200, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 && !tc.application {
					_, _ = io.WriteString(w, `{"spec":`+gitSpecFixture+`}`)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			got, err := client.GetDesiredImages(context.Background(), "backend-prod", strings.Repeat("a", 40), "prod", "app=backend")
			wantCalls := int32(2)
			if tc.application {
				wantCalls = 1
			}
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.message) || steps.IsRetryable(err) != tc.retryable || errors.Is(err, steps.ErrNotFound) != tc.notFound || calls.Load() != wantCalls {
				t.Fatalf("images = %+v, error = %v, retryable = %v, not found = %v, calls = %d", got, err, steps.IsRetryable(err), errors.Is(err, steps.ErrNotFound), calls.Load())
			}
		})
	}
}

func TestGetDesiredImagesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"spec":`+gitSpecFixture+`}`)
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := newTestClient(t, server.URL, server.Client())
	got, err := client.GetDesiredImages(ctx, "backend-prod", strings.Repeat("a", 40), "prod", "app=backend")
	if got != nil || !errors.Is(err, context.Canceled) || steps.IsRetryable(err) || calls.Load() != 2 {
		t.Fatalf("images = %+v, error = %v, calls = %d", got, err, calls.Load())
	}
}
