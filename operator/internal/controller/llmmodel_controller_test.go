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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/config"
)

// testConfig returns a minimal OperatorConfig suitable for envtest.
func testConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		BaseDomain:          "example.com",
		ExternalGatewayName: "external-gateway",
		ExternalGatewayNS:   "envoy-gateway-system",
		InternalGatewayName: "internal-gateway",
		InternalGatewayNS:   "envoy-gateway-system",
		OIDCIssuerURL:       "https://auth.example.com",
		OIDCGroupsClaim:     "groups",
		OIDCAudience:        "",
		DefaultServingImage: "ghcr.io/llm-d/llm-d-cuda:v0.7.0",
		DefaultEPPImage:     "ghcr.io/llm-d/llm-d-inference-scheduler:v0.8.0",
		// Empty matches the post-#59 default. The legacy ReferenceGrant
		// cleanup branch in reconcileDelete checks for non-empty, so
		// leaving it empty disables that branch (which is what new
		// installs want). A separate fixture covers the legacy path.
		APIKeysNamespace: "",
	}
}

// newReconciler returns a reconciler wired to the envtest client.
func newReconciler() *LLMModelReconciler {
	return &LLMModelReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Config: testConfig(),
	}
}

// newMinimalModel creates a minimal LLMModel for testing.
func newMinimalModel(name string) *llmv1alpha1.LLMModel {
	return &llmv1alpha1.LLMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: llmv1alpha1.LLMModelSpec{
			Model: llmv1alpha1.ModelSpec{
				Name:   "test-model/small",
				Source: llmv1alpha1.ModelSourceHuggingFace,
				Storage: llmv1alpha1.StorageSpec{
					// Use emptyDir so no PVC is needed in envtest
					Type: llmv1alpha1.StorageTypeEmptyDir,
					Size: "10Gi",
				},
			},
			Resources: llmv1alpha1.ResourceSpec{
				GPU: llmv1alpha1.GPUSpec{
					Count: 0, // No GPU needed for tests
					Type:  "nvidia",
				},
			},
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"test-group"},
			},
		},
	}
}

// reconcileRequest creates a reconcile.Request for the given name/namespace.
func reconcileRequest(name string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	}
}

// reconcileUntilStable calls Reconcile repeatedly until it returns no requeue.
// Short RequeueAfter values (<=1s) indicate the controller has more immediate work
// to do (e.g. finalizer was just added). Longer values or zero indicate the
// controller is either done or waiting for async work.
func reconcileUntilStable(r *LLMModelReconciler, ctx context.Context, req reconcile.Request) error {
	for i := 0; i < 10; i++ {
		result, err := r.Reconcile(ctx, req)
		if err != nil {
			return err
		}
		// A short RequeueAfter means the controller has more immediate work to do
		// (e.g. finalizer add). Zero means reconciliation is complete.
		// A longer RequeueAfter means waiting for async work (download, startup).
		if result.RequeueAfter <= 0 || result.RequeueAfter > time.Second {
			return nil
		}
	}
	return fmt.Errorf("reconciliation did not stabilize after 10 iterations")
}

