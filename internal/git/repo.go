// Package git reads and changes values in the config repo.
//
// Owner: the Git side of M0. It implements steps.GitRepo. The YAML editing
// itself (keeping comments, keeping "1.10" a string) lives in its own pure
// function in this package so it can be tested without any Git at all.
package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.GitRepo = (*Repo)(nil)

// Repo is the real implementation of steps.GitRepo.
//
// One Repo serves every config repo the worker needs. It keeps one local
// clone per repo URL under cacheDir and fetches before every read, so it
// never works from a stale copy.
type Repo struct {
	// AuthorName and AuthorEmail are the identity on commits SetValue
	// makes. New sets defaults; with a GitHub App, use the App's bot
	// identity, e.g. "deployctl[bot]" and
	// "<app-user-id>+deployctl[bot]@users.noreply.github.com".
	AuthorName  string
	AuthorEmail string

	cacheDir string
	auth     transport.AuthMethod // nil for local repos in tests

	mu     sync.Mutex
	clones map[string]*clone // repo URL -> local clone

	// beforePush, if set, runs just before each push attempt. Tests use it
	// to make "someone else pushed first" happen on purpose.
	beforePush func(attempt int)
}

// maxPushAttempts is how many times SetValue re-fetches and re-applies its
// edit when someone else pushed to the branch first.
const maxPushAttempts = 5

// clone is one local copy. Its mutex serialises fetch, read and (later)
// commit and push on that copy; go-git is not safe for concurrent writes.
type clone struct {
	mu   sync.Mutex
	repo *git.Repository
}

// New returns a Repo that keeps clones under cacheDir.
// auth is how to authenticate to the remote (the GitHub App token source
// in production); pass nil for local repositories.
func New(cacheDir string, auth transport.AuthMethod) (*Repo, error) {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("git cache dir: %w", err)
	}
	return &Repo{
		AuthorName:  "deployctl",
		AuthorEmail: "deployctl@localhost",
		cacheDir:    cacheDir,
		auth:        auth,
		clones:      map[string]*clone{},
	}, nil
}

// GetValue reads the value at ref.Key in ref.File on the tip of ref.Branch.
func (r *Repo) GetValue(ctx context.Context, ref steps.ValueRef) (string, error) {
	c, err := r.open(ctx, ref.Repo)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := r.fetch(ctx, c, ref.Branch); err != nil {
		return "", err
	}
	content, err := readFile(c.repo, ref.Branch, ref.File)
	if err != nil {
		return "", err
	}
	v, err := GetYAMLValue(content, ref.Key)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref.File, err)
	}
	return v, nil
}

// SetValue makes the value at ref equal value, commits and pushes.
//
// If the value is already set it returns changed=false, no commit, and the
// SHA of the branch tip. If someone else pushed to the branch in between,
// it fetches again and re-applies its one-line edit on the new tip, up to
// maxPushAttempts times; it never overwrites a file with an old copy.
//
// The commit is built directly from Git objects, so the clone needs no
// checkout and nothing on disk is left half-edited after a crash.
func (r *Repo) SetValue(ctx context.Context, ref steps.ValueRef, value string, kind steps.ValueKind, msg steps.CommitMessage) (string, bool, error) {
	message, err := formatMessage(msg)
	if err != nil {
		return "", false, err
	}
	c, err := r.open(ctx, ref.Repo)
	if err != nil {
		return "", false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		if err := r.fetch(ctx, c, ref.Branch); err != nil {
			return "", false, err
		}
		tipRef, err := c.repo.Reference(plumbing.NewRemoteReferenceName("origin", ref.Branch), true)
		if err != nil {
			return "", false, fmt.Errorf("branch %s: %w", ref.Branch, steps.ErrNotFound)
		}
		tip, err := c.repo.CommitObject(tipRef.Hash())
		if err != nil {
			return "", false, err
		}

		content, err := readFile(c.repo, ref.Branch, ref.File)
		if err != nil {
			return "", false, err
		}
		edited, changed, err := SetYAMLValue(content, ref.Key, value, kind)
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", ref.File, err)
		}
		if !changed {
			return tip.Hash.String(), false, nil
		}

		sha, err := r.commitFile(c.repo, tip, ref.File, edited, message)
		if err != nil {
			return "", false, err
		}

		if r.beforePush != nil {
			r.beforePush(attempt)
		}
		err = r.push(ctx, c.repo, sha, ref.Branch)
		switch {
		case err == nil:
			return sha.String(), true, nil
		case errors.Is(err, errBranchMoved):
			continue // someone pushed first: fetch and re-apply on the new tip
		default:
			return "", false, err
		}
	}
	return "", false, steps.Retryable(fmt.Errorf(
		"push to %s rejected %d times: the branch keeps moving", ref.Branch, maxPushAttempts))
}

