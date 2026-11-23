package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// remote is a throwaway "GitHub": a bare repository on disk, plus a
// separate working copy used to push commits into it, the way a human
// or another worker would.
type remote struct {
	t    *testing.T
	URL  string // path to the bare repo; go-git clones it like any URL
	work *git.Repository
	dir  string
}

func newRemote(t *testing.T, files map[string]string) *remote {
	t.Helper()
	base := t.TempDir()
	bareDir := filepath.Join(base, "config.git")
	workDir := filepath.Join(base, "work")

	main := plumbing.NewBranchReferenceName("main")
	if _, err := git.PlainInitWithOptions(bareDir, &git.PlainInitOptions{
		Bare:        true,
		InitOptions: git.InitOptions{DefaultBranch: main},
	}); err != nil {
		t.Fatal(err)
	}
	work, err := git.PlainInitWithOptions(workDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: main},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bareDir}}); err != nil {
		t.Fatal(err)
	}
	r := &remote{t: t, URL: bareDir, work: work, dir: workDir}
	r.push(files, "initial")
	return r
}

// push writes files, commits and pushes to the bare repo's main branch.
// It returns the new commit's SHA.
func (r *remote) push(files map[string]string, message string) string {
	r.t.Helper()
	sha := r.commit(files, message)
	r.pushAll()
	return sha
}

// pushAll pushes local commits to the bare repo.
func (r *remote) pushAll() {
	r.t.Helper()
	if err := r.work.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		r.t.Fatal(err)
	}
}

// commit writes files and commits locally without pushing. With no files
// it makes an empty commit. It returns the new commit's SHA.
func (r *remote) commit(files map[string]string, message string) string {
	r.t.Helper()
	wt, err := r.work.Worktree()
	if err != nil {
		r.t.Fatal(err)
	}
	for path, content := range files {
		full := filepath.Join(r.dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			r.t.Fatal(err)
		}
	}
	sig := &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}
	h, err := wt.Commit(message, &git.CommitOptions{Author: sig, AllowEmptyCommits: true})
	if err != nil {
		r.t.Fatal(err)
	}
	return h.String()
}

const valuesFile = "charts/reconcile/values-dev.yaml"

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	r, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGetValue(t *testing.T) {
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}

	got, err := repo.GetValue(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a1b2c3d" {
		t.Fatalf("got %q, want a1b2c3d", got)
	}
}

func TestGetValue_SeesNewerCommits(t *testing.T) {
	rem := newRemote(t, map[string]string{valuesFile: "backend:\n  image:\n    tag: a1b2c3d\n"})
	repo := newTestRepo(t)
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}

	if got, err := repo.GetValue(context.Background(), ref); err != nil || got != "a1b2c3d" {
		t.Fatalf("first read: %q, %v", got, err)
	}

	// Someone else pushes after our clone exists.
	rem.push(map[string]string{valuesFile: "backend:\n  image:\n    tag: e4f5a6b\n"}, "deploy e4f5a6b")

	got, err := repo.GetValue(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != "e4f5a6b" {
		t.Fatalf("read a stale value %q; GetValue must fetch first", got)
	}
}

func TestGetValue_ReusesCloneOnDisk(t *testing.T) {
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	cache := t.TempDir()
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.replicaCount"}

	// Two Repos on the same cache dir simulate a worker restart.
	for i := 0; i < 2; i++ {
		repo, err := New(cache, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := repo.GetValue(context.Background(), ref); err != nil || got != "3" {
			t.Fatalf("run %d: %q, %v", i, got, err)
		}
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 {
		t.Fatalf("expected one clone directory, found %d", len(entries))
	}
}

func TestGetValue_NotFound(t *testing.T) {
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)
	missingRepo := filepath.Join(t.TempDir(), "does-not-exist.git")

	tests := map[string]steps.ValueRef{
		"missing key":    {Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.digest"},
		"missing file":   {Repo: rem.URL, Branch: "main", File: "charts/nope.yaml", Key: "backend.image.tag"},
		"missing branch": {Repo: rem.URL, Branch: "release", File: valuesFile, Key: "backend.image.tag"},
		"missing repo":   {Repo: missingRepo, Branch: "main", File: valuesFile, Key: "backend.image.tag"},
	}
	for name, ref := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := repo.GetValue(context.Background(), ref)
			checkErr(t, err, steps.ErrNotFound)
		})
	}
}

func TestGetValue_Cancelled(t *testing.T) {
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.GetValue(ctx, steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"})
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if steps.IsRetryable(err) {
		t.Fatalf("a cancelled context must not be retried: %v", err)
	}
}

// requestMessage is a commit message in the shape SetValue writes.
func requestMessage(requestID string) string {
	return "deploy(backend/dev): a1b2c3d -> e4f5a6b\n\n" +
		"Requested-by: linh@example.com\n\n" +
		trailerKey + ": " + requestID + "\n"
}

func TestFindRequestCommit(t *testing.T) {
	const id = "0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40"
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		want := rem.push(map[string]string{valuesFile: valuesDev + "# change\n"}, requestMessage(id))
		rem.push(map[string]string{valuesFile: valuesDev + "# later\n"}, "unrelated commit on top")

		sha, found, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", id)
		if err != nil || !found || sha != want {
			t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", sha, found, err, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		rem.push(nil, requestMessage("another-request"))

		sha, found, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", id)
		if err != nil || found || sha != "" {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, nil)", sha, found, err)
		}
	})

	t.Run("prefix of another ID does not match", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		rem.push(nil, requestMessage(id))

		_, found, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", "0b8e")
		if err != nil || found {
			t.Fatalf("short ID %q matched a longer one (found=%v, err=%v)", "0b8e", found, err)
		}
	})

	t.Run("ID mentioned outside a trailer line does not match", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		rem.push(nil, "revert: see "+trailerKey+": "+id+" for context")

		_, found, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", id)
		if err != nil || found {
			t.Fatalf("matched a mention inside a sentence (found=%v, err=%v)", found, err)
		}
	})

	// The case FindRequestCommit exists for: another worker pushed the
	// commit after our clone was made, then crashed before recording it.
	t.Run("pushed by someone else after our clone", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		repo := newTestRepo(t)
		if _, found, err := repo.FindRequestCommit(ctx, rem.URL, "main", id); err != nil || found {
			t.Fatalf("before push: found=%v, err=%v", found, err)
		}

		want := rem.push(nil, requestMessage(id))

		sha, found, err := repo.FindRequestCommit(ctx, rem.URL, "main", id)
		if err != nil || !found || sha != want {
			t.Fatalf("after push: got (%q, %v, %v), want (%q, true, nil); missing fetch?", sha, found, err, want)
		}
	})

	t.Run("older than search depth is not found", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		rem.commit(nil, requestMessage(id))
		for i := 0; i < searchDepth; i++ {
			rem.commit(nil, "filler")
		}
		rem.pushAll()

		_, found, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", id)
		if err != nil || found {
			t.Fatalf("found a commit %d commits deep (found=%v, err=%v)", searchDepth+1, found, err)
		}
	})

	t.Run("missing branch", func(t *testing.T) {
		rem := newRemote(t, map[string]string{valuesFile: valuesDev})
		_, _, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "release", id)
		checkErr(t, err, steps.ErrNotFound)
	})
}
