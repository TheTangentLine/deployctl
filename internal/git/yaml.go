package git

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// GetYAMLValue returns the scalar at a dotted key path, such as
// "backend.image.tag", in a YAML document.
//
// It returns an error wrapping steps.ErrNotFound if any part of the path
// is missing, and a plain error if the path runs into something that is
// not a mapping, or ends on something that is not a scalar.
//
// Pure function: no Git, no disk. SetYAMLValue will share findNode.
func GetYAMLValue(content []byte, key string) (string, error) {
	path, err := splitKey(key)
	if err != nil {
		return "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", fmt.Errorf("parse yaml: %w", err)
	}
	n, err := findNode(&doc, path)
	if err != nil {
		return "", err
	}
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s is not a single value", key)
	}
	return n.Value, nil
}

func splitKey(key string) ([]string, error) {
	path := strings.Split(key, ".")
	for _, p := range path {
		if p == "" {
			return nil, fmt.Errorf("invalid key %q", key)
		}
	}
	return path, nil
}

// findNode walks path through nested mappings and returns the value node
// at the end.
func findNode(doc *yaml.Node, path []string) (*yaml.Node, error) {
	// An empty file parses to a zero node; treat it like an empty mapping.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s: %w", strings.Join(path, "."), steps.ErrNotFound)
	}
	n := doc.Content[0]
	for i, name := range path {
		here := strings.Join(path[:i], ".")
		if n.Kind == yaml.AliasNode {
			return nil, fmt.Errorf("%s: YAML aliases are not supported", here)
		}
		if n.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s is not a mapping, cannot look up %q", here, name)
		}
		next := lookup(n, name)
		if next == nil {
			return nil, fmt.Errorf("%s: %w", strings.Join(path[:i+1], "."), steps.ErrNotFound)
		}
		n = next
	}
	if n.Kind == yaml.AliasNode {
		return nil, errors.New(strings.Join(path, ".") + ": YAML aliases are not supported")
	}
	return n, nil
}

// lookup returns the value node for key in a mapping node, or nil.
// Mapping nodes store keys and values alternately in Content.
func lookup(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
