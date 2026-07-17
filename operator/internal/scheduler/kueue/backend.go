// /*
// Copyright 2026 The Grove Authors.
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

package kueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	queueNameLabel                   = "kueue.x-k8s.io/queue-name"
	podGroupNameLabel                = "kueue.x-k8s.io/pod-group-name"
	podGroupServingAnnotation        = "kueue.x-k8s.io/pod-group-serving"
	retriableInGroupAnnotation       = "kueue.x-k8s.io/retriable-in-group"
	podSetRequiredTopologyAnnotation = "kueue.x-k8s.io/podset-required-topology"
)

var workloadGVK = schema.GroupVersionKind{
	Group:   "kueue.x-k8s.io",
	Version: "v1beta2",
	Kind:    "Workload",
}

type schedulerBackend struct {
	client        client.Client
	scheme        *runtime.Scheme
	name          string
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
	config        configv1alpha1.KueueSchedulerConfiguration
}

var _ scheduler.Backend = (*schedulerBackend)(nil)

func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) scheduler.Backend {
	return &schedulerBackend{
		client:        cl,
		scheme:        scheme,
		name:          string(profile.Name),
		eventRecorder: eventRecorder,
		profile:       profile,
		config:        defaultConfig(),
	}
}

func (b *schedulerBackend) Name() string {
	return b.name
}