// formatMessage renders a commit message with the request trailers in
// the last paragraph, where git recognises trailers:
//
//	deploy(backend/prod): 9f8e7d6 -> e4f5a6b
//
//	Requested-by: linh@example.com
//	Approved-by: priya@example.com
//	Deployctl-Request: 0b8e2c1a-...
func formatMessage(m steps.CommitMessage) (string, error) {
	if m.Subject == "" || m.RequestID == "" {
		return "", errors.New("commit message needs a subject and a request ID")
	}
	for _, s := range []string{m.Subject, m.RequestedBy, m.ApprovedBy, m.RequestID} {
		if strings.ContainsAny(s, "\r\n") {
			return "", errors.New("commit message fields must be single lines")
		}
	}
	var b strings.Builder
	b.WriteString(m.Subject + "\n\n")
	if m.RequestedBy != "" {
		b.WriteString("Requested-by: " + m.RequestedBy + "\n")
	}
	if m.ApprovedBy != "" {
		b.WriteString("Approved-by: " + m.ApprovedBy + "\n")
	}
	b.WriteString(trailerKey + ": " + m.RequestID + "\n")
	return b.String(), nil
}

// commitFile creates a commit on top of parent in which file has the given
// content, and returns its hash. It writes objects only; no ref changes.
func (r *Repo) commitFile(repo *git.Repository, parent *object.Commit, file string, content []byte, message string) (plumbing.Hash, error) {
	path := strings.Split(strings.Trim(file, "/"), "/")
	for _, p := range path {
		if p == "" || p == "." || p == ".." {
			return plumbing.ZeroHash, fmt.Errorf("invalid file path %q", file)
		}
	}
	s := repo.Storer

	blob := s.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	blobHash, err := s.SetEncodedObject(blob)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	root, err := parent.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	treeHash, err := replaceInTree(s, root, path, blobHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("%s: %w", file, err)
	}

	sig := object.Signature{Name: r.AuthorName, Email: r.AuthorEmail, When: time.Now()}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent.Hash},
	}
	obj := s.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(obj)
}

// replaceInTree returns the hash of a copy of tree in which the file at
// path points to blob. Every directory on the way is copied; nothing else
// changes. The file must already exist.
func replaceInTree(s storer.EncodedObjectStorer, tree *object.Tree, path []string, blob plumbing.Hash) (plumbing.Hash, error) {
	entries := make([]object.TreeEntry, len(tree.Entries))
	copy(entries, tree.Entries)

	i := -1
	for j, e := range entries {
		if e.Name == path[0] {
			i = j
			break
		}
	}
	if i < 0 {
		return plumbing.ZeroHash, steps.ErrNotFound
	}

	if len(path) == 1 {
		if !entries[i].Mode.IsFile() {
			return plumbing.ZeroHash, errors.New("not a regular file")
		}
		entries[i].Hash = blob // same name and mode, so the order stays valid
	} else {
		sub, err := tree.Tree(path[0])
		if err != nil {
			return plumbing.ZeroHash, steps.ErrNotFound
		}
		h, err := replaceInTree(s, sub, path[1:], blob)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries[i].Hash = h
	}

	obj := s.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(obj)
}

// errBranchMoved means the push was rejected because the remote branch is
// no longer the commit we built on: someone else pushed first.
var errBranchMoved = errors.New("branch moved")

// branchMovedMessages are how a lost push race is reported. go-git does
// not wrap these in typed errors, so they are matched as text:
//   - "non-fast-forward": go-git's own check before sending
//   - "incorrect old value provided", "failed to update ref",
//     "cannot lock ref", "fetch first": the server lost the race while
//     updating the ref (go-git's server, git-receive-pack, GitHub)
var branchMovedMessages = []string{
	"non-fast-forward",
	"incorrect old value provided",
	"failed to update ref",
	"cannot lock ref",
	"fetch first",
}

