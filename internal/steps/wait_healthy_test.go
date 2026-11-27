package steps_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"github.com/TheTangentLine/deployctl/internal/steps/fakes"
)

// ---- fixtures ----

const image = "registry.example.com/reconcile-backend:"

// pod builds a backend pod on tag. Pods on the same tag share a ReplicaSet.
func pod(name, tag string, ready bool, restarts int32, reason string) steps.Pod {
	return steps.Pod{
		UID: "uid-" + name, Name: name, Ready: ready, Phase: "Running", Restarts: restarts,
		Owner:  &steps.ObjectRef{Kind: "ReplicaSet", Name: "reconcile-backend-" + tag},
		Images: []string{image + tag},
		Containers: []steps.Container{{
			Name: "backend", Image: image + tag, Ready: ready, Restarts: restarts, Reason: reason,
			State: steps.ContainerRunning,
		}},
	}
}

func old(name string) steps.Pod      { return pod(name, "a1b2c3d", true, 0, "") }
func newReady(name string) steps.Pod { return pod(name, "e4f5a6b", true, 0, "") }
func newStarting(name string) steps.Pod {
	return pod(name, "e4f5a6b", false, 0, "")
}

func synced(rev string) steps.AppState {
	return steps.AppState{SyncStatus: "Synced", Health: "Healthy", SyncRevision: rev, AutoSync: true}
}

// clock is a fake clock: Sleep moves time forward instantly.
type clock struct {
	now    time.Time
	sleeps int
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time { return c.now }
func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps++
	c.now = c.now.Add(d)
	return nil
}

// run executes wait_healthy for a deploy of e4f5a6b with scripted Argo CD
// states and pod snapshots, and returns what it reported along the way.
func run(t *testing.T, argo *fakes.Argo, kube *fakes.Kube, in steps.Input, clk *clock) (steps.Result, error, []string) {
	t.Helper()
	var reports []string
	in.Progress = func(msg string) { reports = append(reports, msg) }
	if in.FirstStartedAt.IsZero() {
		in.FirstStartedAt = clk.now
	}
	w := steps.WaitHealthy{Argo: argo, Kube: kube, Now: clk.Now, Sleep: clk.Sleep}
	res, err := w.Run(context.Background(), in)
	return res, err, reports
}

func kubeWith(snapshots ...[]steps.Pod) *fakes.Kube {
	return &fakes.Kube{PodSnapshots: snapshots, Err: map[string]error{}, LogText: map[string]string{}}
}

