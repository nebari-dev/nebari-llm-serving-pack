---
title: Troubleshooting
---
This page covers concrete failure modes seen during production installs. Each section describes the symptom, the likely cause, and the exact commands to diagnose and fix it. For the full validation checklist, see the [installation runbook](/installation/).

---

## vLLM pod stuck in `Pending` - no GPU available

**Symptom:** The model pod sits in `Pending` indefinitely. `kubectl describe pod` shows an event like:

```
0/3 nodes are available: 3 Insufficient nvidia.com/gpu.
```

**Likely cause:** Either no GPU node exists in the cluster, the NVIDIA GPU Operator is not running (so `nvidia.com/gpu` is never advertised), or the GPU node is already fully consumed by another model pod.

**Fix:**

Check whether any node exposes GPU capacity:

```bash
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.capacity.nvidia\.com/gpu}{"\n"}{end}'
```

Expected output is a non-empty count (e.g. `1`) next to at least one node. If every node shows an empty value:

```bash
# Confirm the GPU Operator pods are all Running/Completed
kubectl get pods -n nvidia-gpu-operator --no-headers \
  | awk '$3 != "Running" && $3 != "Completed" {print}'
```

If any GPU Operator pod is not `Running` or `Completed`, fix it before continuing - the device plugin daemonset is what exposes `nvidia.com/gpu` to the scheduler.

For single-GPU clusters, only one model pod can run at a time. A rolling update will deadlock if the new pod cannot schedule alongside the old one. To recover:

```bash
# Scale down first, then let the replacement schedule
kubectl patch llmmodel -n nebari-llm-serving-system <model-name> \
  --type=merge -p '{"spec":{"serving":{"replicas":0}}}'
# Wait for the old pod to terminate
kubectl get pods -n nebari-llm-serving-system -w
# Then scale back up
kubectl patch llmmodel -n nebari-llm-serving-system <model-name> \
  --type=merge -p '{"spec":{"serving":{"replicas":1}}}'
```

---

## Model init container slow or stuck - download phase

**Symptom:** The model pod stays in `Init:0/1` for more than 20 minutes, or the pod keeps restarting after the init container exits non-zero.

**Likely cause:** The `model-downloader` init container is downloading the model weights from HuggingFace. Large models (30 GB+) take 10-20 minutes on typical connections. If it exits with an error, the most common causes are a missing or wrong HuggingFace token (gated model), or transient network issues.

**Fix:**

Tail the init container logs to see download progress:

```bash
kubectl logs -n nebari-llm-serving-system <vllm-pod> -c model-downloader -f
```

If the logs show a 401 or "access denied" from HuggingFace, the model is gated and requires a token. Confirm the `spec.model.authSecretName` field on the LLMModel CR points to a Secret that contains a valid `HF_TOKEN`:

```bash
kubectl get llmmodel -n nebari-llm-serving-system <model-name> \
  -o jsonpath='{.spec.model.authSecretName}'

# Verify the Secret exists and has HF_TOKEN
kubectl get secret -n nebari-llm-serving-system <secret-name> \
  -o jsonpath='{.data.HF_TOKEN}' | base64 -d | head -c 20 && echo
```

If the download completed but the pod keeps crash-looping, check PVC binding - the init container writes to a volume that must be bound before it starts:

```bash
kubectl get pvc -n nebari-llm-serving-system
```

All PVCs should be `Bound`. If any is `Pending`, check your `storageClass` setting in the LLMModel CR against available storage classes (`kubectl get storageclass`). See [shared storage](/shared-storage/) for storage configuration details.

---

## Key-manager returns 403 - user not in the allowed Keycloak group

**Symptom:** A user opens `https://llm-keys.<baseDomain>/` and the SPA's keycloak-js PKCE login succeeds (Keycloak authentication works), but they see no models listed, or receive an HTTP 403 when trying to create an API key.

**Likely cause:** The user's Keycloak account is not a member of the group named in `LLMModel.spec.access.groups` (default: `llm`). The key manager validates the bearer token on every `/api` request and checks group membership from the `groups` claim.

**Fix:**

Confirm the user is in the group using the Keycloak admin API:

```bash
KC_HOST="https://keycloak.<baseDomain>"
KC_REALM=nebari
KC_ADMIN_USER=$(kubectl get secret -n keycloak keycloak-admin-credentials \
  -o jsonpath='{.data.admin-username}' | base64 -d)
KC_ADMIN_PASS=$(kubectl get secret -n keycloak keycloak-admin-credentials \
  -o jsonpath='{.data.admin-password}' | base64 -d)

TOKEN=$(curl -sS -X POST \
  "$KC_HOST/realms/master/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=admin-cli&username=$KC_ADMIN_USER&password=$KC_ADMIN_PASS" \
  | python3 -c 'import sys, json; print(json.load(sys.stdin)["access_token"])')

curl -sS -H "Authorization: Bearer $TOKEN" \
  "$KC_HOST/admin/realms/$KC_REALM/groups?search=llm" | python3 -m json.tool
```

