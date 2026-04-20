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

package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// CreateOrUpdate is a lighter alternative to controllerutil.CreateOrPatch for resources
// whose status subresource is managed by a different controller.
//
// controllerutil.CreateOrPatch pays a fixed overhead per call even when nothing has changed:
//   - 2 × DeepCopyObject   (patch bases for spec and status)
//   - 2 × ToUnstructured   (before/after for the diff)
//   - 1 × reflect.DeepEqual (full unstructured map comparison)
//
// CreateOrUpdate avoids that by marshalling to JSON before and after the mutate function
// and computing a merge-patch diff directly. Status subresource patches are not issued;
// callers that need status updates must use client.Status().Patch separately.
func CreateOrUpdate(ctx context.Context, c client.Client, obj client.Object, f controllerutil.MutateFn) (controllerutil.OperationResult, error) {
	key := client.ObjectKeyFromObject(obj)

	if err := c.Get(ctx, key, obj); err != nil {
		if !apierrors.IsNotFound(err) {
			return controllerutil.OperationResultNone, err
		}
		if err := mutate(f, key, obj); err != nil {
			return controllerutil.OperationResultNone, err
		}
		if err := c.Create(ctx, obj); err != nil {
			return controllerutil.OperationResultNone, err
		}
		return controllerutil.OperationResultCreated, nil
	}

	before, err := json.Marshal(obj)
	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("marshal before-state for %s: %w", key, err)
	}
	if err := mutate(f, key, obj); err != nil {
		return controllerutil.OperationResultNone, err
	}
	after, err := json.Marshal(obj)
	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("marshal after-state for %s: %w", key, err)
	}
	data, err := jsonpatch.CreateMergePatch(before, after)
	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("compute patch for %s: %w", key, err)
	}

	if isEmptyPatch(data) {
		return controllerutil.OperationResultNone, nil
	}

	if err := c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, data)); err != nil {
		return controllerutil.OperationResultNone, err
	}
	return controllerutil.OperationResultUpdated, nil
}

func mutate(f controllerutil.MutateFn, key client.ObjectKey, obj client.Object) error {
	if err := f(); err != nil {
		return err
	}
	if newKey := client.ObjectKeyFromObject(obj); key != newKey {
		return fmt.Errorf("MutateFn cannot mutate object name and/or object namespace")
	}
	return nil
}

func isEmptyPatch(data []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	return len(m) == 0
}
