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

package component

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// RecordCreateOrPatchSuccessEvent records a Normal event when CreateOrPatch changed the object.
// Steady-state reconciles return OperationResultNone and must not generate success events.
func RecordCreateOrPatchSuccessEvent(eventRecorder record.EventRecorder, object runtime.Object, opResult controllerutil.OperationResult, reason, messageFmt string, args ...any) {
	if opResult == controllerutil.OperationResultNone {
		return
	}
	eventRecorder.Eventf(object, corev1.EventTypeNormal, reason, messageFmt, args...)
}
