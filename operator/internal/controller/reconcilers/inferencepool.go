// Resource specs based on GIE inferencepool chart v1.5.0 and the EPP deployment
// template from llm-d-router-endpoint-picker v0.9.0.
// The InferencePool uses inference.networking.k8s.io/v1; the EPP config uses
// llm-d.ai/v1alpha1, the non-deprecated API accepted by llm-d-router v0.9.0.
package reconcilers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

const (
	eppConfigMountDir = "/etc/epp"
	eppConfigFileName = "epp-config.yaml"
	eppConfigVolume   = "epp-config"
)

// eppDefaultConfig is the minimal EndpointPickerConfig the EPP loads via --config-file.
// Without an explicit config, the GIE falls back to a default that includes
// prefix-cache-scorer, which requires a tokenizer (sidecar or HF_TOKEN) we don't
// ship. This config uses only built-in plugins that don't require tokenization.
const eppDefaultConfig = `apiVersion: llm-d.ai/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: max-score-picker
`

// InferencePoolResources holds the Kubernetes resources for the Gateway API Inference Extension.
type InferencePoolResources struct {
	InferencePool     *unstructured.Unstructured // inference.networking.k8s.io/v1 InferencePool
	EPPConfigMap      *corev1.ConfigMap
	EPPDeployment     *appsv1.Deployment
	EPPService        *corev1.Service
	EPPServiceAccount *corev1.ServiceAccount
	EPPRole           *rbacv1.Role
	EPPRoleBinding    *rbacv1.RoleBinding
}

// BuildInferencePoolResources is a pure function that computes the Kubernetes resources
// needed for intelligent inference scheduling using the Gateway API Inference Extension.
func BuildInferencePoolResources(model *llmv1alpha1.LLMModel, cfg *config.OperatorConfig) (*InferencePoolResources, error) {
	labels := StandardLabels(model)
	eppName := model.Name + "-epp"

	inferencePool := buildInferencePool(model, labels)
	eppConfigMap, err := buildEPPConfigMap(model, eppName, labels)
	if err != nil {
		return nil, fmt.Errorf("building EPP ConfigMap: %w", err)
	}
	eppDeployment := buildEPPDeployment(model, eppName, labels, cfg)
	eppService := buildEPPService(model, eppName, labels)
	eppSA := buildEPPServiceAccount(eppName, labels)
	eppRole := buildEPPRole(eppName, labels)
	eppRoleBinding := buildEPPRoleBinding(eppName, labels)

	return &InferencePoolResources{
		InferencePool:     inferencePool,
		EPPConfigMap:      eppConfigMap,
		EPPDeployment:     eppDeployment,
		EPPService:        eppService,
		EPPServiceAccount: eppSA,
		EPPRole:           eppRole,
		EPPRoleBinding:    eppRoleBinding,
	}, nil
}

// eppConfigMapName returns the ConfigMap name that holds the EPP configuration
// for the given EPP deployment.
func eppConfigMapName(eppName string) string {
	return eppName + "-config"
}

// buildEPPConfigMap renders the EndpointPickerConfig YAML for the EPP deployment.
// If the user provides spec.advanced.inferencePool.schedulerConfig, that value
// is used verbatim; otherwise the minimal built-in default is used.
func buildEPPConfigMap(model *llmv1alpha1.LLMModel, eppName string, labels map[string]string) (*corev1.ConfigMap, error) {
	configYAML, err := renderEPPConfig(model.Spec.Advanced.InferencePool.SchedulerConfig)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppConfigMapName(eppName),
			Labels: labels,
		},
		Data: map[string]string{
			eppConfigFileName: configYAML,
		},
	}, nil
}

// renderEPPConfig returns the EPP config YAML to put in the ConfigMap. When the
// user supplies schedulerConfig, its bytes (JSON from the API server) are
// converted to YAML and used directly; otherwise the built-in default is used.
func renderEPPConfig(override *runtime.RawExtension) (string, error) {
	if override == nil || len(override.Raw) == 0 {
		return eppDefaultConfig, nil
	}
	yamlBytes, err := yaml.JSONToYAML(override.Raw)
	if err != nil {
		return "", fmt.Errorf("converting schedulerConfig JSON to YAML: %w", err)
	}
	return string(yamlBytes), nil
}

