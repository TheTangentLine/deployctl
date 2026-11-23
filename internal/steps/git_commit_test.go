package steps_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"github.com/TheTangentLine/deployctl/internal/steps/fakes"
)

// validated is the input git_commit gets after validate: From and To set.
func validated(req steps.Request, from, to string) steps.Input {
	return steps.Input{Request: req, Attempt: 1, Outputs: steps.Outputs{From: from, To: to}}
}

func TestGitCommit_Pushes(t *testing.T) {
	git := &fakes.Git{Values: map[steps.ValueRef]string{tagRef(devTarget): "a1b2c3d"}}
	res, err := steps.GitCommit{Git: git}.Run(context.Background(), validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != steps.Succeeded || git.Pushes != 1 {
		t.Fatalf("outcome=%s pushes=%d", res.Outcome, git.Pushes)
	}
	c := git.Commits[0]
	if res.Outputs.CommitSHA != c.SHA {
		t.Fatalf("CommitSHA = %q, want %q", res.Outputs.CommitSHA, c.SHA)
	}
	if git.Values[tagRef(devTarget)] != "e4f5a6b" {
		t.Fatalf("value = %q", git.Values[tagRef(devTarget)])
	}
	want := steps.CommitMessage{
		Subject:     "deploy(backend/dev): a1b2c3d -> e4f5a6b",
		RequestedBy: "linh@example.com",
		RequestID:   "req-1",
	}
	if c.Message != want {
		t.Fatalf("message = %+v, want %+v", c.Message, want)
	}
	if res.Outputs.From != "a1b2c3d" || res.Outputs.To != "e4f5a6b" {
		t.Fatal("git_commit must keep validate's outputs")
	}
	if !strings.HasPrefix(res.Message, "pushed ") {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestGitCommit_ScaleWritesReplicaCount(t *testing.T) {
	git := &fakes.Git{Values: map[steps.ValueRef]string{replicasRef(devTarget): "3"}}
	_, err := steps.GitCommit{Git: git}.Run(context.Background(), validated(scaleReq(5), "3", "5"))
	if err != nil {
		t.Fatal(err)
	}
	if git.Values[replicasRef(devTarget)] != "5" {
		t.Fatalf("replicas = %q", git.Values[replicasRef(devTarget)])
	}
	if s := git.Commits[0].Message.Subject; s != "scale(backend/dev): 3 -> 5" {
		t.Fatalf("subject = %q", s)
	}
}

// The crash-after-push case: an earlier attempt pushed, then the worker
// died before recording it. The re-run must find that commit and not push.
func TestGitCommit_RerunAfterCrashDoesNotPushTwice(t *testing.T) {
	git := &fakes.Git{
		Values: map[steps.ValueRef]string{tagRef(devTarget): "e4f5a6b"}, // the push landed
		Commits: []fakes.Commit{{
			SHA:     "3c2d1e0aaaa",
			Ref:     tagRef(devTarget),
			Value:   "e4f5a6b",
			Message: steps.CommitMessage{RequestID: "req-1"},
		}},
	}
	in := validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")
	in.Attempt = 2

	res, err := steps.GitCommit{Git: git}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if git.Pushes != 0 {
		t.Fatalf("pushed %d times on re-run", git.Pushes)
	}
	if res.Outcome != steps.Succeeded || res.Outputs.CommitSHA != "3c2d1e0aaaa" {
		t.Fatalf("outcome=%s sha=%q, want succeeded with the earlier commit", res.Outcome, res.Outputs.CommitSHA)
	}
	if res.Message != "found earlier push 3c2d1e0" {
		t.Fatalf("message = %q", res.Message)
	}
}

// Running the step twice in a row ends with exactly one commit.
func TestGitCommit_RunTwice(t *testing.T) {
	git := &fakes.Git{Values: map[steps.ValueRef]string{tagRef(devTarget): "a1b2c3d"}}
	step := steps.GitCommit{Git: git}
	in := validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")

	first, err := step.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := step.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if git.Pushes != 1 || first.Outputs.CommitSHA != second.Outputs.CommitSHA {
		t.Fatalf("pushes=%d first=%s second=%s", git.Pushes, first.Outputs.CommitSHA, second.Outputs.CommitSHA)
	}
}

// Someone else already set the value (not this request): nothing to push.
func TestGitCommit_AlreadySetBySomeoneElse(t *testing.T) {
	git := &fakes.Git{
		Values:  map[steps.ValueRef]string{tagRef(devTarget): "e4f5a6b"},
		Commits: []fakes.Commit{{SHA: "9999999", Message: steps.CommitMessage{RequestID: "another-request"}}},
	}
	res, err := steps.GitCommit{Git: git}.Run(context.Background(), validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != steps.Skipped || git.Pushes != 0 || res.Outputs.CommitSHA != "9999999" {
		t.Fatalf("outcome=%s pushes=%d sha=%q", res.Outcome, git.Pushes, res.Outputs.CommitSHA)
	}
}

func TestGitCommit_ApprovedProdChange(t *testing.T) {
	req := deployReq("e4f5a6b")
	req.Environment, req.Target, req.ApprovedBy = "prod", prodTarget, "priya@example.com"
	git := &fakes.Git{Values: map[steps.ValueRef]string{tagRef(prodTarget): "9f8e7d6"}}

	if _, err := (steps.GitCommit{Git: git}).Run(context.Background(), validated(req, "9f8e7d6", "e4f5a6b")); err != nil {
		t.Fatal(err)
	}
	m := git.Commits[0].Message
	if m.ApprovedBy != "priya@example.com" || m.Subject != "deploy(backend/prod): 9f8e7d6 -> e4f5a6b" {
		t.Fatalf("message = %+v", m)
	}
}

func TestGitCommit_Errors(t *testing.T) {
	unapproved := deployReq("e4f5a6b")
	unapproved.Environment, unapproved.Target = "prod", prodTarget

	tests := []struct {
		name          string
		in            steps.Input
		gitErr        map[string]error
		wantRetryable bool
		wantIn        string
	}{
		{name: "unapproved prod change is refused",
			in: validated(unapproved, "9f8e7d6", "e4f5a6b"), wantIn: "requires approval"},
		{name: "validate did not run",
			in: steps.Input{Request: deployReq("e4f5a6b")}, wantIn: "validate must run first"},
		{name: "trailer search fails: retried",
			in:     validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"),
			gitErr: map[string]error{"FindRequestCommit": steps.Retryable(io.EOF)}, wantRetryable: true},
		{name: "push fails: retried",
			in:     validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"),
			gitErr: map[string]error{"SetValue": steps.Retryable(io.EOF)}, wantRetryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &fakes.Git{
				Values: map[steps.ValueRef]string{tagRef(devTarget): "a1b2c3d", tagRef(prodTarget): "9f8e7d6"},
				Err:    tt.gitErr,
			}
			_, err := steps.GitCommit{Git: git}.Run(context.Background(), tt.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if steps.IsRetryable(err) != tt.wantRetryable {
				t.Fatalf("retryable = %v, want %v (%v)", steps.IsRetryable(err), tt.wantRetryable, err)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("error %q should mention %q", err, tt.wantIn)
			}
			if git.Pushes != 0 {
				t.Fatal("pushed despite the error")
			}
		})
	}
}
