package fakes

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestKubePodSnapshotsAreIndependent(t *testing.T) {
	pod := steps.Pod{
		Name: "backend", UID: "pod-1", Ready: true, Terminating: true,
		Phase: "Running", Reason: "BackOff", Restarts: 4,
		Owner:          &steps.ObjectRef{Kind: "ReplicaSet", Name: "backend-rs", UID: "rs-1"},
		Images:         []string{"backend:v2", "sidecar:v1"},
		Containers:     []steps.Container{{Name: "backend", Image: "backend:v2", ImageID: "runtime://sha256:abc", Restarts: 3, State: steps.ContainerWaiting, Reason: "CrashLoopBackOff", Message: "back-off"}},
		InitContainers: []steps.Container{{Name: "init", Restarts: 1, State: steps.ContainerTerminated}},
	}
	k := &Kube{PodSnapshots: [][]steps.Pod{{}, {pod}}}
	first, err := k.Pods(context.Background(), "demo", "app=backend")
	if err != nil || len(first) != 0 {
		t.Fatalf("first snapshot = %v, %v", first, err)
	}
	for range 3 {
		got, err := k.Pods(context.Background(), "demo", "app=backend")
		if err != nil || !reflect.DeepEqual(got, []steps.Pod{pod}) {
			t.Fatalf("snapshot = %+v, %v", got, err)
		}
		got[0].Name = "changed"
		got[0].Owner.Name = "changed"
		got[0].Images[0] = "changed"
		got[0].Containers[0].Name = "changed"
		got[0].InitContainers[0].Name = "changed"
	}
}

func TestKubeDiagnostics(t *testing.T) {
	k := &Kube{
		EventList: []steps.Event{{Object: "pod", Reason: "new"}, {Object: "unrelated"}, {Object: "rs", Reason: "old"}},
		LogText:   map[string]string{"pod/c": "one\ntwo\nthree\n", "pod/c/previous": "crashed\n", "pod/large": strings.Repeat("a", 70000)},
	}
	objects := []string{"pod", "rs"}
	events, err := k.Events(context.Background(), "demo", objects, 1)
	objects[0] = "changed"
	if err != nil || len(events) != 1 || events[0].Reason != "new" || k.EventCalls[0].Objects[0] != "pod" || k.EventCalls[0].Limit != 1 {
		t.Fatalf("events=%v, %v; calls=%v", events, err, k.EventCalls)
	}
	if got, err := k.Events(context.Background(), "demo", nil, 20); err != nil || len(got) != 0 {
		t.Fatalf("empty objects: %v, %v", got, err)
	}
	if _, err := k.Events(context.Background(), "demo", []string{"pod"}, 0); err == nil {
		t.Fatal("accepted zero limit")
	}
	for _, tc := range []struct {
		container string
		previous  bool
		lines     int64
		want      string
	}{
		{"c", false, 2, "two\nthree\n"}, {"c", true, 50, "crashed\n"}, {"large", false, 1, strings.Repeat("a", 64<<10)},
	} {
		got, err := k.Logs(context.Background(), "demo", "pod", tc.container, tc.previous, tc.lines)
		if err != nil || got != tc.want {
			t.Fatalf("logs: len=%d, %v", len(got), err)
		}
		call := k.LogCalls[len(k.LogCalls)-1]
		if call.Namespace != "demo" || call.Pod != "pod" || call.Container != tc.container || call.Previous != tc.previous || call.TailLines != tc.lines {
			t.Fatalf("call=%+v", call)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := k.Logs(ctx, "demo", "pod", "c", false, 50); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
