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

package podclique

import (
	"context"
	"maps"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllogger "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	controllerName = "podclique-controller"
)

// RegisterWithManager registers the PodClique controller with the given controller manager.
func (r *Reconciler) RegisterWithManager(mgr ctrl.Manager) error {
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: *r.config.ConcurrentSyncs,
		}).
		For(&grovecorev1alpha1.PodClique{},
			builder.WithPredicates(
				predicate.And(
					managedPodCliquePredicate(),
					predicate.Or(
						predicate.GenerationChangedPredicate{},
						predicate.AnnotationChangedPredicate{},
					),
				),
			),
		).
		Owns(&corev1.Pod{}, builder.WithPredicates(r.podPredicate())).
		Watches(
			&grovecorev1alpha1.PodCliqueSet{},
			handler.EnqueueRequestsFromMapFunc(mapPodCliqueSetToPCLQs()),
			builder.WithPredicates(podCliqueSetPredicate()),
		).
		Watches(
			&grovecorev1alpha1.PodCliqueScalingGroup{},
			handler.EnqueueRequestsFromMapFunc(mapPodCliqueScalingGroupToPCLQs()),
			builder.WithPredicates(podCliqueScalingGroupPredicate()),
		).
		Watches(
			&groveschedulerv1alpha1.PodGang{},
			handler.EnqueueRequestsFromMapFunc(mapPodGangToPCLQs()),
			builder.WithPredicates(podGangPredicate()),
		).
		Watches(
			&grovecorev1alpha1.PodGangMap{},
			handler.EnqueueRequestsFromMapFunc(mapPodGangMapToPCLQs()),
			builder.WithPredicates(podGangMapPredicate()),
		).
		Complete(r)
}

