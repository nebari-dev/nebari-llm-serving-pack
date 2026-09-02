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

package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

const (
	finalizerName       = "llm.nebari.dev/cleanup"
	modelConfigHashAnno = "llm.nebari.dev/model-config-hash"
)

// controllerLogger is the interface for structured logging used within this controller.
type controllerLogger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(err error, msg string, keysAndValues ...interface{})
}

// LLMModelReconciler reconciles a LLMModel object
type LLMModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *config.OperatorConfig
}

// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=llmmodels/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts;persistentvolumeclaims;secrets;configmaps;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the main reconciliation loop for LLMModel resources.
func (r *LLMModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo // sequential reconciliation steps
	log := logf.FromContext(ctx)

	// 1. Fetch LLMModel
	model := &llmv1alpha1.LLMModel{}
	if err := r.Get(ctx, req.NamespacedName, model); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Handle deletion
	if !model.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, model)
	}

	// 3. Add finalizer if missing
	if !controllerutil.ContainsFinalizer(model, finalizerName) {
		controllerutil.AddFinalizer(model, finalizerName)
		if err := r.Update(ctx, model); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Requeue immediately - the finalizer update changes the resourceVersion,
		// and continuing with the stale object causes status update conflicts.
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	// 4. Set phase to Pending if empty
	if model.Status.Phase == "" {
		model.Status.Phase = llmv1alpha1.PhasePending
		if err := r.Status().Update(ctx, model); err != nil {
			log.Error(err, "failed to update status phase to Pending")
			return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
		}
	}

	// 5. Reconcile storage (PVC)
	defaultStorageClass := ""
	if r.Config != nil {
		defaultStorageClass = r.Config.DefaultStorageClassName
	}
	storageResult, err := reconcilers.BuildStorageSpec(model, defaultStorageClass)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building storage spec: %w", err)
	}
	if storageResult.PVC != nil {
		storageResult.PVC.Namespace = model.Namespace
		if err := reconcilers.SetOwnerReference(model, storageResult.PVC, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner on PVC: %w", err)
		}
		if err := r.createOrUpdatePVC(ctx, storageResult.PVC); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling PVC: %w", err)
		}
	}

	// 6. Reconcile auth resources (Secret + ConfigMap, both in the model's namespace)
	var authResources *reconcilers.AuthResources
	if r.Config != nil {
		clientIDs, err := apiKeyClientIDs(ctx, r.Client, model.Name, model.Namespace)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reading api key client ids: %w", err)
		}
		credentialSecretNames, err := apiKeySecretNamesWith(ctx, r.Client, model.Namespace, reconcilers.APIKeySecretName(model.Name))
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("listing api key secrets: %w", err)
		}
		authResources, err = reconcilers.BuildAuthResources(model, r.Config, clientIDs, credentialSecretNames)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building auth resources: %w", err)
		}
		if err := r.reconcileAuthSecretAndConfigMap(ctx, authResources); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 8. Reconcile NetworkPolicy
	networkPolicy := reconcilers.BuildNetworkPolicy(model, r.Config)
	if err := reconcilers.SetOwnerReference(model, networkPolicy, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner on NetworkPolicy: %w", err)
	}
	if err := r.createOrUpdateNetworkPolicy(ctx, networkPolicy); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling NetworkPolicy: %w", err)
	}

	// 9. Reconcile model service resources (Deployment, Service, SA, PodMonitor)
	modelServiceResources, err := reconcilers.BuildModelServiceResources(model, storageResult, r.Config)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building model service resources: %w", err)
	}
	if err := r.reconcileModelServiceResources(ctx, log, model, modelServiceResources); err != nil {
		return ctrl.Result{}, err
	}

	// 10. Check deployment readiness to determine phase
	phase := r.determinePhase(ctx, model)
	if phase == llmv1alpha1.PhaseDownloading || phase == llmv1alpha1.PhaseStarting {
		// Update status with phase and replica counts even during intermediate
		// phases. No conditions yet: step 11 has not run, so reporting on the
		// gateway resources would be reporting on work not attempted.
		if err := r.updateStatus(ctx, log, model, phase, nil); err != nil {
			log.Error(err, "failed to update status during intermediate phase", "phase", phase)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// 11. Once model is (or will be) serving, create downstream resources.
	//
	// The gateway kinds here (InferencePool, AIGatewayRoute, SecurityPolicy)
	// come from CRDs this pack does not install - see section 4 of the install
	// runbook. A missing CRD does not fail the reconcile, because everything
	// this pack owns is still worth converging and the applies succeed on a
	// later pass. It does get reported: each outcome becomes a status
	// condition, and a failure downgrades the phase so the model never claims
	// Ready while nothing can route to it.
	var conditions []metav1.Condition
	if r.Config != nil {
		// InferencePool + EPP
		poolResources, err := reconcilers.BuildInferencePoolResources(model, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building inference pool resources: %w", err)
		}
		poolErr, err := r.reconcileInferencePoolResources(ctx, log, model, poolResources)
		if err != nil {
			return ctrl.Result{}, err
		}
		conditions = append(conditions,
			conditionFor(CondInferencePoolReady, poolErr, "InferencePool applied"))

		// Routing (AIGatewayRoutes)
		routingResources, err := reconcilers.BuildRoutingResources(model, r.Config)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building routing resources: %w", err)
		}
		routeExtErr, routeIntErr := r.reconcileRoutingResources(ctx, log, model, routingResources)

		// SecurityPolicies (from auth resources built in step 7)
		var policyExtErr, policyIntErr error
		if authResources != nil {
			policyExtErr, policyIntErr = r.reconcileSecurityPolicies(ctx, log, authResources)
		}

		// One condition per endpoint, covering its route and its policy
		// together: they fail for the same reason and are fixed by the same
		// action, so splitting them would only add noise.
		if routingResources.ExternalRoute != nil {
			conditions = append(conditions, conditionFor(CondExternalEndpointReady,
				errors.Join(routeExtErr, policyExtErr), "external route and apiKeyAuth policy applied"))
		} else {
			conditions = append(conditions, disabledCondition(CondExternalEndpointReady))
		}
		if routingResources.InternalRoute != nil {
			conditions = append(conditions, conditionFor(CondInternalEndpointReady,
				errors.Join(routeIntErr, policyIntErr), "internal route and JWT policy applied"))
		} else {
			conditions = append(conditions, disabledCondition(CondInternalEndpointReady))
		}
	}

	// 12. Update status
	phase = phaseWithRoutingFailure(phase, conditions)
	if err := r.updateStatus(ctx, log, model, phase, conditions); err != nil {
		return ctrl.Result{}, err
	}

	if hasApplyFailure(conditions) {
		// Retry: the usual cause is the AI Gateway or Inference Extension CRDs
		// not being installed yet, which a later pass fixes without any change
		// to the LLMModel itself. Without this the model would sit Degraded
		// until something else triggered a reconcile.
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileDelete handles finalization: cleans up cross-namespace resources and removes the finalizer.
func (r *LLMModelReconciler) reconcileDelete(ctx context.Context, model *llmv1alpha1.LLMModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(model, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("cleaning up auth resources", "model", model.Name)

	// Delete API-key Secret in the model's namespace.
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      reconcilers.APIKeySecretName(model.Name),
		Namespace: model.Namespace,
	}
	if err := r.Get(ctx, secretKey, secret); err == nil {
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting api key secret: %w", err)
		}
	}

	// Delete metadata ConfigMap in the model's namespace.
	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{
		Name:      reconcilers.APIKeyMetadataConfigMapName(model.Name),
		Namespace: model.Namespace,
	}
	if err := r.Get(ctx, cmKey, cm); err == nil {
		if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting api key metadata configmap: %w", err)
		}
	}

	// Pre-#59 deployments may have left a ReferenceGrant behind in the
	// dedicated api-keys namespace. Best-effort cleanup of that legacy
	// resource so the namespace can drain. Skip if the API-keys namespace
	// concept isn't even configured anymore.
	if r.Config != nil && r.Config.APIKeysNamespace != "" && r.Config.APIKeysNamespace != model.Namespace {
		refGrant := &unstructured.Unstructured{}
		refGrant.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "gateway.networking.k8s.io",
			Version: "v1beta1",
			Kind:    "ReferenceGrant",
		})
		refGrantKey := types.NamespacedName{
			Name:      model.Name + "-" + model.Namespace + "-ref-grant",
			Namespace: r.Config.APIKeysNamespace,
		}
		if err := r.Get(ctx, refGrantKey, refGrant); err == nil {
			if err := r.Delete(ctx, refGrant); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "failed to delete legacy ReferenceGrant during cleanup")
			}
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(model, finalizerName)
	if err := r.Update(ctx, model); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcileAuthSecretAndConfigMap creates or updates the API-key Secret and
