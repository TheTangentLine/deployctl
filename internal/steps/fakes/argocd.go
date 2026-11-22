package fakes

import (
	"context"
	"fmt"
	"sync"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.ArgoClient = (*Argo)(nil)

// Argo is a fake Argo CD with one or more applications.
//
// Each Get returns the next state in States[app], and keeps returning the
// last one once the list is used up. That lets a test script a rollout:
//
//	States: map[string][]steps.AppState{
//		"reconcile-dev": {progressing, progressing, healthy},
//	}
type Argo struct {
	mu sync.Mutex

	States map[string][]steps.AppState
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
	return states[i], nil
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