// managedPodCliquePredicate filters PodClique events to only process managed PodCliques owned by expected resources
func managedPodCliquePredicate() predicate.Predicate {
	expectedOwnerKinds := []string{constants.KindPodCliqueScalingGroup, constants.KindPodCliqueSet}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return grovectrlutils.IsManagedPodClique(e.Object, expectedOwnerKinds...)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return grovectrlutils.IsManagedPodClique(e.Object, expectedOwnerKinds...)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return grovectrlutils.IsManagedPodClique(e.ObjectOld, expectedOwnerKinds...)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// podPredicate returns a predicate that filters out pods that are not managed by Grove.
// On Delete for a managed pod it calls ObserveDeletions so the controller can recreate the pod (issue #457).
func (r *Reconciler) podPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			deletedPod, ok := deleteEvent.Object.(*corev1.Pod)
			if !ok || !isManagedPod(deletedPod) {
				return false
			}
			r.recordPodDeletionInExpectations(deletedPod)
			return true
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return isManagedPod(updateEvent.ObjectOld) && !hasPodSpecChanged(updateEvent) && (hasPodStatusChanged(updateEvent) || isMarkedForDeletion(updateEvent))
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// recordPodDeletionInExpectations records the pod's deletion in the expectations store for its owning PodClique so the controller can recreate the pod (issue #457).
func (r *Reconciler) recordPodDeletionInExpectations(pod *corev1.Pod) {
	pclqOwnerRef := k8sutils.FindOwnerRefByKind(pod.OwnerReferences, constants.KindPodClique)
	if pclqOwnerRef == nil {
		return // nothing to do
	}
	logger := ctrllogger.Log.WithName(controllerName)
	podGangName, ok := pod.Labels[apicommon.LabelPodGang]
	if !ok {
		// A pod not yet labeled for a PodGang has no PodGang-scoped delete expectation recorded, so
		// there is nothing to lower.
		return
	}
	pclqObjMeta := metav1.ObjectMeta{Namespace: pod.Namespace, Name: pclqOwnerRef.Name}
	if err := componentutils.ObservePodDeletion(logger, r.expectationsStore, pclqObjMeta, podGangName, pod.UID); err != nil {
		logger.Error(err, "cannot observe deletion, unable to build the PodGang-scoped expectations key", "pclqNamespace", pclqObjMeta.Namespace, "pclqName", pclqObjMeta.Name, "podGang", podGangName)
	}
}

// hasPodSpecChanged checks if the Pod's spec has changed by comparing generation values
func hasPodSpecChanged(updateEvent event.UpdateEvent) bool {
	return updateEvent.ObjectOld.GetGeneration() != updateEvent.ObjectNew.GetGeneration()
}

// hasPodStatusChanged determines if relevant Pod status fields have changed that require reconciliation
func hasPodStatusChanged(updateEvent event.UpdateEvent) bool {
	oldPod, oldOk := updateEvent.ObjectOld.(*corev1.Pod)
	newPod, newOk := updateEvent.ObjectNew.(*corev1.Pod)
	if !oldOk || !newOk {
		return false
	}
	return hasConditionStatusChanged(oldPod.Status.Conditions, newPod.Status.Conditions, corev1.PodReady) ||
		hasConditionStatusChanged(oldPod.Status.Conditions, newPod.Status.Conditions, corev1.PodScheduled) ||
		hasLastTerminationStateChanged(oldPod.Status.InitContainerStatuses, newPod.Status.InitContainerStatuses) ||
		hasLastTerminationStateChanged(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses) ||
		hasStartedAndReadyChangedForAnyContainer(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses)
}

// hasConditionStatusChanged reports whether the given Pod condition transitioned into or out of the
// True state between the old and new conditions. A missing condition counts as not True.
func hasConditionStatusChanged(oldPodConditions, newPodConditions []corev1.PodCondition, condType corev1.PodConditionType) bool {
	isTrue := func(podConditions []corev1.PodCondition) bool {
		cond, ok := lo.Find(podConditions, func(condition corev1.PodCondition) bool {
			return condition.Type == condType
		})
		return ok && cond.Status == corev1.ConditionTrue
	}
	return isTrue(oldPodConditions) != isTrue(newPodConditions)
}

// hasLastTerminationStateChanged detects changes in container termination states with non-zero exit codes
func hasLastTerminationStateChanged(oldContainerStatuses []corev1.ContainerStatus, newContainerStatuses []corev1.ContainerStatus) bool {
	oldErroneousContainerStatus := k8sutils.GetContainerStatusIfTerminatedErroneously(oldContainerStatuses)
	newErroneousContainerStatus := k8sutils.GetContainerStatusIfTerminatedErroneously(newContainerStatuses)
	return utils.OnlyOneIsNil(oldErroneousContainerStatus, newErroneousContainerStatus)
}

// hasStartedAndReadyChangedForAnyContainer checks if any container's Started or Ready status has changed
func hasStartedAndReadyChangedForAnyContainer(oldContainerStatuses []corev1.ContainerStatus, newContainerStatuses []corev1.ContainerStatus) bool {
	for _, oldContainerStatus := range oldContainerStatuses {
		matchingNewContainerStatus, ok := lo.Find(newContainerStatuses, func(containerStatus corev1.ContainerStatus) bool {
			return oldContainerStatus.Name == containerStatus.Name
		})
		if !ok {
			return true
		}
		if matchingNewContainerStatus.Ready != oldContainerStatus.Ready ||
			!ptr.Equal(matchingNewContainerStatus.Started, oldContainerStatus.Started) {
			return true
		}
	}
	return false
}

// mapPodCliqueSetToPCLQs maps a PodCliqueSet to one or more reconcile.Request(s) to its constituent standalone Podcliques.
// These events are needed to keep the PodClique.Status.CurrentPodCliqueSetGenerationHash in sync with the PodCliqueSet.
func mapPodCliqueSetToPCLQs() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pcs, ok := obj.(*grovecorev1alpha1.PodCliqueSet)
		if !ok {
			return nil
		}
		return lo.Map(componentutils.GetPodCliqueFQNsForPCSNotInPCSG(pcs), func(pclqFQN string, _ int) reconcile.Request {
			return reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: pcs.Namespace,
				Name:      pclqFQN,
			}}
		})
	}
}

