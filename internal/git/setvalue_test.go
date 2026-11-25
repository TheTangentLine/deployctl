package git

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// remoteHead returns the tip commit of main in the bare repo, and the
// content of path in it, as GitHub would see them.
func remoteHead(t *testing.T, rem *remote, path string) (*object.Commit, string) {
	t.Helper()
	bare, err := git.PlainOpen(rem.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := bare.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := bare.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	f, err := commit.File(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	return commit, content
}

func deployMsg(id string) steps.CommitMessage {
	return steps.CommitMessage{
		Subject:     "deploy(backend/dev): a1b2c3d -> e4f5a6b",
		RequestedBy: "linh@example.com",
		RequestID:   id,
	}
}

func TestSetValue(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev, "README.md": "hello\n"})
	before, _ := remoteHead(t, rem, valuesFile)
	repo := newTestRepo(t)
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}

	sha, changed, err := repo.SetValue(ctx, ref, "e4f5a6b", steps.ValueString, deployMsg("req-1"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}

	head, content := remoteHead(t, rem, valuesFile)
	if head.Hash.String() != sha {
		t.Fatalf("remote tip %s, SetValue returned %s", head.Hash, sha)
	}
	if len(head.ParentHashes) != 1 || head.ParentHashes[0] != before.Hash {
		t.Fatalf("commit is not on top of the previous tip")
	}
	want, _, _ := SetYAMLValue([]byte(valuesDev), "backend.image.tag", "e4f5a6b", steps.ValueString)
	if content != string(want) {
		t.Fatalf("remote file:\n%s\nwant:\n%s", content, want)
	}
	if !strings.Contains(head.Message, "Requested-by: linh@example.com\n"+trailerKey+": req-1\n") {
		t.Fatalf("unexpected commit message:\n%s", head.Message)
	}
	if head.Author.Name != "deployctl" {
		t.Fatalf("author = %q", head.Author.Name)
	}

	// Files the commit did not touch are unchanged.
	f, err := head.File("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if readme, _ := f.Contents(); readme != "hello\n" {
		t.Fatalf("README changed: %q", readme)
	}

	// The commit is findable by its request ID: the crash-after-push path.
	found, ok, err := newTestRepo(t).FindRequestCommit(ctx, rem.URL, "main", "req-1")
	if err != nil || !ok || found != sha {
		t.Fatalf("FindRequestCommit = (%q, %v, %v), want %q", found, ok, err, sha)
	}
}

func TestSetValue_AlreadySet(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	before, _ := remoteHead(t, rem, valuesFile)
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}

	sha, changed, err := newTestRepo(t).SetValue(ctx, ref, "a1b2c3d", steps.ValueString, deployMsg("req-1"))
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if sha != before.Hash.String() {
		t.Fatalf("sha = %s, want the current tip %s", sha, before.Hash)
	}
	if after, _ := remoteHead(t, rem, valuesFile); after.Hash != before.Hash {
		t.Fatal("a commit was pushed although nothing changed")
	}
}

func TestSetValue_ReplicaCount(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.replicaCount"}

	if _, _, err := newTestRepo(t).SetValue(ctx, ref, "5", steps.ValueInt, deployMsg("req-1")); err != nil {
		t.Fatal(err)
	}
	_, content := remoteHead(t, rem, valuesFile)
	if !strings.Contains(content, "  replicaCount: 5         # managed by deployctl\n") {
		t.Fatalf("unexpected file:\n%s", content)
	}
}

// Someone pushes between our fetch and our push. SetValue must re-fetch,
// re-apply its own edit on the new tip, and keep the other change.
func TestSetValue_SomeonePushedFirst(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)

	frontendChange, _, _ := SetYAMLValue([]byte(valuesDev), "frontend.image.tag", "f00ba47", steps.ValueString)
	var attempts []int
	repo.beforePush = func(attempt int) {
		attempts = append(attempts, attempt)
		if attempt == 1 {
			rem.push(map[string]string{valuesFile: string(frontendChange)}, "someone else deploys frontend")
		}
	}

	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}
	sha, changed, err := repo.SetValue(ctx, ref, "e4f5a6b", steps.ValueString, deployMsg("req-1"))
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("push attempts = %v, want [1 2]", attempts)
	}

	head, content := remoteHead(t, rem, valuesFile)
	if head.Hash.String() != sha {
		t.Fatalf("remote tip %s, SetValue returned %s", head.Hash, sha)
	}
	backend, _ := GetYAMLValue([]byte(content), "backend.image.tag")
	frontend, _ := GetYAMLValue([]byte(content), "frontend.image.tag")
	if backend != "e4f5a6b" || frontend != "f00ba47" {
		t.Fatalf("backend=%s frontend=%s: one change was lost", backend, frontend)
	}
}

