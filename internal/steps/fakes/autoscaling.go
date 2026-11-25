package fakes

import (
	"context"
	"sync"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.AutoscalingReader = (*Autoscaling)(nil)

// Autoscaling scripts the HPA check used by scale validation. A real reader
// belongs to M5, independently of the M0 pod/events/log client.
type Autoscaling struct {
	mu         sync.Mutex
	Autoscaled bool
	Err        error
	Calls      []string
}

func (a *Autoscaling) IsAutoscaled(ctx context.Context, namespace, selector string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	a.Calls = append(a.Calls, "IsAutoscaled "+namespace+" "+selector)
	if a.Err != nil {
		return false, a.Err
	}
	return a.Autoscaled, nil
}
