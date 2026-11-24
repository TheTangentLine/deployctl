package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestMapPod(t *testing.T) {
	yes := true
	deleting := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", UID: "pod-1", DeletionTimestamp: &deleting,
			OwnerReferences: []metav1.OwnerReference{{Name: "ignored"}, {Kind: "ReplicaSet", Name: "backend-rs", UID: "rs-1", Controller: &yes}}},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "backend", Image: "backend:v2"}, {Name: "sidecar", Image: "sidecar:v1"}, {Name: "new", Image: "new:v3"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "init:v1"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "backend", Image: "status-image-must-not-override-spec", ImageID: "runtime://sha256:abc", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}}},
			},
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "init", RestartCount: 1, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", Message: "init failed"}}}},
		},
	}
	got := mapPod(pod)
	if got.UID != "pod-1" || !got.Ready || !got.Terminating || got.Phase != "Running" || got.Restarts != 4 || got.Reason != "Error" {
		t.Fatalf("pod = %+v", got)
	}
	if !reflect.DeepEqual(got.Owner, &steps.ObjectRef{Kind: "ReplicaSet", Name: "backend-rs", UID: "rs-1"}) || !reflect.DeepEqual(got.Images, []string{"backend:v2", "sidecar:v1", "new:v3"}) {
		t.Fatalf("owner/images = %+v, %v", got.Owner, got.Images)
	}
	want := []steps.Container{
		{Name: "backend", Image: "backend:v2", ImageID: "runtime://sha256:abc", Restarts: 3, State: steps.ContainerWaiting, Reason: "CrashLoopBackOff", Message: "back-off"},
		{Name: "sidecar", Image: "sidecar:v1", Ready: true, State: steps.ContainerRunning},
		{Name: "new", Image: "new:v3", State: steps.ContainerUnknown},
	}
	if !reflect.DeepEqual(got.Containers, want) || got.InitContainers[0].State != steps.ContainerTerminated || got.InitContainers[0].Message != "init failed" {
		t.Fatalf("containers = %+v; init = %+v", got.Containers, got.InitContainers)
	}
	pod.Status.Reason = "Evicted"
	if mapPod(pod).Reason != "Evicted" {
		t.Fatal("pod reason must take precedence")
	}
	pod.Status.Reason = ""
	pod.Status.InitContainerStatuses = nil
	if mapPod(pod).Reason != "CrashLoopBackOff" {
		t.Fatal("regular container fallback missing")
	}
	got.Owner.Name = "changed"
	got.Containers[0].Name = "changed"
	if pod.OwnerReferences[1].Name != "backend-rs" || pod.Spec.Containers[0].Name != "backend" {
		t.Fatal("mapper aliased input")
	}
}

func TestPodsSelection(t *testing.T) {
	api := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "z", Namespace: "demo", Labels: map[string]string{"app": "backend"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "demo", Labels: map[string]string{"app": "backend"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "demo", Labels: map[string]string{"app": "other"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other", Labels: map[string]string{"app": "backend"}}},
	)
	c, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	pods, err := c.Pods(context.Background(), "demo", "app in (backend)")
	if err != nil || len(pods) != 2 || pods[0].Name != "a" || pods[1].Name != "z" || pods[1].Ready || pods[0].Owner != nil {
		t.Fatalf("pods = %+v, %v", pods, err)
	}
	empty, err := c.Pods(context.Background(), "demo", "app=missing")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list = %v, %v", empty, err)
	}
	for _, args := range [][2]string{{"", "app=x"}, {"demo", " "}, {"demo", "app in ("}} {
		api.ClearActions()
		if _, err := c.Pods(context.Background(), args[0], args[1]); err == nil || steps.IsRetryable(err) {
			t.Fatalf("input %v: %v", args, err)
		}
		if len(api.Actions()) != 0 {
			t.Fatal("invalid input reached API")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Pods(ctx, "demo", "app=x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestPodsPagination(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/demo/pods" || r.URL.Query().Get("labelSelector") != "app=backend" || r.URL.Query().Get("limit") != "500" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		page := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
		if n == 1 {
			page.Continue = "next"
			page.Items = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "z"}}}
		} else {
			if r.URL.Query().Get("continue") != "next" {
				t.Error("missing continue token")
			}
			page.Items = []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	c, err := NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	pods, err := c.Pods(context.Background(), "demo", "app=backend")
	if err != nil || len(pods) != 2 || pods[0].Name != "a" || calls.Load() != 2 {
		t.Fatalf("pods = %+v, %v; calls=%d", pods, err, calls.Load())
	}
}
