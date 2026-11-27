package steps_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"github.com/TheTangentLine/deployctl/internal/steps/fakes"
)

func TestFinalize(t *testing.T) {
	tests := []struct {
		name    string
		in      steps.Input
		want    *steps.Deployment
		message string
	}{
		{
			name: "deploy is recorded",
			in:   pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"),
			want: &steps.Deployment{
				App: "backend", Environment: "dev", Tag: "e4f5a6b", CommitSHA: ourSHA, RequestID: "req-1",
			},
			message: "deployed e4f5a6b at 3c2d1e0",
		},
		{
			name: "rollback is recorded too, so it can be rolled back from",
			in:   pushed(rollbackReq(), "e4f5a6b", "a1b2c3d"),
			want: &steps.Deployment{
				App: "backend", Environment: "dev", Tag: "a1b2c3d", CommitSHA: ourSHA, RequestID: "req-1",
			},
			message: "rolled back to a1b2c3d at 3c2d1e0",
		},
		{
			name:    "scale records no deployment",
			in:      pushed(scaleReq(5), "3", "5"),
			want:    nil,
			message: "scaled to 5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := steps.Finalize{}.Run(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != steps.Succeeded || res.Message != tt.message {
				t.Fatalf("outcome=%s message=%q, want succeeded %q", res.Outcome, res.Message, tt.message)
			}
			if !reflect.DeepEqual(res.Outputs.Deployment, tt.want) {
				t.Fatalf("deployment = %+v, want %+v", res.Outputs.Deployment, tt.want)
			}
		})
	}
}

// Earlier steps' outputs are passed through untouched.
func TestFinalize_KeepsEarlierOutputs(t *testing.T) {
	in := pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")
	in.Outputs.Warnings = []string{"app is OutOfSync with Git before this change"}

	res, err := steps.Finalize{}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Outputs
	got.Deployment = nil
	if !reflect.DeepEqual(got, in.Outputs) {
		t.Fatalf("outputs changed:\n got %+v\nwant %+v", got, in.Outputs)
	}
}

// A worker that crashed after Run but before storing the result runs
// finalize again and must ask for exactly the same row; the worker's
// insert ignores the second one for the same request.
func TestFinalize_RerunAfterCrash(t *testing.T) {
	in := pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")
	first, err := steps.Finalize{}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Attempt = 2
	second, err := steps.Finalize{}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first %+v, second %+v", first, second)
	}
}

// What finalize records is what a later rollback picks: the newest
// recorded tag that differs from the one Git has now.
func TestFinalize_FeedsRollback(t *testing.T) {
	history := &fakes.History{Tags: map[string][]string{}}
	record := func(from, to string) {
		t.Helper()
		res, err := steps.Finalize{}.Run(context.Background(), pushed(deployReq(to), from, to))
		if err != nil {
			t.Fatal(err)
		}
		d := res.Outputs.Deployment
		key := d.App + "/" + d.Environment
		history.Tags[key] = append([]string{d.Tag}, history.Tags[key]...) // newest first
	}
	record("9f8e7d6", "a1b2c3d")
	record("a1b2c3d", "e4f5a6b")

	git := &fakes.Git{Values: map[steps.ValueRef]string{tagRef(devTarget): "e4f5a6b"}}
	v := steps.Validate{Git: git, Argo: argoWith(healthy), History: history}
	res, err := v.Run(context.Background(), steps.Input{Request: rollbackReq(), Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outputs.From != "e4f5a6b" || res.Outputs.To != "a1b2c3d" {
		t.Fatalf("rollback %s → %s, want e4f5a6b → a1b2c3d", res.Outputs.From, res.Outputs.To)
	}
}

func TestFinalize_Errors(t *testing.T) {
	unknown := pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")
	unknown.Request.Kind = "restart"

	tests := []struct {
		name string
		in   steps.Input
		want string
	}{
		{name: "git_commit has not run", in: validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"), want: "git_commit must run first"},
		{name: "no target value", in: steps.Input{Request: deployReq("e4f5a6b"), Outputs: steps.Outputs{CommitSHA: ourSHA}}, want: "git_commit must run first"},
		{name: "unknown kind", in: unknown, want: `unknown request kind "restart"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := steps.Finalize{}.Run(context.Background(), tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if steps.IsRetryable(err) {
				t.Fatalf("%v should be permanent", err)
			}
		})
	}
}
