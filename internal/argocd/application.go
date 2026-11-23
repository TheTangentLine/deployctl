package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

type application struct {
	Spec   applicationSpec   `json:"spec"`
	Status applicationStatus `json:"status"`
}

type applicationSpec struct {
	SyncPolicy syncPolicy `json:"syncPolicy"`
}

type syncPolicy struct {
	Automated *automatedSyncPolicy `json:"automated"`
}

type automatedSyncPolicy struct {
	Enabled *bool `json:"enabled"`
}

type applicationStatus struct {
	Sync   syncStatus   `json:"sync"`
	Health healthStatus `json:"health"`
}

type syncStatus struct {
	Status   string `json:"status"`
	Revision string `json:"revision"`
}

type healthStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Get reads the application state, optionally refreshing it first.
func (c *Client) Get(ctx context.Context, app string, refresh bool) (steps.AppState, error) {
	if strings.TrimSpace(app) == "" {
		return steps.AppState{}, errors.New("argocd application name must not be empty")
	}
	query := url.Values{}
	if refresh {
		query.Set("refresh", "normal")
	}
	var snapshot *application
	if err := c.doJSON(ctx, http.MethodGet, []string{"applications", app}, query, nil, &snapshot); err != nil {
		return steps.AppState{}, fmt.Errorf("argocd get application %q: %w", app, err)
	}
	if snapshot == nil {
		return steps.AppState{}, fmt.Errorf("argocd get application %q: response must not be null", app)
	}
	state := steps.AppState{
		SyncStatus:    snapshot.Status.Sync.Status,
		SyncRevision:  snapshot.Status.Sync.Revision,
		Health:        snapshot.Status.Health.Status,
		HealthMessage: snapshot.Status.Health.Message,
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
