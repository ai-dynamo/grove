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

package kubernetes

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// constants for extension PodConditionTypes.
const (
	// PodHasAtleastOneContainerWithNonZeroExitCode is a custom corev1.PodConditionType that represents a Pod which is NotReady and has at least one of its containers that
	// have exited with an exit code != 0. This condition type will be used towards setting of MinAvailableBreached condition
	// on the owner resource of a Pod.
	PodHasAtleastOneContainerWithNonZeroExitCode corev1.PodConditionType = "PodHasAtleastOneContainerWithNonZeroExitCode"
	// ScheduleGatedPod is a custom corev1.PodConditionType that represents a Pod which has one or more scheduling gates set.
	ScheduleGatedPod corev1.PodConditionType = "ScheduleGatedPod"
	// TerminatingPod is a custom corev1.PodConditionType that represents that this Pod has deletionTimestamp set on it.
	TerminatingPod corev1.PodConditionType = "TerminatingPod"
	// PodStartedButNotReady is a custom corev1.PodConditionType that represents a Pod that has at least container whose
	// status has started=true and ready=false.
	// NOTE: We are currently NOT supporting any thresholds/initialDelays as defined in container probes. Support for that might come later.
	PodStartedButNotReady corev1.PodConditionType = "PodStartedButNotReady"
)

// CategorizePodsByConditionType groups pods by their pod condition. Three condition types a.k.a. categories are of interest:
// 1. Ready pods, 2. ScheduleGated pods and 3. Pods that are NotReady and have at least one container with non-zero exit code.
func CategorizePodsByConditionType(logger logr.Logger, pods []*corev1.Pod) map[corev1.PodConditionType][]*corev1.Pod {
	podCategories := make(map[corev1.PodConditionType][]*corev1.Pod)
	for _, pod := range pods {
		// Check if the pod has been scheduled or is schedule gated.
		if IsPodScheduled(pod) {
			podCategories[corev1.PodScheduled] = append(podCategories[corev1.PodScheduled], pod)
		} else if IsPodScheduleGated(pod) {
			podCategories[ScheduleGatedPod] = append(podCategories[ScheduleGatedPod], pod)
		}
		// Check if the pod has a deletion timestamp set, which indicates that the pod is terminating.
		if IsResourceTerminating(pod.ObjectMeta) {
			podCategories[TerminatingPod] = append(podCategories[TerminatingPod], pod)
		}
		// check if the pod is ready, has at least one container with a non-zero exit code or has started but not ready containers.
		if IsPodReady(pod) {
			podCategories[corev1.PodReady] = append(podCategories[corev1.PodReady], pod)
		} else if HasAnyContainerExitedErroneously(logger, pod) {
			podCategories[PodHasAtleastOneContainerWithNonZeroExitCode] = append(podCategories[PodHasAtleastOneContainerWithNonZeroExitCode], pod)
		} else if HasAnyStartedButNotReadyContainer(pod) {
			podCategories[PodStartedButNotReady] = append(podCategories[PodStartedButNotReady], pod)
		}
	}
	return podCategories
}

// IsPodScheduleGated checks if there are scheduling gates added to the Pod.
func IsPodScheduleGated(pod *corev1.Pod) bool {
	scheduledCond, ok := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodScheduled
	})
	if !ok {
		return false
	}
	return scheduledCond.Status == corev1.ConditionFalse && scheduledCond.Reason == corev1.PodReasonSchedulingGated
}

// IsPodScheduled checks if the Pod has been scheduled by the scheduler.
func IsPodScheduled(pod *corev1.Pod) bool {
	scheduledCond, ok := lo.Find(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
		return cond.Type == corev1.PodScheduled
	})
	if !ok {
		return false
	}
	return scheduledCond.Status == corev1.ConditionTrue
}

