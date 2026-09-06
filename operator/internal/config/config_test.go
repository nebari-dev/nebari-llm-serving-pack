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

package config

import (
	"strings"
	"testing"
)

func TestLoadFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		wantErr     bool
		errContains []string
		wantConfig  *OperatorConfig
	}{
		{
			name: "all env vars set - parses correctly",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":                "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME":      "external-gw",
				"LLM_EXTERNAL_GATEWAY_NAMESPACE": "custom-external-ns",
				"LLM_INTERNAL_GATEWAY_NAME":      "internal-gw",
				"LLM_INTERNAL_GATEWAY_NAMESPACE": "custom-internal-ns",
				"LLM_OIDC_ISSUER_URL":            "https://auth.example.com",
				"LLM_OIDC_GROUPS_CLAIM":          "custom-groups",
				"LLM_OIDC_AUDIENCE":              "my-audience",
				"LLM_DEFAULT_SERVING_IMAGE":      "my-registry/my-image:v1.0.0",
				"LLM_DEFAULT_EPP_IMAGE":          "my-registry/my-epp:v1.0.0",
				"LLM_API_KEYS_NAMESPACE":         "my-api-keys-ns",
				"POD_NAMESPACE":                  "llm-serving",
				"LLM_CLUSTER_ISSUER_NAME":        "my-issuer",
				"LLM_MANAGE_SHARED_LISTENERS":    "false",
			},
			wantErr: false,
			wantConfig: &OperatorConfig{
				BaseDomain:            "example.com",
				ExternalGatewayName:   "external-gw",
				ExternalGatewayNS:     "custom-external-ns",
				InternalGatewayName:   "internal-gw",
				InternalGatewayNS:     "custom-internal-ns",
				OIDCIssuerURL:         "https://auth.example.com",
				OIDCGroupsClaim:       "custom-groups",
				OIDCAudience:          "my-audience",
				DefaultServingImage:   "my-registry/my-image:v1.0.0",
				DefaultEPPImage:       "my-registry/my-epp:v1.0.0",
				APIKeysNamespace:      "my-api-keys-ns",
				OperatorNamespace:     "llm-serving",
				ClusterIssuerName:     "my-issuer",
				ManageSharedListeners: false,
			},
		},
		{
			name: "missing LLM_BASE_DOMAIN - returns error mentioning the var name",
			envVars: map[string]string{
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
				"POD_NAMESPACE":             "llm-serving",
			},
			wantErr:     true,
			errContains: []string{"LLM_BASE_DOMAIN"},
		},
		{
			name: "missing LLM_EXTERNAL_GATEWAY_NAME - returns error",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
				"POD_NAMESPACE":             "llm-serving",
			},
			wantErr:     true,
			errContains: []string{"LLM_EXTERNAL_GATEWAY_NAME"},
		},
		{
			name: "missing LLM_INTERNAL_GATEWAY_NAME - returns error",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
				"POD_NAMESPACE":             "llm-serving",
			},
			wantErr:     true,
			errContains: []string{"LLM_INTERNAL_GATEWAY_NAME"},
		},
		{
			name: "missing LLM_OIDC_ISSUER_URL - returns error",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
			},
			wantErr:     true,
			errContains: []string{"LLM_OIDC_ISSUER_URL"},
		},
		{
			name: "optional vars missing - uses defaults",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
				"POD_NAMESPACE":             "llm-serving",
			},
			wantErr: false,
			wantConfig: &OperatorConfig{
				BaseDomain:            "example.com",
				ExternalGatewayName:   "external-gw",
				ExternalGatewayNS:     "envoy-gateway-system",
				InternalGatewayName:   "internal-gw",
				InternalGatewayNS:     "envoy-gateway-system",
				OIDCIssuerURL:         "https://auth.example.com",
				OIDCGroupsClaim:       "groups",
				OIDCAudience:          "",
				DefaultServingImage:   "ghcr.io/llm-d/llm-d-cuda:v0.7.0",
				DefaultEPPImage:       "ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.9.0",
				APIKeysNamespace:      "",
				OperatorNamespace:     "llm-serving",
				ClusterIssuerName:     "letsencrypt-production",
				ManageSharedListeners: true,
			},
		},
		{
			name: "OIDCAudience empty - no error",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
				"LLM_OIDC_AUDIENCE":         "",
				"POD_NAMESPACE":             "llm-serving",
			},
			wantErr: false,
			wantConfig: &OperatorConfig{
				BaseDomain:            "example.com",
				ExternalGatewayName:   "external-gw",
				ExternalGatewayNS:     "envoy-gateway-system",
				InternalGatewayName:   "internal-gw",
				InternalGatewayNS:     "envoy-gateway-system",
				OIDCIssuerURL:         "https://auth.example.com",
				OIDCGroupsClaim:       "groups",
				OIDCAudience:          "",
				DefaultServingImage:   "ghcr.io/llm-d/llm-d-cuda:v0.7.0",
				DefaultEPPImage:       "ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.9.0",
				APIKeysNamespace:      "",
				OperatorNamespace:     "llm-serving",
				ClusterIssuerName:     "letsencrypt-production",
				ManageSharedListeners: true,
			},
		},
		{
			name:    "all required vars missing - error mentions all of them",
			envVars: map[string]string{},
			wantErr: true,
			errContains: []string{
				"LLM_BASE_DOMAIN",
				"LLM_EXTERNAL_GATEWAY_NAME",
				"LLM_INTERNAL_GATEWAY_NAME",
				"LLM_OIDC_ISSUER_URL",
			},
		},
		{
			name: "empty POD_NAMESPACE is allowed (webhook test mode)",
			envVars: map[string]string{
				"LLM_BASE_DOMAIN":           "example.com",
				"LLM_EXTERNAL_GATEWAY_NAME": "external-gw",
				"LLM_INTERNAL_GATEWAY_NAME": "internal-gw",
				"LLM_OIDC_ISSUER_URL":       "https://auth.example.com",
			},
			wantErr: false,
			wantConfig: &OperatorConfig{
				BaseDomain:            "example.com",
				ExternalGatewayName:   "external-gw",
				ExternalGatewayNS:     "envoy-gateway-system",
				InternalGatewayName:   "internal-gw",
				InternalGatewayNS:     "envoy-gateway-system",
				OIDCIssuerURL:         "https://auth.example.com",
				OIDCGroupsClaim:       "groups",
				DefaultServingImage:   "ghcr.io/llm-d/llm-d-cuda:v0.7.0",
				DefaultEPPImage:       "ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.9.0",
				OperatorNamespace:     "",
				ClusterIssuerName:     "letsencrypt-production",
				ManageSharedListeners: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env vars before setting the test ones.
			allVars := []string{
				"LLM_BASE_DOMAIN",
				"LLM_EXTERNAL_GATEWAY_NAME",
				"LLM_EXTERNAL_GATEWAY_NAMESPACE",
				"LLM_INTERNAL_GATEWAY_NAME",
				"LLM_INTERNAL_GATEWAY_NAMESPACE",
				"LLM_OIDC_ISSUER_URL",
				"LLM_OIDC_GROUPS_CLAIM",
				"LLM_OIDC_AUDIENCE",
				"LLM_DEFAULT_SERVING_IMAGE",
				"LLM_DEFAULT_EPP_IMAGE",
				"LLM_API_KEYS_NAMESPACE",
				"POD_NAMESPACE",
				"LLM_CLUSTER_ISSUER_NAME",
				"LLM_MANAGE_SHARED_LISTENERS",
			}
			for _, v := range allVars {
				t.Setenv(v, "")
			}
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			got, err := LoadFromEnv()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadFromEnv() expected error but got nil")
				}
				for _, substr := range tt.errContains {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("LoadFromEnv() error = %q, want it to contain %q", err.Error(), substr)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("LoadFromEnv() unexpected error: %v", err)
			}

			if got == nil {
				t.Fatal("LoadFromEnv() returned nil config with no error")
			}

			if got.BaseDomain != tt.wantConfig.BaseDomain {
				t.Errorf("BaseDomain = %q, want %q", got.BaseDomain, tt.wantConfig.BaseDomain)
			}
			if got.ExternalGatewayName != tt.wantConfig.ExternalGatewayName {
				t.Errorf("ExternalGatewayName = %q, want %q", got.ExternalGatewayName, tt.wantConfig.ExternalGatewayName)
			}
			if got.ExternalGatewayNS != tt.wantConfig.ExternalGatewayNS {
				t.Errorf("ExternalGatewayNS = %q, want %q", got.ExternalGatewayNS, tt.wantConfig.ExternalGatewayNS)
			}
			if got.InternalGatewayName != tt.wantConfig.InternalGatewayName {
				t.Errorf("InternalGatewayName = %q, want %q", got.InternalGatewayName, tt.wantConfig.InternalGatewayName)
			}
			if got.InternalGatewayNS != tt.wantConfig.InternalGatewayNS {
				t.Errorf("InternalGatewayNS = %q, want %q", got.InternalGatewayNS, tt.wantConfig.InternalGatewayNS)
			}
			if got.OIDCIssuerURL != tt.wantConfig.OIDCIssuerURL {
				t.Errorf("OIDCIssuerURL = %q, want %q", got.OIDCIssuerURL, tt.wantConfig.OIDCIssuerURL)
			}
			if got.OIDCGroupsClaim != tt.wantConfig.OIDCGroupsClaim {
				t.Errorf("OIDCGroupsClaim = %q, want %q", got.OIDCGroupsClaim, tt.wantConfig.OIDCGroupsClaim)
			}
			if got.OIDCAudience != tt.wantConfig.OIDCAudience {
				t.Errorf("OIDCAudience = %q, want %q", got.OIDCAudience, tt.wantConfig.OIDCAudience)
			}
			if got.DefaultServingImage != tt.wantConfig.DefaultServingImage {
				t.Errorf("DefaultServingImage = %q, want %q", got.DefaultServingImage, tt.wantConfig.DefaultServingImage)
			}
			if got.DefaultEPPImage != tt.wantConfig.DefaultEPPImage {
				t.Errorf("DefaultEPPImage = %q, want %q", got.DefaultEPPImage, tt.wantConfig.DefaultEPPImage)
			}
			if got.APIKeysNamespace != tt.wantConfig.APIKeysNamespace {
				t.Errorf("APIKeysNamespace = %q, want %q", got.APIKeysNamespace, tt.wantConfig.APIKeysNamespace)
			}
			if got.OperatorNamespace != tt.wantConfig.OperatorNamespace {
				t.Errorf("OperatorNamespace = %q, want %q", got.OperatorNamespace, tt.wantConfig.OperatorNamespace)
			}
			if got.ClusterIssuerName != tt.wantConfig.ClusterIssuerName {
				t.Errorf("ClusterIssuerName = %q, want %q", got.ClusterIssuerName, tt.wantConfig.ClusterIssuerName)
			}
			if got.ManageSharedListeners != tt.wantConfig.ManageSharedListeners {
				t.Errorf("ManageSharedListeners = %v, want %v", got.ManageSharedListeners, tt.wantConfig.ManageSharedListeners)
			}
		})
	}
}
