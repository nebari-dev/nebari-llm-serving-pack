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
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestConditionFor(t *testing.T) {
	tests := []struct {
		name        string
		condType    string
		err         error
		okMessage   string
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name:        "no error reports Applied",
			condType:    CondExternalEndpointReady,
			err:         nil,
			okMessage:   "external route applied",
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonApplied,
			wantMessage: "external route applied",
		},
		{
			name:        "error reports ApplyFailed and carries the message",
			condType:    CondInternalEndpointReady,
			err:         errors.New(`no matches for kind "AIGatewayRoute"`),
			okMessage:   "internal route applied",
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonApplyFailed,
			wantMessage: `no matches for kind "AIGatewayRoute"`,
		},
		{
			name:        "joined errors surface both causes",
			condType:    CondExternalEndpointReady,
			err:         errors.Join(errors.New("route failed"), errors.New("policy failed")),
			okMessage:   "unused",
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonApplyFailed,
			wantMessage: "route failed\npolicy failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conditionFor(tt.condType, tt.err, tt.okMessage)
			if got.Type != tt.condType {
				t.Errorf("Type = %q, want %q", got.Type, tt.condType)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}

func TestHasApplyFailure(t *testing.T) {
	applied := metav1.Condition{Type: CondExternalEndpointReady, Status: metav1.ConditionTrue, Reason: reasonApplied}
	failed := metav1.Condition{Type: CondInternalEndpointReady, Status: metav1.ConditionFalse, Reason: reasonApplyFailed}
	disabled := disabledCondition(CondInternalEndpointReady)

	tests := []struct {
		name  string
		conds []metav1.Condition
		want  bool
	}{
		{name: "no conditions", conds: nil, want: false},
		{name: "all applied", conds: []metav1.Condition{applied}, want: false},
		{name: "one failed", conds: []metav1.Condition{applied, failed}, want: true},
		{
			// A disabled endpoint is False, but deliberately so. Treating it as a
			// failure would degrade every model that turns an endpoint off.
			name:  "disabled endpoint is not a failure",
			conds: []metav1.Condition{applied, disabled},
			want:  false,
		},
		{name: "disabled and failed", conds: []metav1.Condition{disabled, failed}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasApplyFailure(tt.conds); got != tt.want {
				t.Errorf("hasApplyFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPhaseWithRoutingFailure(t *testing.T) {
	failed := []metav1.Condition{{
		Type:   CondExternalEndpointReady,
		Status: metav1.ConditionFalse,
		Reason: reasonApplyFailed,
	}}
	ok := []metav1.Condition{{
		Type:   CondExternalEndpointReady,
		Status: metav1.ConditionTrue,
		Reason: reasonApplied,
	}}

	tests := []struct {
		name  string
		phase llmv1alpha1.LLMModelPhase
		conds []metav1.Condition
		want  llmv1alpha1.LLMModelPhase
	}{
		{
			// The pod is up but nothing can route to it. That is exactly
			// what Degraded means; Error would overstate it.
			name:  "Ready degrades when an apply failed",
			phase: llmv1alpha1.PhaseReady,
			conds: failed,
			want:  llmv1alpha1.PhaseDegraded,
		},
		{
			name:  "Ready stays Ready when every apply succeeded",
			phase: llmv1alpha1.PhaseReady,
			conds: ok,
			want:  llmv1alpha1.PhaseReady,
		},
		{
			// Pending already says something more specific than Degraded
			// (there is no Deployment yet), so it is left alone.
			name:  "Pending is not downgraded",
			phase: llmv1alpha1.PhasePending,
			conds: failed,
			want:  llmv1alpha1.PhasePending,
		},
		{
			name:  "Error is not softened to Degraded",
			phase: llmv1alpha1.PhaseError,
			conds: failed,
			want:  llmv1alpha1.PhaseError,
		},
		{
			name:  "already Degraded stays Degraded",
			phase: llmv1alpha1.PhaseDegraded,
			conds: failed,
			want:  llmv1alpha1.PhaseDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phaseWithRoutingFailure(tt.phase, tt.conds); got != tt.want {
				t.Errorf("phaseWithRoutingFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}
