package fakes

import (
	"context"
	"sync"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var _ steps.KubeReader = (*Kube)(nil)

// Kube is a fake read-only cluster.
//
// Pods works like Argo.States: each call returns the next snapshot in
// PodSnapshots and keeps returning the last one, so a test can script a
// rolling update. The namespace and selector are recorded but not used to
// filter; set up only the pods the test is about.
type Kube struct {
	mu sync.Mutex

	PodSnapshots [][]steps.Pod
	EventList    []steps.Event
	// LogText maps "pod/container" (or "pod/container/previous") to log text.
	LogText    map[string]string
	Autoscaled bool
	Err        map[string]error

	// Calls records "Method namespace selector-or-pod" for assertions.
	Calls []string

	next int
}

func (k *Kube) Pods(ctx context.Context, namespace, selector string) ([]steps.Pod, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Pods "+namespace+" "+selector)
	if err := k.Err["Pods"]; err != nil {
		return nil, err
	}
	if len(k.PodSnapshots) == 0 {
		return nil, nil
	}
	i := k.next
	if i < len(k.PodSnapshots)-1 {
		k.next = i + 1
	}
	return k.PodSnapshots[i], nil
}

func (k *Kube) Events(ctx context.Context, namespace string, objects []string, limit int) ([]steps.Event, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Events "+namespace)
	if err := k.Err["Events"]; err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, o := range objects {
		want[o] = true
	}
	var out []steps.Event
	for _, e := range k.EventList {
		if len(want) == 0 || want[e.Object] {
			out = append(out, e)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (k *Kube) Logs(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Logs "+namespace+" "+pod)
	if err := k.Err["Logs"]; err != nil {
		return "", err
	}
	key := pod + "/" + container
	if previous {
		key += "/previous"
	}
	return k.LogText[key], nil
}

func (k *Kube) IsAutoscaled(ctx context.Context, namespace, selector string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "IsAutoscaled "+namespace+" "+selector)
	if err := k.Err["IsAutoscaled"]; err != nil {
		return false, err
	}
	return k.Autoscaled, nil
}
