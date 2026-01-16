// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

// Package aic provides AIConfigurator plan storage and retrieval logic.
package aic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// PlanLabelKey is the label key used to identify AIC plan ConfigMaps
	PlanLabelKey = "grove.io/aic-plan"

	// PlanDataKey is the key in ConfigMap.Data that contains the plan JSON
	PlanDataKey = "plan.json"

	// PlanConfigMapSuffix is the suffix added to the plan name to create the ConfigMap name
	PlanConfigMapSuffix = "-aic-plan"

	// StoredAtAnnotation is the annotation key for when the plan was stored
	StoredAtAnnotation = "grove.io/stored-at"
)

// StoragePlan represents an AIConfigurator plan for inference deployment stored in ConfigMap
type StoragePlan struct {
	// Model is the model identifier (e.g., "QWEN3_32B")
	Model string `json:"model"`

	// System is the hardware system type (e.g., "h200_sxm")
	System string `json:"system"`

	// ServingMode is the serving mode (e.g., "disagg", "agg")
	ServingMode string `json:"serving_mode"`

	// Config contains the deployment configuration
	Config StoragePlanConfig `json:"config"`

	// Expected contains the expected performance metrics
	Expected StoragePlanExpected `json:"expected,omitempty"`

	// SLA contains the SLA targets
	SLA StoragePlanSLA `json:"sla,omitempty"`
}

// StoragePlanConfig represents the deployment configuration in a stored plan
type StoragePlanConfig struct {
	// PrefillWorkers is the number of prefill workers
	PrefillWorkers int `json:"prefill_workers"`

	// DecodeWorkers is the number of decode workers
	DecodeWorkers int `json:"decode_workers"`

	// PrefillTP is the tensor parallelism for prefill workers
	PrefillTP int `json:"prefill_tp"`

	// DecodeTP is the tensor parallelism for decode workers
	DecodeTP int `json:"decode_tp"`
}

// StoragePlanExpected represents expected performance metrics
type StoragePlanExpected struct {
	// ThroughputTokensPerSecPerGPU is the expected throughput in tokens per second per GPU
	ThroughputTokensPerSecPerGPU float64 `json:"throughput_tokens_per_sec_per_gpu,omitempty"`

	// TTFTMs is the expected time to first token in milliseconds
	TTFTMs float64 `json:"ttft_ms,omitempty"`

	// TPOTMs is the expected time per output token in milliseconds
	TPOTMs float64 `json:"tpot_ms,omitempty"`
}

// StoragePlanSLA represents SLA targets
type StoragePlanSLA struct {
	// TTFTMs is the SLA target for time to first token in milliseconds
	TTFTMs float64 `json:"ttft_ms,omitempty"`

	// TPOTMs is the SLA target for time per output token in milliseconds
	TPOTMs float64 `json:"tpot_ms,omitempty"`
}

// StoredPlan wraps a StoragePlan with metadata about storage
type StoredPlan struct {
	// Name is the name of the plan (PodCliqueSet name)
	Name string

	// Namespace is the namespace where the plan is stored
	Namespace string

	// StoredAt is when the plan was stored
	StoredAt time.Time

	// Plan is the actual plan data
	Plan StoragePlan
}

// PlanStore provides methods for storing and retrieving AIC plans
type PlanStore struct {
	clientset kubernetes.Interface
}

// NewPlanStore creates a new PlanStore
func NewPlanStore(clientset kubernetes.Interface) *PlanStore {
	return &PlanStore{
		clientset: clientset,
	}
}

// Store stores a plan as a ConfigMap
func (s *PlanStore) Store(ctx context.Context, name, namespace string, plan *StoragePlan) error {
	// Serialize the plan to JSON
	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize plan: %w", err)
	}

	configMapName := name + PlanConfigMapSuffix
	now := time.Now().UTC()

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels: map[string]string{
				PlanLabelKey: name,
			},
			Annotations: map[string]string{
				StoredAtAnnotation: now.Format(time.RFC3339),
			},
		},
		Data: map[string]string{
			PlanDataKey: string(planJSON),
		},
	}

	// Try to get existing ConfigMap
	existing, err := s.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err == nil {
		// Update existing ConfigMap
		configMap.ResourceVersion = existing.ResourceVersion
		_, err = s.clientset.CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update plan ConfigMap: %w", err)
		}
	} else {
		// Create new ConfigMap
		_, err = s.clientset.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create plan ConfigMap: %w", err)
		}
	}

	return nil
}

// Get retrieves a stored plan by name
func (s *PlanStore) Get(ctx context.Context, name, namespace string) (*StoredPlan, error) {
	configMapName := name + PlanConfigMapSuffix

	configMap, err := s.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get plan ConfigMap: %w", err)
	}

	return s.configMapToStoredPlan(configMap)
}

