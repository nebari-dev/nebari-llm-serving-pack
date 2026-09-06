package reconcilers

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const (
	testPoolModelName     = "my-model"
	testPoolEPPName       = "my-model-epp"
	testPoolEPPLabel      = "epp"
	testInferencePoolKind = "InferencePool"
	testInferenceAPIGroup = "inference.networking.k8s.io"
)

func defaultInferencePoolModel() *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPoolModelName,
			Namespace: "test-ns",
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "mistralai/Mistral-7B-v0.1",
				Source: llmv1alpha1.ModelSourceHuggingFace,
			},
		},
	}
}

func defaultInferencePoolConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gw",
		InternalGatewayName: "internal-gw",
		OIDCIssuerURL:       "https://oidc.example.com",
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.7.0",
		DefaultEPPImage:     "ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.9.0",
	}
}

func TestBuildInferencePoolResources(t *testing.T) { //nolint:gocyclo // table-driven test
	t.Parallel()

	tests := []struct {
		name  string
		model *llmv1alpha1.LLMModel
		cfg   *config.OperatorConfig
		check func(t *testing.T, result *InferencePoolResources, err error)
	}{
		{
			name:  "InferencePool: correct apiVersion, kind, name",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				ip := result.InferencePool
				if ip == nil {
					t.Fatal("expected InferencePool to be non-nil")
				}
				if ip.GetAPIVersion() != "inference.networking.k8s.io/v1" {
					t.Errorf("expected apiVersion inference.networking.k8s.io/v1, got %q", ip.GetAPIVersion())
				}
				if ip.GetKind() != testInferencePoolKind {
					t.Errorf("expected kind InferencePool, got %q", ip.GetKind())
				}
				if ip.GetName() != testPoolModelName {
					t.Errorf("expected name my-model, got %q", ip.GetName())
				}
			},
		},
		{
			name:  "InferencePool: targetPortNumber is 8000",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec, ok := result.InferencePool.Object["spec"].(map[string]interface{})
				if !ok {
					t.Fatal("expected InferencePool spec to be a map")
				}
				targetPorts, ok := spec["targetPorts"].([]interface{})
				if !ok || len(targetPorts) == 0 {
					t.Fatal("expected targetPorts in InferencePool spec")
				}
				portEntry := targetPorts[0].(map[string]interface{})
				if portEntry["number"] != int64(8000) {
					t.Errorf("expected targetPorts[0].number=8000, got %v", portEntry["number"])
				}
			},
		},
		{
			name:  "InferencePool: endpointPickerRef points to EPP service",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec, ok := result.InferencePool.Object["spec"].(map[string]interface{})
				if !ok {
					t.Fatal("expected InferencePool spec to be a map")
				}
				epRef, ok := spec["endpointPickerRef"].(map[string]interface{})
				if !ok {
					t.Fatal("expected endpointPickerRef in InferencePool spec")
				}
				if epRef["name"] != testPoolEPPName {
					t.Errorf("expected endpointPickerRef.name=my-model-epp, got %v", epRef["name"])
				}
				if epRef["kind"] != "Service" {
					t.Errorf("expected endpointPickerRef.kind=Service, got %v", epRef["kind"])
				}
				port := epRef["port"].(map[string]interface{})
				if port["number"] != int64(9002) {
					t.Errorf("expected port.number=9002, got %v", port["number"])
				}
			},
		},
		{
			name:  "InferencePool: selector matches model pods",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec, ok := result.InferencePool.Object["spec"].(map[string]interface{})
				if !ok {
					t.Fatal("expected InferencePool spec to be a map")
				}
				selector, ok := spec["selector"].(map[string]interface{})
				if !ok {
					t.Fatal("expected selector in InferencePool spec")
				}
				matchLabels, ok := selector["matchLabels"].(map[string]interface{})
				if !ok {
					t.Fatal("expected matchLabels in selector")
				}
				instanceLabel, ok := matchLabels["app.kubernetes.io/instance"]
				if !ok {
					t.Fatal("expected app.kubernetes.io/instance in matchLabels")
				}
				if instanceLabel != testPoolModelName {
					t.Errorf("expected matchLabels app.kubernetes.io/instance=my-model, got %v", instanceLabel)
				}
				// Must also filter by app.kubernetes.io/name=llmmodel to
				// exclude the EPP pod, which shares the instance label.
				nameLabel, ok := matchLabels["app.kubernetes.io/name"]
				if !ok {
					t.Fatal("expected app.kubernetes.io/name in matchLabels (to exclude EPP pod)")
				}
				if nameLabel != "llmmodel" {
					t.Errorf("expected matchLabels app.kubernetes.io/name=llmmodel, got %v", nameLabel)
				}
			},
		},
		{
			name:  "InferencePool: labels set to StandardLabels",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				labels := result.InferencePool.GetLabels()
				if labels["app.kubernetes.io/managed-by"] != testAuthSAName {
					t.Errorf("expected managed-by label, got %q", labels["app.kubernetes.io/managed-by"])
				}
				if labels["app.kubernetes.io/instance"] != testPoolModelName {
					t.Errorf("expected instance label my-model, got %q", labels["app.kubernetes.io/instance"])
				}
			},
		},
		{
			name:  "EPP Deployment: correct name, image, replicas",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				dep := result.EPPDeployment
				if dep == nil {
					t.Fatal("expected EPPDeployment to be non-nil")
				}
				if dep.Name != testPoolEPPName {
					t.Errorf("expected EPP deployment name my-model-epp, got %q", dep.Name)
				}
				if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
					t.Errorf("expected EPP deployment replicas=1, got %v", dep.Spec.Replicas)
				}
				containers := dep.Spec.Template.Spec.Containers
				if len(containers) != 1 {
					t.Fatalf("expected 1 container, got %d", len(containers))
				}
				if containers[0].Image != "ghcr.io/llm-d/llm-d-router-endpoint-picker:v0.9.0" {
					t.Errorf("expected EPP image, got %q", containers[0].Image)
				}
				if containers[0].Name != testPoolEPPLabel {
					t.Errorf("expected container name epp, got %q", containers[0].Name)
				}
			},
		},
		{
			name:  "EPP Deployment: custom EPP image from config overrides the default",
			model: defaultInferencePoolModel(),
			cfg: func() *config.OperatorConfig {
				cfg := defaultInferencePoolConfig()
				cfg.DefaultEPPImage = "mirror.internal/llm-d/custom-epp:v1"
				return cfg
			}(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				containers := result.EPPDeployment.Spec.Template.Spec.Containers
				if len(containers) != 1 {
					t.Fatalf("expected 1 container, got %d", len(containers))
				}
				if containers[0].Image != "mirror.internal/llm-d/custom-epp:v1" {
					t.Errorf("expected EPP image from config, got %q", containers[0].Image)
				}
			},
		},
		{
			name:  "EPP Deployment: container ports 9002, 9003 and 9090",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				ports := result.EPPDeployment.Spec.Template.Spec.Containers[0].Ports
				foundGRPC := false
				foundGRPCHealth := false
				foundMetrics := false
				for _, p := range ports {
					if p.ContainerPort == 9002 && p.Name == "grpc" {
						foundGRPC = true
					}
					if p.ContainerPort == 9003 && p.Name == "grpc-health" {
						foundGRPCHealth = true
					}
					if p.ContainerPort == 9090 && p.Name == "metrics" {
						foundMetrics = true
					}
				}
				if !foundGRPC {
					t.Error("expected container port 9002 named grpc")
				}
				if !foundGRPCHealth {
					t.Error("expected container port 9003 named grpc-health")
				}
				if !foundMetrics {
					t.Error("expected container port 9090 named metrics")
				}
			},
		},
		{
			name:  "EPP Deployment: args include pool-name, pool-namespace, ports, and config-file",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				args := result.EPPDeployment.Spec.Template.Spec.Containers[0].Args
				assertArgValue(t, args, "--pool-name", testPoolModelName)
				assertArgValue(t, args, "--pool-namespace", "test-ns")
				assertArgValue(t, args, "--grpc-port", "9002")
				assertArgValue(t, args, "--grpc-health-port", "9003")
				assertArgValue(t, args, "--metrics-port", "9090")
				assertArgValue(t, args, "--config-file", "/etc/epp/epp-config.yaml")
			},
		},
		{
			name:  "EPP Deployment: gRPC health probes on port 9003",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				c := result.EPPDeployment.Spec.Template.Spec.Containers[0]
				if c.LivenessProbe == nil || c.LivenessProbe.GRPC == nil {
					t.Fatal("expected gRPC liveness probe")
				}
				if c.LivenessProbe.GRPC.Port != 9003 {
					t.Errorf("expected liveness probe port 9003, got %d", c.LivenessProbe.GRPC.Port)
				}
				if c.ReadinessProbe == nil || c.ReadinessProbe.GRPC == nil {
					t.Fatal("expected gRPC readiness probe")
				}
				if c.ReadinessProbe.GRPC.Port != 9003 {
					t.Errorf("expected readiness probe port 9003, got %d", c.ReadinessProbe.GRPC.Port)
				}
			},
		},
		{
			name:  "EPP Deployment: config volume mounted from ConfigMap at /etc/epp",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				spec := result.EPPDeployment.Spec.Template.Spec
				// VolumeMount
				var mount *corev1.VolumeMount
				for i, m := range spec.Containers[0].VolumeMounts {
					if m.Name == "epp-config" {
						mount = &spec.Containers[0].VolumeMounts[i]
						break
					}
				}
				if mount == nil {
					t.Fatal("expected volumeMount named epp-config")
				}
				if mount.MountPath != "/etc/epp" {
					t.Errorf("expected mountPath /etc/epp, got %q", mount.MountPath)
				}
				// Volume
				var vol *corev1.Volume
				for i, v := range spec.Volumes {
					if v.Name == "epp-config" {
						vol = &spec.Volumes[i]
						break
					}
				}
				if vol == nil {
					t.Fatal("expected volume named epp-config")
				}
				if vol.ConfigMap == nil {
					t.Fatal("expected epp-config volume to reference a ConfigMap")
				}
				if vol.ConfigMap.Name != testPoolEPPName+"-config" {
					t.Errorf("expected ConfigMap name %s-config, got %q", testPoolEPPName, vol.ConfigMap.Name)
				}
			},
		},
		{
			name:  "EPP ConfigMap: contains epp-config.yaml with EndpointPickerConfig kind",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				cm := result.EPPConfigMap
				if cm == nil {
					t.Fatal("expected EPPConfigMap to be non-nil")
				}
				if cm.Name != testPoolEPPName+"-config" {
					t.Errorf("expected ConfigMap name %s-config, got %q", testPoolEPPName, cm.Name)
				}
				data, ok := cm.Data["epp-config.yaml"]
				if !ok {
					t.Fatal("expected epp-config.yaml key in ConfigMap Data")
				}
				if !strings.Contains(data, "kind: EndpointPickerConfig") {
					t.Errorf("expected EndpointPickerConfig kind in config, got: %s", data)
				}
				if !strings.Contains(data, "max-score-picker") {
					t.Errorf("expected max-score-picker plugin in config, got: %s", data)
				}
			},
		},
		{
			name:  "EPP ConfigMap: default config is valid YAML",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				data := result.EPPConfigMap.Data["epp-config.yaml"]
				parsed := map[string]any{}
				if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
					t.Fatalf("default epp-config.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
				}
				if parsed["kind"] != "EndpointPickerConfig" {
					t.Errorf("expected parsed kind=EndpointPickerConfig, got %v", parsed["kind"])
				}
				if parsed["apiVersion"] != "llm-d.ai/v1alpha1" {
					t.Errorf("expected parsed apiVersion=llm-d.ai/v1alpha1, got %v", parsed["apiVersion"])
				}
			},
		},
		{
			name: "EPP ConfigMap: user schedulerConfig override replaces default",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultInferencePoolModel()
				m.Spec.Advanced.InferencePool.SchedulerConfig = &runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"llm-d.ai/v1alpha1","kind":"EndpointPickerConfig","plugins":[{"type":"prefix-cache-scorer"},{"type":"max-score-picker"}],"schedulingProfiles":[{"name":"default","plugins":[{"pluginRef":"max-score-picker"},{"pluginRef":"prefix-cache-scorer","weight":2}]}]}`),
				}
				return m
			}(),
			cfg: defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				data := result.EPPConfigMap.Data["epp-config.yaml"]
				if !strings.Contains(data, "prefix-cache-scorer") {
					t.Errorf("expected override to include prefix-cache-scorer, got: %s", data)
				}
				parsed := map[string]any{}
				if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
					t.Fatalf("rendered override is not valid YAML: %v", err)
				}
			},
		},
		{
			name: "EPP ConfigMap: invalid schedulerConfig JSON returns error",
			model: func() *llmv1alpha1.LLMModel {
				m := defaultInferencePoolModel()
				m.Spec.Advanced.InferencePool.SchedulerConfig = &runtime.RawExtension{
					Raw: []byte(`{not valid json`),
				}
				return m
			}(),
			cfg: defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err == nil {
					t.Fatal("expected error for invalid schedulerConfig JSON, got nil")
				}
				if result != nil {
					t.Error("expected nil result on error")
				}
			},
		},
		{
			name:  "EPP Deployment: has resource requests and limits",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				resources := result.EPPDeployment.Spec.Template.Spec.Containers[0].Resources
				if resources.Requests == nil {
					t.Fatal("expected resource requests to be set")
				}
				if resources.Limits == nil {
					t.Fatal("expected resource limits to be set")
				}
				cpuReq, ok := resources.Requests["cpu"]
				if !ok {
					t.Error("expected cpu in resource requests")
				} else if cpuReq.String() != "100m" {
					t.Errorf("expected cpu request 100m, got %s", cpuReq.String())
				}
				memReq, ok := resources.Requests["memory"]
				if !ok {
					t.Error("expected memory in resource requests")
				} else if memReq.String() != "256Mi" {
					t.Errorf("expected memory request 256Mi, got %s", memReq.String())
				}
				cpuLim, ok := resources.Limits["cpu"]
				if !ok {
					t.Error("expected cpu in resource limits")
				} else if cpuLim.String() != "500m" {
					t.Errorf("expected cpu limit 500m, got %s", cpuLim.String())
				}
				memLim, ok := resources.Limits["memory"]
				if !ok {
					t.Error("expected memory in resource limits")
				} else if memLim.String() != "512Mi" {
					t.Errorf("expected memory limit 512Mi, got %s", memLim.String())
				}
			},
		},
		{
			name:  "EPP Deployment: labels include app.kubernetes.io/name: epp",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				labels := result.EPPDeployment.Labels
				if labels["app.kubernetes.io/name"] != testPoolEPPLabel {
					t.Errorf("expected app.kubernetes.io/name=epp, got %q", labels["app.kubernetes.io/name"])
				}
				// Also check pod template labels
				podLabels := result.EPPDeployment.Spec.Template.Labels
				if podLabels["app.kubernetes.io/name"] != testPoolEPPLabel {
					t.Errorf("expected pod template app.kubernetes.io/name=epp, got %q", podLabels["app.kubernetes.io/name"])
				}
			},
		},
		{
			name:  "EPP Service: ports 9002 and 9090",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				svc := result.EPPService
				if svc == nil {
					t.Fatal("expected EPPService to be non-nil")
				}
				if svc.Name != testPoolEPPName {
					t.Errorf("expected service name my-model-epp, got %q", svc.Name)
				}
				foundGRPC := false
				foundMetrics := false
				for _, p := range svc.Spec.Ports {
					if p.Port == 9002 && p.Name == "grpc" {
						foundGRPC = true
					}
					if p.Port == 9090 && p.Name == "metrics" {
						foundMetrics = true
					}
				}
				if !foundGRPC {
					t.Error("expected service port 9002 named grpc")
				}
				if !foundMetrics {
					t.Error("expected service port 9090 named metrics")
				}
			},
		},
		{
			name:  "EPP ServiceAccount: correct name",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				sa := result.EPPServiceAccount
				if sa == nil {
					t.Fatal("expected EPPServiceAccount to be non-nil")
				}
				if sa.Name != testPoolEPPName {
					t.Errorf("expected service account name my-model-epp, got %q", sa.Name)
				}
			},
		},
		{
			name:  "EPP Role: correct permissions for InferencePool, pods, endpoints, services",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				role := result.EPPRole
				if role == nil {
					t.Fatal("expected EPPRole to be non-nil")
				}
				if role.Name != testPoolEPPName {
					t.Errorf("expected role name my-model-epp, got %q", role.Name)
				}

				// Check InferencePool rule
				foundIPRule := false
				foundPodsRule := false
				foundEndpointsRule := false
				for _, rule := range role.Rules {
					for _, apiGroup := range rule.APIGroups {
						if apiGroup == testInferenceAPIGroup {
							for _, resource := range rule.Resources {
								if resource == "inferencepools" {
									foundIPRule = true
									// Check verbs
									assertContainsVerbs(t, rule.Verbs, []string{"get", "list", "watch"})
								}
							}
						}
						if apiGroup == "" {
							for _, resource := range rule.Resources {
								if resource == "pods" {
									foundPodsRule = true
									assertContainsVerbs(t, rule.Verbs, []string{"get", "list", "watch"})
								}
								if resource == "endpoints" || resource == "services" {
									foundEndpointsRule = true
								}
							}
						}
					}
				}
				if !foundIPRule {
					t.Error("expected rule for inference.networking.k8s.io/inferencepools")
				}
				if !foundPodsRule {
					t.Error("expected rule for pods")
				}
				if !foundEndpointsRule {
					t.Error("expected rule for endpoints/services")
				}
			},
		},
		{
			name:  "EPP RoleBinding: links Role to ServiceAccount",
			model: defaultInferencePoolModel(),
			cfg:   defaultInferencePoolConfig(),
			check: func(t *testing.T, result *InferencePoolResources, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				rb := result.EPPRoleBinding
				if rb == nil {
					t.Fatal("expected EPPRoleBinding to be non-nil")
				}
				if rb.Name != testPoolEPPName {
					t.Errorf("expected role binding name my-model-epp, got %q", rb.Name)
				}
				if rb.RoleRef.Name != testPoolEPPName {
					t.Errorf("expected roleRef name my-model-epp, got %q", rb.RoleRef.Name)
				}
				if rb.RoleRef.Kind != "Role" {
					t.Errorf("expected roleRef kind Role, got %q", rb.RoleRef.Kind)
				}
				if len(rb.Subjects) != 1 {
					t.Fatalf("expected 1 subject, got %d", len(rb.Subjects))
				}
				if rb.Subjects[0].Name != testPoolEPPName {
					t.Errorf("expected subject name my-model-epp, got %q", rb.Subjects[0].Name)
				}
				if rb.Subjects[0].Kind != "ServiceAccount" {
					t.Errorf("expected subject kind ServiceAccount, got %q", rb.Subjects[0].Kind)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := BuildInferencePoolResources(tt.model, tt.cfg)
			tt.check(t, result, err)
		})
	}
}

// assertContainsVerbs checks that all expectedVerbs are present in verbs.
func assertContainsVerbs(t *testing.T, verbs []string, expectedVerbs []string) {
	t.Helper()
	for _, expected := range expectedVerbs {
		found := false
		for _, v := range verbs {
			if v == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected verb %q in verbs %v", expected, verbs)
		}
	}
}