// HasAnyContainerExitedErroneously checks if any container has been terminated with a non-zero exit code in a Pod.
func HasAnyContainerExitedErroneously(logger logr.Logger, pod *corev1.Pod) bool {
	podObjKey := client.ObjectKeyFromObject(pod)
	// check init container statuses
	erroneousInitContainerStatus := GetContainerStatusIfTerminatedErroneously(pod.Status.InitContainerStatuses)
	if erroneousInitContainerStatus != nil {
		logTerminatedErroneouslyPodContainerStatus(logger, podObjKey, erroneousInitContainerStatus)
		return true
	}
	// check non-init container statuses
	erroneousContainerStatus := GetContainerStatusIfTerminatedErroneously(pod.Status.ContainerStatuses)
	if erroneousContainerStatus != nil {
		logTerminatedErroneouslyPodContainerStatus(logger, podObjKey, erroneousContainerStatus)
		return true
	}
	return false
}

// HasAnyStartedButNotReadyContainer checks if there is at least one container which has started but not ready.
func HasAnyStartedButNotReadyContainer(pod *corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Started != nil && *containerStatus.Started && !containerStatus.Ready {
			return true
		}
	}
	return false
}

// HasAnyContainerNotStarted checks if any container has not yet passed its startup probe.
// In Kubernetes, Started remains false (or nil) until the startup probe succeeds.
// This distinguishes pods in the startup probe phase from those that have started but are not ready.
// Returns false if there are no container statuses (the pod would be uncategorized by the caller).
func HasAnyContainerNotStarted(pod *corev1.Pod) bool {
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Started == nil || !*containerStatus.Started {
			return true
		}
	}
	return false
}

// ComputeHash computes a hash given one or more corev1.PodTemplateSpec.
//
// Each template is canonicalized before being fed into the underlying fnv
// hash. Canonicalization sorts every Kubernetes API slice that is declared
// +listType=map AND whose order does not influence runtime behavior, so
// that two PodTemplateSpecs which represent the same desired state but were
// serialized in different orders by an upstream controller (e.g. emitted
// from non-deterministic Go map iteration) hash to the same value.
//
// Sorted (+listType=map, order-independent at runtime):
//   - PodSpec.Containers (key: name)
//   - PodSpec.Volumes (key: name)
//   - PodSpec.ImagePullSecrets (key: name)
//   - PodSpec.HostAliases (key: ip)
//   - PodSpec.TopologySpreadConstraints (key: topologyKey, whenUnsatisfiable)
//   - PodSpec.ResourceClaims (key: name)
//   - PodSpec.EphemeralContainers (key: name)
//   - PodSpec.SchedulingGates (key: name)
//   - Container.Ports (key: containerPort, protocol) — for regular, init,
//     and ephemeral containers
//   - Container.VolumeMounts (key: mountPath) — same scope
//   - Container.VolumeDevices (key: devicePath) — same scope
//   - Container.Resources.Claims (key: name) — same scope
//
// Intentionally NOT sorted (slice order is part of the desired state):
//   - PodSpec.InitContainers — +listType=map but the field doc states init
//     containers "are run in the order they appear in this list", so a
//     reorder is a real spec change.
//   - Container.Env — +listType=map but order participates in $(VAR)
//     substitution; reordering can change runtime values.
//   - Container.EnvFrom — +listType=atomic; the API treats the slice as a
//     single value and its order is part of that value.
//   - PodSpec.Tolerations — +listType=atomic.
//   - Container.Args / Container.Command — ordered argument lists.
//   - Container.ResizePolicy — +listType=atomic.
//
// Anything not listed is left untouched: either it is a scalar/struct field
// (no slice reorder possible), it is +listType=set (already a set, no key),
// or it does not appear in the PodSpec types we hash here.
func ComputeHash(podTemplateSpecs ...*corev1.PodTemplateSpec) string {
	podTemplateSpecHasher := fnv.New64a()
	podTemplateSpecHasher.Reset()
	for _, podTemplateSpec := range podTemplateSpecs {
		canonical := canonicalizePodTemplateSpecForHashing(podTemplateSpec)
		_, _ = fmt.Fprintf(podTemplateSpecHasher, "%v", dump.ForHash(canonical))
	}
	return rand.SafeEncodeString(fmt.Sprint(podTemplateSpecHasher.Sum64()))
}

