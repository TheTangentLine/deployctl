package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

type syncRequest struct {
	Revision string `json:"revision"`
}

// Sync asks Argo CD to apply the given revision.
func (c *Client) Sync(ctx context.Context, app, revision string) error {
	if strings.TrimSpace(app) == "" {
		return errors.New("argocd application name must not be empty")
	}
	if strings.TrimSpace(revision) == "" {
		return errors.New("argocd sync revision must not be empty")
	}
	err := c.doJSON(ctx, http.MethodPost, []string{"applications", app, "sync"}, nil, syncRequest{Revision: revision}, nil)
	if err == nil {
		return nil
	}
	var responseErr *responseError
	if errors.As(err, &responseErr) && responseErr.statusCode == http.StatusBadRequest &&
		responseErr.grpcCode == 9 && responseErr.message == "another operation is already in progress" {
		err = steps.Retryable(err)
	}
	return fmt.Errorf("argocd sync application %q at revision %q: %w", app, revision, err)
}