func (b *schedulerBackend) Init(_ client.Client) error {
	if b.profile.Config == nil || len(b.profile.Config.Raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(b.profile.Config.Raw, &b.config); err != nil {
		return fmt.Errorf("failed to unmarshal kueue scheduler config: %w", err)
	}
	b.config = withDefaults(b.config)
	return nil
}

// SyncPodGang builds a prebuilt Kueue Workload for every PodGang, mapping each PodGroup to a Kueue podSet.
// Standalone PodCliques express partial-gang semantics via minCount == minAvailable, while PodCliques that
// belong to a PodCliqueScalingGroup are treated as all-or-nothing (minCount == count) under the POC
// assumption that PCSG.minAvailable == PCSG.replicas.
func (b *schedulerBackend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	pcsName := podGang.Labels[apicommon.LabelPartOfKey]
	if pcsName == "" {
		return nil
	}
	pcs := &grovecorev1alpha1.PodCliqueSet{}
	if err := b.client.Get(ctx, client.ObjectKey{Namespace: podGang.Namespace, Name: pcsName}, pcs); err != nil {
		return fmt.Errorf("failed to get PodCliqueSet %s/%s for PodGang %s: %w", podGang.Namespace, pcsName, podGang.Name, err)
	}

	desired, err := b.buildPrebuiltWorkload(pcs, podGang)
	if err != nil {
		return err
	}
	existing := newKueueWorkload(podGang.Namespace, podGang.Name)
	if err = b.client.Get(ctx, client.ObjectKeyFromObject(existing), existing); err != nil {
		if apierrors.IsNotFound(err) {
			if err = b.client.Create(ctx, desired); err != nil {
				return fmt.Errorf("failed to create prebuilt kueue Workload %s/%s: %w", podGang.Namespace, podGang.Name, err)
			}
			log.FromContext(ctx).Info("Created prebuilt Kueue Workload", "workload", client.ObjectKeyFromObject(desired))
			return nil
		}
		return fmt.Errorf("failed to get prebuilt kueue Workload %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}
	// Kueue Workload podSets are immutable once admitted, so the Workload is only created and not updated in this POC.
	return nil
}

func newKueueWorkload(namespace, name string) *unstructured.Unstructured {
	workload := &unstructured.Unstructured{}
	workload.SetGroupVersionKind(workloadGVK)
	workload.SetNamespace(namespace)
	workload.SetName(name)
	return workload
}

// buildPrebuiltWorkload constructs a prebuilt Kueue Workload from a PodGang, mapping each PodGroup to a Kueue
// podSet. The podSet name is the PodGroup FQN so scaling-group replicas of the same clique stay unique. count
// is the clique replicas; minCount is the PodGroup minReplicas for standalone cliques and is forced to count
// for scaling-group cliques (POC assumption PCSG.minAvailable == PCSG.replicas).
func (b *schedulerBackend) buildPrebuiltWorkload(pcs *grovecorev1alpha1.PodCliqueSet, podGang *groveschedulerv1alpha1.PodGang) (*unstructured.Unstructured, error) {
	queueName := pcs.Labels[queueNameLabel]
	if queueName == "" {
		return nil, fmt.Errorf("PodCliqueSet %s/%s must set label %q for Kueue queue selection", pcs.Namespace, pcs.Name, queueNameLabel)
	}

	// Kueue permits at most one podSet per Workload to set minCount (partial admission is limited to a single
	// podSet). Grove therefore only sets minCount on the first podSet whose minCount is strictly less than its
	// count; all other podSets (including every PodCliqueScalingGroup clique, which is all-or-nothing in this POC)
	// omit minCount, which Kueue defaults to count.
	minCountUsed := false
	podSets := make([]any, 0, len(podGang.Spec.PodGroups))
	for _, podGroup := range podGang.Spec.PodGroups {
		cliqueTemplate := findCliqueTemplateForPodGroup(pcs, podGroup.Name)
		if cliqueTemplate == nil {
			return nil, fmt.Errorf("no clique template found for PodGroup %q in PodCliqueSet %s/%s", podGroup.Name, pcs.Namespace, pcs.Name)
		}
		podSpec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&cliqueTemplate.Spec.PodSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to convert pod spec for clique %q: %w", cliqueTemplate.Name, err)
		}
		topologyKey := scheduler.RequiredTopologyKeyForPodGroup(podGang, podGroup.Name, b.config.RequiredTopologyKey)

		template := map[string]any{"spec": podSpec}
		if topologyKey != "" {
			template["metadata"] = map[string]any{
				"annotations": map[string]any{podSetRequiredTopologyAnnotation: topologyKey},
			}
		}

		count := int64(cliqueTemplate.Spec.Replicas)
		minCount := int64(podGroup.MinReplicas)

		podSet := map[string]any{
			"name":     podGroup.Name,
			"count":    count,
			"template": template,
		}
		if !isCliqueInScalingGroup(pcs, cliqueTemplate.Name) && minCount > 0 && minCount < count && !minCountUsed {
			podSet["minCount"] = minCount
			minCountUsed = true
		}
		if topologyKey != "" {
			podSet["topologyRequest"] = map[string]any{"required": topologyKey}
		}
		podSets = append(podSets, podSet)
	}

	workload := newKueueWorkload(podGang.Namespace, podGang.Name)
	if err := unstructured.SetNestedField(workload.Object, queueName, "spec", "queueName"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedSlice(workload.Object, podSets, "spec", "podSets"); err != nil {
		return nil, err
	}
	// Own the Workload by the PodGang so Kubernetes garbage collection removes it once the PodGang and all
	// member Pods (which Kueue adds as owners on admission) are gone.
	if err := controllerutil.SetOwnerReference(podGang, workload, b.scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference on prebuilt kueue Workload %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}
	return workload, nil
}

// isCliqueInScalingGroup reports whether the named clique is a member of any PodCliqueScalingGroup.
func isCliqueInScalingGroup(pcs *grovecorev1alpha1.PodCliqueSet, cliqueName string) bool {
	for _, config := range pcs.Spec.Template.PodCliqueScalingGroupConfigs {
		for _, name := range config.CliqueNames {
			if name == cliqueName {
				return true
			}
		}
	}
	return false
}

// findCliqueTemplateForPodGroup resolves the PodClique template whose role name matches the PodGroup FQN.
// PodGroup names are fully qualified (<pcs>-<replicaIndex>-<cliqueName>), so the longest suffix match wins.
func findCliqueTemplateForPodGroup(pcs *grovecorev1alpha1.PodCliqueSet, podGroupName string) *grovecorev1alpha1.PodCliqueTemplateSpec {
	var best *grovecorev1alpha1.PodCliqueTemplateSpec
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if podGroupName == cliqueTemplate.Name || strings.HasSuffix(podGroupName, "-"+cliqueTemplate.Name) {
			if best == nil || len(cliqueTemplate.Name) > len(best.Name) {
				best = cliqueTemplate
			}
		}
	}
	return best
}

func (b *schedulerBackend) PreparePod(pod *corev1.Pod) error {
	pod.Spec.SchedulerName = b.config.UnderlyingSchedulerName
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	if pod.Labels[podGroupNameLabel] == "" {
		pod.Labels[podGroupNameLabel] = pod.Labels[apicommon.LabelPodClique]
	}
	// Grove-managed pods are deliberately NOT marked as a Kueue "serving" pod group. A serving group is never
	// considered finished, so Kueue would never remove the kueue.x-k8s.io/managed finalizer on teardown (the
	// prebuilt Workload has no Kueue finalizer, so Workload deletion cannot trigger finalization either). As a
	// non-serving unretriable group, Kueue finalizes the Pods once they all terminate, letting them drain.
	if pod.Annotations[retriableInGroupAnnotation] == "" {
		pod.Annotations[retriableInGroupAnnotation] = "false"
	}
	if b.config.RequiredTopologyKey != "" && pod.Annotations[podSetRequiredTopologyAnnotation] == "" {
		pod.Annotations[podSetRequiredTopologyAnnotation] = b.config.RequiredTopologyKey
	}
	return nil
}

// ValidatePodCliqueSet rejects PodCliqueSets that cannot be represented as a valid Kueue Workload.
// Grove builds one prebuilt Kueue Workload per PodGang and maps each standalone PodClique with
// minAvailable < replicas to a Kueue podSet that sets minCount. Kueue permits at most one podSet per
// Workload to set minCount, so at most one standalone PodClique may declare partial-gang
// (minAvailable < replicas) semantics. PodCliques that belong to a PodCliqueScalingGroup are
// all-or-nothing and never set minCount, so they are excluded from this check.
func (b *schedulerBackend) ValidatePodCliqueSet(_ context.Context, pcs *grovecorev1alpha1.PodCliqueSet) error {
	var (
		partialGangCliques     []string
		partialGangPCSGCliques []string
	)
	for _, cliqueTemplate := range pcs.Spec.Template.Cliques {
		if cliqueTemplate == nil {
			continue
		}
		minAvailable := cliqueTemplate.Spec.MinAvailable
		// A nil minAvailable defaults to replicas, so it is a full gang.
		if minAvailable == nil {
			continue
		}
		isPartialGang := *minAvailable > 0 && *minAvailable < cliqueTemplate.Spec.Replicas
		if !isPartialGang {
			continue
		}
		if isCliqueInScalingGroup(pcs, cliqueTemplate.Name) {
			partialGangPCSGCliques = append(partialGangPCSGCliques, cliqueTemplate.Name)
			continue
		}
		partialGangCliques = append(partialGangCliques, cliqueTemplate.Name)
	}

	// PodCliques that belong to a PodCliqueScalingGroup are treated as all-or-nothing by the Kueue backend:
	// their podSets never set minCount, so a member clique's minAvailable < replicas would be silently
	// ignored. Reject it so an accepted PodCliqueSet behaves as specified.
	if len(partialGangPCSGCliques) > 0 {
		return fmt.Errorf("kueue backend requires PodCliques that are members of a PodCliqueScalingGroup to set minAvailable == replicas (all-or-nothing), but the following set minAvailable < replicas: %s", strings.Join(partialGangPCSGCliques, ", "))
	}

	// Grove maps each standalone PodClique with minAvailable < replicas to a Kueue podSet that sets minCount,
	// and Kueue permits minCount on at most one podSet per Workload.
	if len(partialGangCliques) > 1 {
		return fmt.Errorf("kueue backend allows at most one standalone PodClique with minAvailable < replicas because Kueue permits minCount on at most one podSet per Workload, but found %d: %s", len(partialGangCliques), strings.Join(partialGangCliques, ", "))
	}
	return nil
}

func defaultConfig() configv1alpha1.KueueSchedulerConfiguration {
	return configv1alpha1.KueueSchedulerConfiguration{
		UnderlyingSchedulerName: string(configv1alpha1.SchedulerNameKube),
	}
}

func withDefaults(cfg configv1alpha1.KueueSchedulerConfiguration) configv1alpha1.KueueSchedulerConfiguration {
	defaults := defaultConfig()
	if cfg.UnderlyingSchedulerName == "" {
		cfg.UnderlyingSchedulerName = defaults.UnderlyingSchedulerName
	}
	return cfg
}
