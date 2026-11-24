package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func sampleEvent(name, namespace, kind, object string, seconds int64) corev1.Event {
	return corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, CreationTimestamp: metav1.NewTime(time.Unix(seconds, 0))},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object}, Type: "Warning", Reason: name, Message: "message " + name}
}

func TestEventsFilteringAndOrdering(t *testing.T) {
	old := sampleEvent("old", "demo", "Pod", "backend", 1)
	series := sampleEvent("series", "demo", "Pod", "backend", 1)
	series.Series = &corev1.EventSeries{LastObservedTime: metav1.NewMicroTime(time.Unix(50, 0))}
	last := sampleEvent("last", "demo", "ReplicaSet", "backend-rs", 1)
	last.LastTimestamp = metav1.NewTime(time.Unix(30, 0))
	modern := sampleEvent("modern", "demo", "Pod", "backend", 1)
	modern.EventTime = metav1.NewMicroTime(time.Unix(40, 0))
	tie := sampleEvent("a-tie", "demo", "Pod", "backend", 40)
	wrongKind := sampleEvent("wrong-kind", "demo", "Deployment", "backend", 100)
	wrongName := sampleEvent("wrong-name", "demo", "Pod", "other", 100)
	wrongNamespace := sampleEvent("wrong-namespace", "other", "Pod", "backend", 100)
	api := fake.NewClientset(&old, &series, &last, &modern, &tie, &wrongKind, &wrongName, &wrongNamespace)
	c, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Events(context.Background(), "demo", []string{"backend", "backend-rs", "backend"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []steps.Event{
		{Object: "backend", Type: "Warning", Reason: "series", Message: "message series"},
		{Object: "backend", Type: "Warning", Reason: "a-tie", Message: "message a-tie"},
		{Object: "backend", Type: "Warning", Reason: "modern", Message: "message modern"},
		{Object: "backend-rs", Type: "Warning", Reason: "last", Message: "message last"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	empty, err := c.Events(context.Background(), "demo", []string{"missing"}, 20)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%v, %v", empty, err)
	}
	api.ClearActions()
	for _, tc := range []struct {
		namespace string
		objects   []string
		limit     int
		fail      bool
	}{
		{"demo", nil, 20, false}, {"", []string{"x"}, 20, true}, {"demo", []string{"x"}, 0, true}, {"demo", []string{"x"}, -1, true}, {"demo", []string{" "}, 20, true},
	} {
		got, err := c.Events(context.Background(), tc.namespace, tc.objects, tc.limit)
		if (err != nil) != tc.fail || len(got) != 0 || steps.IsRetryable(err) {
			t.Fatalf("input %+v: %+v, %v", tc, got, err)
		}
	}
	if len(api.Actions()) != 0 {
		t.Fatal("empty/invalid inputs reached API")
	}
}

func TestEventsPaginationBeforeLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/demo/events" || r.URL.Query().Get("limit") != "500" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		page := corev1.EventList{TypeMeta: metav1.TypeMeta{Kind: "EventList", APIVersion: "v1"}}
		if n == 1 {
			page.Continue = "next"
			page.Items = []corev1.Event{sampleEvent("old", "demo", "Pod", "backend", 1)}
		} else {
			if r.URL.Query().Get("continue") != "next" {
				t.Error("missing continuation token")
			}
			page.Items = []corev1.Event{sampleEvent("new", "demo", "Pod", "backend", 10)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	c, err := NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Events(context.Background(), "demo", []string{"backend"}, 1)
	if err != nil || len(got) != 1 || got[0].Reason != "new" || calls.Load() != 2 {
		t.Fatalf("events = %+v, %v; calls=%d", got, err, calls.Load())
	}
}