// metadata ConfigMap. Both live in the LLMModel's own namespace (no longer in
// a separate api-keys namespace) so the SecurityPolicy can reference them
// without a cross-namespace credentialRef. These resources have no owner
// references because they hold user-issued API keys whose lifetime should
// outlive a reapply of the LLMModel; cleanup is handled by the finalizer.
func (r *LLMModelReconciler) reconcileAuthSecretAndConfigMap(
	ctx context.Context,
	auth *reconcilers.AuthResources,
) error {
	if err := r.createOrUpdateSecret(ctx, auth.APIKeySecret); err != nil {
		return fmt.Errorf("reconciling api key secret: %w", err)
	}
	if err := r.createOrUpdateConfigMapPreserveData(ctx, auth.APIKeyMetadataCM); err != nil {
		return fmt.Errorf("reconciling api key metadata configmap: %w", err)
	}
	return nil
}

// reconcileModelServiceResources creates or updates the Deployment, Service, SA, and PodMonitor.
// It includes breaking-change detection for the Deployment.
func (r *LLMModelReconciler) reconcileModelServiceResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	resources *reconcilers.ModelServiceResources,
) error {
	// ServiceAccount
	resources.ServiceAccount.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.ServiceAccount, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on ServiceAccount: %w", err)
	}
	if err := r.createOrUpdateServiceAccount(ctx, resources.ServiceAccount); err != nil {
		return fmt.Errorf("reconciling ServiceAccount: %w", err)
	}

	// Service
	resources.Service.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.Service, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on Service: %w", err)
	}
	if err := r.createOrUpdateService(ctx, resources.Service); err != nil {
		return fmt.Errorf("reconciling Service: %w", err)
	}

	// Deployment - with breaking-change detection
	resources.Deployment.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, resources.Deployment, r.Scheme); err != nil {
		return fmt.Errorf("setting owner on Deployment: %w", err)
	}
	hash := modelConfigHash(model)
	if resources.Deployment.Annotations == nil {
		resources.Deployment.Annotations = make(map[string]string)
	}
	resources.Deployment.Annotations[modelConfigHashAnno] = hash

	existing := &appsv1.Deployment{}
	getErr := r.Get(ctx, types.NamespacedName{Name: resources.Deployment.Name, Namespace: resources.Deployment.Namespace}, existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("getting existing Deployment: %w", getErr)
	}
	if getErr == nil {
		// Deployment exists - check for breaking changes
		existingHash := existing.Annotations[modelConfigHashAnno]
		if existingHash != "" && existingHash != hash {
			log.Info("model config changed - deleting Deployment to trigger re-download",
				"old-hash", existingHash, "new-hash", hash)
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting stale Deployment: %w", err)
			}
			// Will be re-created on next reconcile
			return nil
		}
		// Update spec while preserving resource version
		existing.Spec = resources.Deployment.Spec
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		existing.Annotations[modelConfigHashAnno] = hash
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating Deployment: %w", err)
		}
	} else {
		// Create new deployment
		if err := r.Create(ctx, resources.Deployment); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Deployment: %w", err)
		}
	}

	// PodMonitor (optional CRD)
	if resources.PodMonitor != nil {
		resources.PodMonitor.SetNamespace(model.Namespace)
		if err := r.createOrUpdateUnstructured(ctx, resources.PodMonitor); err != nil {
			log.Error(err, "failed to reconcile PodMonitor - CRD may not be installed, skipping")
		}
	}

	return nil
}

