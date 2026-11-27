package steps_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// prodReq is a production deploy that has not been decided yet.
func prodReq(tag string) steps.Request {
	r := deployReq(tag)
	r.Environment, r.Target = "prod", prodTarget
	return r
}

func TestApprovalGate(t *testing.T) {
	approved := prodReq("e4f5a6b")
	approved.ApprovedBy = "priya@example.com"

	withWarnings := func(n int) steps.Input {
		in := validated(prodReq("e4f5a6b"), "9f8e7d6", "e4f5a6b")
		for i := 0; i < n; i++ {
			in.Outputs.Warnings = append(in.Outputs.Warnings, "app is Degraded before this change")
		}
		return in
	}

	tests := []struct {
		name    string
		in      steps.Input
		outcome steps.Outcome
		message string
	}{
		{
			name:    "undecided: park the request",
			in:      validated(prodReq("e4f5a6b"), "9f8e7d6", "e4f5a6b"),
			outcome: steps.Waiting,
			message: "waiting for approval: 9f8e7d6 → e4f5a6b",
		},
		{
			name:    "one warning is mentioned",
			in:      withWarnings(1),
			outcome: steps.Waiting,
			message: "waiting for approval: 9f8e7d6 → e4f5a6b (1 warning)",
		},
		{
			name:    "several warnings are counted",
			in:      withWarnings(2),
			outcome: steps.Waiting,
			message: "waiting for approval: 9f8e7d6 → e4f5a6b (2 warnings)",
		},
		{
			name: "scale waits the same way",
			in: func() steps.Input {
				r := scaleReq(5)
				r.Environment, r.Target = "prod", prodTarget
				return validated(r, "3", "5")
			}(),
			outcome: steps.Waiting,
			message: "waiting for approval: 3 → 5",
		},
		{
			name:    "already approved: the gate is open",
			in:      validated(approved, "9f8e7d6", "e4f5a6b"),
			outcome: steps.Succeeded,
			message: "approved by priya@example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := steps.ApprovalGate{}.Run(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tt.outcome || res.Message != tt.message {
				t.Fatalf("outcome=%s message=%q, want %s %q", res.Outcome, res.Message, tt.outcome, tt.message)
			}
			if !reflect.DeepEqual(res.Outputs, tt.in.Outputs) {
				t.Fatalf("outputs changed:\n got %+v\nwant %+v", res.Outputs, tt.in.Outputs)
			}
		})
	}
}

// A worker that crashed after Run but before storing the result runs the
// gate again. Nothing happened the first time, so the answer is the same.
func TestApprovalGate_RerunAfterCrash(t *testing.T) {
	in := validated(prodReq("e4f5a6b"), "9f8e7d6", "e4f5a6b")
	first, err := steps.ApprovalGate{}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Attempt = 2
	second, err := steps.ApprovalGate{}.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first %+v, second %+v", first, second)
	}
}

func TestApprovalGate_Errors(t *testing.T) {
	tests := []struct {
		name string
		in   steps.Input
		want string
	}{
		{
			name: "environment does not require approval",
			in:   validated(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b"),
			want: "backend/dev does not require approval",
		},
		{
			name: "validate has not run",
			in:   steps.Input{Request: prodReq("e4f5a6b"), Attempt: 1},
			want: "validate must run first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := steps.ApprovalGate{}.Run(context.Background(), tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if steps.IsRetryable(err) {
				t.Fatalf("%v should be permanent", err)
			}
		})
	}
}
