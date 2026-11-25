package steps_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"github.com/TheTangentLine/deployctl/internal/steps/fakes"
)

// world is the fake outside world a validate run sees.
type world struct {
	tag, replicas string           // current values in Git
	argo          steps.AppState   // Argo CD state
	autoscaled    bool             // an HPA targets the app
	history       []string         // successful deploys, newest first
	errs          map[string]error // "Git.GetValue", "Argo.Get", ...
}

func (w world) validate() (steps.Validate, *fakes.Git) {
	git := &fakes.Git{Values: map[steps.ValueRef]string{}, Err: map[string]error{}}
	if w.tag != "" {
		git.Values[tagRef(devTarget)] = w.tag
	}
	if w.replicas != "" {
		git.Values[replicasRef(devTarget)] = w.replicas
	}
	argo := &fakes.Argo{States: map[string][]steps.AppState{devTarget.ArgoApp: {w.argo}}, Err: map[string]error{}}
	autoscaling := &fakes.Autoscaling{Autoscaled: w.autoscaled}
	hist := &fakes.History{Tags: map[string][]string{"backend/dev": w.history}}
	for name, err := range w.errs {
		switch {
		case strings.HasPrefix(name, "Git."):
			git.Err[strings.TrimPrefix(name, "Git.")] = err
		case strings.HasPrefix(name, "Argo."):
			argo.Err[strings.TrimPrefix(name, "Argo.")] = err
		case name == "Autoscaling.IsAutoscaled":
			autoscaling.Err = err
		case name == "History":
			hist.Err = err
		}
	}
	return steps.Validate{Git: git, Argo: argo, Autoscaling: autoscaling, History: hist}, git
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		req          steps.Request
		world        world
		wantOutcome  steps.Outcome
		wantFrom     string
		wantTo       string
		wantWarnings int
	}{
		{
			name:        "deploy",
			req:         deployReq("e4f5a6b"),
			world:       world{tag: "a1b2c3d", argo: healthy},
			wantOutcome: steps.Succeeded, wantFrom: "a1b2c3d", wantTo: "e4f5a6b",
		},
		{
			name:        "deploy the tag already in Git",
			req:         deployReq("a1b2c3d"),
			world:       world{tag: "a1b2c3d", argo: healthy},
			wantOutcome: steps.NoChange, wantFrom: "a1b2c3d", wantTo: "a1b2c3d",
		},
		{
			name:        "deploy to a degraded app warns but goes ahead",
			req:         deployReq("e4f5a6b"),
			world:       world{tag: "a1b2c3d", argo: steps.AppState{SyncStatus: "Synced", Health: "Degraded", HealthMessage: "progress deadline exceeded"}},
			wantOutcome: steps.Succeeded, wantFrom: "a1b2c3d", wantTo: "e4f5a6b", wantWarnings: 1,
		},
		{
			name:        "out of sync and missing: two warnings",
			req:         deployReq("e4f5a6b"),
			world:       world{tag: "a1b2c3d", argo: steps.AppState{SyncStatus: "OutOfSync", Health: "Missing"}},
			wantOutcome: steps.Succeeded, wantFrom: "a1b2c3d", wantTo: "e4f5a6b", wantWarnings: 2,
		},
		{
			name:        "scale",
			req:         scaleReq(5),
			world:       world{replicas: "3", argo: healthy},
			wantOutcome: steps.Succeeded, wantFrom: "3", wantTo: "5",
		},
		{
			name:        "scale to zero",
			req:         scaleReq(0),
			world:       world{replicas: "3", argo: healthy},
			wantOutcome: steps.Succeeded, wantFrom: "3", wantTo: "0",
		},
		{
			name:        "rollback after a failed deploy: newest good tag",
			req:         rollbackReq(),
			world:       world{tag: "f00ba47", argo: healthy, history: []string{"e4f5a6b", "a1b2c3d"}},
			wantOutcome: steps.Succeeded, wantFrom: "f00ba47", wantTo: "e4f5a6b",
		},
		{
			name:        "rollback of a deploy that passed health checks: skips the current tag",
			req:         rollbackReq(),
			world:       world{tag: "c0ffee1", argo: healthy, history: []string{"c0ffee1", "e4f5a6b"}},
			wantOutcome: steps.Succeeded, wantFrom: "c0ffee1", wantTo: "e4f5a6b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, git := tt.world.validate()
			in := steps.Input{Request: tt.req, Attempt: 1}

			res, err := v.Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tt.wantOutcome || res.Outputs.From != tt.wantFrom || res.Outputs.To != tt.wantTo {
				t.Fatalf("got %s %q -> %q, want %s %q -> %q",
					res.Outcome, res.Outputs.From, res.Outputs.To, tt.wantOutcome, tt.wantFrom, tt.wantTo)
			}
			if len(res.Outputs.Warnings) != tt.wantWarnings {
				t.Fatalf("warnings = %q, want %d", res.Outputs.Warnings, tt.wantWarnings)
			}
			if res.Message == "" {
				t.Fatal("empty message")
			}
			if git.Pushes != 0 || len(git.Commits) != 0 {
				t.Fatal("validate must not change Git")
			}
		})
	}
}

