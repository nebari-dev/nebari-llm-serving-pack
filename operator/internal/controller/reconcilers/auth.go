package reconcilers

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// apiKeyClientIDHeader is the request header Envoy Gateway populates with the
// authenticated API key's client ID (the api-keys Secret data-key name). The
// per-route authorization rule matches this header against the model's own
// client IDs so a key minted for one model cannot call another. The api_key_auth
// filter (order 6) runs before the RBAC/authorization filter (order 301), so the
// header is present when authorization evaluates.
const apiKeyClientIDHeader = "x-llm-client-id"

// authzActionAllow and authzActionDeny are the Envoy Gateway SecurityPolicy
// authorization action values. Both the external (API-key) and internal (JWT)
// policies default to Deny and add a single Allow rule scoped to the model's
// own principals.
const (
	authzActionAllow = "Allow"
	authzActionDeny  = "Deny"
)

// AuthResources holds all auth-related resources for an LLMModel. All
// resources live in the LLMModel's own namespace - the operator no longer
// uses a separate api-keys namespace because Envoy Gateway's
// SecurityPolicy.spec.apiKeyAuth.credentialRefs rejects cross-namespace
// Secret references (it does not honor ReferenceGrant for that field; see
// nebari-dev/nebari-llm-serving-pack#59). The model webhook enforces that
// LLMModel CRs live in the operator's namespace so that the key-manager
// (also in the operator namespace) can find these Secrets.
type AuthResources struct {
	APIKeySecret           *corev1.Secret
	APIKeyMetadataCM       *corev1.ConfigMap
	ExternalSecurityPolicy *unstructured.Unstructured // apiKeyAuth SecurityPolicy, nil if external disabled
	InternalSecurityPolicy *unstructured.Unstructured // JWT SecurityPolicy, nil if internal disabled
}

// BuildAuthResources is a pure function that computes all auth-related Kubernetes resources
// for the given LLMModel: API key Secret, metadata ConfigMap, and SecurityPolicies.
// All resources are placed in the LLMModel's own namespace.
func BuildAuthResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig, clientIDs []string, credentialSecretNames []string) (*AuthResources, error) {
	result := &AuthResources{}

	labels := authResourceLabels(model)

	result.APIKeySecret = buildAPIKeySecret(model, labels)
	result.APIKeyMetadataCM = buildAPIKeyMetadataConfigMap(model, labels)

	if boolOrDefault(model.Spec.Endpoints.External.Enabled, true) {
		result.ExternalSecurityPolicy = buildExternalSecurityPolicy(model, clientIDs, credentialSecretNames)
	}

	if boolOrDefault(model.Spec.Endpoints.Internal.Enabled, true) {
		result.InternalSecurityPolicy = buildInternalSecurityPolicy(model, cfg)
	}

	return result, nil
}

// ClientIDsFromSecret returns the sorted data-key names of an api-keys Secret.
// Each data-key name is an Envoy Gateway client ID (one per minted API key;
// the key-manager writes secret.Data[clientID] = apiKey). The list is sorted so
// SecurityPolicy rendering is deterministic across reconciles. A nil or
// dataless Secret yields an empty slice.
func ClientIDsFromSecret(secret *corev1.Secret) []string {
	if secret == nil {
		return []string{}
	}
	ids := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	return ids
}

// authResourceLabels returns the labels applied to the API-key Secret and
// metadata ConfigMap. The `llm.nebari.dev/model` label (from StandardLabels)
// is the documented selector for `kubectl get secrets -l llm.nebari.dev/model`
// (see https://nebari-dev.github.io/nebari-llm-serving-pack/local-development/).
// The additional `llm.nebari.dev/model-name`
// label is required by the key-manager, which filters API-key metadata
// ConfigMaps on it (see key-manager/internal/secrets/manager.go). The
// `model-namespace` label was redundant once these resources moved into the
// model's own namespace (#59) and was dropped.
func authResourceLabels(model *llmv1alpha1.LLMModel) map[string]string {
	labels := StandardLabels(model)
	labels["llm.nebari.dev/model-name"] = model.Name
	return labels
}

func buildAPIKeySecret(model *llmv1alpha1.LLMModel, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      APIKeySecretName(model.Name),
			Namespace: model.Namespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func buildAPIKeyMetadataConfigMap(model *llmv1alpha1.LLMModel, labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      APIKeyMetadataConfigMapName(model.Name),
			Namespace: model.Namespace,
			Labels:    labels,
		},
	}
}

