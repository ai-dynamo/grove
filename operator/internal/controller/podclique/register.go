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

package podclique

import (
	"context"
	"strings"

	"github.com/ai-dynamo/grove/operator/api/common/constants"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/controller/common/component/utils"
	grovectrlutils "github.com/ai-dynamo/grove/operator/internal/controller/utils"
	"github.com/ai-dynamo/grove/operator/internal/expect"
	"github.com/ai-dynamo/grove/operator/internal/utils"
	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
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
					predicate.GenerationChangedPredicate{},
					managedPodCliquePredicate(),
				),
			),
		).
		Watches(
			&corev1.Pod{},
			newPodDeleteExpectationsHandler(r.expectationsStore, mgr),
			builder.WithPredicates(podPredicate()),
		).
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
		Complete(r)
}

// podDeleteExpectationsHandler removes UIDs from create expectations on Pod Delete events.
// When a pod is deleted (e.g. kubectl delete), the informer receives a Delete event. We remove
// the pod's UID from uidsToAdd so the controller can recreate it. This distinguishes "pod deleted"
// from "informer slow" without TTL or API bypass.
type podDeleteExpectationsHandler struct {
	expectationsStore *expect.ExpectationsStore
	inner             handler.EventHandler
}

func newPodDeleteExpectationsHandler(expectationsStore *expect.ExpectationsStore, mgr ctrl.Manager) handler.EventHandler {
	return &podDeleteExpectationsHandler{
		expectationsStore: expectationsStore,
		inner:             handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &grovecorev1alpha1.PodClique{}, handler.OnlyControllerOwner()),
	}
}

func (h *podDeleteExpectationsHandler) Create(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.inner.Create(ctx, e, q)
}

func (h *podDeleteExpectationsHandler) Update(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.inner.Update(ctx, e, q)
}

func (h *podDeleteExpectationsHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if pod, ok := e.Object.(*corev1.Pod); ok && isManagedPod(pod) {
		if ownerRef := k8sutils.FindOwnerRefByKind(pod.OwnerReferences, constants.KindPodClique); ownerRef != nil {
			pclqObjMeta := metav1.ObjectMeta{Namespace: pod.Namespace, Name: ownerRef.Name}
			if controlleeKey, err := expect.ControlleeKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: pclqObjMeta}); err == nil {
				logger := ctrllogger.FromContext(ctx).WithName(controllerName).WithName("pod-delete-handler")
				h.expectationsStore.ObserveCreationCancellation(logger, controlleeKey, pod.UID)
			}
		}
	}
	h.inner.Delete(ctx, e, q)
}

func (h *podDeleteExpectationsHandler) Generic(ctx context.Context, e event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	h.inner.Generic(ctx, e, q)
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
func podPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return false },
		DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
			deletedPod, ok := deleteEvent.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			return isManagedPod(deletedPod)
		},
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return isManagedPod(updateEvent.ObjectOld) && !hasPodSpecChanged(updateEvent) && hasPodStatusChanged(updateEvent)
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
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
	return hasReadyConditionChanged(oldPod.Status.Conditions, newPod.Status.Conditions) ||
		hasLastTerminationStateChanged(oldPod.Status.InitContainerStatuses, newPod.Status.InitContainerStatuses) ||
		hasLastTerminationStateChanged(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses) ||
		hasStartedAndReadyChangedForAnyContainer(oldPod.Status.ContainerStatuses, newPod.Status.ContainerStatuses)
}

// hasReadyConditionChanged checks if the Pod's Ready condition status has transitioned
func hasReadyConditionChanged(oldPodConditions, newPodConditions []corev1.PodCondition) bool {
	getReadyCondition := func(podConditions []corev1.PodCondition) (corev1.PodCondition, bool) {
		return lo.Find(podConditions, func(condition corev1.PodCondition) bool {
			return condition.Type == corev1.PodReady
		})
	}
	oldPodReadyCondition, oldOk := getReadyCondition(oldPodConditions)
	newPodReadyCondition, newOk := getReadyCondition(newPodConditions)
	oldPodReady := oldOk && oldPodReadyCondition.Status == corev1.ConditionTrue
	newPodReady := newOk && newPodReadyCondition.Status == corev1.ConditionTrue
	return oldPodReady != newPodReady
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
			matchingNewContainerStatus.Started != oldContainerStatus.Started {
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
			return oldPCS.Status.CurrentGenerationHash != newPCS.Status.CurrentGenerationHash
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
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
			return oldPCSG.Status.CurrentPodCliqueSetGenerationHash != nil && newPCSG.Status.RollingUpdateProgress != nil &&
				*oldPCSG.Status.CurrentPodCliqueSetGenerationHash != newPCSG.Status.RollingUpdateProgress.PodCliqueSetGenerationHash
		},
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
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

// podGangPredicate allows all PodGang create and update events to trigger PodClique reconciliation
func podGangPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return true },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		UpdateFunc:  func(_ event.UpdateEvent) bool { return true },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}
}

// isManagedPod checks if a Pod is managed by Grove and owned by a PodClique
func isManagedPod(obj client.Object) bool {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return false
	}
	return grovectrlutils.HasExpectedOwner(constants.KindPodClique, pod.OwnerReferences) && grovectrlutils.IsManagedByGrove(pod.GetLabels())
}
