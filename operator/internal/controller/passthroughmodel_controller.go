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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

// PassthroughModelReconciler reconciles a PassthroughModel object.
//
// Unlike the LLMModel reconciler there is no serving stack to manage: the
// provider serves the models, this controller only provisions gateway
// routing plus the same two auth layers (per-user API keys externally,
// Keycloak JWT internally) that served models get, so the key-manager can
// mint keys for external providers exactly as it does for local ones.
type PassthroughModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Config *config.OperatorConfig
}

// +kubebuilder:rbac:groups=llm.nebari.dev,resources=passthroughmodels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=passthroughmodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=llm.nebari.dev,resources=passthroughmodels/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=backends;securitypolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aigateway.envoyproxy.io,resources=aigatewayroutes;aiservicebackends;backendsecuritypolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=backendtlspolicies,verbs=get;list;watch;create;update;patch;delete

// Reconcile provisions all gateway and auth resources for a PassthroughModel.
func (r *PassthroughModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	pm := &llmv1alpha1.PassthroughModel{}
	if err := r.Get(ctx, req.NamespacedName, pm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !pm.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, pm)
	}

	if !controllerutil.ContainsFinalizer(pm, finalizerName) {
		controllerutil.AddFinalizer(pm, finalizerName)
		if err := r.Update(ctx, pm); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	clientIDs, err := apiKeyClientIDs(ctx, r.Client, pm.Name, pm.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading api key client ids: %w", err)
	}
	credentialSecretNames, err := apiKeySecretNamesWith(ctx, r.Client, pm.Namespace, reconcilers.APIKeySecretName(pm.Name))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing api key secrets: %w", err)
	}
	resources, err := reconcilers.BuildPassthroughResources(pm, r.Config, clientIDs, credentialSecretNames)
	if err != nil {
		// Surface the build failure as a condition so `kubectl describe`
		// explains the Error phase rather than only the reconcile log.
		buildCond := metav1.Condition{
			Type:    CondBackendConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  "BuildFailed",
			Message: err.Error(),
		}
		if statusErr := r.updateStatus(ctx, log, pm, llmv1alpha1.PassthroughPhaseError, []metav1.Condition{buildCond}); statusErr != nil {
			log.Error(statusErr, "failed to update status after build error")
		}
		return ctrl.Result{}, fmt.Errorf("building passthrough resources: %w", err)
	}

	// API-key Secret + metadata ConfigMap first: they hold user-issued keys,
	// carry no owner references (lifetime outlives a reapply of the CR), and
	// are cleaned up by the finalizer. Failures here are real errors, not
	// missing-CRD conditions.
	if err := r.createOrUpdateSecret(ctx, resources.APIKeySecret); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling api key secret: %w", err)
	}
	if err := r.createOrUpdateConfigMap(ctx, resources.APIKeyMetadataCM); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling api key metadata configmap: %w", err)
	}

	// Gateway kinds tolerate a missing CRD: a failed apply does not fail the
	// whole reconcile. Each failure is captured in a status condition and the
	// reconcile is requeued so the resources are retried once the CRDs exist.
	// The LLMModel reconciler does the same.
	conditions := []metav1.Condition{}

	backendErr := r.applyAll(ctx, log, pm,
		resources.Backend,
		resources.BackendTLSPolicy,
		resources.AIServiceBackend,
		resources.BackendSecurityPolicy,
	)
	conditions = append(conditions, conditionFor(CondBackendConfigured, backendErr, "provider backend resources applied"))

	externalEnabled := resources.ExternalRoute != nil
	if externalEnabled {
		externalErr := r.applyAll(ctx, log, pm, resources.ExternalRoute, resources.ExternalSecurityPolicy)
		conditions = append(conditions, conditionFor(CondExternalEndpointReady, externalErr, "external route and apiKeyAuth policy applied"))
	} else {
		conditions = append(conditions, disabledCondition(CondExternalEndpointReady))
	}

	internalEnabled := resources.InternalRoute != nil
	if internalEnabled {
		internalErr := r.applyAll(ctx, log, pm, resources.InternalRoute, resources.InternalSecurityPolicy)
		conditions = append(conditions, conditionFor(CondInternalEndpointReady, internalErr, "internal route and JWT policy applied"))
	} else {
		conditions = append(conditions, disabledCondition(CondInternalEndpointReady))
	}

	phase := llmv1alpha1.PassthroughPhaseReady
	if hasApplyFailure(conditions) {
		phase = llmv1alpha1.PassthroughPhaseError
	}

	if err := r.updateStatus(ctx, log, pm, phase, conditions); err != nil {
		return ctrl.Result{}, err
	}

	if phase == llmv1alpha1.PassthroughPhaseError {
		// Retry: the usual cause is the AI Gateway CRDs not being
		// installed yet (install ordering on a fresh cluster).
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileDelete cleans up the auth resources (no owner references) and
// removes the finalizer. All gateway resources carry owner references and
// are garbage-collected by Kubernetes.
func (r *PassthroughModelReconciler) reconcileDelete(ctx context.Context, pm *llmv1alpha1.PassthroughModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(pm, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("cleaning up passthrough auth resources", "passthroughmodel", pm.Name)

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: reconcilers.APIKeySecretName(pm.Name), Namespace: pm.Namespace}
	if err := r.Get(ctx, secretKey, secret); err == nil {
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting api key secret: %w", err)
		}
	}

	cm := &corev1.ConfigMap{}
	cmKey := types.NamespacedName{Name: reconcilers.APIKeyMetadataConfigMapName(pm.Name), Namespace: pm.Namespace}
	if err := r.Get(ctx, cmKey, cm); err == nil {
		if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting api key metadata configmap: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(pm, finalizerName)
	if err := r.Update(ctx, pm); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// applyAll createOrUpdates every given unstructured object, setting the
// PassthroughModel as controller owner so Kubernetes garbage-collects them
// on delete. The first error is returned after logging; remaining objects
// are still attempted so one missing CRD does not block the others.
func (r *PassthroughModelReconciler) applyAll(ctx context.Context, log controllerLogger, owner *llmv1alpha1.PassthroughModel, objs ...*unstructured.Unstructured) error {
	var firstErr error
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		if err := controllerutil.SetControllerReference(owner, obj, r.Scheme); err != nil {
			return fmt.Errorf("setting owner on %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if err := r.createOrUpdateUnstructured(ctx, obj); err != nil {
			log.Error(err, "failed to reconcile resource - CRD may not be installed",
				"kind", obj.GetKind(), "name", obj.GetName())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *PassthroughModelReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	// Preserve existing data (API keys written by the key-manager); update labels only.
	existing.Labels = secret.Labels
	return r.Update(ctx, existing)
}

func (r *PassthroughModelReconciler) createOrUpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	// Preserve existing data (key metadata written by the key-manager).
	existing.Labels = cm.Labels
	return r.Update(ctx, existing)
}

func (r *PassthroughModelReconciler) createOrUpdateUnstructured(ctx context.Context, obj *unstructured.Unstructured) error {
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

func (r *PassthroughModelReconciler) updateStatus(
	ctx context.Context,
	log controllerLogger,
	pm *llmv1alpha1.PassthroughModel,
	phase llmv1alpha1.PassthroughModelPhase,
	conditions []metav1.Condition,
) error {
	fresh := &llmv1alpha1.PassthroughModel{}
	if err := r.Get(ctx, types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	fresh.Status.ObservedGeneration = fresh.Generation
	for _, c := range conditions {
		c.ObservedGeneration = fresh.Generation
		meta.SetStatusCondition(&fresh.Status.Conditions, c)
	}

	// Shared endpoint URLs, same hostname pair as every served model.
	if r.Config != nil {
		if boolOrDefaultStatus(fresh.Spec.Endpoints.External.Enabled) {
			fresh.Status.Endpoints.External = "https://" + reconcilers.SharedExternalHostname(r.Config.BaseDomain)
		}
		if boolOrDefaultStatus(fresh.Spec.Endpoints.Internal.Enabled) {
			fresh.Status.Endpoints.Internal = "https://" + reconcilers.SharedInternalHostname(r.Config.BaseDomain)
		}
	}

	if err := r.Status().Update(ctx, fresh); err != nil {
		log.Error(err, "failed to update PassthroughModel status")
		return err
	}
	return nil
}

func boolOrDefaultStatus(b *bool) bool {
	if b == nil {
		return true
	}
	return *b
}

// SetupWithManager sets up the controller with the Manager.
func (r *PassthroughModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.PassthroughModel{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				return enqueueAllModelsForAPIKeySecret(ctx, r.Client, obj, &llmv1alpha1.PassthroughModelList{})
			}),
			builder.WithPredicates(managedByOperatorPredicate()),
		).
		Named("passthroughmodel").
		Complete(r)
}
