package steps

import (
	"errors"
	"fmt"
	"strconv"
)

// valueRef returns which value in the config repo a request changes, and
// how that value is typed: the image tag for deploy and rollback, the
// replica count for scale.
func valueRef(r Request) (ValueRef, ValueKind, error) {
	t := r.Target
	ref := ValueRef{Repo: t.Repo, Branch: t.Branch, File: t.ValuesFile}
	switch r.Kind {
	case KindDeploy, KindRollback:
		if t.ImageTagKey == "" {
			return ValueRef{}, 0, errors.New("app has no imageTagKey configured")
		}
		ref.Key = t.ImageTagKey
		return ref, ValueString, nil
	case KindScale:
		if t.ReplicasKey == "" {
			return ValueRef{}, 0, fmt.Errorf("%s does not support scale: no replicasKey configured", r.App)
		}
		ref.Key = t.ReplicasKey
		return ref, ValueInt, nil
	default:
		return ValueRef{}, 0, fmt.Errorf("unknown request kind %q", r.Kind)
	}
}

// requestedValue is the target value for deploy and scale. Rollback has no
// requested value; validate looks it up in the deployment history.
func requestedValue(r Request) (string, error) {
	switch r.Kind {
	case KindDeploy:
		if r.Tag == "" {
			return "", errors.New("deploy needs a tag")
		}
		return r.Tag, nil
	case KindScale:
		if r.Replicas < 0 {
			return "", fmt.Errorf("replicas must be 0 or more, got %d", r.Replicas)
		}
		return strconv.Itoa(r.Replicas), nil
	default:
		return "", fmt.Errorf("no requested value for %q", r.Kind)
	}
}

// shortSHA shortens a commit SHA for messages.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
