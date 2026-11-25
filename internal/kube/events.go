package kube

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Events returns newest-first Pod/ReplicaSet events matching object names.
// Object UIDs are not part of this interface; reused names cannot be distinguished.
func (c *Client) Events(ctx context.Context, namespace string, objects []string, limit int) ([]steps.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, classify(ctx, "events", err)
	}
	if err := requireValue("namespace", namespace); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("kube event limit must be positive")
	}
	want := make(map[string]bool, len(objects))
	for _, object := range objects {
		if err := requireValue("event object name", object); err != nil {
			return nil, err
		}
		want[object] = true
	}
	if len(want) == 0 {
		return []steps.Event{}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	options := metav1.ListOptions{Limit: pageSize}
	var matched []corev1.Event
	for {
		page, err := c.api.CoreV1().Events(namespace).List(requestCtx, options)
		if err != nil {
			return nil, classify(ctx, "list events", err)
		}
		for _, event := range page.Items {
			if want[event.InvolvedObject.Name] && (event.InvolvedObject.Kind == "Pod" || event.InvolvedObject.Kind == "ReplicaSet") {
				matched = append(matched, event)
			}
		}
		if page.Continue == "" {
			break
		}
		options.Continue = page.Continue
	}
	slices.SortFunc(matched, func(a, b corev1.Event) int {
		if order := eventTime(b).Compare(eventTime(a)); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := make([]steps.Event, 0, min(limit, len(matched)))
	for _, event := range matched[:min(limit, len(matched))] {
		out = append(out, steps.Event{Object: event.InvolvedObject.Name, Type: event.Type, Reason: event.Reason, Message: event.Message})
	}
	return out, nil
}

func eventTime(event corev1.Event) time.Time {
	latest := event.CreationTimestamp.Time
	for _, stamp := range []time.Time{event.LastTimestamp.Time, event.EventTime.Time} {
		if stamp.After(latest) {
			latest = stamp
		}
	}
	if event.Series != nil && event.Series.LastObservedTime.Time.After(latest) {
		latest = event.Series.LastObservedTime.Time
	}
	return latest
}