// reconcileInferencePoolResources applies the InferencePool and the EPP's own
// resources. The returned poolErr reports whether the InferencePool itself
// could be applied (it needs a CRD this pack does not install); a non-nil err
// means something this pack does own failed, which is fatal to the reconcile.
func (r *LLMModelReconciler) reconcileInferencePoolResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	pool *reconcilers.InferencePoolResources,
) (poolErr error, err error) {
	// InferencePool (unstructured CRD - non-fatal if missing, but reported).
	// The error is returned via poolErr rather than failing the reconcile: the
	// EPP Deployment and RBAC below are still worth converging, and the pool
	// applies on a later pass once the Inference Extension CRDs exist.
	pool.InferencePool.SetNamespace(model.Namespace)
	poolErr = r.createOrUpdateUnstructured(ctx, pool.InferencePool)
	if poolErr != nil {
		log.Error(poolErr, "failed to reconcile InferencePool - CRD may not be installed")
	}

	// EPP ServiceAccount
	pool.EPPServiceAccount.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPServiceAccount, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP ServiceAccount: %w", err)
	}
	if err := r.createOrUpdateServiceAccount(ctx, pool.EPPServiceAccount); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP ServiceAccount: %w", err)
	}

	// EPP Role
	pool.EPPRole.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPRole, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP Role: %w", err)
	}
	if err := r.createOrUpdateRole(ctx, pool.EPPRole); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP Role: %w", err)
	}

	// EPP RoleBinding
	pool.EPPRoleBinding.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPRoleBinding, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP RoleBinding: %w", err)
	}
	if err := r.createOrUpdateRoleBinding(ctx, pool.EPPRoleBinding); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP RoleBinding: %w", err)
	}

	// EPP Service
	pool.EPPService.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPService, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP Service: %w", err)
	}
	if err := r.createOrUpdateService(ctx, pool.EPPService); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP Service: %w", err)
	}

	// EPP ConfigMap (must exist before Deployment so the volume mount resolves)
	pool.EPPConfigMap.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPConfigMap, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP ConfigMap: %w", err)
	}
	if err := r.createOrUpdateConfigMap(ctx, pool.EPPConfigMap); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP ConfigMap: %w", err)
	}

	// EPP Deployment
	pool.EPPDeployment.Namespace = model.Namespace
	if err := reconcilers.SetOwnerReference(model, pool.EPPDeployment, r.Scheme); err != nil {
		return poolErr, fmt.Errorf("setting owner on EPP Deployment: %w", err)
	}
	if err := r.createOrUpdateDeployment(ctx, pool.EPPDeployment); err != nil {
		return poolErr, fmt.Errorf("reconciling EPP Deployment: %w", err)
	}

	return poolErr, nil
}

