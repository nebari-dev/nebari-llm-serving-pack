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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

const (
	passthroughCredentialSecretIndex = "spec.provider.credentialSecretName"
	providerCredentialAPIKey         = "apiKey"
)

func resolvePassthroughCredential(
	ctx context.Context,
	c client.Reader,
	pm *llmv1alpha1.PassthroughModel,
) (metav1.Condition, error) {
	secretName := pm.Spec.Provider.CredentialSecretName
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: pm.Namespace}, secret)
	if apierrors.IsNotFound(err) {
		return newCredentialCondition(
			metav1.ConditionFalse,
			"SecretNotFound",
			"referenced credential Secret \""+secretName+"\" was not found",
		), nil
	}
	if err != nil {
		return metav1.Condition{}, err
	}

	if len(secret.Data[providerCredentialAPIKey]) == 0 {
		return newCredentialCondition(
			metav1.ConditionFalse,
			"APIKeyMissing",
			"referenced credential Secret \""+secretName+"\" does not contain a non-empty \"apiKey\" entry",
		), nil
	}

	return newCredentialCondition(
		metav1.ConditionTrue,
		"Resolved",
		"referenced credential Secret contains a non-empty \"apiKey\" entry",
	), nil
}

func newCredentialCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    CondCredentialResolved,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

func indexPassthroughModelByCredentialSecret(obj client.Object) []string {
	pm, ok := obj.(*llmv1alpha1.PassthroughModel)
	if !ok || pm.Spec.Provider.CredentialSecretName == "" {
		return nil
	}
	return []string{pm.Spec.Provider.CredentialSecretName}
}

func enqueuePassthroughModelsForCredentialSecret(
	ctx context.Context,
	c client.Reader,
	obj client.Object,
) []reconcile.Request {
	var models llmv1alpha1.PassthroughModelList
	if err := c.List(
		ctx,
		&models,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{passthroughCredentialSecretIndex: obj.GetName()},
	); err != nil {
		logf.FromContext(ctx).Error(
			err,
			"listing PassthroughModels for credential Secret change",
			"secret", obj.GetName(),
			"namespace", obj.GetNamespace(),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(models.Items))
	for i := range models.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      models.Items[i].Name,
			Namespace: models.Items[i].Namespace,
		}})
	}
	return requests
}