func buildExternalSecurityPolicy(model *llmv1alpha1.LLMModel, clientIDs []string, credentialSecretNames []string) *unstructured.Unstructured {
	return buildAPIKeyAuthSecurityPolicy(
		model.Name+"-external-auth",
		model.Namespace,
		labelsToInterface(StandardLabels(model)),
		model.Name+"-external",
		credentialSecretNames,
		clientIDs,
	)
}

// buildAPIKeyAuthSecurityPolicy renders the SecurityPolicy that gates an
// external HTTPRoute behind per-user API keys. Shared between the LLMModel
// and PassthroughModel reconcilers so both endpoint types present the same
// client UX (Authorization header carrying a key from the api-keys Secret).
func buildAPIKeyAuthSecurityPolicy(name, namespace string, labels map[string]interface{}, routeName string, credentialSecretNames []string, clientIDs []string) *unstructured.Unstructured {
	// credentialRefs pools EVERY model's api-keys Secret on the shared
	// listener, not just this model's. EG's api_key_auth filter runs before
	// the AI Gateway ext_proc resolves the model, so authentication happens on
	// whichever catch-all route wins; pooling lets any valid key authenticate
	// there, and the per-route authorization block below scopes it to this
	// model. All Secrets are same-namespace (#59). See #116.
	credRefs := make([]interface{}, 0, len(credentialSecretNames))
	for _, secretName := range credentialSecretNames {
		credRefs = append(credRefs, map[string]interface{}{
			"group": "",
			"kind":  "Secret",
			"name":  secretName,
		})
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "SecurityPolicy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels":    labels,
			},
			"spec": map[string]interface{}{
				"targetRefs": []interface{}{
					map[string]interface{}{
						"group": "gateway.networking.k8s.io",
						"kind":  "HTTPRoute",
						"name":  routeName,
					},
				},
				"apiKeyAuth": map[string]interface{}{
					// No `namespace` field - Envoy Gateway's APIKeyAuth rejects
					// any cross-namespace credentialRef (and does not honor
					// ReferenceGrant for this field). The Secret lives in the
					// same namespace as this SecurityPolicy by design.
					"credentialRefs": credRefs,
					"extractFrom": []interface{}{
						map[string]interface{}{
							"headers": []interface{}{
								"Authorization",
							},
						},
					},
					// forwardClientIDHeader surfaces the matched credential's
					// client ID to the authorization filter (and upstream).
					// sanitize strips the API key before the backend sees it.
					"forwardClientIDHeader": apiKeyClientIDHeader,
					"sanitize":              true,
				},
				// Authentication (apiKeyAuth) is pooled per listener: any valid
				// key on the shared listener authenticates. Authorization is
				// per-route and is what scopes a key to its model: deny by
				// default, allow only this model's client IDs.
				"authorization": buildAPIKeyAuthorization(clientIDs),
			},
		},
	}
}

// buildAPIKeyAuthorization returns a deny-by-default authorization block that
// allows only the given client IDs (matched on the forwarded client-ID header).
// With no client IDs it returns deny-all (no Allow rule), which is correct for a
// model that has no keys minted yet: every request, including one bearing a key
// valid for a different model on the shared listener, is denied.
func buildAPIKeyAuthorization(clientIDs []string) map[string]interface{} {
	authz := map[string]interface{}{
		"defaultAction": authzActionDeny,
	}
	if len(clientIDs) == 0 {
		return authz
	}
	values := make([]interface{}, 0, len(clientIDs))
	for _, id := range clientIDs {
		values = append(values, id)
	}
	authz["rules"] = []interface{}{
		map[string]interface{}{
			"name":   "allow-model-clients",
			"action": authzActionAllow,
			"principal": map[string]interface{}{
				"headers": []interface{}{
					map[string]interface{}{
						"name":   apiKeyClientIDHeader,
						"values": values,
					},
				},
			},
		},
	}
	return authz
}

func buildInternalSecurityPolicy(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) *unstructured.Unstructured {
	return buildJWTSecurityPolicy(
		model.Name+"-internal-auth",
		model.Namespace,
		labelsToInterface(StandardLabels(model)),
		model.Name+"-internal",
		cfg,
		isPublic(model),
		model.Spec.Access.Groups,
	)
}