func TestValidate_KeepsEarlierOutputs(t *testing.T) {
	v, _ := world{tag: "a1b2c3d", argo: healthy}.validate()
	in := steps.Input{Request: deployReq("e4f5a6b"), Outputs: steps.Outputs{Warnings: []string{"earlier"}}}
	res, err := v.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Outputs.Warnings, []string{"earlier"}) {
		t.Fatalf("warnings = %q", res.Outputs.Warnings)
	}
}

func TestValidate_Errors(t *testing.T) {
	retryable := steps.Retryable(io.ErrUnexpectedEOF)
	noScaleKey := scaleReq(5)
	noScaleKey.Target.ReplicasKey = ""

	tests := []struct {
		name          string
		req           steps.Request
		world         world
		wantRetryable bool
		wantNotFound  bool
		wantIn        string // substring of the error message
	}{
		{name: "Git unreachable is retried", req: deployReq("e4f5a6b"),
			world: world{tag: "a1b2c3d", argo: healthy, errs: map[string]error{"Git.GetValue": retryable}}, wantRetryable: true},
		{name: "Argo CD unreachable is retried", req: deployReq("e4f5a6b"),
			world: world{tag: "a1b2c3d", argo: healthy, errs: map[string]error{"Argo.Get": retryable}}, wantRetryable: true},
		{name: "values key missing", req: deployReq("e4f5a6b"),
			world: world{argo: healthy}, wantNotFound: true},
		{name: "Argo CD app missing", req: deployReq("e4f5a6b"),
			world: world{tag: "a1b2c3d", argo: healthy, errs: map[string]error{"Argo.Get": steps.ErrNotFound}}, wantNotFound: true, wantIn: "reconcile-dev"},
		{name: "autoscaled app cannot be scaled", req: scaleReq(5),
			world: world{replicas: "3", argo: healthy, autoscaled: true}, wantIn: "HPA"},
		{name: "autoscaling reader transient error", req: scaleReq(5),
			world: world{replicas: "3", errs: map[string]error{"Autoscaling.IsAutoscaled": retryable}}, wantRetryable: true},
		{name: "autoscaling reader permanent error", req: scaleReq(5),
			world: world{replicas: "3", errs: map[string]error{"Autoscaling.IsAutoscaled": errors.New("forbidden")}}, wantIn: "forbidden"},
		{name: "app without replicasKey cannot be scaled", req: noScaleKey,
			world: world{replicas: "3", argo: healthy}, wantIn: "does not support scale"},
		{name: "negative replicas", req: scaleReq(-1),
			world: world{replicas: "3", argo: healthy}, wantIn: "0 or more"},
		{name: "deploy without a tag", req: deployReq(""),
			world: world{tag: "a1b2c3d", argo: healthy}, wantIn: "needs a tag"},
		{name: "rollback with no history", req: rollbackReq(),
			world: world{tag: "a1b2c3d", argo: healthy}, wantIn: "no earlier successful deployment"},
		{name: "rollback when the only good tag is the current one", req: rollbackReq(),
			world: world{tag: "a1b2c3d", argo: healthy, history: []string{"a1b2c3d"}}, wantIn: "no earlier successful deployment"},
		{name: "history unavailable is retried", req: rollbackReq(),
			world: world{tag: "a1b2c3d", argo: healthy, errs: map[string]error{"History": retryable}}, wantRetryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := tt.world.validate()
			_, err := v.Run(context.Background(), steps.Input{Request: tt.req, Attempt: 1})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := steps.IsRetryable(err); got != tt.wantRetryable {
				t.Fatalf("retryable = %v, want %v (%v)", got, tt.wantRetryable, err)
			}
			if got := errors.Is(err, steps.ErrNotFound); got != tt.wantNotFound {
				t.Fatalf("not found = %v, want %v (%v)", got, tt.wantNotFound, err)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("error %q should mention %q", err, tt.wantIn)
			}
		})
	}
}

func TestValidate_OptionalAutoscalingReader(t *testing.T) {
	for _, req := range []steps.Request{deployReq("v2"), rollbackReq(), scaleReq(5)} {
		t.Run(string(req.Kind), func(t *testing.T) {
			v, git := world{tag: "v1", replicas: "3", argo: healthy, history: []string{"v0"}}.validate()
			v.Autoscaling = nil
			_, err := v.Run(context.Background(), steps.Input{Request: req})
			if req.Kind != steps.KindScale {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "no autoscaling reader") || steps.IsRetryable(err) {
				t.Fatalf("scale without HPA reader: %v", err)
			}
			if len(git.Commits) != 0 {
				t.Fatal("validation wrote Git")
			}
		})
	}
}
