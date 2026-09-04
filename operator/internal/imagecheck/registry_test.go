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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testTokenPath = "/token"

func TestParseImageReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		image      string
		registry   string
		repository string
		reference  string
	}{
		{
			name:       "tag",
			image:      "ghcr.io/example/model:v1.2.3",
			registry:   "ghcr.io",
			repository: "example/model",
			reference:  "v1.2.3",
		},
		{
			name:       "digest",
			image:      "registry.example.com/team/model@sha256:1234",
			registry:   "registry.example.com",
			repository: "team/model",
			reference:  "sha256:1234",
		},
		{
			name:       "registry port",
			image:      "localhost:5000/team/model:latest",
			registry:   "localhost:5000",
			repository: "team/model",
			reference:  "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseImageReference(tt.image)
			if err != nil {
				t.Fatalf("parseImageReference() unexpected error: %v", err)
			}
			if got.Registry != tt.registry || got.Repository != tt.repository || got.Reference != tt.reference {
				t.Fatalf("parseImageReference() = %#v", got)
			}
		})
	}
}

func TestParseImageReferenceRejectsIncompleteReferences(t *testing.T) {
	t.Parallel()

	for _, image := range []string{"model:latest", "ghcr.io/example/model", "https://ghcr.io/example/model:v1"} {
		t.Run(image, func(t *testing.T) {
			t.Parallel()
			if _, err := parseImageReference(image); err == nil {
				t.Fatalf("parseImageReference(%q) expected error", image)
			}
		})
	}
}

func TestCheckerCompletesAnonymousBearerChallenge(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/example/model/manifests/v1":
			if r.Method != http.MethodHead {
				t.Errorf("manifest method = %s, want HEAD", r.Method)
			}
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.manifest.v1+json") {
				t.Errorf("manifest Accept header = %q", r.Header.Get("Accept"))
			}
			if r.Header.Get("Authorization") != "Bearer anonymous-token" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					`Bearer realm=%q,service="registry.test",scope="repository:example/model:pull"`,
					server.URL+testTokenPath,
				))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		case testTokenPath:
			if r.Header.Get("Authorization") != "" {
				t.Errorf("token request unexpectedly had Authorization header")
			}
			if got := r.URL.Query().Get("service"); got != "registry.test" {
				t.Errorf("token service = %q, want registry.test", got)
			}
			if got := r.URL.Query().Get("scope"); got != "repository:example/model:pull" {
				t.Errorf("token scope = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"anonymous-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	checker := NewChecker(server.Client())
	image := strings.TrimPrefix(server.URL, "https://") + "/example/model:v1"
	if err := checker.Check(context.Background(), image); err != nil {
		t.Fatalf("Check() unexpected error: %v", err)
	}
}

func TestCheckerReportsManifestFailureAfterAuthentication(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testTokenPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"anonymous-token"}`))
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(
				`Bearer realm=%q,service="registry.test",scope="repository:example/missing:pull"`,
				server.URL+testTokenPath,
			))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	checker := NewChecker(server.Client())
	image := strings.TrimPrefix(server.URL, "https://") + "/example/missing:v1"
	err := checker.Check(context.Background(), image)
	if err == nil || !strings.Contains(err.Error(), "manifest not found") {
		t.Fatalf("Check() error = %v, want manifest not found", err)
	}
}

func TestCheckerClassifiesTokenServiceFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		want       string
	}{
		{name: "denied", statusCode: http.StatusForbidden, want: "anonymous token request denied"},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, want: "token service rate limit exceeded"},
		{name: "unavailable", statusCode: http.StatusServiceUnavailable, want: "token service unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == testTokenPath {
					w.WriteHeader(tt.statusCode)
					return
				}
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					`Bearer realm=%q,service="registry.test",scope="repository:example/private:pull"`,
					server.URL+testTokenPath,
				))
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(server.Close)

			checker := NewChecker(server.Client())
			image := strings.TrimPrefix(server.URL, "https://") + "/example/private:v1"
			err := checker.Check(context.Background(), image)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Check() error = %v, want %q", err, tt.want)
			}
		})
	}
}
