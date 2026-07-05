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
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// withFakeClock installs a controllable clock on the package-level no-op event limiter for the
// duration of a test and restores the previous limiter afterwards.
func withFakeClock(t *testing.T) *time.Time {
	t.Helper()
	previous := noopEventLimiter
	t.Cleanup(func() { noopEventLimiter = previous })

	now := time.Now()
	noopEventLimiter = newEventRateLimiter(noopSuccessEventInterval, noopSuccessEventBurst)
	noopEventLimiter.now = func() time.Time { return now }
	return &now
}

func drainEvent(t *testing.T, recorder *record.FakeRecorder) (string, bool) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		return event, true
	default:
		return "", false
	}
}

func TestRecordCreateOrPatchSuccessEvent_ActualChangesAlwaysRecorded(t *testing.T) {
	tests := []struct {
		name     string
		opResult controllerutil.OperationResult
	}{
		{name: "created object", opResult: controllerutil.OperationResultCreated},
		{name: "updated object", opResult: controllerutil.OperationResultUpdated},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withFakeClock(t)
			recorder := record.NewFakeRecorder(10)
			obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

			// Repeated actual changes must never be rate-limited.
			for i := 0; i < 3; i++ {
				RecordCreateOrPatchSuccessEvent(recorder, obj, tc.opResult, "Synced", "synced %s", obj.Name)
				event, ok := drainEvent(t, recorder)
				assert.True(t, ok, "expected an event on iteration %d", i)
				assert.Equal(t, "Normal Synced synced test", event)
			}
		})
	}
}

func TestRecordCreateOrPatchSuccessEvent_NoopRateLimited(t *testing.T) {
	now := withFakeClock(t)
	recorder := record.NewFakeRecorder(10)
	obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	emit := func() {
		RecordCreateOrPatchSuccessEvent(recorder, obj, controllerutil.OperationResultNone, "Synced", "synced %s", obj.Name)
	}

	// The first steady-state event is surfaced (burst).
	emit()
	event, ok := drainEvent(t, recorder)
	assert.True(t, ok, "expected the first no-op event to be recorded")
	assert.Equal(t, "Normal Synced synced test", event)

	// Immediately repeated no-op events for the same object+reason are throttled.
	for i := 0; i < 5; i++ {
		emit()
		_, ok := drainEvent(t, recorder)
		assert.False(t, ok, "did not expect a throttled no-op event on iteration %d", i)
	}

	// Once the interval elapses, a single event is allowed through again.
	*now = now.Add(noopSuccessEventInterval + time.Second)
	emit()
	event, ok = drainEvent(t, recorder)
	assert.True(t, ok, "expected a no-op event after the interval elapsed")
	assert.Equal(t, "Normal Synced synced test", event)
}

func TestRecordCreateOrPatchSuccessEvent_DistinctKeysLimitedIndependently(t *testing.T) {
	withFakeClock(t)
	recorder := record.NewFakeRecorder(10)
	objA := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a"}}
	objB := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"}}

	RecordCreateOrPatchSuccessEvent(recorder, objA, controllerutil.OperationResultNone, "Synced", "synced")
	RecordCreateOrPatchSuccessEvent(recorder, objB, controllerutil.OperationResultNone, "Synced", "synced")

	// Both distinct objects should surface their first no-op event.
	_, ok := drainEvent(t, recorder)
	assert.True(t, ok)
	_, ok = drainEvent(t, recorder)
	assert.True(t, ok)
	_, ok = drainEvent(t, recorder)
	assert.False(t, ok)
}
