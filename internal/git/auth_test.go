package git

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// The AuthFunc is asked for credentials on every operation, so a token
// that expires can be replaced between them.
func TestAuthFunc_CalledForEveryOperation(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	var calls atomic.Int32
	repo, err := New(t.TempDir(), func(context.Context) (transport.AuthMethod, error) {
		calls.Add(1)
		return nil, nil // local repo: no credentials needed
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}

	if _, err := repo.GetValue(ctx, ref); err != nil { // clone + fetch
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("after first read: %d calls, want 2 (clone, fetch)", got)
	}
	if _, err := repo.GetValue(ctx, ref); err != nil { // fetch only
		t.Fatal(err)
	}
	if _, _, err := repo.SetValue(ctx, ref, "e4f5a6b", steps.ValueString, deployMsg("r1")); err != nil { // fetch + push
		t.Fatal(err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("total %d calls, want 5", got)
	}
}

func TestAuthFunc_ErrorStopsTheOperation(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	tokenDown := steps.Retryable(errors.New("token endpoint unavailable"))
	repo, err := New(t.TempDir(), TokenAuth(func(context.Context) (string, error) {
		return "", tokenDown
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.GetValue(ctx, steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"})
	if !errors.Is(err, tokenDown) || !steps.IsRetryable(err) {
		t.Fatalf("got %v, want the token error, still retryable", err)
	}
}

func TestTokenAuth(t *testing.T) {
	auth, err := TokenAuth(func(context.Context) (string, error) { return "ghs_abc", nil })(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok || basic.Username != "x-access-token" || basic.Password != "ghs_abc" {
		t.Fatalf("got %#v", auth)
	}
	// The token must never show up when credentials are printed or logged.
	if s := basic.String(); s == "" || strings.Contains(s, "ghs_abc") {
		t.Fatalf("String() leaks the token: %q", s)
	}
}
