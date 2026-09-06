/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package imagecheck

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// DefaultImage records an image reference and its location in values.yaml.
type DefaultImage struct {
	Path      string
	Reference string
}

// ParseDefaultImages finds every value named image. It supports both a full
// scalar reference and a mapping with repository and tag fields.
func ParseDefaultImages(valuesYAML []byte) ([]DefaultImage, error) {
	var values any
	if err := yaml.Unmarshal(valuesYAML, &values); err != nil {
		return nil, fmt.Errorf("parsing values YAML: %w", err)
	}

	images := make([]DefaultImage, 0)
	if err := collectDefaultImages(values, nil, &images); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("no image defaults found")
	}

	sort.Slice(images, func(i, j int) bool {
		return images[i].Path < images[j].Path
	})
	return images, nil
}

func collectDefaultImages(value any, path []string, images *[]DefaultImage) error {
	switch node := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			childPath := appendPath(path, key)
			if key == "image" {
				reference, err := parseImageValue(node[key], childPath)
				if err != nil {
					return err
				}
				*images = append(*images, DefaultImage{
					Path:      strings.Join(childPath, "."),
					Reference: reference,
				})
				continue
			}
			if err := collectDefaultImages(node[key], childPath, images); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range node {
			childPath := appendPath(path, fmt.Sprintf("[%d]", index))
			if err := collectDefaultImages(child, childPath, images); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseImageValue(value any, path []string) (string, error) {
	pathText := strings.Join(path, ".")
	switch image := value.(type) {
	case string:
		if strings.TrimSpace(image) == "" {
			return "", fmt.Errorf("%s is empty", pathText)
		}
		return strings.TrimSpace(image), nil
	case map[string]any:
		repository, err := requiredString(image, "repository", pathText)
		if err != nil {
			return "", err
		}
		tag, err := requiredString(image, "tag", pathText)
		if err != nil {
			return "", err
		}
		return repository + ":" + tag, nil
	default:
		return "", fmt.Errorf("%s must be a string or repository/tag mapping", pathText)
	}
}

func requiredString(values map[string]any, key, parentPath string) (string, error) {
	value, ok := values[key]
	if !ok {
		return "", fmt.Errorf("%s.%s is required", parentPath, key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s.%s must be a non-empty string", parentPath, key)
	}
	return strings.TrimSpace(text), nil
}

func appendPath(path []string, element string) []string {
	result := make([]string, len(path), len(path)+1)
	copy(result, path)
	return append(result, element)
}
