package steps_test

import (
	"github.com/TheTangentLine/deployctl/internal/steps"
)

// Shared fixtures for step tests: the backend app from the docs.

var devTarget = steps.Target{
	Repo:        "https://github.com/example/reconcile.git",
	Branch:      "main",
	ValuesFile:  "charts/reconcile/values-dev.yaml",
	ImageTagKey: "backend.image.tag",
	ReplicasKey: "backend.replicaCount",
	ArgoApp:     "reconcile-dev",
	Namespace:   "demo-dev",
	Selector:    "app=reconcile-backend",
}

var prodTarget = func() steps.Target {
	t := devTarget
	t.ValuesFile = "charts/reconcile/values-prod.yaml"
	t.ArgoApp = "reconcile-prod"
	t.Namespace = "demo-prod"
	t.RequiresApproval = true
	return t
}()

func tagRef(t steps.Target) steps.ValueRef {
	return steps.ValueRef{Repo: t.Repo, Branch: t.Branch, File: t.ValuesFile, Key: t.ImageTagKey}
}

func replicasRef(t steps.Target) steps.ValueRef {
	return steps.ValueRef{Repo: t.Repo, Branch: t.Branch, File: t.ValuesFile, Key: t.ReplicasKey}
}

func deployReq(tag string) steps.Request {
	return steps.Request{
		ID: "req-1", Kind: steps.KindDeploy, App: "backend", Environment: "dev",
		Tag: tag, RequestedBy: "linh@example.com", Target: devTarget,
	}
}

func scaleReq(replicas int) steps.Request {
	r := deployReq("")
	r.Kind, r.Tag, r.Replicas = steps.KindScale, "", replicas
	return r
}

func rollbackReq() steps.Request {
	r := deployReq("")
	r.Kind, r.Tag = steps.KindRollback, ""
	return r
}

var healthy = steps.AppState{SyncStatus: "Synced", Health: "Healthy"}