// protectedMessages are how GitHub reports branch protection or a ruleset
// blocking the push. Retrying will not help.
var protectedMessages = []string{"GH006", "GH013", "protected branch"}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// push sends commit to refs/heads/<branch> on origin, without force.
// It returns errBranchMoved if the branch has moved on.
func (r *Repo) push(ctx context.Context, repo *git.Repository, commit plumbing.Hash, branch string) error {
	// Push needs a ref name, not a bare hash. Use a private ref namespace so
	// we never move a local branch.
	local := plumbing.ReferenceName("refs/deployctl/push/" + branch)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(local, commit)); err != nil {
		return err
	}
	err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(string(local) + ":refs/heads/" + branch)},
		Auth:       r.auth,
	})
	switch {
	case err == nil:
		return nil
	case containsAny(err.Error(), protectedMessages):
		// Permanent: branch protection or a ruleset blocks direct pushes.
		return fmt.Errorf("push to %s blocked by branch protection; allow the deployctl GitHub App to bypass it: %w", branch, err)
	case containsAny(err.Error(), branchMovedMessages):
		return fmt.Errorf("%w: %v", errBranchMoved, err)
	default:
		return classify(ctx, fmt.Errorf("push to %s: %w", branch, err))
	}
}

// trailerKey is the commit trailer that ties a commit to a request.
// SetValue must write exactly this.
const trailerKey = "Deployctl-Request"

// searchDepth is how many recent commits FindRequestCommit looks through.
const searchDepth = 200

// FindRequestCommit looks for a commit on the tip of branch whose message
// has the trailer "Deployctl-Request: <requestID>", within the last
// searchDepth commits. It fetches first, so it also finds a commit pushed
// by another worker that crashed before recording it.
func (r *Repo) FindRequestCommit(ctx context.Context, repo, branch, requestID string) (string, bool, error) {
	c, err := r.open(ctx, repo)
	if err != nil {
		return "", false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := r.fetch(ctx, c, branch); err != nil {
		return "", false, err
	}
	tip, err := c.repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return "", false, fmt.Errorf("branch %s: %w", branch, steps.ErrNotFound)
	}

	commits, err := c.repo.Log(&git.LogOptions{From: tip.Hash()})
	if err != nil {
		return "", false, err
	}
	defer commits.Close()

	want := trailerKey + ": " + requestID
	var sha string
	seen := 0
	err = commits.ForEach(func(commit *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hasLine(commit.Message, want) {
			sha = commit.Hash.String()
			return storer.ErrStop
		}
		seen++
		if seen >= searchDepth {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return sha, sha != "", nil
}

// hasLine reports whether message contains line as a whole line.
func hasLine(message, line string) bool {
	for _, l := range strings.Split(message, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}

// open returns the local clone for url, cloning it on first use.
func (r *Repo) open(ctx context.Context, url string) (*clone, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if c, ok := r.clones[url]; ok {
		return c, nil
	}

	dir := filepath.Join(r.cacheDir, dirName(url))

	// Reuse a clone left on disk by a previous run of the worker.
	if repo, err := git.PlainOpen(dir); err == nil {
		c := &clone{repo: repo}
		r.clones[url] = c
		return c, nil
	}
	// Missing or broken: start fresh.
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("clean %s: %w", dir, err)
	}

	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:        url,
		Auth:       r.auth,
		NoCheckout: true, // reads go through Git objects, not files on disk
	})
	if err != nil {
		_ = os.RemoveAll(dir) // don't leave a half-cloned directory behind
		return nil, classify(ctx, fmt.Errorf("clone %s: %w", url, err))
	}
	c := &clone{repo: repo}
	r.clones[url] = c
	return c, nil
}

// fetch updates refs/remotes/origin/<branch> from the remote.
// Callers must hold c.mu.
func (r *Repo) fetch(ctx context.Context, c *clone, branch string) error {
	spec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	err := c.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{spec},
		Auth:       r.auth,
		Force:      true,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	var noRef git.NoMatchingRefSpecError
	if errors.As(err, &noRef) {
		return fmt.Errorf("branch %s: %w", branch, steps.ErrNotFound)
	}
	return classify(ctx, fmt.Errorf("fetch %s: %w", branch, err))
}

// readFile returns the contents of path at the tip of origin/<branch>.
func readFile(repo *git.Repository, branch, path string) ([]byte, error) {
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, fmt.Errorf("branch %s: %w", branch, steps.ErrNotFound)
		}
		return nil, err
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, err
	}
	f, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return nil, fmt.Errorf("%s: %w", path, steps.ErrNotFound)
		}
		return nil, err
	}
	content, err := f.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

// classify turns an error from talking to the remote into the worker's
// error classes (see internal/steps/errors.go).
func classify(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err() // cancelled: the worker lost its lease or is shutting down
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return fmt.Errorf("%w: %w", steps.ErrNotFound, err)
	case errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed),
		errors.Is(err, transport.ErrInvalidAuthMethod):
		return err // permanent: retrying will not fix credentials
	default:
		return steps.Retryable(err) // network trouble, remote 5xx, timeouts
	}
}

// dirName turns a repo URL into a safe directory name.
func dirName(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:8])
}
