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

package utils

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	commonrevision "github.com/ai-dynamo/grove/operator/internal/controller/common/revision"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// revisionCacheKey is a context key for per-reconcile memoization of GetPodCliqueSetRevision results.
type revisionCacheKey struct{}

// revisionCache holds one slot keyed by "namespace/name".
type revisionCache struct {
	byKey map[string]*commonrevision.Revision
}

// WithPodCliqueSetRevisionCache returns a context that memoizes GetPodCliqueSetRevision results for the
// lifetime of one reconcile. Call this once at the top of Reconcile and propagate the ctx.
func WithPodCliqueSetRevisionCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, revisionCacheKey{}, &revisionCache{byKey: make(map[string]*commonrevision.Revision, 1)})
}

// GetPodCliqueSetRevision gets the current Revision object for a given PodCliqueSet.
// When the context carries a cache from WithPodCliqueSetRevisionCache, the first lookup populates it and subsequent calls skip the Get.
func GetPodCliqueSetRevision(ctx context.Context, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet) (*commonrevision.Revision, error) {
	if pcs.Status.CurrentRevision == nil || *pcs.Status.CurrentRevision == "" {
		return nil, fmt.Errorf("PodCliqueSet %v has no current ControllerRevision", client.ObjectKeyFromObject(pcs))
	}

	currentRevision := *pcs.Status.CurrentRevision
	key := pcs.Namespace + "/" + currentRevision
	cache, _ := ctx.Value(revisionCacheKey{}).(*revisionCache)
	if cache != nil {
		if revision, ok := cache.byKey[key]; ok {
			return revision, nil
		}
	}

	controllerRevision := &appsv1.ControllerRevision{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: currentRevision}, controllerRevision); err != nil {
		return nil, err
	}

	revision, err := commonrevision.DecodeRevision(controllerRevision)
	if err != nil {
		return nil, fmt.Errorf("ControllerRevision %v has invalid revision data: %w", client.ObjectKeyFromObject(controllerRevision), err)
	}

	if cache != nil {
		cache.byKey[key] = revision
	}

	return revision, nil
}
