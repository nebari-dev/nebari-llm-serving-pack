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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// newPassthroughReconciler returns a reconciler wired to the envtest client.
func newPassthroughReconciler() *PassthroughModelReconciler {
	return &PassthroughModelReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Config: testConfig(),
	}
}

func newPassthroughModel(name, namespace string) *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: llmv1alpha1.PassthroughModelSpec{
			Provider: llmv1alpha1.ProviderSpec{
				Hostname:             "openrouter.ai",
				Port:                 443,
				SchemaVersion:        "api/v1",
				CredentialSecretName: name + "-provider-credential",
			},
			Models: llmv1alpha1.PassthroughModels{
				CatchAll: true,
				Declared: []string{"openai/gpt-5.2"},
			},
			Access: llmv1alpha1.AccessSpec{
				Groups: []string{"llm"},
			},
		},
	}
}

var _ = Describe("PassthroughModel Controller", func() {
	ctx := context.Background()

	// The envtest environment installs only this operator's CRDs, so the
	// gateway-kind applies (Backend, AIGatewayRoute, ...) fail with
	// no-kind-match, exactly like an LLMModel reconcile on a cluster
	// without the AI Gateway installed. The controller tolerates that and
	// reports it via status conditions, which is what these tests pin.

	Describe("Reconciling a PassthroughModel", func() {
		var pmName string
		var counter int

		BeforeEach(func() {
			counter++
			pmName = fmt.Sprintf("pt-test-%d", counter)
			pm := newPassthroughModel(pmName, "default")
			Expect(k8sClient.Create(ctx, pm)).To(Succeed())
		})

		AfterEach(func() {
			pm := &llmv1alpha1.PassthroughModel{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: pmName, Namespace: "default"}, pm); err == nil {
				_ = k8sClient.Delete(ctx, pm)
				r := newPassthroughReconciler()
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}})
			}
			credentialSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:      pmName + "-provider-credential",
				Namespace: "default",
			}}
			_ = k8sClient.Delete(ctx, credentialSecret)
		})

		It("adds the finalizer and creates the api-keys Secret and metadata ConfigMap", func() {
			r := newPassthroughReconciler()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			pm := &llmv1alpha1.PassthroughModel{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, pm)).To(Succeed())
			Expect(pm.Finalizers).To(ContainElement("llm.nebari.dev/cleanup"))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pmName + "-api-keys", Namespace: "default"}, secret)).To(Succeed())
			Expect(secret.Labels["llm.nebari.dev/model-name"]).To(Equal(pmName))
			Expect(secret.Labels["app.kubernetes.io/name"]).To(Equal("passthroughmodel"))

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pmName + "-api-key-metadata", Namespace: "default"}, cm)).To(Succeed())
			Expect(cm.Labels["llm.nebari.dev/model-name"]).To(Equal(pmName))
		})

		It("preserves user-written API keys across reconciles", func() {
			r := newPassthroughReconciler()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			key := types.NamespacedName{Name: pmName + "-api-keys", Namespace: "default"}
			Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
			secret.Data = map[string][]byte{"alice": []byte("sk-user-key")}
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, key, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("alice", []byte("sk-user-key")))
		})

		It("reports gateway CRD apply failures in conditions and sets endpoint URLs", func() {
			r := newPassthroughReconciler()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			pm := &llmv1alpha1.PassthroughModel{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, pm)).To(Succeed())

			// No AI Gateway CRDs in envtest: every gateway apply fails, so
			// phase is Error and conditions carry ApplyFailed.
			Expect(pm.Status.Phase).To(Equal(llmv1alpha1.PassthroughPhaseError))
			var backendCond *metav1.Condition
			for i := range pm.Status.Conditions {
				if pm.Status.Conditions[i].Type == CondBackendConfigured {
					backendCond = &pm.Status.Conditions[i]
				}
			}
			Expect(backendCond).NotTo(BeNil())
			Expect(backendCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(backendCond.Reason).To(Equal("ApplyFailed"))

			Expect(pm.Status.Endpoints.External).To(Equal("https://llm.example.com"))
			Expect(pm.Status.Endpoints.Internal).To(Equal("https://llm-internal.example.com"))
			Expect(pm.Status.ObservedGeneration).To(Equal(pm.Generation))
		})

		DescribeTable("reports unresolved provider credentials",
			func(secretData map[string][]byte, expectedReason string) {
				if secretData != nil {
					credentialSecret := &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      pmName + "-provider-credential",
							Namespace: "default",
						},
						Data: secretData,
					}
					Expect(k8sClient.Create(ctx, credentialSecret)).To(Succeed())
				}

				r := newPassthroughReconciler()
				req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
				_, err := r.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())

				pm := &llmv1alpha1.PassthroughModel{}
				Expect(k8sClient.Get(ctx, req.NamespacedName, pm)).To(Succeed())
				condition := meta.FindStatusCondition(pm.Status.Conditions, "CredentialResolved")
				Expect(condition).NotTo(BeNil())
				Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				Expect(condition.Reason).To(Equal(expectedReason))
			},
			Entry("when the referenced Secret does not exist", nil, "SecretNotFound"),
			Entry("when the Secret does not contain apiKey", map[string][]byte{}, "APIKeyMissing"),
			Entry("when apiKey is empty", map[string][]byte{"apiKey": []byte("")}, "APIKeyMissing"),
		)

		It("reports a resolved provider credential", func() {
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pmName + "-provider-credential",
					Namespace: "default",
				},
				Data: map[string][]byte{"apiKey": []byte("provider-key")},
			}
			Expect(k8sClient.Create(ctx, credentialSecret)).To(Succeed())

			r := newPassthroughReconciler()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			pm := &llmv1alpha1.PassthroughModel{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, pm)).To(Succeed())
			condition := meta.FindStatusCondition(pm.Status.Conditions, "CredentialResolved")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("Resolved"))
		})

		It("cleans up the Secret and ConfigMap on deletion", func() {
			r := newPassthroughReconciler()
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: pmName, Namespace: "default"}}
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			pm := &llmv1alpha1.PassthroughModel{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, pm)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pm)).To(Succeed())

			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pmName + "-api-keys", Namespace: "default"}, secret)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "api-keys Secret should be deleted")

			cm := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: pmName + "-api-key-metadata", Namespace: "default"}, cm)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "metadata ConfigMap should be deleted")

			err = k8sClient.Get(ctx, req.NamespacedName, pm)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "PassthroughModel should be gone after finalizer removal")
		})
	})
})
