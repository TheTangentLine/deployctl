package steps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WaitHealthy watches until the change is live and healthy, or fails with
// enough information to act. It checks every PollInterval until Timeout has
// passed since the step FIRST started (in.FirstStartedAt), so a worker crash
// does not restart the clock.
//
// The change is live and healthy when all of these hold at once:
//
//   - Argo CD reports Synced and Healthy;
//   - Argo CD's synced revision contains the change: it is the commit
//     git_commit pushed, or a later one whose desired images still carry the
//     new tag (someone pushed on top). Without this, the step could pass on
//     the OLD revision, which is also Synced and Healthy for a moment;
//   - every pod behind the selector (ignoring terminating ones) is ready,
//     and for deploy and rollback runs the new tag; for scale, the number of
//     ready pods equals the new count.
//
// It fails early, without waiting for the timeout, when Argo CD reports a
// failed sync of our commit or Degraded health for it, when a new pod's
// containers restarted MaxRestarts times since the step first saw the pod
// (crash loop), or when new pods stay unable to start (image cannot be
// pulled, container cannot be created) for StuckPolls checks in a row.
//
// On failure it collects diagnostics (Argo CD's health message, recent
// events of the failing pods and their ReplicaSets, and log tails of their
// failing containers) and returns them in the Result alongside the error.
//
// It never rolls back. Short outages of Argo CD or Kubernetes are ridden
// out within the timeout; if they last past it, the step returns the last
// retryable error so the worker can try again later.
type WaitHealthy struct {
	Argo ArgoClient
	Kube KubeReader

	PollInterval time.Duration // default 5s
	Timeout      time.Duration // default 5m (worker.rolloutTimeout)
	MaxRestarts  int32         // default 3
	StuckPolls   int           // default 3

	// Now and Sleep default to the real clock; tests replace them so no
	// test waits for real.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

var _ Step = WaitHealthy{}

func (WaitHealthy) Name() string { return NameWaitHealthy }

// Reasons a container cannot start that will not fix themselves in time.
var stuckReasons = []string{"ErrImagePull", "ImagePullBackOff", "CreateContainerConfigError", "CreateContainerError"}

// Reasons that are hopeless immediately.
var fatalReasons = []string{"InvalidImageName"}

func (w WaitHealthy) Run(ctx context.Context, in Input) (Result, error) {
	w = w.withDefaults()
	req := in.Request
	out := in.Outputs
	if out.CommitSHA == "" || out.To == "" {
		return Result{}, errors.New("wait_healthy: no commit or target value; git_commit must run first")
	}
	if req.Kind == KindScale {
		if _, err := strconv.Atoi(out.To); err != nil {
			return Result{}, fmt.Errorf("wait_healthy: scale target %q is not a number", out.To)
		}
	}

	start := in.FirstStartedAt
	if start.IsZero() {
		start = w.Now()
	}
	deadline := start.Add(w.Timeout)

	s := &watch{w: w, req: req, out: out, restarts: map[string]int32{}}
	var lastErr error
	for {
		v, err := s.check(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			return Result{}, ctx.Err()
		case err != nil && !IsRetryable(err):
			return Result{}, err
		case err != nil:
			lastErr = err // a blip: keep watching until the deadline
		default:
			lastErr = nil
			if v.failure != "" {
				return s.fail(ctx, v.failure)
			}
			if v.done {
				return Result{Outcome: Succeeded, Outputs: out, Message: v.progress}, nil
			}
			in.Report(v.progress)
		}

		if !w.Now().Before(deadline) {
			if lastErr != nil {
				return Result{}, fmt.Errorf("wait_healthy: still failing after %s: %w", w.Timeout, lastErr)
			}
			return s.fail(ctx, fmt.Sprintf("timed out after %s (%s)", w.Timeout, s.lastProgress))
		}
		if err := w.Sleep(ctx, w.PollInterval); err != nil {
			return Result{}, err
		}
	}
}

func (w WaitHealthy) withDefaults() WaitHealthy {
	if w.PollInterval <= 0 {
		w.PollInterval = 5 * time.Second
	}
	if w.Timeout <= 0 {
		w.Timeout = 5 * time.Minute
	}
	if w.MaxRestarts <= 0 {
		w.MaxRestarts = 3
	}
	if w.StuckPolls <= 0 {
		w.StuckPolls = 3
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Sleep == nil {
		w.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return w
}

// watch is the state carried between checks of one run.
type watch struct {
	w   WaitHealthy
	req Request
	out Outputs

	restarts     map[string]int32 // pod UID -> restarts when first seen
	stuck        int              // consecutive checks with a stuck new pod
	lastState    AppState
	lastPods     []Pod
	lastProgress string
}

// verdict is the result of one check.
type verdict struct {
	done     bool
	failure  string // non-empty: fail now
	progress string // one line for the CLI
}

func (s *watch) check(ctx context.Context) (verdict, error) {
	t := s.req.Target
	sha := s.out.CommitSHA

	state, err := s.w.Argo.Get(ctx, t.ArgoApp, false)
	if err != nil {
		return verdict{}, err
	}
	s.lastState = state

	if op := state.Operation; op != nil && op.Revision == sha && (op.Phase == "Failed" || op.Phase == "Error") {
		msg := "Argo CD could not apply " + shortSHA(sha)
		if op.Message != "" {
			msg += ": " + op.Message
		}
		return verdict{failure: msg}, nil
	}

	ours, err := s.revisionHasChange(ctx, state.SyncRevision)
	if err != nil {
		return verdict{}, err
	}
	if ours && state.Health == "Degraded" {
		msg := "Argo CD reports the app Degraded"
		if state.HealthMessage != "" {
			msg += ": " + state.HealthMessage
		}
		return verdict{failure: msg}, nil
	}

	pods, err := s.w.Kube.Pods(ctx, t.Namespace, t.Selector)
	if err != nil {
		return verdict{}, err
	}
	s.lastPods = pods

	podsDone, progress, failure := s.judgePods(pods)
	s.lastProgress = progress
	if failure != "" {
		return verdict{failure: failure, progress: progress}, nil
	}
	// For scale, the old count can never match the new one (validate made
	// sure they differ), so pods at the new count prove the change is live
	// even when Argo CD has since synced a later commit.
	if !ours && s.req.Kind == KindScale && podsDone && state.SyncRevision != "" {
		ours = true
	}
	if !ours {
		progress += ", waiting for Argo CD to pick up " + shortSHA(sha)
	} else if state.SyncStatus != "Synced" || state.Health != "Healthy" {
		progress += ", Argo CD " + state.SyncStatus + "/" + state.Health
	}
	done := podsDone && ours && state.SyncStatus == "Synced" && state.Health == "Healthy"
	return verdict{done: done, progress: progress}, nil
}

// revisionHasChange reports whether Argo CD's synced revision includes our
// change: it is our commit, or (for deploy and rollback) a later commit
// whose desired images still carry the new tag. For scale, a later commit
// cannot be judged from images; the ready-pod count is the evidence then.
func (s *watch) revisionHasChange(ctx context.Context, revision string) (bool, error) {
	switch {
	case revision == "":
		return false, nil
	case strings.EqualFold(revision, s.out.CommitSHA):
		return true, nil
	case s.req.Kind == KindScale:
		return false, nil
	}
	t := s.req.Target
	images, err := s.w.Argo.GetDesiredImages(ctx, t.ArgoApp, revision, t.Namespace, t.Selector)
	if err != nil {
		return false, err
	}
	for _, img := range images {
		if imageTag(img) == s.out.To {
			return true, nil
		}
	}
	return false, nil
}

// judgePods decides whether the pods show the change, describes progress,
// and spots pods that will not recover.
func (s *watch) judgePods(pods []Pod) (done bool, progress, failure string) {
	var active []Pod
	for _, p := range pods {
		if !p.Terminating {
			active = append(active, p)
		}
	}

	// The pods this change is responsible for: for deploy and rollback the
	// ones on the new tag, for scale all of them.
	var mine []Pod
	for _, p := range active {
		if s.req.Kind == KindScale || runsTag(p, s.out.To) {
			mine = append(mine, p)
		}
	}
	ready := 0
	for _, p := range mine {
		if p.Ready {
			ready++
		}
	}

	stuckNow := false
	for _, p := range mine {
		id := podID(p)
		base, seen := s.restarts[id]
		if !seen {
			s.restarts[id] = p.Restarts
			base = p.Restarts
		}
		if n := p.Restarts - base; n >= s.w.MaxRestarts {
			return false, "", fmt.Sprintf("pod %s restarted %d times%s", p.Name, n, reasonSuffix(p))
		}
		for _, c := range p.Containers {
			if slices.Contains(fatalReasons, c.Reason) {
				return false, "", fmt.Sprintf("pod %s cannot start: %s%s", p.Name, c.Reason, messageSuffix(c.Message))
			}
			if slices.Contains(stuckReasons, c.Reason) {
				stuckNow = true
			}
		}
	}
	if stuckNow {
		s.stuck++
	} else {
		s.stuck = 0
	}

	switch s.req.Kind {
	case KindScale:
		want, _ := strconv.Atoi(s.out.To)
		progress = fmt.Sprintf("%d/%d pods ready", ready, want)
		done = ready == want && len(active) == want
	default:
		progress = fmt.Sprintf("%d/%d pods ready on %s", ready, len(active), s.out.To)
		done = len(active) > 0 && len(mine) == len(active) && ready == len(active)
	}

	if s.stuck >= s.w.StuckPolls {
		for _, p := range mine {
			for _, c := range p.Containers {
				if slices.Contains(stuckReasons, c.Reason) {
					return false, progress, fmt.Sprintf("pod %s cannot start: %s%s", p.Name, c.Reason, messageSuffix(c.Message))
				}
			}
		}
	}
	return done, progress, ""
}

// fail collects diagnostics and returns them with a permanent error.
func (s *watch) fail(ctx context.Context, reason string) (Result, error) {
	out := s.out
	out.Diagnostics = s.diagnose(ctx)
	return Result{Outputs: out, Message: reason}, errors.New(reason)
}

const (
	diagMaxPods       = 3
	diagMaxEvents     = 20
	diagMaxContainers = 3
	diagTailLines     = 50
	diagMaxBytes      = 64 << 10 // all diagnostics of one request together
)

// diagnose gathers what a person needs to see why the rollout failed. It is
// best effort: a diagnostic that cannot be fetched is left out, it never
// hides the real failure. Everything together stays within diagMaxBytes,
// filled in order: health message, events, then log tails.
func (s *watch) diagnose(ctx context.Context) *Diagnostics {
	t := s.req.Target
	budget := diagMaxBytes
	d := &Diagnostics{HealthMessage: keepHead(s.lastState.HealthMessage, budget), Logs: map[string]string{}}
	budget -= len(d.HealthMessage)

	// Pods that are not ready, new ones (this change's) first. A ready pod
	// on the old tag is just the rollout not having replaced it yet.
	var bad []Pod
	for _, pass := range []bool{true, false} {
		for _, p := range s.lastPods {
			isNew := s.req.Kind == KindScale || runsTag(p, s.out.To)
			if !p.Terminating && !p.Ready && isNew == pass && len(bad) < diagMaxPods {
				bad = append(bad, p)
			}
		}
	}

	var objects []string
	for _, p := range bad {
		objects = append(objects, p.Name)
		if p.Owner != nil && p.Owner.Name != "" && !slices.Contains(objects, p.Owner.Name) {
			objects = append(objects, p.Owner.Name)
		}
	}
	if len(objects) > 0 {
		if events, err := s.w.Kube.Events(ctx, t.Namespace, objects, diagMaxEvents); err == nil {
			for _, e := range events {
				size := len(e.Object) + len(e.Type) + len(e.Reason) + len(e.Message)
				if size > budget {
					break
				}
				d.Events = append(d.Events, e)
				budget -= size
			}
		}
	}

	logs := 0
	for _, p := range bad {
		for _, c := range p.Containers {
			if c.Ready || logs >= diagMaxContainers || budget <= 0 {
				continue
			}
			// After a crash the useful lines are in the previous instance.
			previous := c.Restarts > 0
			text, err := s.w.Kube.Logs(ctx, t.Namespace, p.Name, c.Name, previous, diagTailLines)
			if err != nil || text == "" {
				continue
			}
			key := p.Name + "/" + c.Name
			if previous {
				key += " (previous)"
			}
			// The end of a log is where the failure is, so keep the tail.
			text = keepTail(text, budget-len(key))
			if text == "" {
				continue
			}
			d.Logs[key] = text
			budget -= len(key) + len(text)
			logs++
		}
	}
	return d
}

// keepHead returns at most limit bytes from the start of s, without cutting
// a UTF-8 character.
func keepHead(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// keepTail returns at most limit bytes from the end of s. When it has to
// cut, it starts at a line boundary so the first line is not a fragment,
// unless that would leave nothing.
func keepTail(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	tail := s[len(s)-limit:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		return tail[i+1:]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail
}

// runsTag reports whether any regular container of the pod runs tag.
func runsTag(p Pod, tag string) bool {
	if len(p.Containers) > 0 {
		for _, c := range p.Containers {
			if imageTag(c.Image) == tag {
				return true
			}
		}
		return false
	}
	for _, img := range p.Images {
		if imageTag(img) == tag {
			return true
		}
	}
	return false
}

// imageTag returns the tag of an image reference, or "" if it has none:
// registry.example.com:5000/team/app:e4f5a6b@sha256:... -> e4f5a6b
func imageTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[colon+1:]
	}
	return ""
}

func podID(p Pod) string {
	if p.UID != "" {
		return p.UID
	}
	return p.Name
}

func reasonSuffix(p Pod) string {
	if p.Reason != "" {
		return " (" + p.Reason + ")"
	}
	for _, c := range p.Containers {
		if c.Reason != "" {
			return " (" + c.Reason + ")"
		}
	}
	return ""
}

func messageSuffix(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}
