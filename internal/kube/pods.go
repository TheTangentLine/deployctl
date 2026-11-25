package kube

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/TheTangentLine/deployctl/internal/steps"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Pods includes terminating pods. Readiness/rollout policy belongs to consumers.
func (c *Client) Pods(ctx context.Context, namespace, selector string) ([]steps.Pod, error) {
	if err := ctx.Err(); err != nil {
		return nil, classify(ctx, "pods", err)
	}
	if err := requireValue("namespace", namespace); err != nil {
		return nil, err
	}
	if err := requireValue("selector", selector); err != nil {
		return nil, err
	}
	if _, err := labels.Parse(selector); err != nil {
		return nil, fmt.Errorf("kube selector: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	options := metav1.ListOptions{LabelSelector: selector, Limit: pageSize}
	out := []steps.Pod{}
	for {
		page, err := c.api.CoreV1().Pods(namespace).List(requestCtx, options)
		if err != nil {
			return nil, classify(ctx, "list pods", err)
		}
		for i := range page.Items {
			out = append(out, mapPod(&page.Items[i]))
		}
		if page.Continue == "" {
			break
		}
		options.Continue = page.Continue
	}
	slices.SortFunc(out, func(a, b steps.Pod) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func mapPod(p *corev1.Pod) steps.Pod {
	out := steps.Pod{
		Name: p.Name, UID: string(p.UID), Phase: string(p.Status.Phase),
		Reason: p.Status.Reason, Terminating: p.DeletionTimestamp != nil,
		Containers:     mapContainers(p.Spec.Containers, p.Status.ContainerStatuses),
		InitContainers: mapContainers(p.Spec.InitContainers, p.Status.InitContainerStatuses),
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == corev1.PodReady {
			out.Ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	for _, owner := range p.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			out.Owner = &steps.ObjectRef{Kind: owner.Kind, Name: owner.Name, UID: string(owner.UID)}
			break
		}
	}
	for _, container := range out.Containers {
		out.Images = append(out.Images, container.Image)
	}
	for _, containers := range [][]steps.Container{out.InitContainers, out.Containers} {
		for _, container := range containers {
			out.Restarts += container.Restarts
			if out.Reason == "" && !container.Ready && container.Reason != "" {
				out.Reason = container.Reason
			}
		}
	}
	return out
}

func mapContainers(specs []corev1.Container, statuses []corev1.ContainerStatus) []steps.Container {
	byName := make(map[string]corev1.ContainerStatus, len(statuses))
	for _, status := range statuses {
		byName[status.Name] = status
	}
	out := make([]steps.Container, 0, len(specs))
	for _, spec := range specs {
		container := steps.Container{Name: spec.Name, Image: spec.Image, State: steps.ContainerUnknown}
		if status, ok := byName[spec.Name]; ok {
			container.ImageID, container.Ready, container.Restarts = status.ImageID, status.Ready, status.RestartCount
			switch {
			case status.State.Waiting != nil:
				container.State = steps.ContainerWaiting
				container.Reason, container.Message = status.State.Waiting.Reason, status.State.Waiting.Message
			case status.State.Running != nil:
				container.State = steps.ContainerRunning
			case status.State.Terminated != nil:
				container.State = steps.ContainerTerminated
				container.Reason, container.Message = status.State.Terminated.Reason, status.State.Terminated.Message
			}
		}
		out = append(out, container)
	}
	return out
}