func deployIn() steps.Input { return pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b") }

// ---- success ----

// A whole rolling update, one check at a time.
func TestWaitHealthy_RollingUpdate(t *testing.T) {
	argo := argoWith(
		synced(oldSHA), // Argo CD has not seen our commit yet
		steps.AppState{SyncStatus: "Synced", Health: "Progressing", SyncRevision: ourSHA},
		synced(ourSHA),
	)
	argo.ImageSets = map[fakes.ImageQuery]map[string]string{
		{App: devTarget.ArgoApp, Revision: oldSHA, Namespace: devTarget.Namespace, Selector: devTarget.Selector}: {"backend": image + "a1b2c3d"},
	}
	kube := kubeWith(
		[]steps.Pod{old("p1"), old("p2"), old("p3")},
		[]steps.Pod{old("p1"), old("p2"), newStarting("n1")},
		[]steps.Pod{old("p1"), newReady("n1"), newReady("n2")},
		[]steps.Pod{newReady("n1"), newReady("n2"), newReady("n3")},
	)
	clk := newClock()

	res, err, reports := run(t, argo, kube, deployIn(), clk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != steps.Succeeded || res.Message != "3/3 pods ready on e4f5a6b" {
		t.Fatalf("outcome=%s message=%q", res.Outcome, res.Message)
	}
	if clk.sleeps != 3 {
		t.Fatalf("checked %d times, want 4", clk.sleeps+1)
	}
	want := []string{
		"0/3 pods ready on e4f5a6b, waiting for Argo CD to pick up 3c2d1e0",
		"0/3 pods ready on e4f5a6b, Argo CD Synced/Progressing",
		"2/3 pods ready on e4f5a6b",
	}
	if strings.Join(reports, "\n") != strings.Join(want, "\n") {
		t.Fatalf("progress:\n%s\nwant:\n%s", strings.Join(reports, "\n"), strings.Join(want, "\n"))
	}
}

// Right after the push Argo CD still says Synced and Healthy, about the OLD
// commit. Even with every pod already on the new tag, the step must wait
// until Argo CD has synced a revision that contains the change.
func TestWaitHealthy_NotFooledByOldRevision(t *testing.T) {
	argo := argoWith(synced(oldSHA), synced(oldSHA), synced(ourSHA))
	argo.ImageSets = map[fakes.ImageQuery]map[string]string{
		{App: devTarget.ArgoApp, Revision: oldSHA, Namespace: devTarget.Namespace, Selector: devTarget.Selector}: {"backend": image + "a1b2c3d"},
	}
	kube := kubeWith([]steps.Pod{newReady("n1"), newReady("n2")})
	clk := newClock()

	if _, err, _ := run(t, argo, kube, deployIn(), clk); err != nil {
		t.Fatal(err)
	}
	if clk.sleeps != 2 {
		t.Fatalf("finished after %d checks, want 3 (only once Argo CD synced our commit)", clk.sleeps+1)
	}
}

// Someone pushed after us and Argo CD synced their commit. It still carries
// our tag, so our change is live.
func TestWaitHealthy_LaterCommitStillHasOurTag(t *testing.T) {
	argo := argoWith(synced(laterSHA))
	argo.ImageSets = map[fakes.ImageQuery]map[string]string{
		{App: devTarget.ArgoApp, Revision: laterSHA, Namespace: devTarget.Namespace, Selector: devTarget.Selector}: {
			"backend": image + "e4f5a6b", "proxy": "envoyproxy/envoy:v1.31.0",
		},
	}
	kube := kubeWith([]steps.Pod{newReady("n1")})

	res, err, _ := run(t, argo, kube, deployIn(), newClock())
	if err != nil || res.Outcome != steps.Succeeded {
		t.Fatalf("outcome=%s err=%v", res.Outcome, err)
	}
}

func TestWaitHealthy_IgnoresTerminatingPods(t *testing.T) {
	leaving := old("p1")
	leaving.Terminating = true
	res, err, _ := run(t, argoWith(synced(ourSHA)), kubeWith([]steps.Pod{leaving, newReady("n1"), newReady("n2")}), deployIn(), newClock())
	if err != nil || res.Message != "2/2 pods ready on e4f5a6b" {
		t.Fatalf("message=%q err=%v", res.Message, err)
	}
}

func TestWaitHealthy_Scale(t *testing.T) {
	scaleIn := pushed(scaleReq(5), "3", "5")
	threeOf := func(n int) []steps.Pod {
		var p []steps.Pod
		for i := 0; i < n; i++ {
			p = append(p, old(string(rune('a'+i))))
		}
		return p
	}

	t.Run("up", func(t *testing.T) {
		kube := kubeWith(threeOf(3), append(threeOf(3), pod("x", "a1b2c3d", false, 0, ""), pod("y", "a1b2c3d", false, 0, "")), threeOf(5))
		res, err, _ := run(t, argoWith(synced(ourSHA)), kube, scaleIn, newClock())
		if err != nil || res.Message != "5/5 pods ready" {
			t.Fatalf("message=%q err=%v", res.Message, err)
		}
	})
	t.Run("to zero", func(t *testing.T) {
		in := pushed(scaleReq(0), "3", "0")
		res, err, _ := run(t, argoWith(synced(ourSHA)), kubeWith(threeOf(3), nil), in, newClock())
		if err != nil || res.Message != "0/0 pods ready" {
			t.Fatalf("message=%q err=%v", res.Message, err)
		}
	})
	t.Run("a later commit was synced: the pod count is the evidence", func(t *testing.T) {
		res, err, _ := run(t, argoWith(synced(laterSHA)), kubeWith(threeOf(5)), scaleIn, newClock())
		if err != nil || res.Outcome != steps.Succeeded {
			t.Fatalf("outcome=%s err=%v", res.Outcome, err)
		}
	})
}

// ---- failing early ----

func TestWaitHealthy_CrashLoopFailsEarlyWithDiagnostics(t *testing.T) {
	kube := kubeWith(
		[]steps.Pod{old("p1"), pod("n1", "e4f5a6b", false, 0, "")},
		[]steps.Pod{old("p1"), pod("n1", "e4f5a6b", false, 1, "CrashLoopBackOff")},
		[]steps.Pod{old("p1"), pod("n1", "e4f5a6b", false, 3, "CrashLoopBackOff")},
	)
	kube.EventList = []steps.Event{
		{Object: "n1", Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container backend"},
		{Object: "reconcile-backend-e4f5a6b", Type: "Normal", Reason: "SuccessfulCreate", Message: "Created pod: n1"},
		{Object: "p1", Type: "Normal", Reason: "Pulled", Message: "unrelated healthy old pod"},
	}
	kube.LogText["n1/backend/previous"] = "starting\npanic: missing env INVOICE_ROUNDING_MODE\n"
	clk := newClock()

	res, err, _ := run(t, argoWith(synced(ourSHA)), kube, deployIn(), clk)
	if err == nil || !strings.Contains(err.Error(), "pod n1 restarted 3 times") || steps.IsRetryable(err) {
		t.Fatalf("err = %v, want a permanent crash-loop failure", err)
	}
	if clk.sleeps != 2 {
		t.Fatalf("failed after %d checks, want 3: should not wait for the timeout", clk.sleeps+1)
	}

	d := res.Outputs.Diagnostics
	if d == nil {
		t.Fatal("no diagnostics")
	}
	if len(d.Events) != 2 {
		t.Fatalf("events = %+v, want the failing pod's and its ReplicaSet's", d.Events)
	}
	if !strings.Contains(d.Logs["n1/backend (previous)"], "panic: missing env INVOICE_ROUNDING_MODE") {
		t.Fatalf("logs = %q", d.Logs)
	}
	if res.Outputs.CommitSHA != ourSHA {
		t.Fatal("earlier outputs lost on failure")
	}
}

// Diagnostics are stored with the step, so all of them together stay
// within 64 KiB, however long the logs are. The end of each log is kept,
// because that is where the crash is.
func TestWaitHealthy_DiagnosticsAreCapped(t *testing.T) {
	crashing := func(restarts int32) []steps.Pod {
		return []steps.Pod{
			pod("n1", "e4f5a6b", false, restarts, "CrashLoopBackOff"),
			pod("n2", "e4f5a6b", false, restarts, "CrashLoopBackOff"),
			pod("n3", "e4f5a6b", false, restarts, "CrashLoopBackOff"),
		}
	}
	kube := kubeWith(crashing(0), crashing(3))
	long := strings.Repeat("noise noise noise noise noise noise noise noise\n", 900) // ~43 KiB
	for _, p := range []string{"n1", "n2", "n3"} {
		kube.LogText[p+"/backend/previous"] = long + "panic: " + p + " cannot reach the database\n"
	}
	argo := argoWith(steps.AppState{SyncStatus: "Synced", Health: "Progressing", SyncRevision: ourSHA,
		HealthMessage: strings.Repeat("é", 200)})

	res, err, _ := run(t, argo, kube, deployIn(), newClock())
	if err == nil || !strings.Contains(err.Error(), "restarted 3 times") {
		t.Fatalf("err = %v, want a crash-loop failure", err)
	}
	d := res.Outputs.Diagnostics
	size := len(d.HealthMessage)
	for _, e := range d.Events {
		size += len(e.Object) + len(e.Type) + len(e.Reason) + len(e.Message)
	}
	for k, v := range d.Logs {
		size += len(k) + len(v)
		if !strings.HasSuffix(v, "cannot reach the database\n") {
			t.Fatalf("%s lost the end of its log", k)
		}
		if !strings.HasPrefix(v, "noise") && !strings.HasPrefix(v, "panic") {
			t.Fatalf("%s starts mid-line: %q", k, v[:20])
		}
	}
	if size > 64<<10 {
		t.Fatalf("diagnostics are %d bytes, want at most %d", size, 64<<10)
	}
	if len(d.Logs) == 0 || !strings.Contains(d.Logs["n1/backend (previous)"], "panic: n1") {
		t.Fatalf("the first failing pod's log must be kept, got keys %v", keys(d.Logs))
	}
}

func keys(m map[string]string) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	return k
}

// A pod that has been crashing since before this step started does not
// count against it: only restarts seen during the step do.
func TestWaitHealthy_RestartsCountFromFirstSight(t *testing.T) {
	kube := kubeWith(
		[]steps.Pod{pod("n1", "e4f5a6b", false, 7, "")},
		[]steps.Pod{pod("n1", "e4f5a6b", true, 7, "")},
	)
	if _, err, _ := run(t, argoWith(synced(ourSHA)), kube, deployIn(), newClock()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitHealthy_ImagePullFailsAfterStuckPolls(t *testing.T) {
	pullErr := pod("n1", "e4f5a6b", false, 0, "ImagePullBackOff")
	pullErr.Containers[0].Message = `Back-off pulling image "registry.example.com/reconcile-backend:e4f5a6b"`

	t.Run("stuck", func(t *testing.T) {
		clk := newClock()
		_, err, _ := run(t, argoWith(synced(ourSHA)), kubeWith([]steps.Pod{old("p1"), pullErr}), deployIn(), clk)
		if err == nil || !strings.Contains(err.Error(), "pod n1 cannot start: ImagePullBackOff") {
			t.Fatalf("err = %v", err)
		}
		if clk.sleeps != 2 {
			t.Fatalf("failed after %d checks, want 3", clk.sleeps+1)
		}
	})
	t.Run("a registry blip that recovers is not a failure", func(t *testing.T) {
		kube := kubeWith([]steps.Pod{old("p1"), pullErr}, []steps.Pod{old("p1"), pullErr}, []steps.Pod{newReady("n1")})
		if _, err, _ := run(t, argoWith(synced(ourSHA)), kube, deployIn(), newClock()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWaitHealthy_FailsImmediately(t *testing.T) {
	tests := []struct {
		name   string
		argo   steps.AppState
		pods   []steps.Pod
		wantIn string
	}{
		{
			name:   "invalid image name",
			argo:   synced(ourSHA),
			pods:   []steps.Pod{pod("n1", "e4f5a6b", false, 0, "InvalidImageName")},
			wantIn: "cannot start: InvalidImageName",
		},
		{
			name: "Argo CD sync of our commit failed",
			argo: steps.AppState{SyncStatus: "OutOfSync", Health: "Healthy", SyncRevision: ourSHA,
				Operation: &steps.SyncOperationState{Revision: ourSHA, Phase: "Failed", Message: "one or more objects failed to apply"}},
			pods:   []steps.Pod{old("p1")},
			wantIn: "Argo CD could not apply 3c2d1e0: one or more objects failed to apply",
		},
		{
			name:   "Degraded at our revision",
			argo:   steps.AppState{SyncStatus: "Synced", Health: "Degraded", SyncRevision: ourSHA, HealthMessage: "Deployment exceeded its progress deadline"},
			pods:   []steps.Pod{newStarting("n1")},
			wantIn: "Degraded: Deployment exceeded its progress deadline",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newClock()
			_, err, _ := run(t, argoWith(tt.argo), kubeWith(tt.pods), deployIn(), clk)
			if err == nil || !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("err = %v, want %q", err, tt.wantIn)
			}
			if clk.sleeps != 0 {
				t.Fatalf("waited %d times before failing", clk.sleeps)
			}
		})
	}
}

// A Degraded app before our change (validate warned about it) must not
// fail the step while Argo CD has not picked up our commit.
func TestWaitHealthy_DegradedBeforeOurChangeIsNotOurFailure(t *testing.T) {
	argo := argoWith(
		steps.AppState{SyncStatus: "Synced", Health: "Degraded", SyncRevision: oldSHA},
		synced(ourSHA),
	)
	argo.ImageSets = map[fakes.ImageQuery]map[string]string{
		{App: devTarget.ArgoApp, Revision: oldSHA, Namespace: devTarget.Namespace, Selector: devTarget.Selector}: {"backend": image + "a1b2c3d"},
	}
	if _, err, _ := run(t, argo, kubeWith([]steps.Pod{newReady("n1")}), deployIn(), newClock()); err != nil {
		t.Fatal(err)
	}
}

// ---- timeouts and outages ----

func TestWaitHealthy_Timeout(t *testing.T) {
	kube := kubeWith([]steps.Pod{old("p1"), newReady("n1"), newStarting("n2")})
	kube.LogText["n2/backend"] = "waiting for database...\n"
	argo := argoWith(steps.AppState{SyncStatus: "Synced", Health: "Progressing", SyncRevision: ourSHA, HealthMessage: "Waiting for rollout to finish"})
	clk := newClock()

	res, err, _ := run(t, argo, kube, deployIn(), clk)
	if err == nil || err.Error() != "timed out after 5m0s (1/3 pods ready on e4f5a6b)" || steps.IsRetryable(err) {
		t.Fatalf("err = %v", err)
	}
	if clk.sleeps != 60 { // 5 minutes of 5-second polls
		t.Fatalf("polled %d times", clk.sleeps)
	}
	d := res.Outputs.Diagnostics
	if d == nil || d.HealthMessage != "Waiting for rollout to finish" || d.Logs["n2/backend"] == "" {
		t.Fatalf("diagnostics = %+v", d)
	}
}

// After a worker crash the step restarts, but the clock does not.
func TestWaitHealthy_TimeoutCountsFromFirstStart(t *testing.T) {
	clk := newClock()
	in := deployIn()
	in.FirstStartedAt = clk.now.Add(-10 * time.Minute)

	_, err, _ := run(t, argoWith(synced(ourSHA)), kubeWith([]steps.Pod{newStarting("n1")}), in, clk)
	if err == nil || !strings.HasPrefix(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if clk.sleeps != 0 {
		t.Fatalf("waited %d times; the deadline had already passed", clk.sleeps)
	}
}

// A one-off retryable error from Argo CD or Kubernetes is ridden out.
func TestWaitHealthy_RidesOutBlips(t *testing.T) {
	argo := argoWith(synced(ourSHA))
	kube := kubeWith([]steps.Pod{newReady("n1")})
	clk := newClock()
	calls := 0
	w := steps.WaitHealthy{Argo: argo, Kube: kube, Now: clk.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		calls++
		delete(kube.Err, "Pods") // the outage ends after one check
		return clk.Sleep(ctx, d)
	}}
	kube.Err["Pods"] = steps.Retryable(io.ErrUnexpectedEOF)

	in := deployIn()
	in.FirstStartedAt = clk.now
	res, err := w.Run(context.Background(), in)
	if err != nil || res.Outcome != steps.Succeeded || calls != 1 {
		t.Fatalf("outcome=%s err=%v sleeps=%d", res.Outcome, err, calls)
	}
}

func TestWaitHealthy_OutageLongerThanTimeoutIsRetryable(t *testing.T) {
	argo := argoWith(synced(ourSHA))
	argo.Err["Get"] = steps.Retryable(io.ErrUnexpectedEOF)
	_, err, _ := run(t, argo, kubeWith(), deployIn(), newClock())
	if err == nil || !steps.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable so the worker tries again later", err)
	}
}

func TestWaitHealthy_PermanentClientErrorStopsAtOnce(t *testing.T) {
	argo := argoWith(synced(ourSHA))
	argo.Err["Get"] = errors.New("argocd HTTP 403: permission denied")
	clk := newClock()
	_, err, _ := run(t, argo, kubeWith(), deployIn(), clk)
	if err == nil || steps.IsRetryable(err) || clk.sleeps != 0 {
		t.Fatalf("err=%v sleeps=%d", err, clk.sleeps)
	}
}

func TestWaitHealthy_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clk := newClock()
	w := steps.WaitHealthy{Argo: argoWith(synced(ourSHA)), Kube: kubeWith([]steps.Pod{newStarting("n1")}), Now: clk.Now, Sleep: clk.Sleep}
	_, err := w.Run(ctx, deployIn())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWaitHealthy_NeedsCommit(t *testing.T) {
	in := deployIn()
	in.Outputs.CommitSHA = ""
	_, err, _ := run(t, argoWith(synced(ourSHA)), kubeWith(), in, newClock())
	if err == nil || !strings.Contains(err.Error(), "git_commit must run first") {
		t.Fatalf("err = %v", err)
	}
}