// buildJWTSecurityPolicy renders the SecurityPolicy that gates an internal
// HTTPRoute behind Keycloak JWT validation, with group authorization when
// the access policy is not public. Shared between the LLMModel and
// PassthroughModel reconcilers.
func buildJWTSecurityPolicy(name, namespace string, labels map[string]interface{}, routeName string, cfg *config.OperatorConfig, public bool, groups []string) *unstructured.Unstructured {
	// Keycloak-specific JWKS path. The rest of the pack assumes Keycloak
	// (issuer URL convention, group-membership mapper, etc.) and Keycloak
	// does not serve JWKS at /.well-known/jwks.json. See issue #61.
	jwksURI := cfg.OIDCJWKSURI
	if jwksURI == "" {
		jwksURI = cfg.OIDCIssuerURL + "/protocol/openid-connect/certs"
	}
	remoteJWKS := map[string]interface{}{
		"uri": jwksURI,
	}
	// With a backend Service configured, Envoy routes the JWKS fetch to
	// that Service instead of resolving the URI host through DNS and
	// trusting its certificate. This is the private-CA escape hatch: an
	// http:// in-cluster URI plus a backendRef needs no trust material at
	// all, while the issuer above still validates the external `iss`
	// claim. Cross-namespace refs need a ReferenceGrant in the Service's
	// namespace (the chart renders one).
	if cfg.OIDCJWKSBackendService != "" {
		backendRef := map[string]interface{}{
			"group": "",
			"kind":  "Service",
			"name":  cfg.OIDCJWKSBackendService,
			"port":  int64(cfg.OIDCJWKSBackendPort),
		}
		if cfg.OIDCJWKSBackendNamespace != "" {
			backendRef["namespace"] = cfg.OIDCJWKSBackendNamespace
		}
		remoteJWKS["backendRefs"] = []interface{}{backendRef}
	}

	provider := map[string]interface{}{
		"name":       "oidc",
		"issuer":     cfg.OIDCIssuerURL,
		"remoteJWKS": remoteJWKS,
		"claimToHeaders": []interface{}{
			map[string]interface{}{
				"header": "X-Auth-Groups",
				"claim":  cfg.OIDCGroupsClaim,
			},
			map[string]interface{}{
				"header": "X-Auth-User",
				"claim":  "preferred_username",
			},
		},
	}

	if cfg.OIDCAudience != "" {
		provider["audiences"] = []interface{}{cfg.OIDCAudience}
	}

	spec := map[string]interface{}{
		"targetRefs": []interface{}{
			map[string]interface{}{
				"group": "gateway.networking.k8s.io",
				"kind":  "HTTPRoute",
				"name":  routeName,
			},
		},
		"jwt": map[string]interface{}{
			"providers": []interface{}{
				provider,
			},
		},
	}

	// When access is not public, add an authorization block that requires
	// the JWT's groups claim to contain at least one of the allowed groups.
	// The webhooks reject CRs where Public is false and Groups is empty, so
	// we never render an Allow rule with no values.
	if !public {
		spec["authorization"] = buildGroupAuthorization(groups, cfg.OIDCGroupsClaim)
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.envoyproxy.io/v1alpha1",
			"kind":       "SecurityPolicy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels":    labels,
			},
			"spec": spec,
		},
	}
}

// isPublic returns true when the model's access policy is explicitly public.
func isPublic(model *llmv1alpha1.LLMModel) bool {
	return isPublicAccess(model.Spec.Access)
}

// buildGroupAuthorization returns an Envoy Gateway Authorization config that
// allows requests whose JWT groups claim contains one of the listed groups and
// denies everything else by default.
func buildGroupAuthorization(groups []string, groupsClaim string) map[string]interface{} {
	values := make([]interface{}, 0, len(groups))
	for _, g := range groups {
		values = append(values, g)
	}
	return map[string]interface{}{
		"defaultAction": authzActionDeny,
		"rules": []interface{}{
			map[string]interface{}{
				"name":   "allow-groups",
				"action": authzActionAllow,
				"principal": map[string]interface{}{
					"jwt": map[string]interface{}{
						"provider": "oidc",
						"claims": []interface{}{
							map[string]interface{}{
								"name":      groupsClaim,
								"valueType": "StringArray",
								"values":    values,
							},
						},
					},
				},
			},
		},
	}
}