// canonicalizePodTemplateSpecForHashing returns a deep-copied PodTemplateSpec
// with every order-independent +listType=map slice sorted by its API key,
// so that two specs representing the same desired state always produce the
// same byte-for-byte serialization. See the doc on ComputeHash for the full
// list of slices canonicalized and the rationale for the slices left alone.
func canonicalizePodTemplateSpecForHashing(in *corev1.PodTemplateSpec) *corev1.PodTemplateSpec {
	if in == nil {
		return nil
	}
	out := in.DeepCopy()
	canonicalizePodSpecForHashing(&out.Spec)
	return out
}

func canonicalizePodSpecForHashing(spec *corev1.PodSpec) {
	// Containers run in parallel and are +listType=map keyed by name —
	// safe to sort. After sorting the slice, also canonicalize the
	// per-container map-list fields (ports, volumeMounts, volumeDevices,
	// resources.claims).
	sort.SliceStable(spec.Containers, func(i, j int) bool {
		return spec.Containers[i].Name < spec.Containers[j].Name
	})
	for i := range spec.Containers {
		canonicalizeContainerForHashing(&spec.Containers[i])
	}

	// InitContainers slice itself is NOT reordered — execution sequence
	// matters — but the per-container map-list fields inside each init
	// container ARE order-independent and get canonicalized.
	for i := range spec.InitContainers {
		canonicalizeContainerForHashing(&spec.InitContainers[i])
	}

	// EphemeralContainers are +listType=map keyed by name; ordering has no
	// runtime meaning. Sort them and canonicalize their inner slices via
	// the embedded EphemeralContainerCommon (same shape as Container).
	sort.SliceStable(spec.EphemeralContainers, func(i, j int) bool {
		return spec.EphemeralContainers[i].Name < spec.EphemeralContainers[j].Name
	})
	for i := range spec.EphemeralContainers {
		canonicalizeEphemeralContainerForHashing(&spec.EphemeralContainers[i])
	}

	sort.SliceStable(spec.Volumes, func(i, j int) bool {
		return spec.Volumes[i].Name < spec.Volumes[j].Name
	})
	sort.SliceStable(spec.ImagePullSecrets, func(i, j int) bool {
		return spec.ImagePullSecrets[i].Name < spec.ImagePullSecrets[j].Name
	})
	sort.SliceStable(spec.HostAliases, func(i, j int) bool {
		return spec.HostAliases[i].IP < spec.HostAliases[j].IP
	})
	sort.SliceStable(spec.TopologySpreadConstraints, func(i, j int) bool {
		a, b := spec.TopologySpreadConstraints[i], spec.TopologySpreadConstraints[j]
		if a.TopologyKey != b.TopologyKey {
			return a.TopologyKey < b.TopologyKey
		}
		return string(a.WhenUnsatisfiable) < string(b.WhenUnsatisfiable)
	})
	sort.SliceStable(spec.ResourceClaims, func(i, j int) bool {
		return spec.ResourceClaims[i].Name < spec.ResourceClaims[j].Name
	})
	sort.SliceStable(spec.SchedulingGates, func(i, j int) bool {
		return spec.SchedulingGates[i].Name < spec.SchedulingGates[j].Name
	})
}

// canonicalizeContainerForHashing sorts the order-independent +listType=map
// slices inside a single Container in place: Ports, VolumeMounts,
// VolumeDevices, Resources.Claims. Order-significant slices (Env, EnvFrom,
// Args, Command) are intentionally left untouched.
func canonicalizeContainerForHashing(c *corev1.Container) {
	canonicalizePortsForHashing(c.Ports)
	canonicalizeVolumeMountsForHashing(c.VolumeMounts)
	canonicalizeVolumeDevicesForHashing(c.VolumeDevices)
	canonicalizeResourceClaimsForHashing(c.Resources.Claims)
}

// canonicalizeEphemeralContainerForHashing applies the same canonicalization
// as canonicalizeContainerForHashing to the embedded EphemeralContainerCommon
// inside an EphemeralContainer.
func canonicalizeEphemeralContainerForHashing(ec *corev1.EphemeralContainer) {
	canonicalizePortsForHashing(ec.Ports)
	canonicalizeVolumeMountsForHashing(ec.VolumeMounts)
	canonicalizeVolumeDevicesForHashing(ec.VolumeDevices)
	canonicalizeResourceClaimsForHashing(ec.Resources.Claims)
}