var _ = Describe("LLMModel Controller", func() {
	const testNamespace = "default"

	ctx := context.Background()

	Describe("Finalizer and Phase", func() {
		const modelName = "test-finalizer-phase"

		AfterEach(func() {
			// Clean up after each test
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				// Remove finalizer so delete can proceed
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("adds the finalizer and sets phase to Pending on first reconcile", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("reconciling")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying finalizer was added")
			fetched := &llmv1alpha1.LLMModel{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, fetched)).To(Succeed())
			Expect(fetched.Finalizers).To(ContainElement(finalizerName))

			By("verifying phase is Pending or Starting (deployment just created)")
			Expect(fetched.Status.Phase).To(Or(
				Equal(llmv1alpha1.PhasePending),
				Equal(llmv1alpha1.PhaseStarting),
				Equal(llmv1alpha1.PhaseReady),
			))
		})
	})

	Describe("Core resources created", func() {
		const modelName = "test-core-resources"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("creates Deployment, Service, and ServiceAccount", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("reconciling")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying Deployment was created")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, dep)).To(Succeed())
			Expect(dep.Labels["app.kubernetes.io/instance"]).To(Equal(modelName))
			Expect(dep.Annotations[modelConfigHashAnno]).NotTo(BeEmpty())

			By("verifying Service was created")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8000)))

			By("verifying ServiceAccount was created")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName + "-sa", Namespace: testNamespace}, sa)).To(Succeed())
		})
	})

	Describe("NetworkPolicy created", func() {
		const modelName = "test-netpol"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("creates the default-deny NetworkPolicy", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("reconciling")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying NetworkPolicy was created")
			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-deny-direct",
				Namespace: testNamespace,
			}, np)).To(Succeed())
			Expect(np.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress))
			Expect(np.Spec.PodSelector.MatchLabels["app.kubernetes.io/instance"]).To(Equal(modelName))
		})
	})

	Describe("Auth resources (Secret + ConfigMap in the model's namespace)", func() {
		const modelName = "test-auth-resources"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("creates Secret and ConfigMap in the model's own namespace", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("reconciling")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying API key Secret exists in the model's namespace")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-api-keys",
				Namespace: testNamespace,
			}, secret)).To(Succeed())

			By("verifying API key metadata ConfigMap exists in the model's namespace")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-api-key-metadata",
				Namespace: testNamespace,
			}, cm)).To(Succeed())
		})

		It("preserves key data written by the key-manager across reconciles", func() {
			By("creating the LLMModel and reconciling")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())
			r := newReconciler()
			Expect(reconcileUntilStable(r, ctx, reconcileRequest(modelName))).To(Succeed())

			By("simulating the key-manager writing a key + metadata")
			secret := &corev1.Secret{}
			secretKey := types.NamespacedName{Name: modelName + "-api-keys", Namespace: testNamespace}
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			secret.Data = map[string][]byte{"key-abc123": []byte("sk-secret-value")}
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Name: modelName + "-api-key-metadata", Namespace: testNamespace}
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			cm.Data = map[string]string{"key-abc123": `{"name":"my key","created_by":"user"}`}
			Expect(k8sClient.Update(ctx, cm)).To(Succeed())

			By("reconciling again")
			Expect(reconcileUntilStable(r, ctx, reconcileRequest(modelName))).To(Succeed())

			By("verifying the Secret data survived the reconcile")
			Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("key-abc123"))

			By("verifying the metadata ConfigMap data survived the reconcile")
			Expect(k8sClient.Get(ctx, cmKey, cm)).To(Succeed())
			Expect(cm.Data).To(HaveKey("key-abc123"))
		})
	})

	Describe("Deletion with finalizer cleanup", func() {
		const modelName = "test-deletion"

		It("deletes the API-key Secret and ConfigMap on LLMModel deletion", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("first reconcile (creates resources and adds finalizer)")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying auth resources exist in the model's namespace")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-api-keys",
				Namespace: testNamespace,
			}, secret)).To(Succeed())

			By("deleting the LLMModel (triggers finalizer)")
			fetched := &llmv1alpha1.LLMModel{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, fetched)).To(Succeed())
			Expect(k8sClient.Delete(ctx, fetched)).To(Succeed())

			By("reconciling the deletion")
			err = reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying API-key Secret was deleted")
			deletedSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-api-keys",
				Namespace: testNamespace,
			}, deletedSecret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying metadata ConfigMap was deleted")
			deletedCM := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-api-key-metadata",
				Namespace: testNamespace,
			}, deletedCM)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			By("verifying the LLMModel is gone (finalizer removed)")
			deletedModel := &llmv1alpha1.LLMModel{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, deletedModel)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Describe("Breaking change detection", func() {
		const modelName = "test-breaking-change"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("deletes and recreates the Deployment when model name changes", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("first reconcile")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("capturing the initial deployment UID")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, dep)).To(Succeed())
			originalUID := dep.UID
			originalHash := dep.Annotations[modelConfigHashAnno]
			Expect(originalHash).NotTo(BeEmpty())

			By("changing the model name (breaking change)")
			fetched := &llmv1alpha1.LLMModel{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, fetched)).To(Succeed())
			fetched.Spec.Model.Name = "different-model/changed"
			Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

			By("reconciling after breaking change")
			err = reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the Deployment was deleted (not yet recreated)")
			deletedDep := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, deletedDep)
			// Either deleted or recreated with a new hash
			if err == nil {
				// If it was immediately recreated, the UID or hash should differ
				newHash := deletedDep.Annotations[modelConfigHashAnno]
				Expect(newHash).NotTo(Equal(originalHash))
			} else {
				Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}

			By("reconciling again to recreate the Deployment")
			err = reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying the new Deployment has a different hash")
			newDep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, newDep)).To(Succeed())
			Expect(newDep.Annotations[modelConfigHashAnno]).NotTo(Equal(originalHash))
			// UID changes when an object is deleted and recreated
			Expect(newDep.UID).NotTo(Equal(originalUID))
		})
	})

	Describe("Both endpoints disabled", func() {
		const modelName = "test-no-endpoints"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("still creates Deployment and NetworkPolicy when endpoints are disabled", func() {
			By("creating the LLMModel with both endpoints disabled")
			falseVal := false
			model := newMinimalModel(modelName)
			model.Spec.Endpoints = llmv1alpha1.EndpointSpec{
				External: llmv1alpha1.ExternalEndpointSpec{Enabled: &falseVal},
				Internal: llmv1alpha1.InternalEndpointSpec{Enabled: &falseVal},
			}
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			By("reconciling")
			r := newReconciler()
			err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())

			By("verifying Deployment still exists")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, dep)).To(Succeed())

			By("verifying NetworkPolicy still exists")
			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      modelName + "-deny-direct",
				Namespace: testNamespace,
			}, np)).To(Succeed())
		})
	})

	// The envtest API server has this operator's CRDs but not the AI Gateway's
	// or the Gateway API Inference Extension's, so applying an AIGatewayRoute,
	// SecurityPolicy or InferencePool fails with no-kind-match - exactly what a
	// real cluster looks like when section 4 of the install runbook was skipped.
	// Previously the reconciler logged those failures and reported the model
	// Ready anyway, which is what made the runtime 404 so hard to diagnose.
	Describe("Missing gateway CRDs", func() {
		const modelName = "test-missing-crds"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("reports Degraded with a failing endpoint condition instead of Ready", func() {
			By("creating the LLMModel and reconciling to create the Deployment")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			r := newReconciler()
			Expect(reconcileUntilStable(r, ctx, reconcileRequest(modelName))).To(Succeed())

			By("marking the Deployment ready so the reconcile reaches the routing step")
			// Nothing runs the Deployment controller in envtest, so readyReplicas
			// stays 0 and determinePhase would return Starting, short-circuiting
			// before any gateway resource is applied. Setting status by hand is
			// what gets us to the code path under test.
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, dep)).To(Succeed())
			dep.Status.Replicas = 1
			dep.Status.ReadyReplicas = 1
			Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

			By("reconciling again now that the model counts as serving")
			_, err := r.Reconcile(ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred(), "a missing CRD must not fail the whole reconcile")

			By("checking the model does not claim to be Ready")
			fresh := &llmv1alpha1.LLMModel{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, fresh)).To(Succeed())
			Expect(fresh.Status.Phase).To(Equal(llmv1alpha1.PhaseDegraded),
				"a model whose routes could not be applied is not Ready")

			By("checking the failure is visible as a condition")
			cond := meta.FindStatusCondition(fresh.Status.Conditions, CondExternalEndpointReady)
			Expect(cond).NotTo(BeNil(), "the external endpoint condition must be reported")
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonApplyFailed))
			Expect(cond.Message).NotTo(BeEmpty(), "the condition must say what failed")

			By("checking the InferencePool prerequisite is reported separately")
			poolCond := meta.FindStatusCondition(fresh.Status.Conditions, CondInferencePoolReady)
			Expect(poolCond).NotTo(BeNil(), "a missing GIE CRD must be distinguishable from a missing AI Gateway CRD")
			Expect(poolCond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("requeues so it recovers once the CRDs are installed", func() {
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			r := newReconciler()
			Expect(reconcileUntilStable(r, ctx, reconcileRequest(modelName))).To(Succeed())

			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, dep)).To(Succeed())
			dep.Status.Replicas = 1
			dep.Status.ReadyReplicas = 1
			Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

			result, err := r.Reconcile(ctx, reconcileRequest(modelName))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0),
				"a degraded model must be retried so it heals once the CRDs exist")
		})
	})

	Describe("Idempotency", func() {
		const modelName = "test-idempotent"

		AfterEach(func() {
			model := &llmv1alpha1.LLMModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: modelName, Namespace: testNamespace}, model); err == nil {
				model.Finalizers = nil
				_ = k8sClient.Update(ctx, model)
				_ = k8sClient.Delete(ctx, model)
			}
		})

		It("reconciles multiple times without error", func() {
			By("creating the LLMModel")
			model := newMinimalModel(modelName)
			Expect(k8sClient.Create(ctx, model)).To(Succeed())

			r := newReconciler()

			By("reconciling three times")
			for i := 0; i < 3; i++ {
				err := reconcileUntilStable(r, ctx, reconcileRequest(modelName))
				Expect(err).NotTo(HaveOccurred(), "reconcile %d should not error", i+1)
			}

			By("verifying a single Deployment exists")
			depList := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, depList,
				client.InNamespace(testNamespace),
			)).To(Succeed())

			count := 0
			for _, d := range depList.Items {
				if d.Name == modelName {
					count++
				}
			}
			Expect(count).To(Equal(1))
		})
	})
})