If the `llm` group does not exist, create it (see the installation runbook section 7.2). If the group exists but the user is not a member, add them through the Keycloak admin console or API.

Also verify the `groups` claim is included in tokens - the Keycloak client must have a group-membership protocol mapper configured. Check the key-manager pod logs for the claim it is actually receiving:

```bash
kubectl logs -n nebari-llm-serving-system deploy/nebari-llm-serving-key-manager --tail=30
```

---

## Gateway HTTPS listener not `Programmed: True` - missing TLS cert

**Symptom:** External requests to `https://llm.<baseDomain>/` fail with a TLS handshake error or connection refused. Running the listener check shows `Programmed: False`:

```bash
kubectl get gateway -n envoy-gateway-system nebari-gateway \
  -o jsonpath='{range .status.listeners[*]}{.name}: {range .conditions[?(@.type=="Programmed")]}{.status} {.message}{end}{"\n"}{end}'
```

**Likely cause:** The `nebari-llm-shared-tls` Certificate in the pack namespace has not yet reached `Ready=True`, so the gateway listener has no TLS secret to reference. On fresh installs this is often a cert-manager ACME HTTP-01 challenge race when multiple certs are issued simultaneously.

**Fix:**

Check certificate status across all namespaces:

```bash
kubectl get certificate -A
```

All certs listed in the [installation validation checklist](/installation/) must show `Ready=True`. If `nebari-llm-shared-tls` is stuck:

```bash
kubectl describe certificate -n nebari-llm-serving-system nebari-llm-shared-tls
kubectl get certificaterequest,order,challenge -n nebari-llm-serving-system
```

