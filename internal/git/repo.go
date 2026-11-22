// Package git reads and changes values in the config repo.
//
// Owner: the Git side of M0. It implements steps.GitRepo. The YAML editing
// itself (keeping comments, keeping "1.10" a string) lives in its own pure
// function in this package so it can be tested without any Git at all.
package git

import (
	"context"
	"errors"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.GitRepo = (*Repo)(nil)

var errNotImplemented = errors.New("git: not implemented")

// Repo is the real implementation of steps.GitRepo.
type Repo struct{}

func (r *Repo) GetValue(ctx context.Context, ref steps.ValueRef) (string, error) {
	return "", errNotImplemented
}

func (r *Repo) SetValue(ctx context.Context, ref steps.ValueRef, value string, kind steps.ValueKind, msg steps.CommitMessage) (string, bool, error) {
	return "", false, errNotImplemented
}

func (r *Repo) FindRequestCommit(ctx context.Context, repo, branch, requestID string) (string, bool, error) {
	return "", false, errNotImplemented
}
