package argocd

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"k8s.io/apimachinery/pkg/labels"
)

var _ steps.ArgoClient = (*Client)(nil)

type manifestResponse struct {
	Manifests []string `json:"manifests"`
	Revision  string   `json:"revision"`
}

// GetDesiredImages reads a Deployment's desired container images at a Git SHA.
func (c *Client) GetDesiredImages(ctx context.Context, app, revision, namespace, selector string) (map[string]string, error) {
	if len(revision) != 40 {
		return nil, errors.New("argocd desired images require a full 40-character Git commit SHA")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return nil, fmt.Errorf("argocd desired images require a hexadecimal Git commit SHA: %w", err)
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("argocd desired images require a namespace")
	}
	selection, err := labels.Parse(selector)
	if err != nil {
		return nil, fmt.Errorf("argocd desired images selector: %w", err)
	}
	snapshot, err := c.getApplication(ctx, app, false)
	if err != nil {
		return nil, err
	}
	if err := snapshot.Spec.validateGitSource(); err != nil {
		return nil, fmt.Errorf("argocd desired images for application %q: %w", app, err)
	}
	revision = strings.ToLower(revision)
	query := url.Values{"revision": {revision}}
	if len(snapshot.Spec.Sources) == 1 {
		// Argo CD uses per-source revision parameters for spec.sources.
		query.Set("sourcePositions", "1")
		query.Set("revisions", revision)
	}
	var rendered *manifestResponse
	if err := c.doJSON(ctx, http.MethodGet, []string{"applications", app, "manifests"}, query, nil, &rendered); err != nil {
		return nil, fmt.Errorf("argocd manifests for application %q at %s: %w", app, revision, err)
	}
	if rendered == nil {
		return nil, fmt.Errorf("argocd manifests for application %q: response must not be null", app)
	}
	// Argo CD v3.6.2 omits revision from this endpoint's response.
	if rendered.Revision != "" && !strings.EqualFold(rendered.Revision, revision) {
		return nil, fmt.Errorf("argocd manifests revision %q does not match requested SHA %s", rendered.Revision, revision)
	}
	images, err := desiredImages(rendered.Manifests, snapshot.Spec.Destination.Namespace, namespace, selection)
	if err != nil {
		return nil, fmt.Errorf("argocd desired images for application %q at %s: %w", app, revision, err)
	}
	return images, nil
}

// validateGitSource restricts manifest reads to one effective Git source.
func (s applicationSpec) validateGitSource() error {
	if len(s.SourceHydrator) != 0 && strings.TrimSpace(string(s.SourceHydrator)) != "null" {
		return errors.New("sourceHydrator is not supported in M0")
	}
	source := s.Source
	if len(s.Sources) > 0 {
		if len(s.Sources) != 1 {
			return fmt.Errorf("M0 requires one Git source; found %d sources", len(s.Sources))
		}
		source = &s.Sources[0]
	}
	if source == nil || strings.TrimSpace(source.RepoURL) == "" {
		return errors.New("M0 requires one Git source with a repoURL")
	}
	if source.Chart != "" || strings.HasPrefix(strings.ToLower(source.RepoURL), "oci://") {
		return errors.New("Helm repository and OCI sources are not supported in M0; use a Git source")
	}
	if source.Ref != "" && source.Path == "" {
		return errors.New("reference-only sources are not supported in M0")
	}
	return nil
}
