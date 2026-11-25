// Package archtest checks the dependency rules from
// docs/reference/architecture.md, so they fail the build instead of
// relying on review:
//
//   - internal/steps imports no client package (it uses interfaces only).
//   - internal/git, internal/argocd and internal/kube do not import each other.
//   - internal/api (from M1) never depends on internal/git or
//     internal/githubapp, so the API can never push to Git or mint a
//     GitHub token.
//   - internal/git does not depend on internal/githubapp; the worker wires
//     them together with git.TokenAuth.
package archtest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/TheTangentLine/deployctl"

var rules = []struct {
	pkg       string
	forbidden []string
}{
	{"internal/steps", []string{"internal/git", "internal/githubapp", "internal/argocd", "internal/kube", "internal/store", "internal/worker"}},
	{"internal/git", []string{"internal/githubapp", "internal/argocd", "internal/kube"}},
	{"internal/githubapp", []string{"internal/git", "internal/argocd", "internal/kube"}},
	{"internal/argocd", []string{"internal/git", "internal/githubapp", "internal/kube"}},
	{"internal/kube", []string{"internal/git", "internal/githubapp", "internal/argocd"}},
	{"internal/api", []string{"internal/git", "internal/githubapp"}},
}

func TestImportRules(t *testing.T) {
	for _, r := range rules {
		deps, ok := transitiveDeps(t, module+"/"+r.pkg)
		if !ok {
			continue // package does not exist yet
		}
		for _, f := range r.forbidden {
			if deps[module+"/"+f] {
				t.Errorf("%s must not depend on %s", r.pkg, f)
			}
		}
	}
}

// transitiveDeps returns every package pkg depends on, directly or not.
// ok is false if pkg does not exist.
func transitiveDeps(t *testing.T, pkg string) (map[string]bool, bool) {
	t.Helper()
	// -e reports a missing package in the JSON instead of failing.
	out, err := exec.Command("go", "list", "-e", "-json", pkg).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pkg, err)
	}
	var p struct {
		Deps  []string
		Error *struct{ Err string }
	}
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	if p.Error != nil {
		if strings.Contains(p.Error.Err, "no required module provides package") ||
			strings.Contains(p.Error.Err, "no Go files") {
			return nil, false
		}
		t.Fatalf("go list %s: %s", pkg, p.Error.Err)
	}
	deps := map[string]bool{}
	for _, d := range p.Deps {
		deps[d] = true
	}
	return deps, true
}
