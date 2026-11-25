package argocd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"k8s.io/apimachinery/pkg/labels"
)

const deploymentFixture = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: backend
  namespace: prod
  labels:
    app: deployment-only
spec:
  template:
    metadata:
      labels:
        app: backend
        tier: api
        track: stable
        example.com/team: platform
    spec:
      initContainers:
        - name: bootstrap
          image: registry.example.com/bootstrap:latest
      containers:
        - name: backend
          image: registry.example.com/backend:1.2.3
        - name: proxy
          image: registry.example.com/proxy@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`

const configMapFixture = `apiVersion: v1
kind: ConfigMap
metadata:
  name: config
data:
  setting: value
`

func TestDesiredImages(t *testing.T) {
	want := map[string]string{
		"backend": "registry.example.com/backend:1.2.3",
		"proxy":   "registry.example.com/proxy@sha256:" + strings.Repeat("a", 64),
	}
	for _, tc := range []struct {
		name, selector, defaultNamespace string
		manifests                        []string
	}{
		{"pod template labels", "app=backend", "prod", []string{deploymentFixture}},
		{"double equals", "app==backend", "prod", []string{deploymentFixture}},
		{"not equals", "app!=frontend", "prod", []string{deploymentFixture}},
		{"set and existence selectors", "app in (backend,frontend),tier notin (web),track,!disabled,example.com/team=platform", "prod", []string{deploymentFixture}},
		{"exists", "app", "prod", []string{deploymentFixture}},
		{"empty selector", "", "prod", []string{deploymentFixture}},
		{"destination namespace fallback", "app=backend", "prod", []string{strings.Replace(deploymentFixture, "  namespace: prod\n", "", 1)}},
		{"explicit namespace takes precedence", "app=backend", "different", []string{deploymentFixture}},
		{"multiple resource kinds and namespaces", "app=backend", "prod", []string{configMapFixture, strings.Replace(deploymentFixture, "namespace: prod", "namespace: other", 1), strings.Replace(deploymentFixture, "kind: Deployment", "kind: StatefulSet", 1), deploymentFixture}},
		{"multiple YAML documents", "app=backend", "prod", []string{configMapFixture + "---\n" + deploymentFixture}},
		{"empty YAML documents", "app=backend", "prod", []string{"", "---\n# comment\n---\n" + deploymentFixture + "---\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, err := labels.Parse(tc.selector)
			if err != nil {
				t.Fatal(err)
			}
			got, err := desiredImages(tc.manifests, tc.defaultNamespace, "prod", selector)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("images = %+v, want %+v; error = %v", got, want, err)
			}
		})
	}
}

func TestDesiredImagesJSON(t *testing.T) {
	manifest := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"backend","namespace":"prod"},"spec":{"template":{"metadata":{"labels":{"app":"backend"}},"spec":{"containers":[{"name":"backend","image":"registry.example.com/backend:1.2.3"}],"initContainers":[{"name":"bootstrap","image":"bootstrap:latest"}]}}}}`
	selector, err := labels.Parse("app=backend")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"backend": "registry.example.com/backend:1.2.3"}
	first, err := desiredImages([]string{manifest}, "prod", "prod", selector)
	if err != nil || !reflect.DeepEqual(first, want) {
		t.Fatalf("images = %+v, error = %v", first, err)
	}
	first["backend"] = "changed"
	first["extra"] = "changed"
	second, err := desiredImages([]string{manifest}, "prod", "prod", selector)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("caller mutation leaked: images = %+v, error = %v", second, err)
	}
}

func TestDesiredImagesErrors(t *testing.T) {
	for _, tc := range []struct {
		name, defaultNamespace, namespace, selector, message string
		manifests                                            []string
	}{
		{"no manifests", "prod", "prod", "app=backend", "no Deployment matches", nil},
		{"no Deployment", "prod", "prod", "app=backend", "no Deployment matches", []string{configMapFixture}},
		{"wrong namespace", "prod", "other", "app=backend", "no Deployment matches", []string{deploymentFixture}},
		{"wrong labels", "prod", "prod", "app=frontend", "no Deployment matches", []string{deploymentFixture}},
		{"outer labels do not select workload", "prod", "prod", "app=deployment-only", "no Deployment matches", []string{deploymentFixture}},
		{"multiple matches", "prod", "prod", "app=backend", "multiple Deployments match", []string{deploymentFixture, strings.Replace(deploymentFixture, "  name: backend\n", "  name: another\n", 1)}},
		{"invalid YAML", "prod", "prod", "app=backend", "manifest 1 document 1", []string{"apiVersion: ["}},
		{"scalar document", "prod", "prod", "app=backend", "manifest 1 document 1", []string{"42"}},
		{"missing resource type", "prod", "prod", "app=backend", "apiVersion and kind", []string{"{}"}},
		{"unsupported Deployment API", "prod", "prod", "app=backend", "not supported", []string{strings.Replace(deploymentFixture, "apps/v1", "extensions/v1beta1", 1)}},
		{"unsupported List", "prod", "prod", "app=backend", "List resources are not supported", []string{"apiVersion: v1\nkind: List\nitems: []"}},
		{"unsupported DeploymentList", "prod", "prod", "app=backend", "DeploymentList resources are not supported", []string{"apiVersion: apps/v1\nkind: DeploymentList\nitems: []"}},
		{"unresolved namespace", "", "prod", "app=backend", "no destination namespace", []string{strings.Replace(deploymentFixture, "  namespace: prod\n", "", 1)}},
		{"missing Deployment name", "prod", "prod", "app=backend", "has no name", []string{strings.Replace(deploymentFixture, "  name: backend\n", "", 1)}},
		{"numeric label", "prod", "prod", "app=backend", "cannot unmarshal number", []string{strings.Replace(deploymentFixture, "app: backend", "app: 42", 1)}},
		{"no regular containers", "prod", "prod", "app=backend", "no regular containers", []string{strings.Replace(deploymentFixture, "      containers:", "      ignoredContainers:", 1)}},
		{"missing container name", "prod", "prod", "app=backend", "name and image", []string{strings.Replace(deploymentFixture, "- name: backend", "- name: ''", 1)}},
		{"missing container image", "prod", "prod", "app=backend", "name and image", []string{strings.Replace(deploymentFixture, "image: registry.example.com/backend:1.2.3", "image: ''", 1)}},
		{"numeric image", "prod", "prod", "app=backend", "cannot unmarshal number", []string{strings.Replace(deploymentFixture, "image: registry.example.com/backend:1.2.3", "image: 42", 1)}},
		{"duplicate container names", "prod", "prod", "app=backend", "duplicate container name", []string{strings.Replace(deploymentFixture, "- name: proxy", "- name: backend", 1)}},
		{"invalid later document", "prod", "prod", "app=backend", "manifest 1 document 2", []string{deploymentFixture + "---\napiVersion: ["}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, err := labels.Parse(tc.selector)
			if err != nil {
				t.Fatal(err)
			}
			got, err := desiredImages(tc.manifests, tc.defaultNamespace, tc.namespace, selector)
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.message) || steps.IsRetryable(err) {
				t.Fatalf("images = %+v, error = %v; want permanent error containing %q", got, err, tc.message)
			}
		})
	}
}
