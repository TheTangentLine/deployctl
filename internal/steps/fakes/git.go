// Package fakes provides in-memory implementations of the interfaces in
// internal/steps, for testing steps without GitHub, Argo CD or a cluster.
//
// Each fake has exported fields: set them up before the test, inspect
// them after. Each fake also has an Err map from method name to the
// error that method should return, for testing failure paths.
package fakes

import (
	"context"
	"fmt"
	"sync"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.GitRepo = (*Git)(nil)

// Commit is one commit recorded by the fake Git.
type Commit struct {
	SHA     string
	Ref     steps.ValueRef
	Value   string
	Message steps.CommitMessage
}

// Git is an in-memory config repo.
type Git struct {
	mu sync.Mutex

	// Values holds the current value at each ref.
	Values map[steps.ValueRef]string
	// Commits holds every commit, oldest first. Pre-populate it to
	// simulate "a previous attempt already pushed".
	Commits []Commit
	// Pushes counts SetValue calls that created a commit.
	Pushes int
	// Err makes a method fail: Err["SetValue"] = steps.Retryable(io.EOF).
	Err map[string]error
}

func (g *Git) fail(method string) error {
	if g.Err == nil {
		return nil
	}
	return g.Err[method]
}

func (g *Git) GetValue(ctx context.Context, ref steps.ValueRef) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("GetValue"); err != nil {
		return "", err
	}
	v, ok := g.Values[ref]
	if !ok {
		return "", fmt.Errorf("%s in %s: %w", ref.Key, ref.File, steps.ErrNotFound)
	}
	return v, nil
}

func (g *Git) SetValue(ctx context.Context, ref steps.ValueRef, value string, kind steps.ValueKind, msg steps.CommitMessage) (string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("SetValue"); err != nil {
		return "", false, err
	}
	current, ok := g.Values[ref]
	if !ok {
		return "", false, fmt.Errorf("%s in %s: %w", ref.Key, ref.File, steps.ErrNotFound)
	}
	if current == value {
		return g.head(), false, nil
	}
	if g.Values == nil {
		g.Values = map[steps.ValueRef]string{}
	}
	g.Values[ref] = value
	sha := fmt.Sprintf("%07x", 0xc0de000+len(g.Commits)+1)
	g.Commits = append(g.Commits, Commit{SHA: sha, Ref: ref, Value: value, Message: msg})
	g.Pushes++
	return sha, true, nil
}

func (g *Git) FindRequestCommit(ctx context.Context, repo, branch, requestID string) (string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.fail("FindRequestCommit"); err != nil {
		return "", false, err
	}
	for i := len(g.Commits) - 1; i >= 0; i-- {
		c := g.Commits[i]
		if c.Ref.Repo == repo && c.Ref.Branch == branch && c.Message.RequestID == requestID {
			return c.SHA, true, nil
		}
	}
	return "", false, nil
}

// head returns the SHA of the newest commit, or "" if there is none.
// Callers must hold g.mu.
func (g *Git) head() string {
	if len(g.Commits) == 0 {
		return ""
	}
	return g.Commits[len(g.Commits)-1].SHA
}