// reconcileRoutingResources creates or updates AIGatewayRoute resources and
// returns, per endpoint, whether the apply succeeded. A missing AI Gateway CRD
// is not fatal - the caller turns the error into a status condition and
// requeues - but it must not be swallowed, because the runtime symptom of an
// absent route is a bare HTTP 404 with nothing to point at the cause.
func (r *LLMModelReconciler) reconcileRoutingResources(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	routing *reconcilers.RoutingResources,
) (externalErr, internalErr error) {
	if routing.ExternalRoute != nil {
		routing.ExternalRoute.SetNamespace(model.Namespace)
		if externalErr = r.createOrUpdateUnstructured(ctx, routing.ExternalRoute); externalErr != nil {
			log.Error(externalErr, "failed to reconcile external AIGatewayRoute - CRD may not be installed")
		}
	}
	if routing.InternalRoute != nil {
		routing.InternalRoute.SetNamespace(model.Namespace)
		if internalErr = r.createOrUpdateUnstructured(ctx, routing.InternalRoute); internalErr != nil {
			log.Error(internalErr, "failed to reconcile internal AIGatewayRoute - CRD may not be installed")
		}
	}
	return externalErr, internalErr
}

// reconcileSecurityPolicies creates or updates SecurityPolicy resources,
// returning per-endpoint outcomes for the same reason as
// reconcileRoutingResources: a SecurityPolicy that was never applied leaves the
// endpoint unauthenticated-looking rather than obviously broken.
func (r *LLMModelReconciler) reconcileSecurityPolicies(
	ctx context.Context,
	log controllerLogger,
	auth *reconcilers.AuthResources,
) (externalErr, internalErr error) {
	if auth.ExternalSecurityPolicy != nil {
		if externalErr = r.createOrUpdateUnstructured(ctx, auth.ExternalSecurityPolicy); externalErr != nil {
			log.Error(externalErr, "failed to reconcile external SecurityPolicy - CRD may not be installed")
		}
	}
	if auth.InternalSecurityPolicy != nil {
		if internalErr = r.createOrUpdateUnstructured(ctx, auth.InternalSecurityPolicy); internalErr != nil {
			log.Error(internalErr, "failed to reconcile internal SecurityPolicy - CRD may not be installed")
		}
	}
	return externalErr, internalErr
}