// podCliqueSetPredicate filters PodCliqueSet events to only process generation hash changes
func podCliqueSetPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(event event.UpdateEvent) bool {
			oldPCS, okOld := event.ObjectOld.(*grovecorev1alpha1.PodCliqueSet)
			newPCS, okNew := event.ObjectNew.(*grovecorev1alpha1.PodCliqueSet)
			if !okOld || !okNew {
				return false
			}
			return !stringPointersEqual(oldPCS.Status.CurrentGenerationHash, newPCS.Status.CurrentGenerationHash) ||
				pcsCurrentlyUpdatingReplicaChanged(oldPCS.Status.UpdateProgress, newPCS.Status.UpdateProgress)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// pcsCurrentlyUpdatingReplicaChanged reports whether the replica currently being updated has changed between the old and new PodCliqueSet update progress.
func pcsCurrentlyUpdatingReplicaChanged(oldProgress, newProgress *grovecorev1alpha1.PodCliqueSetUpdateProgress) bool {
	oldReplicaIndex, oldOK := currentPCSReplicaInUpdate(oldProgress)
	newReplicaIndex, newOK := currentPCSReplicaInUpdate(newProgress)
	if oldOK != newOK {
		return true
	}
	return oldOK && oldReplicaIndex != newReplicaIndex
}

// currentPCSReplicaInUpdate returns the replica index of the PodCliqueSet replica currently being updated, if any.
func currentPCSReplicaInUpdate(progress *grovecorev1alpha1.PodCliqueSetUpdateProgress) (int32, bool) {
	if progress == nil || len(progress.CurrentlyUpdating) == 0 {
		return 0, false
	}
	return progress.CurrentlyUpdating[0].ReplicaIndex, true
}

// mapPodCliqueScalingGroupToPCLQs maps a PodCliqueScalingGroup to one or more reconcile.Request(s) to its constituent PodCliques.
// These events are needed to keep the PodClique.Status.CurrentPodCliqueSetGenerationHash in sync with the PodCliqueSet.
func mapPodCliqueScalingGroupToPCLQs() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pcsg, ok := obj.(*grovecorev1alpha1.PodCliqueScalingGroup)
		if !ok {
			return nil
		}
		return lo.Map(componentutils.GetPodCliqueFQNsForPCSG(pcsg), func(pclqFQN string, _ int) reconcile.Request {
			return reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: pcsg.Namespace,
				Name:      pclqFQN,
			}}
		})
	}
}

// podCliqueScalingGroupPredicate filters PodCliqueScalingGroup events to only process rolling update changes
func podCliqueScalingGroupPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(event event.UpdateEvent) bool {
			oldPCSG, okOld := event.ObjectOld.(*grovecorev1alpha1.PodCliqueScalingGroup)
			newPCSG, okNew := event.ObjectNew.(*grovecorev1alpha1.PodCliqueScalingGroup)
			if !okOld || !okNew {
				return false
			}
			return !stringPointersEqual(oldPCSG.Status.CurrentPodCliqueSetGenerationHash, newPCSG.Status.CurrentPodCliqueSetGenerationHash) ||
				pcsgUpdateTargetGenerationChanged(oldPCSG.Status.UpdateProgress, newPCSG.Status.UpdateProgress)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// pcsgUpdateTargetGenerationChanged reports whether the PodCliqueScalingGroup update's target PodCliqueSet generation hash has changed between the old and new update progress.
func pcsgUpdateTargetGenerationChanged(oldProgress, newProgress *grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress) bool {
	oldTarget, oldOK := pcsgUpdateTargetGeneration(oldProgress)
	newTarget, newOK := pcsgUpdateTargetGeneration(newProgress)
	if oldOK != newOK {
		return true
	}
	return oldOK && oldTarget != newTarget
}

// pcsgUpdateTargetGeneration returns the target PodCliqueSet generation hash for an in-progress PodCliqueScalingGroup update, if any.
func pcsgUpdateTargetGeneration(progress *grovecorev1alpha1.PodCliqueScalingGroupUpdateProgress) (string, bool) {
	if progress == nil {
		return "", false
	}
	return progress.PodCliqueSetGenerationHash, true
}

func stringPointersEqual(oldValue, newValue *string) bool {
	if oldValue == nil || newValue == nil {
		return oldValue == newValue
	}
	return *oldValue == *newValue
}

// mapPodGangToPCLQs maps a PodGang to one or more reconcile.Request(s) for its constituent PodClique's.
func mapPodGangToPCLQs() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		podGang, ok := obj.(*groveschedulerv1alpha1.PodGang)
		if !ok {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(podGang.Spec.PodGroups))
		for _, podGroup := range podGang.Spec.PodGroups {
			if len(podGroup.PodReferences) == 0 {
				continue
			}
			podRefName := podGroup.PodReferences[0].Name
			pclqFQN := extractPCLQNameFromPodName(podRefName)
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pclqFQN, Namespace: podGang.Namespace},
			})
		}
		return requests
	}
}

// extractPCLQNameFromPodName extracts the PodClique name from a Pod name by removing the replica index suffix
func extractPCLQNameFromPodName(podName string) string {
	endIndex := strings.LastIndex(podName, "-")
	return podName[:endIndex]
}

