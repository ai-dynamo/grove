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

package component

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// CreateOrPatchSpec fetches the object identified by obj, applies mutateFn to bring it to its
// desired state, and patches it back only when the mutation actually changed something. mutateFn
// may be nil, in which case the object is created or left unchanged as-is.
//
// It is a cheaper alternative to controllerutil.CreateOrPatch on the reconcile hot path.
// controllerutil.CreateOrPatch converts both the pre- and post-mutation object to an unstructured
// map on every call (reflect-heavy structToUnstructured) before diffing them, so a no-op reconcile
// still pays the full conversion + comparison cost. This helper instead compares the typed objects
// directly with equality.Semantic.DeepEqual and skips the network Patch entirely when they are
// equal; the merge patch is only computed (lazily, inside client.Patch) on the change path.
//
// Comparing the whole typed object keeps this safe regardless of which spec/metadata fields mutateFn
// touches: it can never skip a genuine change.
//
// Unlike controllerutil.CreateOrPatch, this helper does NOT diff or patch the Status subresource,
// and it never returns OperationResultUpdatedStatus or OperationResultUpdatedStatusOnly. mutateFn
// must therefore not touch the object's Status: a status mutation would be observed as a change and
// trigger a Patch against the main resource, which the API server silently ignores for the status
// subresource. The status change would be lost while the caller still sees OperationResultUpdated.
// Callers that need to reconcile status must do so separately via client.Status().Patch. The "Spec"
// suffix is a reminder of this contract.
func CreateOrPatchSpec(ctx context.Context, c client.Client, obj client.Object, mutateFn controllerutil.MutateFn) (controllerutil.OperationResult, error) {
	key := client.ObjectKeyFromObject(obj)
	if err := c.Get(ctx, key, obj); err != nil {
		if !apierrors.IsNotFound(err) {
			return controllerutil.OperationResultNone, err
		}
		if mutateFn != nil {
			if err := mutate(mutateFn, key, obj); err != nil {
				return controllerutil.OperationResultNone, err
			}
		}
		if err := c.Create(ctx, obj); err != nil {
			return controllerutil.OperationResultNone, err
		}
		return controllerutil.OperationResultCreated, nil
	}

	existing := obj.DeepCopyObject().(client.Object)
	if mutateFn != nil {
		if err := mutate(mutateFn, key, obj); err != nil {
			return controllerutil.OperationResultNone, err
		}
	}

	if equality.Semantic.DeepEqual(existing, obj) {
		return controllerutil.OperationResultNone, nil
	}

	if err := c.Patch(ctx, obj, client.MergeFrom(existing)); err != nil {
		return controllerutil.OperationResultNone, err
	}
	return controllerutil.OperationResultUpdated, nil
}

// mutate applies mutateFn to obj and validates that it did not change the object's identity.
// This mirrors the identity guard controllerutil applies to its own MutateFn.
func mutate(mutateFn controllerutil.MutateFn, key client.ObjectKey, obj client.Object) error {
	if err := mutateFn(); err != nil {
		return err
	}
	if newKey := client.ObjectKeyFromObject(obj); key != newKey {
		return fmt.Errorf("MutateFn cannot mutate object name and/or object namespace")
	}
	return nil
}