// determinePhase inspects the model Deployment and its pods to determine the current lifecycle phase.
func (r *LLMModelReconciler) determinePhase(ctx context.Context, model *llmv1alpha1.LLMModel) llmv1alpha1.LLMModelPhase {
	log := logf.FromContext(ctx)

	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, dep); err != nil {
		if apierrors.IsNotFound(err) {
			return llmv1alpha1.PhasePending
		}
		log.Error(err, "failed to get Deployment for phase determination")
		return llmv1alpha1.PhaseError
	}

	// Check if any pod has a running init container (downloading phase)
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(model.Namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": model.Name},
	); err != nil {
		log.Error(err, "failed to list pods for phase determination")
		return llmv1alpha1.PhaseError
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Running != nil {
				return llmv1alpha1.PhaseDownloading
			}
		}
	}

	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}

	readyReplicas := dep.Status.ReadyReplicas

	if readyReplicas == 0 {
		return llmv1alpha1.PhaseStarting
	}
	if readyReplicas >= desired {
		return llmv1alpha1.PhaseReady
	}
	return llmv1alpha1.PhaseDegraded
}

// updateStatus writes the final status fields to a fresh copy of the LLMModel.
func (r *LLMModelReconciler) updateStatus(
	ctx context.Context,
	log controllerLogger,
	model *llmv1alpha1.LLMModel,
	phase llmv1alpha1.LLMModelPhase,
	conditions []metav1.Condition,
) error {
	// Fetch fresh copy to avoid update conflicts
	fresh := &llmv1alpha1.LLMModel{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	fresh.Status.ObservedGeneration = fresh.Generation
	for _, c := range conditions {
		c.ObservedGeneration = fresh.Generation
		meta.SetStatusCondition(&fresh.Status.Conditions, c)
	}

	// Update replica counts
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: model.Name, Namespace: model.Namespace}, dep); err == nil {
		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		fresh.Status.Replicas = llmv1alpha1.ReplicaStatus{
			Ready:   dep.Status.ReadyReplicas,
			Desired: desired,
		}
	}

	// Update endpoint URLs. Every model on the cluster shares the same
	// hostname pair (`llm.<baseDomain>` external, `llm-internal.<baseDomain>`
	// internal); per-model routing happens via the `x-ai-eg-model` header
	// matched on each AIGatewayRoute (see reconcilers.BuildRoutingResources).
	// The status URL is therefore identical across models - clients
	// disambiguate by setting the `model` field in the OpenAI-compatible
	// request body, which the AI Gateway extracts into the header.
	if r.Config != nil {
		if r.Config.ExternalGatewayName != "" {
			fresh.Status.Endpoints.External = "https://" + reconcilers.SharedExternalHostname(r.Config.BaseDomain)
		}
		if r.Config.InternalGatewayName != "" {
			fresh.Status.Endpoints.Internal = "https://" + reconcilers.SharedInternalHostname(r.Config.BaseDomain)
		}
	}

	if err := r.Status().Update(ctx, fresh); err != nil {
		log.Error(err, "failed to update LLMModel status")
		return err
	}
	return nil
}