// canonicalizePortsForHashing sorts ContainerPort entries by their composite
// API key (containerPort, protocol). Container.Ports is +listType=map.
func canonicalizePortsForHashing(ports []corev1.ContainerPort) {
	sort.SliceStable(ports, func(p, q int) bool {
		a, b := ports[p], ports[q]
		if a.ContainerPort != b.ContainerPort {
			return a.ContainerPort < b.ContainerPort
		}
		return string(a.Protocol) < string(b.Protocol)
	})
}

// canonicalizeVolumeMountsForHashing sorts VolumeMount entries by mountPath.
// Container.VolumeMounts is +listType=map +listMapKey=mountPath; order has
// no runtime meaning.
func canonicalizeVolumeMountsForHashing(mounts []corev1.VolumeMount) {
	sort.SliceStable(mounts, func(i, j int) bool {
		return mounts[i].MountPath < mounts[j].MountPath
	})
}

// canonicalizeVolumeDevicesForHashing sorts VolumeDevice entries by
// devicePath. Container.VolumeDevices is +listType=map +listMapKey=devicePath.
func canonicalizeVolumeDevicesForHashing(devices []corev1.VolumeDevice) {
	sort.SliceStable(devices, func(i, j int) bool {
		return devices[i].DevicePath < devices[j].DevicePath
	})
}

// canonicalizeResourceClaimsForHashing sorts ResourceClaim entries by name.
// Container.Resources.Claims is +listType=map +listMapKey=name.
func canonicalizeResourceClaimsForHashing(claims []corev1.ResourceClaim) {
	sort.SliceStable(claims, func(i, j int) bool {
		return claims[i].Name < claims[j].Name
	})
}

// GetContainerStatusIfTerminatedErroneously gets the first occurrence of corev1.ContainerStatus (across init, sidecar and main containers)
// that has a non-zero LastTerminationState.Terminated.ExitCode. The reason to choose `containerStatus.LastTerminationState` instead of `containerStatus.State` is that
// the `containerStatus.State` oscillates between waiting and terminating in case of containers exiting with non-zero exit code, while the `containerStatus.LastTerminationState`
// captures the last termination state and only changes when the container starts properly after multiple attempts, thus
// it is a more stable target state to observe.
func GetContainerStatusIfTerminatedErroneously(containerStatuses []corev1.ContainerStatus) *corev1.ContainerStatus {
	containerStatus, ok := lo.Find(containerStatuses, func(containerStatus corev1.ContainerStatus) bool {
		return containerStatus.LastTerminationState.Terminated != nil && containerStatus.LastTerminationState.Terminated.ExitCode != 0
	})
	if !ok {
		return nil
	}
	return &containerStatus
}

// logTerminatedErroneouslyPodContainerStatus logs details about a container that terminated with a non-zero exit code.
func logTerminatedErroneouslyPodContainerStatus(logger logr.Logger, podObjKey client.ObjectKey, containerStatus *corev1.ContainerStatus) {
	if containerStatus != nil && containerStatus.LastTerminationState.Terminated != nil {
		logger.Info("container previously exited with a non-zero exit code",
			"pod", podObjKey,
			"container", containerStatus.Name,
			"exitCode", containerStatus.LastTerminationState.Terminated.ExitCode,
			"exitReason", containerStatus.LastTerminationState.Terminated.Reason)
	}
}

// IsPodActive determines if a pod is active (running, pending, or will be restarted)
// Returns true if the pod is active
// Returns false if the pod is terminating or has failed permanently
func IsPodActive(pod *corev1.Pod) bool {
	return !IsResourceTerminating(pod.ObjectMeta) &&
		pod.Status.Phase != corev1.PodSucceeded &&
		pod.Status.Phase != corev1.PodFailed
}

// IsPodReady checks if the corev1.PodReady condition is set to true for the Pod.
// If the condition is set to true, it returns true else if the condition is not present or set to false it will return false.
func IsPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// IsPodPending checks if the Pod is pending, which indicates that the pod has not yet been scheduled.
func IsPodPending(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodPending
}
