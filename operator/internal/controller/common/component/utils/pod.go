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

package utils

import (
	"context"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// pclqPodsCacheKey is a context key for per-reconcile memoization of GetPCLQPods results.
// A PodClique reconcile calls GetPCLQPods from both reconcileSpec and reconcileStatus;
// without the cache we list+filter the informer cache twice per reconcile. Stash the result
// on a fresh ctx via WithPCLQPodsCache and the second call reuses it.
type pclqPodsCacheKey struct{}

// pclqPodsCache holds one slot per PodClique UID. UIDs are used rather than names so a
// stale cache entry can never bleed into a recreated object with the same name.
type pclqPodsCache struct {
	byUID map[types.UID][]*corev1.Pod
}

// WithPCLQPodsCache returns a context that memoizes GetPCLQPods results for the lifetime of
// one reconcile. Call this once at the top of Reconcile and propagate the returned context.
func WithPCLQPodsCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, pclqPodsCacheKey{}, &pclqPodsCache{byUID: make(map[types.UID][]*corev1.Pod, 1)})
}

// podControllerUIDIndexField is the name of the field index registered on Pods that maps a
// Pod to the UID of its controlling owner (the owner reference with Controller=true). It is
// a synthetic index name, not a real field path; the only requirement is that it match the
// key passed to client.MatchingFields in GetPCLQPods.
const podControllerUIDIndexField = ".metadata.controller.uid"

// indexPodByControllerUID extracts the controlling owner UID from a Pod for the
// podControllerUIDIndexField index. Pods without a controller owner reference are not
// indexed. For grove-managed Pods the controller is always the owning PodClique, so this
// index maps a Pod to its PodClique's UID.
func indexPodByControllerUID(obj client.Object) []string {
	// GetControllerOfNoCopy avoids copying the OwnerReference: this runs per Pod on every
	// cache add/update and only the UID is read.
	controllerRef := metav1.GetControllerOfNoCopy(obj)
	if controllerRef == nil {
		return nil
	}
	return []string{string(controllerRef.UID)}
}

// RegisterPodControllerUIDIndex registers the Pod field index that GetPCLQPods queries via
// client.MatchingFields. Call it exactly once during controller setup (RegisterControllers)
// so the single shared manager cache carries the index before any reconcile lists Pods.
//
// The index is required, not an optimization: GetPCLQPods selects Pods with a field selector,
// and controller-runtime's cache returns an error from List when the field index is not
// registered. A field index is used because the cache cannot index label selectors — the
// MatchingLabels equivalent would scan every grove-managed Pod in the namespace on each call
// (O(pods-in-namespace)); the field index serves the same list via ByIndex.
func RegisterPodControllerUIDIndex(ctx context.Context, fieldIndexer client.FieldIndexer) error {
	return fieldIndexer.IndexField(ctx, &corev1.Pod{}, podControllerUIDIndexField, indexPodByControllerUID)
}

// GetPCLQPods lists all Pods controlled by the given PodClique.
// It queries the podControllerUIDIndexField field index (registered by
// RegisterPodControllerUIDIndex) keyed on the PodClique UID. That key is the controller
// owner-reference UID, exactly the ownership relation metav1.IsControlledBy used to verify
// in-memory, so no post-filter is needed.
//
// When ctx carries a cache from WithPCLQPodsCache, the first call populates the cache and
// subsequent calls in the same reconcile return the cached slice without hitting the
// informer.
func GetPCLQPods(ctx context.Context, cl client.Client, _ string, pclq *grovecorev1alpha1.PodClique) ([]*corev1.Pod, error) {
	cache, _ := ctx.Value(pclqPodsCacheKey{}).(*pclqPodsCache)
	if cache != nil {
		if pods, ok := cache.byUID[pclq.UID]; ok {
			return pods, nil
		}
	}
	podList := &corev1.PodList{}
	if err := cl.List(ctx,
		podList,
		client.InNamespace(pclq.Namespace),
		client.MatchingFields{podControllerUIDIndexField: string(pclq.UID)},
	); err != nil {
		return nil, err
	}
	ownedPods := lo.ToSlicePtr(podList.Items)
	if cache != nil {
		cache.byUID[pclq.UID] = ownedPods
	}
	return ownedPods, nil
}

// AddEnvVarsToContainers adds the given environment variables to the Pod containers.
func AddEnvVarsToContainers(containers []corev1.Container, envVars []corev1.EnvVar) {
	for i := range containers {
		containers[i].Env = append(containers[i].Env, envVars...)
	}
}

// PodsToObjectNames converts a slice of Pods to a slice of string representations in "namespace/name" format.
func PodsToObjectNames(pods []*corev1.Pod) []string {
	return lo.Map(pods, func(pod *corev1.Pod, _ int) string {
		return cache.NamespacedNameAsObjectName(client.ObjectKeyFromObject(pod)).String()
	})
}
