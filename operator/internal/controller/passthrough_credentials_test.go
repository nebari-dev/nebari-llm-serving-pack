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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func passthroughModelWithCredential(name, namespace, secretName string) *llmv1alpha1.PassthroughModel {
	return &llmv1alpha1.PassthroughModel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: llmv1alpha1.PassthroughModelSpec{
			Provider: llmv1alpha1.ProviderSpec{CredentialSecretName: secretName},
		},
	}
}

func TestEnqueuePassthroughModelsForCredentialSecret(t *testing.T) {
	objects := []client.Object{
		passthroughModelWithCredential("openrouter-primary", "llm", "openrouter-credential"),
		passthroughModelWithCredential("openrouter-fallback", "llm", "openrouter-credential"),
		passthroughModelWithCredential("different-provider", "llm", "other-credential"),
		passthroughModelWithCredential("same-secret-other-namespace", "other", "openrouter-credential"),
	}
	c := fake.NewClientBuilder().
		WithScheme(apikeysTestScheme(t)).
		WithObjects(objects...).
		WithIndex(
			&llmv1alpha1.PassthroughModel{},
			passthroughCredentialSecretIndex,
			indexPassthroughModelByCredentialSecret,
		).
		Build()

	requests := enqueuePassthroughModelsForCredentialSecret(
		context.Background(),
		c,
		namedSecret("openrouter-credential", "llm"),
	)

	want := map[types.NamespacedName]bool{
		{Name: "openrouter-primary", Namespace: "llm"}:  true,
		{Name: "openrouter-fallback", Namespace: "llm"}: true,
	}
	if len(requests) != len(want) {
		t.Fatalf("got %d requests (%v), want %d", len(requests), requests, len(want))
	}
	for _, request := range requests {
		if !want[request.NamespacedName] {
			t.Errorf("unexpected reconcile request for %s", request.NamespacedName)
			continue
		}
		delete(want, request.NamespacedName)
	}
	for missing := range want {
		t.Errorf("missing reconcile request for %s", missing)
	}
}

func TestEnqueuePassthroughModelsForUnreferencedSecret(t *testing.T) {
	model := passthroughModelWithCredential("openrouter", "llm", "openrouter-credential")
	c := fake.NewClientBuilder().
		WithScheme(apikeysTestScheme(t)).
		WithObjects(model).
		WithIndex(
			&llmv1alpha1.PassthroughModel{},
			passthroughCredentialSecretIndex,
			indexPassthroughModelByCredentialSecret,
		).
		Build()

	requests := enqueuePassthroughModelsForCredentialSecret(
		context.Background(),
		c,
		namedSecret("unrelated", "other"),
	)

	if len(requests) != 0 {
		t.Fatalf("got requests %v for an unreferenced Secret", requests)
	}
}

func TestPassthroughPhaseForConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		want       llmv1alpha1.PassthroughModelPhase
	}{
		{
			name: "an unresolved credential does not block readiness",
			conditions: []metav1.Condition{{
				Type:   "CredentialResolved",
				Status: metav1.ConditionFalse,
				Reason: "SecretNotFound",
			}},
			want: llmv1alpha1.PassthroughPhaseReady,
		},
		{
			name: "a gateway apply failure sets the error phase",
			conditions: []metav1.Condition{{
				Type:   CondBackendConfigured,
				Status: metav1.ConditionFalse,
				Reason: "ApplyFailed",
			}},
			want: llmv1alpha1.PassthroughPhaseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := passthroughPhaseForConditions(tt.conditions); got != tt.want {
				t.Fatalf("got phase %q, want %q", got, tt.want)
			}
		})
	}
}
