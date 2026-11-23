package argocd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/labels"
)

type manifestHeader struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type manifestMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type deploymentManifest struct {
	Metadata manifestMetadata `json:"metadata"`
	Spec     deploymentSpec   `json:"spec"`
}

type deploymentSpec struct {
	Template podTemplate `json:"template"`
}

type podTemplate struct {
	Metadata manifestMetadata `json:"metadata"`
	Spec     podSpec          `json:"spec"`
}

type podSpec struct {
	Containers []containerImage `json:"containers"`
}

type containerImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// desiredImages selects exactly one Deployment from rendered YAML or JSON.
func desiredImages(manifests []string, defaultNamespace, namespace string, selector labels.Selector) (map[string]string, error) {
	var selected *deploymentManifest
	for i, text := range manifests {
		decoder := yaml.NewDecoder(strings.NewReader(text))
		for document := 1; ; document++ {
			var value any
			if err := decoder.Decode(&value); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("manifest %d document %d: %w", i+1, document, err)
			}
			if value == nil {
				continue
			}
			// JSON decoding keeps numeric YAML fields from becoming strings.
			data, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("manifest %d document %d: %w", i+1, document, err)
			}
			var header manifestHeader
			if err := json.Unmarshal(data, &header); err != nil {
				return nil, fmt.Errorf("manifest %d document %d: %w", i+1, document, err)
			}
			if header.Kind == "" || header.APIVersion == "" {
				return nil, fmt.Errorf("manifest %d document %d: apiVersion and kind are required", i+1, document)
			}
			if header.Kind == "List" || header.Kind == "DeploymentList" {
				return nil, fmt.Errorf("manifest %d: %s resources are not supported in M0", i+1, header.Kind)
			}
			if header.Kind != "Deployment" {
				continue
			}
			if header.APIVersion != "apps/v1" {
				return nil, fmt.Errorf("manifest %d: Deployment API %q is not supported in M0", i+1, header.APIVersion)
			}
			var deployment deploymentManifest
			if err := json.Unmarshal(data, &deployment); err != nil {
				return nil, fmt.Errorf("manifest %d Deployment: %w", i+1, err)
			}
			workloadNamespace := deployment.Metadata.Namespace
			if workloadNamespace == "" {
				workloadNamespace = defaultNamespace
			}
			if workloadNamespace == "" {
				return nil, fmt.Errorf("Deployment %q has no namespace and the Application has no destination namespace", deployment.Metadata.Name)
			}
			if workloadNamespace != namespace || !selector.Matches(labels.Set(deployment.Spec.Template.Metadata.Labels)) {
				continue
			}
			if strings.TrimSpace(deployment.Metadata.Name) == "" {
				return nil, errors.New("matching Deployment has no name")
			}
			if selected != nil {
				return nil, fmt.Errorf("multiple Deployments match namespace %q and selector %q: %s, %s", namespace, selector, selected.Metadata.Name, deployment.Metadata.Name)
			}
			selected = &deployment
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("no Deployment matches namespace %q and selector %q", namespace, selector)
	}
	return selected.containerImages()
}

// containerImages returns regular container images keyed by name.
func (d *deploymentManifest) containerImages() (map[string]string, error) {
	containers := d.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return nil, fmt.Errorf("Deployment %q has no regular containers", d.Metadata.Name)
	}
	images := make(map[string]string, len(containers))
	for _, container := range containers {
		if strings.TrimSpace(container.Name) == "" || strings.TrimSpace(container.Image) == "" {
			return nil, fmt.Errorf("Deployment %q: each regular container must have a name and image", d.Metadata.Name)
		}
		if _, duplicate := images[container.Name]; duplicate {
			return nil, fmt.Errorf("Deployment %q has duplicate container name %q", d.Metadata.Name, container.Name)
		}
		images[container.Name] = container.Image
	}
	return images, nil
}
