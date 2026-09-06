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
	"reflect"
	"strings"
	"testing"
)

func TestParseDefaultImages(t *testing.T) {
	t.Parallel()

	values := []byte(`
keyManager:
  image:
    repository: ghcr.io/example/key-manager
    tag: "latest"
    pullPolicy: Always
defaults:
  serving:
    image: ghcr.io/example/serving:v1.2.3
  epp:
    image: ghcr.io/example/epp@sha256:1234
`)

	got, err := ParseDefaultImages(values)
	if err != nil {
		t.Fatalf("ParseDefaultImages() unexpected error: %v", err)
	}
	want := []DefaultImage{
		{Path: "defaults.epp.image", Reference: "ghcr.io/example/epp@sha256:1234"},
		{Path: "defaults.serving.image", Reference: "ghcr.io/example/serving:v1.2.3"},
		{Path: "keyManager.image", Reference: "ghcr.io/example/key-manager:latest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDefaultImages() = %#v, want %#v", got, want)
	}
}

func TestParseDefaultImagesRejectsIncompleteImageMapping(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte(`component:
  image:
    repository: ghcr.io/example/component
`))
	if err == nil || !strings.Contains(err.Error(), "component.image.tag") {
		t.Fatalf("ParseDefaultImages() error = %v, want missing component.image.tag", err)
	}
}

func TestParseDefaultImagesRejectsEmptySet(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte("feature:\n  enabled: true\n"))
	if err == nil || !strings.Contains(err.Error(), "no image defaults") {
		t.Fatalf("ParseDefaultImages() error = %v, want no image defaults", err)
	}
}

func TestParseDefaultImagesRejectsInvalidYAML(t *testing.T) {
	t.Parallel()

	_, err := ParseDefaultImages([]byte("component: [\n"))
	if err == nil || !strings.Contains(err.Error(), "parsing values YAML") {
		t.Fatalf("ParseDefaultImages() error = %v, want YAML parse error", err)
	}
}