func buildInferencePool(model *llmv1alpha1.LLMModel, labels map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "inference.networking.k8s.io/v1",
			"kind":       "InferencePool",
			"metadata": map[string]interface{}{
				"name":   model.Name,
				"labels": labelsToInterface(labels),
			},
			"spec": map[string]interface{}{
				"targetPorts": []interface{}{
					map[string]interface{}{
						"number": int64(8000),
					},
				},
				// Selector must exclude the EPP pod, which also carries
				// `app.kubernetes.io/instance=<model.Name>` (it's part of
				// the same logical deployment). Adding
				// `app.kubernetes.io/name=llmmodel` narrows the match to
				// just the model (vLLM) pods. Without this, the
				// InferencePool advertises the EPP pod's IP as a backend
				// on port 8000, where nothing is listening, and every
				// inference request 503s with "Connection refused".
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app.kubernetes.io/instance": model.Name,
						"app.kubernetes.io/name":     "llmmodel",
					},
				},
				"endpointPickerRef": map[string]interface{}{
					"name": model.Name + "-epp",
					"kind": "Service",
					"port": map[string]interface{}{
						"number": int64(9002),
					},
				},
			},
		},
	}
}

func buildEPPDeployment(model *llmv1alpha1.LLMModel, eppName string, labels map[string]string, cfg *config.OperatorConfig) *appsv1.Deployment {
	replicas := int32(1)

	// EPP-specific labels extend the standard labels with app.kubernetes.io/name: epp
	eppLabels := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		eppLabels[k] = v
	}
	eppLabels["app.kubernetes.io/name"] = "epp"

	grpcHealthProbe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			GRPC: &corev1.GRPCAction{
				Port:    9003,
				Service: ptr.To("envoy.service.ext_proc.v3.ExternalProcessor"),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}

	container := corev1.Container{
		Name:  "epp",
		Image: cfg.DefaultEPPImage,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: 9002, Protocol: corev1.ProtocolTCP},
			{Name: "grpc-health", ContainerPort: 9003, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
		},
		Args: []string{
			"--pool-name", model.Name,
			"--pool-namespace", model.Namespace,
			"--grpc-port", "9002",
			"--grpc-health-port", "9003",
			"--metrics-port", "9090",
			"--config-file", eppConfigMountDir + "/" + eppConfigFileName,
			// TODO: expose --zap-encoder and --v via operator config; `--v 4` is
			// debug-verbose for steady-state production but matches upstream
			// reference deployments and aids post-crash-fix triage.
			"--zap-encoder", "json",
			"--v", "4",
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      eppConfigVolume,
				MountPath: eppConfigMountDir,
				ReadOnly:  true,
			},
		},
		LivenessProbe:  grpcHealthProbe.DeepCopy(),
		ReadinessProbe: grpcHealthProbe.DeepCopy(),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: eppLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": model.Name,
					"app.kubernetes.io/name":     "epp",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: eppLabels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: eppName,
					Containers:         []corev1.Container{container},
					Volumes: []corev1.Volume{
						{
							Name: eppConfigVolume,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: eppConfigMapName(eppName),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildEPPService(model *llmv1alpha1.LLMModel, eppName string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app.kubernetes.io/instance": model.Name,
				"app.kubernetes.io/name":     "epp",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "grpc",
					Port:       9002,
					TargetPort: intstr.FromString("grpc"),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "metrics",
					Port:       9090,
					TargetPort: intstr.FromString("metrics"),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func buildEPPServiceAccount(eppName string, labels map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
	}
}

func buildEPPRole(eppName string, labels map[string]string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"inference.networking.k8s.io"},
				Resources: []string{"inferencepools"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				// The llm-d inference scheduler (EPP) v0.8.x watches these GIE
				// resources; without RBAC the informers never sync and the EPP
				// crash-loops ("failed waiting for *v1alpha2.InferenceObjective
				// Informer to sync"), so the served model never gets a healthy
				// upstream.
				APIGroups: []string{"inference.networking.x-k8s.io"},
				Resources: []string{"inferenceobjectives", "inferencemodelrewrites", "inferencepools", "inferencepoolimports"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"endpoints", "services"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func buildEPPRoleBinding(eppName string, labels map[string]string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   eppName,
			Labels: labels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     eppName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: eppName,
			},
		},
	}
}
