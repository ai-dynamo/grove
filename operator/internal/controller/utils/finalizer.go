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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// AddAndPatchFinalizer uses merge-patch strategy to patch the given object with the given finalizer.
func AddAndPatchFinalizer(ctx context.Context, writer client.Writer, obj client.Object, finalizer string) error {
	return patchFinalizer(ctx,
		writer,
		obj,
		mergeFromWithOptimisticLock,
		controllerutil.AddFinalizer,
		finalizer,
	)
}

// RemoveAndPatchFinalizer uses merge-patch strategy to patch the given object thereby resulting in removal of the given finalizer.
func RemoveAndPatchFinalizer(ctx context.Context, writer client.Writer, obj client.Object, finalizer string) error {
	return client.IgnoreNotFound(
		patchFinalizer(ctx,
			writer,
			obj,
			mergeFromWithOptimisticLock,
			controllerutil.RemoveFinalizer,
			finalizer,
		),
	)
}

// mergeFromWithOptimisticLock returns a client.Patch with the given client.Object as the base object and the optimistic lock option.
func mergeFromWithOptimisticLock(obj client.Object, opts ...client.MergeFromOption) client.Patch {
	return client.MergeFromWithOptions(obj, append(opts, client.MergeFromWithOptimisticLock{})...)
}

// patchFn is a function that returns a client.Patch with the given client.Object as the base object.
// The client.Patch returned is then used to patch the object.
type patchFn func(client.Object, ...client.MergeFromOption) client.Patch

// mutateFn is a function that mutates the object with the given finalizer.
type mutateFn func(client.Object, string) bool

// patchFinalizer applies a finalizer mutation to an object and patches it.
func patchFinalizer(ctx context.Context, writer client.Writer, obj client.Object, patchFunc patchFn, mutateFunc mutateFn, finalizer string) error {
	beforePatch := obj.DeepCopyObject().(client.Object)
	mutateFunc(obj, finalizer)
	return writer.Patch(ctx, obj, patchFunc(beforePatch))
}