// modelConfigHash computes an 8-byte hex hash of the model's breaking configuration fields.
// If the hash changes on an existing Deployment, the Deployment is deleted to trigger re-download.
func modelConfigHash(model *llmv1alpha1.LLMModel) string {
	data := model.Name +
		string(model.Spec.Model.Source) +
		string(model.Spec.Model.Storage.Type) +
		model.Spec.Model.Image +
		model.Spec.Model.Revision +
		model.Spec.Model.Name
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

// --- createOrUpdate helpers ---

func (r *LLMModelReconciler) createOrUpdatePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, pvc)
	}
	if err != nil {
		return err
	}
	// PVC specs are mostly immutable; update labels/annotations only
	existing.Labels = pvc.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	// Preserve existing data (API keys written by other controllers/users); update labels only
	existing.Labels = secret.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	existing.Labels = cm.Labels
	existing.Data = cm.Data
	return r.Update(ctx, existing)
}

// createOrUpdateConfigMapPreserveData is for ConfigMaps whose data is owned by
// another writer (the key-manager stores API-key metadata in them); the
// operator only manages labels, mirroring createOrUpdateSecret.
func (r *LLMModelReconciler) createOrUpdateConfigMapPreserveData(ctx context.Context, cm *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	existing.Labels = cm.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateNetworkPolicy(ctx context.Context, np *networkingv1.NetworkPolicy) error {
	existing := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: np.Name, Namespace: np.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, np)
	}
	if err != nil {
		return err
	}
	existing.Spec = np.Spec
	existing.Labels = np.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateServiceAccount(ctx context.Context, sa *corev1.ServiceAccount) error {
	existing := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: sa.Name, Namespace: sa.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, sa)
	}
	if err != nil {
		return err
	}
	existing.Labels = sa.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateService(ctx context.Context, svc *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, svc)
	}
	if err != nil {
		return err
	}
	// Preserve ClusterIP assigned by the API server
	svc.Spec.ClusterIP = existing.Spec.ClusterIP
	svc.Spec.ClusterIPs = existing.Spec.ClusterIPs
	existing.Spec = svc.Spec
	existing.Labels = svc.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, dep)
	}
	if err != nil {
		return err
	}
	existing.Spec = dep.Spec
	existing.Labels = dep.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateRole(ctx context.Context, role *rbacv1.Role) error {
	existing := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: role.Name, Namespace: role.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, role)
	}
	if err != nil {
		return err
	}
	existing.Rules = role.Rules
	existing.Labels = role.Labels
	return r.Update(ctx, existing)
}

func (r *LLMModelReconciler) createOrUpdateRoleBinding(ctx context.Context, rb *rbacv1.RoleBinding) error {
	existing := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, rb)
	}
	if err != nil {
		return err
	}
	existing.RoleRef = rb.RoleRef
	existing.Subjects = rb.Subjects
	existing.Labels = rb.Labels
	return r.Update(ctx, existing)
}

// createOrUpdateUnstructured creates or updates an unstructured resource.
// Errors for optional CRD-based resources should be logged by the caller rather than
// failing the reconciliation.
func (r *LLMModelReconciler) createOrUpdateUnstructured(
	ctx context.Context,
	obj *unstructured.Unstructured,
) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	err := r.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, obj)
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLMModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.LLMModel{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				return enqueueAllModelsForAPIKeySecret(ctx, r.Client, obj, &llmv1alpha1.LLMModelList{})
			}),
			builder.WithPredicates(managedByOperatorPredicate()),
		).
		Named("llmmodel").
		Complete(r)
}