// List lists all stored plans in a namespace
func (s *PlanStore) List(ctx context.Context, namespace string) ([]*StoredPlan, error) {
	labelSelector := PlanLabelKey
	configMaps, err := s.clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list plan ConfigMaps: %w", err)
	}

	var plans []*StoredPlan
	for i := range configMaps.Items {
		plan, err := s.configMapToStoredPlan(&configMaps.Items[i])
		if err != nil {
			// Skip invalid plans but continue
			continue
		}
		plans = append(plans, plan)
	}

	return plans, nil
}

// Delete deletes a stored plan
func (s *PlanStore) Delete(ctx context.Context, name, namespace string) error {
	configMapName := name + PlanConfigMapSuffix

	err := s.clientset.CoreV1().ConfigMaps(namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete plan ConfigMap: %w", err)
	}

	return nil
}

// configMapToStoredPlan converts a ConfigMap to a StoredPlan
func (s *PlanStore) configMapToStoredPlan(configMap *corev1.ConfigMap) (*StoredPlan, error) {
	planJSON, ok := configMap.Data[PlanDataKey]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s does not contain plan data", configMap.Name)
	}

	var plan StoragePlan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w", err)
	}

	// Get the plan name from the label
	name, ok := configMap.Labels[PlanLabelKey]
	if !ok {
		// Fall back to deriving from ConfigMap name
		if len(configMap.Name) > len(PlanConfigMapSuffix) {
			name = configMap.Name[:len(configMap.Name)-len(PlanConfigMapSuffix)]
		} else {
			name = configMap.Name
		}
	}

	// Parse stored time
	var storedAt time.Time
	if storedAtStr, ok := configMap.Annotations[StoredAtAnnotation]; ok {
		if t, err := time.Parse(time.RFC3339, storedAtStr); err == nil {
			storedAt = t
		}
	}
	if storedAt.IsZero() {
		storedAt = configMap.CreationTimestamp.Time
	}

	return &StoredPlan{
		Name:      name,
		Namespace: configMap.Namespace,
		StoredAt:  storedAt,
		Plan:      plan,
	}, nil
}

// ParsePlanFromFile parses a plan from JSON or YAML content
func ParsePlanFromFile(content []byte) (*StoragePlan, error) {
	var plan StoragePlan

	// Try JSON first
	if err := json.Unmarshal(content, &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan: %w", err)
	}

	return &plan, nil
}

// DiffResult represents a single difference between plan and deployed config
type DiffResult struct {
	// Setting is the name of the setting being compared
	Setting string

	// Planned is the planned value
	Planned string

	// Deployed is the deployed value
	Deployed string

	// Matches indicates if the values match
	Matches bool
}

// ComputeDiff computes the differences between a plan and deployed configuration
func ComputeDiff(plan *StoragePlan, deployed *DeployedConfig) []DiffResult {
	var results []DiffResult

	// Compare prefill workers
	results = append(results, DiffResult{
		Setting:  "Prefill Workers",
		Planned:  fmt.Sprintf("%d", plan.Config.PrefillWorkers),
		Deployed: fmt.Sprintf("%d", deployed.PrefillWorkers),
		Matches:  plan.Config.PrefillWorkers == deployed.PrefillWorkers,
	})

	// Compare decode workers
	results = append(results, DiffResult{
		Setting:  "Decode Workers",
		Planned:  fmt.Sprintf("%d", plan.Config.DecodeWorkers),
		Deployed: fmt.Sprintf("%d", deployed.DecodeWorkers),
		Matches:  plan.Config.DecodeWorkers == deployed.DecodeWorkers,
	})

	// Compare prefill TP
	results = append(results, DiffResult{
		Setting:  "Prefill TP",
		Planned:  fmt.Sprintf("%d", plan.Config.PrefillTP),
		Deployed: fmt.Sprintf("%d", deployed.PrefillTP),
		Matches:  plan.Config.PrefillTP == deployed.PrefillTP,
	})

	// Compare decode TP
	results = append(results, DiffResult{
		Setting:  "Decode TP",
		Planned:  fmt.Sprintf("%d", plan.Config.DecodeTP),
		Deployed: fmt.Sprintf("%d", deployed.DecodeTP),
		Matches:  plan.Config.DecodeTP == deployed.DecodeTP,
	})

	return results
}

// DeployedConfig represents the configuration extracted from a deployed PodCliqueSet
type DeployedConfig struct {
	// PrefillWorkers is the number of prefill workers
	PrefillWorkers int

	// DecodeWorkers is the number of decode workers
	DecodeWorkers int

	// PrefillTP is the tensor parallelism for prefill workers
	PrefillTP int

	// DecodeTP is the tensor parallelism for decode workers
	DecodeTP int
}
