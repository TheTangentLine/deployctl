package fakes

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.ArgoClient = (*Argo)(nil)

// ImageQuery identifies a desired-images fixture independently of app state.
type ImageQuery struct {
	App       string
	Revision  string
	Namespace string
	Selector  string
}

// Argo is a fake Argo CD with one or more applications.
//
// Each Get returns the next state in States[app], and keeps returning the
// last one once the list is used up. That lets a test script a rollout:
//
//	States: map[string][]steps.AppState{
//		"reconcile-dev": {progressing, progressing, healthy},
//	}
//
// Set AppState.Operation in snapshots to script pending, running or completed
// syncs. Get copies the operation so callers cannot change the fixture.
type Argo struct {
	mu sync.Mutex

	States map[string][]steps.AppState
	// ImageSets maps an exact query to container name -> full image reference.
	// GetDesiredImages returns a copy so callers cannot change the fixture.
	ImageSets map[ImageQuery]map[string]string
	// Refreshes counts Get calls with refresh=true, per app.
	Refreshes map[string]int
	// Syncs records every Sync call as "app@revision".
	Syncs []string
	Err   map[string]error

	next map[string]int
}

func (a *Argo) Get(ctx context.Context, app string, refresh bool) (steps.AppState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Err["Get"]; err != nil {
		return steps.AppState{}, err
	}
	states, ok := a.States[app]
	if !ok || len(states) == 0 {
		return steps.AppState{}, fmt.Errorf("application %s: %w", app, steps.ErrNotFound)
	}
	if refresh {
		if a.Refreshes == nil {
			a.Refreshes = map[string]int{}
		}
		a.Refreshes[app]++
	}
	if a.next == nil {
		a.next = map[string]int{}
	}
	i := a.next[app]
	if i < len(states)-1 {
		a.next[app] = i + 1
	}
	state := states[i]
	if state.Operation != nil {
		operation := *state.Operation
		state.Operation = &operation
	}
	return state, nil
}

func (a *Argo) GetDesiredImages(ctx context.Context, app, revision, namespace, selector string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Err["GetDesiredImages"]; err != nil {
		return nil, err
	}
	if _, ok := a.States[app]; !ok {
		return nil, fmt.Errorf("application %s: %w", app, steps.ErrNotFound)
	}
	query := ImageQuery{App: app, Revision: revision, Namespace: namespace, Selector: selector}
	images, ok := a.ImageSets[query]
	if !ok {
		return nil, fmt.Errorf("desired images for %+v: fixture not configured", query)
	}
	return maps.Clone(images), nil
}

func (a *Argo) Sync(ctx context.Context, app, revision string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Err["Sync"]; err != nil {
		return err
	}
	if _, ok := a.States[app]; !ok {
		return fmt.Errorf("application %s: %w", app, steps.ErrNotFound)
	}
	a.Syncs = append(a.Syncs, app+"@"+revision)
	return nil
}
