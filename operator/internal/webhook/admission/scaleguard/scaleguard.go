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

package scaleguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	componentutils "github.com/ai-dynamo/grove/operator/internal/utils/component"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// replicasSpec reads spec.replicas from an admission object. A PodClique, a PodCliqueScalingGroup, and the
// scale subresource of either all carry spec.replicas, so one shape decodes every request this guard sees.
type replicasSpec struct {
	Spec struct {
		Replicas int32 `json:"replicas"`
	} `json:"spec"`
}

// Handle validates a PodClique or PodCliqueScalingGroup update. It first rejects a spec.replicas change
// while a coherent update is in progress on the owning PodCliqueSet. When no coherent update is in progress
// it then rejects a change that would leave spec.replicas below spec.minAvailable. Both checks matter only
// when spec.replicas changes, and both cover a change made on the resource and one made through the scale
// subresource. The webhook fires only on updates, so a create is never seen here and the PodCliqueSet
// template validation covers it.
//
// target is an empty PodClique or PodCliqueScalingGroup that the stored resource is fetched into, and its
// concrete type selects the guarded kind.
func Handle(ctx context.Context, req admission.Request, cl client.Client, logger logr.Logger, target client.Object) admission.Response {
	if req.Operation != admissionv1.Update {
		return admission.Allowed("only spec.replicas changes are validated")
	}
	oldReplicas, err := requestReplicas(req.OldObject.Raw)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("could not read spec.replicas from the old object: %w", err))
	}
	newReplicas, err := requestReplicas(req.Object.Raw)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("could not read spec.replicas from the object: %w", err))
	}
	if oldReplicas == newReplicas {
		return admission.Allowed("spec.replicas is unchanged")
	}

	targetObjKey := client.ObjectKey{Namespace: req.Namespace, Name: req.Name}
	if err := cl.Get(ctx, targetObjKey, target); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Target not found, allowing the replica change", "target", targetObjKey)
			return admission.Allowed("target not found")
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if resp := denyReplicasChangeDuringCoherentUpdate(ctx, cl, logger, target); !resp.Allowed {
		return resp
	}
	return denyReplicasBelowMinAvailable(target, newReplicas)
}

// requestReplicas reads spec.replicas from the raw object of an admission request.
func requestReplicas(raw []byte) (int32, error) {
	var rs replicasSpec
	if err := json.Unmarshal(raw, &rs); err != nil {
		return 0, err
	}
	return rs.Spec.Replicas, nil
}

// denyReplicasChangeDuringCoherentUpdate denies a spec.replicas change while a coherent update is in
// progress on the PodCliqueSet that owns target. A not-yet-updated replica cannot scale coherently while
// the template is at a newer revision, so scaling is blocked on every replica for the duration of the
// update. The change is admitted when the owning PodCliqueSet cannot be resolved so recovery stays
// unblocked, and when no coherent update is in progress.
func denyReplicasChangeDuringCoherentUpdate(ctx context.Context, cl client.Client, logger logr.Logger, target client.Object) admission.Response {
	// Get the parent PodCliqueSet resource.
	objectMeta := metav1.ObjectMeta{Name: target.GetName(), Namespace: target.GetNamespace(), Labels: target.GetLabels()}
	pcs, err := componentutils.GetPodCliqueSet(ctx, cl, objectMeta)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Owning PodCliqueSet not found, allowing the replica change", "target", client.ObjectKeyFromObject(target))
			return admission.Allowed("owning PodCliqueSet not found")
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}
	// check if the coherent update is in progress for this PodCliqueSet.
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		return admission.Denied(fmt.Sprintf("spec.replicas changes are not allowed while a coherent update is in progress on PodCliqueSet %v, complete the update before scaling",
			client.ObjectKeyFromObject(pcs)))
	}
	return admission.Allowed("owning PodCliqueSet has no coherent update in progress")
}

// denyReplicasBelowMinAvailable denies a change that would leave spec.replicas below spec.minAvailable.
// minAvailable is immutable, so its value is read from the stored resource in target for a full update and
// for a scale subresource request alike.
func denyReplicasBelowMinAvailable(target client.Object, newReplicas int32) admission.Response {
	minAvailable := minAvailableOf(target)
	if minAvailable != nil && newReplicas < *minAvailable {
		return admission.Denied(fmt.Sprintf("spec.replicas %d must not be less than spec.minAvailable %d", newReplicas, *minAvailable))
	}
	return admission.Allowed("spec.replicas is at or above spec.minAvailable")
}

// minAvailableOf returns spec.minAvailable of the fetched PodClique or PodCliqueScalingGroup, or nil for any
// other type. minAvailable is immutable, so the stored value is also the value the request would produce
// for a full update and for a scale subresource request.
func minAvailableOf(target client.Object) *int32 {
	switch resource := target.(type) {
	case *grovecorev1alpha1.PodClique:
		return resource.Spec.MinAvailable
	case *grovecorev1alpha1.PodCliqueScalingGroup:
		return resource.Spec.MinAvailable
	default:
		return nil
	}
}
