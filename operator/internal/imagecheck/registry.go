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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

var bearerAttributePattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_-]*)="([^"]*)"`)

type imageReference struct {
	Registry   string
	Repository string
	Reference  string
}

// Checker verifies that an anonymously accessible OCI image manifest exists.
type Checker struct {
	client *http.Client
}

// NewChecker creates a registry checker. A nil client uses a bounded default.
func NewChecker(client *http.Client) *Checker {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Checker{client: client}
}

// Check completes the registry's anonymous Bearer challenge, then verifies the
// manifest with HEAD. It never reads Docker credentials or sends basic auth.
func (c *Checker) Check(ctx context.Context, image string) error {
	reference, err := parseImageReference(image)
	if err != nil {
		return err
	}

	response, err := c.headManifest(ctx, reference, "")
	if err != nil {
		return fmt.Errorf("requesting manifest challenge: %w", err)
	}
	if isSuccess(response.StatusCode) {
		closeResponseBody(response)
		return nil
	}
	if response.StatusCode != http.StatusUnauthorized {
		defer closeResponseBody(response)
		return manifestStatusError(response.StatusCode)
	}

	challenge := response.Header.Get("WWW-Authenticate")
	closeResponseBody(response)
	token, err := c.requestAnonymousToken(ctx, challenge)
	if err != nil {
		return err
	}

	response, err = c.headManifest(ctx, reference, token)
	if err != nil {
		return fmt.Errorf("requesting authenticated manifest HEAD: %w", err)
	}
	defer closeResponseBody(response)
	if !isSuccess(response.StatusCode) {
		return manifestStatusError(response.StatusCode)
	}
	return nil
}

func parseImageReference(image string) (imageReference, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return imageReference{}, fmt.Errorf("image reference is empty")
	}
	if strings.Contains(image, "://") {
		return imageReference{}, fmt.Errorf("image reference %q must not include a URL scheme", image)
	}

	lastSlash := strings.LastIndex(image, "/")
	if lastSlash <= 0 {
		return imageReference{}, fmt.Errorf("image reference %q must include an explicit registry and repository", image)
	}

	name := image
	manifestReference := ""
	if at := strings.LastIndex(image, "@"); at > lastSlash {
		name = image[:at]
		manifestReference = image[at+1:]
	} else if colon := strings.LastIndex(image, ":"); colon > lastSlash {
		name = image[:colon]
		manifestReference = image[colon+1:]
	}
	if manifestReference == "" {
		return imageReference{}, fmt.Errorf("image reference %q must include an explicit tag or digest", image)
	}

	registry, repository, ok := strings.Cut(name, "/")
	if !ok || registry == "" || repository == "" {
		return imageReference{}, fmt.Errorf("image reference %q must include an explicit registry and repository", image)
	}
	if !strings.Contains(registry, ".") && !strings.Contains(registry, ":") && registry != "localhost" {
		return imageReference{}, fmt.Errorf("image reference %q must use a fully qualified registry", image)
	}

	return imageReference{
		Registry:   registry,
		Repository: repository,
		Reference:  manifestReference,
	}, nil
}

func (c *Checker) headManifest(ctx context.Context, image imageReference, token string) (*http.Response, error) {
	manifestURL := fmt.Sprintf(
		"https://%s/v2/%s/manifests/%s",
		image.Registry,
		image.Repository,
		url.PathEscape(image.Reference),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building manifest request: %w", err)
	}
	request.Header.Set("Accept", manifestAccept)
	request.Header.Set("User-Agent", "nebari-llm-serving-image-check/1.0")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return c.client.Do(request)
}

func (c *Checker) requestAnonymousToken(ctx context.Context, challenge string) (string, error) {
	tokenURL, err := parseBearerChallenge(challenge)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("building anonymous token request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "nebari-llm-serving-image-check/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("requesting anonymous token: %w", err)
	}
	defer closeResponseBody(response)
	if !isSuccess(response.StatusCode) {
		return "", tokenStatusError(response.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decoding anonymous token response: %w", err)
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	if payload.AccessToken != "" {
		return payload.AccessToken, nil
	}
	return "", fmt.Errorf("anonymous token response did not contain a token")
}

func parseBearerChallenge(challenge string) (*url.URL, error) {
	challenge = strings.TrimSpace(challenge)
	if len(challenge) < len("Bearer ") || !strings.EqualFold(challenge[:len("Bearer ")], "Bearer ") {
		return nil, fmt.Errorf("registry did not provide a Bearer authentication challenge")
	}

	attributes := make(map[string]string)
	for _, match := range bearerAttributePattern.FindAllStringSubmatch(challenge[len("Bearer "):], -1) {
		attributes[strings.ToLower(match[1])] = match[2]
	}
	realm := attributes["realm"]
	if realm == "" {
		return nil, fmt.Errorf("registry Bearer challenge did not include a realm")
	}

	tokenURL, err := url.Parse(realm)
	if err != nil {
		return nil, fmt.Errorf("parsing registry token realm: %w", err)
	}
	if tokenURL.Scheme != "https" || tokenURL.Host == "" {
		return nil, fmt.Errorf("registry token realm must be an absolute HTTPS URL")
	}
	query := tokenURL.Query()
	if service := attributes["service"]; service != "" {
		query.Set("service", service)
	}
	if scope := attributes["scope"]; scope != "" {
		query.Set("scope", scope)
	}
	tokenURL.RawQuery = query.Encode()
	return tokenURL, nil
}

func manifestStatusError(statusCode int) error {
	switch statusCode {
	case http.StatusNotFound:
		return fmt.Errorf("manifest not found: HTTP %d", statusCode)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("manifest is not anonymously accessible: HTTP %d", statusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("registry rate limit exceeded: HTTP %d", statusCode)
	default:
		if statusCode >= http.StatusInternalServerError {
			return fmt.Errorf("registry unavailable: HTTP %d", statusCode)
		}
		return fmt.Errorf("manifest check failed: HTTP %d", statusCode)
	}
}

func tokenStatusError(statusCode int) error {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("anonymous token request denied: HTTP %d", statusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("registry token service rate limit exceeded: HTTP %d", statusCode)
	default:
		if statusCode >= http.StatusInternalServerError {
			return fmt.Errorf("registry token service unavailable: HTTP %d", statusCode)
		}
		return fmt.Errorf("anonymous token request failed: HTTP %d", statusCode)
	}
}

func isSuccess(statusCode int) bool {
	return statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices
}

func closeResponseBody(response *http.Response) {
	_ = response.Body.Close()
}
