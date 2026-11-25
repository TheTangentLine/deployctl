package fakes

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	EventList    []steps.Event // fixtures must already be ordered newest first
	// LogText maps "pod/container" (or "pod/container/previous") to log text.
	LogText map[string]string
	Err     map[string]error

	// Calls records "Method namespace selector-or-pod" for assertions.
	Calls      []string
	PodCalls   []PodCall
	EventCalls []EventCall
	LogCalls   []LogCall

	next int
}

type PodCall struct{ Namespace, Selector string }
type EventCall struct {
	Namespace string
	Objects   []string
	Limit     int
}
type LogCall struct {
	Namespace, Pod, Container string
	Previous                  bool
	TailLines                 int64
}

func (k *Kube) Pods(ctx context.Context, namespace, selector string) ([]steps.Pod, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Pods "+namespace+" "+selector)
	k.PodCalls = append(k.PodCalls, PodCall{Namespace: namespace, Selector: selector})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	out := slices.Clone(k.PodSnapshots[i])
	for j := range out {
		out[j].Images = slices.Clone(out[j].Images)
		out[j].Containers = slices.Clone(out[j].Containers)
		out[j].InitContainers = slices.Clone(out[j].InitContainers)
		if out[j].Owner != nil {
			owner := *out[j].Owner
			out[j].Owner = &owner
		}
	}
	return out, nil
}

func (k *Kube) Events(ctx context.Context, namespace string, objects []string, limit int) ([]steps.Event, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Events "+namespace)
	k.EventCalls = append(k.EventCalls, EventCall{Namespace: namespace, Objects: slices.Clone(objects), Limit: limit})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(namespace) == "" || limit <= 0 {
		return nil, errors.New("fake kube events need namespace and positive limit")
	}
	for _, object := range objects {
		if strings.TrimSpace(object) == "" {
			return nil, errors.New("fake kube events need object names")
		}
	}
	if len(objects) == 0 {
		return []steps.Event{}, nil
	}
	if err := k.Err["Events"]; err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, o := range objects {
		want[o] = true
	}
	var out []steps.Event
	for _, e := range k.EventList {
		if want[e.Object] {
			out = append(out, e)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (k *Kube) Logs(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Calls = append(k.Calls, "Logs "+namespace+" "+pod)
	k.LogCalls = append(k.LogCalls, LogCall{Namespace: namespace, Pod: pod, Container: container, Previous: previous, TailLines: tailLines})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(pod) == "" || strings.TrimSpace(container) == "" || tailLines <= 0 {
		return "", errors.New("fake kube logs need namespace, pod, container and positive tailLines")
	}
	if err := k.Err["Logs"]; err != nil {
		return "", err
	}
	key := pod + "/" + container
	if previous {
		key += "/previous"
	}
	text := k.LogText[key]
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if int64(len(lines)) > tailLines {
		text = strings.Join(lines[len(lines)-int(tailLines):], "")
	}
	const logLimit = 64 << 10
	if len(text) > logLimit {
		text = text[:logLimit]
	}
	return text, nil
}
