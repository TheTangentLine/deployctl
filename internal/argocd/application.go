package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

type application struct {
	Spec      applicationSpec   `json:"spec"`
	Status    applicationStatus `json:"status"`
	Operation *operation        `json:"operation"`
}

type applicationSpec struct {
	SyncPolicy     syncPolicy             `json:"syncPolicy"`
	Source         *applicationSource     `json:"source"`
	Sources        []applicationSource    `json:"sources"`
	Destination    applicationDestination `json:"destination"`
	SourceHydrator json.RawMessage        `json:"sourceHydrator"`
}

type applicationSource struct {
	RepoURL string `json:"repoURL"`
	Path    string `json:"path"`
	Chart   string `json:"chart"`
	Ref     string `json:"ref"`
}

type applicationDestination struct {
	Namespace string `json:"namespace"`
}

type syncPolicy struct {
	Automated *automatedSyncPolicy `json:"automated"`
}

type automatedSyncPolicy struct {
	Enabled *bool `json:"enabled"`
}

type applicationStatus struct {
	Sync           syncStatus      `json:"sync"`
	Health         healthStatus    `json:"health"`
	OperationState *operationState `json:"operationState"`
}

type syncStatus struct {
	Status   string `json:"status"`
	Revision string `json:"revision"`
}

type healthStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type operation struct {
	Sync *syncOperation `json:"sync"`
}

type syncOperation struct {
	Revision string `json:"revision"`
}

type operationState struct {
	Operation  operation            `json:"operation"`
	Phase      string               `json:"phase"`
	Message    string               `json:"message"`
	SyncResult *syncOperationResult `json:"syncResult"`
}

type syncOperationResult struct {
	Revision string `json:"revision"`
}

// Get reads the application state, optionally refreshing it first.
func (c *Client) Get(ctx context.Context, app string, refresh bool) (steps.AppState, error) {
	snapshot, err := c.getApplication(ctx, app, refresh)
	if err != nil {
		return steps.AppState{}, err
	}
	state := steps.AppState{
		SyncStatus:    snapshot.Status.Sync.Status,
		SyncRevision:  snapshot.Status.Sync.Revision,
		Health:        snapshot.Status.Health.Status,
		HealthMessage: snapshot.Status.Health.Message,
		Operation:     snapshot.syncOperationState(),
	}
	if state.SyncStatus == "" {
		state.SyncStatus = "Unknown"
	}
	if state.Health == "" {
		state.Health = "Unknown"
	}
	automated := snapshot.Spec.SyncPolicy.Automated
	state.AutoSync = automated != nil && (automated.Enabled == nil || *automated.Enabled)
	return state, nil
}

// getApplication reads the API document before mapping its status.
func (c *Client) getApplication(ctx context.Context, app string, refresh bool) (*application, error) {
	if strings.TrimSpace(app) == "" {
		return nil, errors.New("argocd application name must not be empty")
	}
	query := url.Values{}
	if refresh {
		query.Set("refresh", "normal")
	}
	var snapshot *application
	if err := c.doJSON(ctx, http.MethodGet, []string{"applications", app}, query, nil, &snapshot); err != nil {
		return nil, fmt.Errorf("argocd get application %q: %w", app, err)
	}
	if snapshot == nil {
		return nil, fmt.Errorf("argocd get application %q: response must not be null", app)
	}
	return snapshot, nil
}

// syncOperationState separates a new sync request from a previous operation.
func (a *application) syncOperationState() *steps.SyncOperationState {
	state := a.Status.OperationState
	if a.Operation != nil && a.Operation.Sync != nil {
		requested := a.Operation.Sync
		if state == nil || (state.Phase != "Running" && state.Phase != "Terminating") ||
			state.Operation.Sync == nil || state.Operation.Sync.Revision != requested.Revision {
			return &steps.SyncOperationState{Revision: requested.Revision, Phase: "Pending"}
		}
	}
	if state == nil || state.Phase == "" || (state.Operation.Sync == nil && state.SyncResult == nil) {
		return nil
	}
	var revision string
	if state.SyncResult != nil {
		revision = state.SyncResult.Revision
	}
	if revision == "" && state.Operation.Sync != nil {
		revision = state.Operation.Sync.Revision
	}
	return &steps.SyncOperationState{Revision: revision, Phase: state.Phase, Message: state.Message}
}
