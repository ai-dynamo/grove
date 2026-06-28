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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestRecordCreateOrPatchSuccessEvent(t *testing.T) {
	tests := []struct {
		name        string
		opResult    controllerutil.OperationResult
		expectEvent bool
	}{
		{name: "created object", opResult: controllerutil.OperationResultCreated, expectEvent: true},
		{name: "updated object", opResult: controllerutil.OperationResultUpdated, expectEvent: true},
		{name: "unchanged object", opResult: controllerutil.OperationResultNone, expectEvent: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := record.NewFakeRecorder(1)
			obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

			RecordCreateOrPatchSuccessEvent(recorder, obj, tc.opResult, "Synced", "synced %s", obj.Name)

			select {
			case event := <-recorder.Events:
				if !tc.expectEvent {
					t.Fatalf("unexpected event: %s", event)
				}
				assert.Equal(t, "Normal Synced synced test", event)
			default:
				if tc.expectEvent {
					t.Fatal("expected event")
				}
			}
		})
	}
}
