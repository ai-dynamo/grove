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

func requestReplicas(raw []byte) (int32, error) {
	var rs replicasSpec
	if err := json.Unmarshal(raw, &rs); err != nil {
		return 0, err
	}
	return rs.Spec.Replicas, nil
}

// Handle rejects a spec.replicas change while a coherent update is in progress on the owning PodCliqueSet,
// and admits every other request. Scaling is blocked on every replica for the duration of the update, not
// only the replica currently rolling, because a not-yet-updated replica cannot scale coherently while the
// PodCliqueSet template is at a newer revision. The change is caught whether it is made directly on the
// resource or through its scale subresource, since both carry spec.replicas.
//
// target is an empty PodClique or PodCliqueScalingGroup that the resource under review is fetched into, and
// its concrete type selects the guarded kind. The owning PodCliqueSet is read from the fetched resource.
// When the owner cannot be resolved the change is admitted so recovery stays unblocked.
func Handle(ctx context.Context, req admission.Request, cl client.Client, logger logr.Logger, target client.Object) admission.Response {
	if req.Operation != admissionv1.Update {
		return admission.Allowed("only updates that change spec.replicas are validated")
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

	targetKey := client.ObjectKey{Namespace: req.Namespace, Name: req.Name}
	if err := cl.Get(ctx, targetKey, target); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Target not found, allowing the replica change", "target", targetKey)
			return admission.Allowed("target not found")
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}
	objectMeta := metav1.ObjectMeta{Name: target.GetName(), Namespace: target.GetNamespace(), Labels: target.GetLabels()}

	pcs, err := componentutils.GetPodCliqueSet(ctx, cl, objectMeta)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Owning PodCliqueSet not found, allowing the replica change", "target", targetKey)
			return admission.Allowed("owning PodCliqueSet not found")
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if componentutils.IsCoherentUpdateInProgress(pcs) {
		return admission.Denied(fmt.Sprintf("spec.replicas changes are not allowed while a coherent update is in progress on PodCliqueSet %v, complete the update before scaling",
			client.ObjectKeyFromObject(pcs)))
	}
	return admission.Allowed("owning PodCliqueSet has no coherent update in progress")
}