func TestSetValue_BranchKeepsMoving(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)
	n := 0
	repo.beforePush = func(int) {
		n++
		rem.push(map[string]string{"noise.txt": fmt.Sprint(n)}, "busy branch")
	}

	ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: "backend.image.tag"}
	_, _, err := repo.SetValue(ctx, ref, "e4f5a6b", steps.ValueString, deployMsg("req-1"))
	if err == nil || !steps.IsRetryable(err) {
		t.Fatalf("want a retryable error after %d attempts, got %v", maxPushAttempts, err)
	}
	if n != maxPushAttempts {
		t.Fatalf("attempts = %d, want %d", n, maxPushAttempts)
	}
}

func TestSetValue_Errors(t *testing.T) {
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesDev})
	repo := newTestRepo(t)
	ref := func(file, key string) steps.ValueRef {
		return steps.ValueRef{Repo: rem.URL, Branch: "main", File: file, Key: key}
	}

	t.Run("missing key", func(t *testing.T) {
		_, _, err := repo.SetValue(ctx, ref(valuesFile, "backend.image.digest"), "x", steps.ValueString, deployMsg("r"))
		checkErr(t, err, steps.ErrNotFound)
	})
	t.Run("missing file", func(t *testing.T) {
		_, _, err := repo.SetValue(ctx, ref("charts/nope.yaml", "a"), "x", steps.ValueString, deployMsg("r"))
		checkErr(t, err, steps.ErrNotFound)
	})
	t.Run("no request ID", func(t *testing.T) {
		_, _, err := repo.SetValue(ctx, ref(valuesFile, "backend.image.tag"), "x", steps.ValueString, steps.CommitMessage{Subject: "s"})
		checkErr(t, err, errAny)
	})
	t.Run("newline in subject", func(t *testing.T) {
		m := deployMsg("r")
		m.Subject = "deploy\n\nDeployctl-Request: someone-else"
		_, _, err := repo.SetValue(ctx, ref(valuesFile, "backend.image.tag"), "x", steps.ValueString, m)
		checkErr(t, err, errAny)
	})
}

// valuesMany has one key per writer, so concurrent writers never touch
// the same value but always touch the same file.
func valuesMany(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "app%d:\n  image:\n    tag: old   # managed by deployctl\n", i)
	}
	return b.String()
}

// Several worker goroutines share one Repo (and one clone). The locks must
// keep them from corrupting the clone; run with -race.
func TestSetValue_ConcurrentSameRepo(t *testing.T) {
	const writers = 8
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesMany(writers)})
	repo := newTestRepo(t)

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: fmt.Sprintf("app%d.image.tag", i)}
			_, _, err := repo.SetValue(ctx, ref, fmt.Sprintf("new%d", i), steps.ValueString, deployMsg(fmt.Sprintf("req-%d", i)))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	checkAllSet(t, rem, writers)
}

// Two worker pods: two Repos with separate clones push to the same branch
// at the same time. Git's non-fast-forward rejection plus re-apply must
// keep every change.
func TestSetValue_ConcurrentTwoWorkers(t *testing.T) {
	const writers = 6
	ctx := context.Background()
	rem := newRemote(t, map[string]string{valuesFile: valuesMany(writers)})
	pods := []*Repo{newTestRepo(t), newTestRepo(t)}

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref := steps.ValueRef{Repo: rem.URL, Branch: "main", File: valuesFile, Key: fmt.Sprintf("app%d.image.tag", i)}
			_, _, err := pods[i%2].SetValue(ctx, ref, fmt.Sprintf("new%d", i), steps.ValueString, deployMsg(fmt.Sprintf("req-%d", i)))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	checkAllSet(t, rem, writers)
}

func checkAllSet(t *testing.T, rem *remote, n int) {
	t.Helper()
	_, content := remoteHead(t, rem, valuesFile)
	for i := 0; i < n; i++ {
		v, err := GetYAMLValue([]byte(content), fmt.Sprintf("app%d.image.tag", i))
		if err != nil || v != fmt.Sprintf("new%d", i) {
			t.Errorf("app%d.image.tag = %q (%v): a change was lost", i, v, err)
		}
	}
}
