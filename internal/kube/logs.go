package kube

import (
	"context"
	"errors"
	"io"

	corev1 "k8s.io/api/core/v1"
)

const logReadLimit = 64 << 10

// Logs reads one container instance without following it. The response is
// truncated to 64 KiB; the consumer separately caps total request diagnostics.
func (c *Client) Logs(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", classify(ctx, "logs", err)
	}
	for _, field := range []struct{ name, value string }{{"namespace", namespace}, {"pod", pod}, {"container", container}} {
		if err := requireValue(field.name, field.value); err != nil {
			return "", err
		}
	}
	if tailLines <= 0 {
		return "", errors.New("kube log tailLines must be positive")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stream, err := c.api.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, Previous: previous, TailLines: &tailLines, Follow: false,
	}).MaxRetries(0).Stream(requestCtx)
	if err != nil {
		return "", classify(ctx, "open logs", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, logReadLimit))
	if err != nil {
		return "", classify(ctx, "read logs", err)
	}
	if err := requestCtx.Err(); err != nil {
		return "", classify(ctx, "read logs", err)
	}
	return string(data), nil
}