For the `nebari-gateway-cert` in `envoy-gateway-system` stuck due to the HTTP-01 race (tracked as nebari-dev/nebari-infrastructure-core#267), delete it and let ArgoCD recreate it:

```bash
kubectl delete certificate -n envoy-gateway-system nebari-gateway-cert
kubectl get certificate -n envoy-gateway-system nebari-gateway-cert -w
```

NIC must ship with `cert-manager.maxConcurrentChallenges: 1` to prevent ACME solver races on overlapping SANs.

---

## AI Gateway webhook `caBundle` stale - envoy proxy pod fails to create

**Symptom:** The envoy proxy deployment is stuck at `0/1`. ReplicaSet events show:

```
failed calling webhook "ai-gateway-controller.envoy-ai-gateway-system.svc.cluster.local":
x509: certificate signed by unknown authority
```

External connectivity is down entirely.

**Likely cause:** The AI Gateway controller generates a self-signed CA at startup and patches the `MutatingWebhookConfiguration` with the CA bundle. After the controller pod is rescheduled (node replacement, rolling update), the new pod generates a new CA while the webhook configuration still holds the old bundle. The mismatch causes every envoy proxy pod create to be rejected.

**Fix:**

Restart the AI Gateway controller to force a fresh CA and webhook patch:

```bash
kubectl rollout restart deploy -n envoy-ai-gateway-system ai-gateway-controller
kubectl rollout status deploy -n envoy-ai-gateway-system ai-gateway-controller
```

The envoy proxy deployment should recover within about 30 seconds once the controller is back:

```bash
kubectl get deploy -n envoy-gateway-system -w
```

Verify the proxy pod now has the `ai-gateway-extproc` sidecar:

```bash
kubectl get pods -n envoy-gateway-system \
  -l gateway.envoyproxy.io/owning-gateway-name=nebari-gateway \
  -o jsonpath='{range .items[*]}{.metadata.name}: {range .spec.containers[*]}{.name},{end}{"\n"}{end}'
```

Expected output includes both `envoy` and `ai-gateway-extproc` for each proxy pod. If `ai-gateway-extproc` is still missing after the controller restart, delete the proxy pods to re-trigger webhook injection:

```bash
kubectl delete pod -n envoy-gateway-system \
  -l gateway.envoyproxy.io/owning-gateway-name=nebari-gateway
```

---

## LLMModel not reconciling - operator logs show errors

**Symptom:** `kubectl get llmmodel -n nebari-llm-serving-system` shows the model stuck in `Pending` or `Degraded` phase after more than a few minutes, and no child resources (HTTPRoute, InferencePool, SecurityPolicy) appear.

**Likely cause:** The operator cannot reconcile the LLMModel CR due to a missing dependency (Keycloak unreachable, missing Secret, invalid field value) or because the LLMModel was applied to the wrong namespace (the validating webhook rejects CRs created outside the operator namespace).

**Fix:**

Check operator logs for the reconciliation error:

```bash
kubectl logs -n nebari-llm-serving-system \
  deploy/nebari-llm-serving-operator --tail=50
```

(The pack's own operator - `nebari-llm-serving-operator` in `nebari-llm-serving-system` - reconciles LLMModels. This is distinct from the upstream Nebari Operator / NIC in `nebari-operator-system`, which only provisions NebariApps.)

Common operator log errors and their fixes:

- `keycloak: connection refused` or `keycloak: 401` - the operator cannot reach Keycloak. Verify the `KEYCLOAK_URL` environment variable and that the `keycloak` namespace pods are healthy:

  ```bash
  kubectl get deploy -n nebari-llm-serving-system nebari-llm-serving-operator \
    -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
    | grep KEYCLOAK
  ```

  Expected: `KEYCLOAK_ENABLED=true`, `KEYCLOAK_URL=http://keycloak-keycloakx-http.keycloak.svc.cluster.local:8080`, `KEYCLOAK_REALM=nebari`.

- `LLMModel namespace rejected` - the CR was applied outside the operator namespace. The validating webhook only accepts LLMModels in the pack namespace. Reapply to `nebari-llm-serving-system` (or whichever namespace your pack is installed in).

- `secret not found` - `spec.model.authSecretName` references a Secret that does not exist in the operator namespace. Create it before reapplying the LLMModel.

Check the LLMModel status conditions for a machine-readable message:

```bash
kubectl get llmmodel -n nebari-llm-serving-system <model-name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
```

See the [configuration reference](/configuration/) for valid LLMModel field values.

---

## `direct_response: 500` on `/v1/chat/completions`

**Symptom:** POST requests to the external or internal endpoint return a 500 response body of `{"error": "direct_response: 500"}` immediately - the request never reaches vLLM.

**Likely cause:** The Envoy proxy is returning a synthetic 500 before the AI Gateway extension processor handles the request. This usually means `extensionApis.enableEnvoyPatchPolicy` or `extensionApis.enableBackend` is not set in the Envoy Gateway configuration, so the AI Gateway route cannot attach its filter chain.

**Fix:**

Verify the Envoy Gateway ConfigMap has both extension API flags:

```bash
kubectl get configmap -n envoy-gateway-system envoy-gateway-config \
  -o jsonpath='{.data.envoy-gateway\.yaml}' | grep -A5 extensionApis
```

Expected:

```yaml
extensionApis:
  enableEnvoyPatchPolicy: true
  enableBackend: true
```

If either flag is missing, update the ConfigMap (or the ArgoCD Application values) and restart the Envoy Gateway controller:

```bash
kubectl rollout restart deploy -n envoy-gateway-system envoy-gateway
```

---

## External provider (PassthroughModel) not working

**Upstream 401 / auth errors from the provider.** The gateway injects the key from `spec.provider.credentialSecretName`. Inspect the credential condition first:

```console
$ kubectl -n nebari-llm-serving-system get passthroughmodel <name> \
    -o jsonpath='{range .status.conditions[?(@.type=="CredentialResolved")]}{.status} {.reason}: {.message}{"\n"}{end}'
```

`SecretNotFound` means the referenced Secret does not exist in the PassthroughModel's namespace. `APIKeyMissing` means the Secret exists but its `apiKey` entry is absent or empty. Create or update the Secret with that exact key name; the operator watches referenced credential Secrets and refreshes the condition automatically. `Resolved` confirms only that the entry is non-empty. If the provider still returns 401, replace an invalid, expired, or revoked key.

**PassthroughModel stuck with `ApplyFailed`.** This condition usually means the Envoy AI Gateway CRDs are not installed; the operator requeues every minute rather than failing outright. Check the CR and the CRDs:

```bash
kubectl -n nebari-llm-serving-system get passthroughmodel <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl get crd | grep aigateway
```

Install the AI Gateway (see [Installation](/installation/)) and the next reconcile clears the condition.

**A model id you expected to hit the provider is served locally instead.** Per-LLMModel routes match Host and `x-ai-eg-model`, so they always win over a catch-all PassthroughModel. If a declared provider model id collides with a served model name, the served model wins. Rename or remove the conflicting LLMModel, or declare the provider model under a distinct id.

```bash
# If the model id you expected to reach the provider appears here, that
# LLMModel's route wins over the catch-all. Rename or remove the LLMModel,
# or declare the provider model under a different id.
kubectl get llmmodels -n nebari-llm-serving-system
```