// podGangPredicate filters PodGang events to trigger on initialization and spec updates
func podGangPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(_ event.DeleteEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPG, okOld := e.ObjectOld.(*groveschedulerv1alpha1.PodGang)
			newPG, okNew := e.ObjectNew.(*groveschedulerv1alpha1.PodGang)
			if !okOld || !okNew {
				return false
			}

			// Trigger when PodGang transitions to Initialized=True
			oldInitialized := isPodGangInitialized(e.ObjectOld)
			newInitialized := isPodGangInitialized(e.ObjectNew)
			if !oldInitialized && newInitialized {
				return true
			}

			// Also trigger when PodGang spec changes (e.g., scale out/in adds/removes pod references)
			// This ensures scheduling gates are removed from newly added pods
			// Check if metadata.generation changed (Kubernetes increments this on spec changes)
			if newInitialized && oldPG.GetGeneration() != newPG.GetGeneration() {
				return true
			}

			return false
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// mapPodGangMapToPCLQs maps a PodGangMap to reconcile.Request(s) for the standalone PodCliques whose
// per-PodGang distribution it carries. A coherent-update sub-step can redistribute a standalone
// PodClique across anchor entries without changing its total, and no PodClique, PodGang or
// PodCliqueSet watch observes that move, so the PodGangMap is the only signal. The requests cover
// every standalone PodClique named across the entries.
func mapPodGangMapToPCLQs() handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		pgm, ok := obj.(*grovecorev1alpha1.PodGangMap)
		if !ok {
			return nil
		}
		pcsName := componentutils.GetPodCliqueSetName(pgm.ObjectMeta)
		rnr := apicommon.ResourceNameReplica{Name: pcsName, Replica: int(pgm.Spec.PodCliqueSetReplicaIndex)}
		cliqueNames := make(map[string]struct{})
		for _, entry := range pgm.Spec.Entries {
			for cliqueName := range entry.PodCliques {
				cliqueNames[cliqueName] = struct{}{}
			}
		}
		requests := make([]reconcile.Request, 0, len(cliqueNames))
		for cliqueName := range cliqueNames {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: pgm.Namespace,
				Name:      apicommon.GeneratePodCliqueName(rnr, cliqueName),
			}})
		}
		return requests
	}
}

// podGangMapPredicate triggers the PodClique controller on a PodGangMap create and on an update that
// changes the standalone PodClique placement. It does not fire on other spec churn such as
// PodCliqueScalingGroup replica-index moves, which the PodClique controller does not consume.
func podGangMapPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPGM, okOld := e.ObjectOld.(*grovecorev1alpha1.PodGangMap)
			newPGM, okNew := e.ObjectNew.(*grovecorev1alpha1.PodGangMap)
			if !okOld || !okNew {
				return false
			}
			return standaloneDistributionChanged(oldPGM, newPGM)
		},
	}
}

// standaloneDistributionChanged reports whether the standalone PodClique counts differ between the
// two PodGangMaps. It compares the maps returned by standaloneDistribution. Because those maps key a
// count by both the entry epoch and the PodClique name, moving a PodClique from one entry to another
// with the same total still counts as a change.
func standaloneDistributionChanged(oldPGM, newPGM *grovecorev1alpha1.PodGangMap) bool {
	return !maps.Equal(standaloneDistribution(oldPGM), standaloneDistribution(newPGM))
}

// standaloneDistribution returns the standalone PodClique count of every entry. The key of each count
// is the entry epoch and the PodClique name joined together, so the same PodClique under two
// different epochs yields two distinct keys. Entries with no standalone PodCliques contribute no keys.
func standaloneDistribution(pgm *grovecorev1alpha1.PodGangMap) map[string]int32 {
	dist := make(map[string]int32)
	for _, entry := range pgm.Spec.Entries {
		for cliqueName, count := range entry.PodCliques {
			dist[entry.Epoch+"/"+cliqueName] = count
		}
	}
	return dist
}

// isPodGangInitialized checks if a PodGang has Initialized condition set to True.
func isPodGangInitialized(obj client.Object) bool {
	podGang, ok := obj.(*groveschedulerv1alpha1.PodGang)
	if !ok {
		return false
	}

	// Check if Initialized condition is True
	return meta.IsStatusConditionTrue(podGang.Status.Conditions, string(groveschedulerv1alpha1.PodGangConditionTypeInitialized))
}

// isManagedPod checks if a Pod is managed by Grove and owned by a PodClique
func isManagedPod(obj client.Object) bool {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return false
	}
	return grovectrlutils.HasExpectedOwner(constants.KindPodClique, pod.OwnerReferences) && grovectrlutils.IsManagedByGrove(pod.GetLabels())
}

func isMarkedForDeletion(updateEvent event.UpdateEvent) bool {
	oldPod, oldOk := updateEvent.ObjectOld.(*corev1.Pod)
	newPod, newOk := updateEvent.ObjectNew.(*corev1.Pod)
	if !oldOk || !newOk {
		return false
	}

	return oldPod.DeletionTimestamp == nil && newPod.DeletionTimestamp != nil
}
