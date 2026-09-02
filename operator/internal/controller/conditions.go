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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// Status condition types and reasons shared by the LLMModel and
// PassthroughModel reconcilers.
//
// Both controllers apply gateway resources whose CRDs are installed
// separately from this pack (the Envoy AI Gateway's and the Gateway API
// Inference Extension's - see section 4 of the install runbook). A missing CRD
// is not fatal to a reconcile: the rest of the model still converges, and the
// applies succeed on a later pass once the CRDs exist. But it must be
// *visible*, because the runtime symptom of a route that was never created is
// an HTTP 404 or 500 with nothing in the ArgoCD UI to explain it.
const (
	// CondBackendConfigured covers the provider plumbing on a PassthroughModel:
	// Backend, BackendTLSPolicy, AIServiceBackend, BackendSecurityPolicy.
	CondBackendConfigured = "BackendConfigured"
	// CondExternalEndpointReady covers the external route + apiKeyAuth policy.
	CondExternalEndpointReady = "ExternalEndpointReady"
	// CondInternalEndpointReady covers the internal route + JWT policy.
	CondInternalEndpointReady = "InternalEndpointReady"
	// CondInferencePoolReady covers the InferencePool an LLMModel needs. It is
	// reported separately from the endpoint conditions because it comes from a
	// different CRD bundle: a cluster can have the AI Gateway CRDs and not the
	// Inference Extension's, and the llm-d End-Point Picker crashloops without
	// InferencePool while routing looks fine.
	CondInferencePoolReady = "InferencePoolReady"
)

const (
	reasonApplied     = "Applied"
	reasonApplyFailed = "ApplyFailed"
	reasonDisabled    = "EndpointDisabled"
)

// conditionFor turns the outcome of an apply into a status condition. A nil
// err reports Applied with okMessage; a non-nil err reports ApplyFailed and
// carries the error text, which is the only place a reader learns *which*
// resource could not be applied.
func conditionFor(condType string, err error, okMessage string) metav1.Condition {
	if err != nil {
		return metav1.Condition{
			Type:    condType,
			Status:  metav1.ConditionFalse,
			Reason:  reasonApplyFailed,
			Message: err.Error(),
		}
	}
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionTrue,
		Reason:  reasonApplied,
		Message: okMessage,
	}
}

// disabledCondition reports an endpoint the spec switched off. It is False,
// like a failure, but for a deliberate reason - hasApplyFailure keeps the two
// apart so turning an endpoint off does not degrade the model.
func disabledCondition(condType string) metav1.Condition {
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionFalse,
		Reason:  reasonDisabled,
		Message: "endpoint disabled in spec",
	}
}

// hasApplyFailure reports whether any condition failed to apply, as opposed to
// being deliberately disabled.
func hasApplyFailure(conds []metav1.Condition) bool {
	for _, c := range conds {
		if c.Status == metav1.ConditionFalse && c.Reason == reasonApplyFailed {
			return true
		}
	}
	return false
}

// phaseWithRoutingFailure downgrades a Ready model to Degraded when one of its
// gateway resources could not be applied. A model whose pod is serving but
// whose routes do not exist is unreachable, which is what Degraded describes;
// Error would overstate it, since the workload itself is healthy.
//
// Only Ready is downgraded. Pending, Downloading and Starting already say
// something more specific about where the model is, and Error is not softened.
func phaseWithRoutingFailure(phase llmv1alpha1.LLMModelPhase, conds []metav1.Condition) llmv1alpha1.LLMModelPhase {
	if phase == llmv1alpha1.PhaseReady && hasApplyFailure(conds) {
		return llmv1alpha1.PhaseDegraded
	}
	return phase
}
