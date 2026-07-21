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

package eventrecorder

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func testConfig() Config {
	return Config{
		Rules: []RateLimitRule{
			{
				EventType: corev1.EventTypeNormal,
				Reasons:   []string{"Synced"},
				Interval:  defaultNoopSuccessEventInterval,
				Burst:     defaultNoopSuccessEventBurst,
			},
		},
	}
}

func testClient(t *testing.T, delegate record.EventRecorder) (*Client, *time.Time) {
	t.Helper()
	client := New(delegate, testConfig())
	now := time.Now()
	client.limiters[0].now = func() time.Time { return now }
	return client, &now
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

func TestCreateOrPatchSuccess_ActualChangesAlwaysRecorded(t *testing.T) {
	tests := []struct {
		name     string
		opResult controllerutil.OperationResult
	}{
		{name: "created object", opResult: controllerutil.OperationResultCreated},
		{name: "updated object", opResult: controllerutil.OperationResultUpdated},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delegate := record.NewFakeRecorder(10)
			client, _ := testClient(t, delegate)
			obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

			for i := 0; i < 3; i++ {
				client.CreateOrPatchSuccess(obj, tc.opResult, "Synced", "synced %s", obj.Name)
				event, ok := drainEvent(t, delegate)
				assert.True(t, ok, "expected an event on iteration %d", i)
				assert.Equal(t, "Normal Synced synced test", event)
			}
		})
	}
}

func TestCreateOrPatchSuccess_NoopRateLimited(t *testing.T) {
	delegate := record.NewFakeRecorder(10)
	client, now := testClient(t, delegate)
	obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	emit := func() {
		client.CreateOrPatchSuccess(obj, controllerutil.OperationResultNone, "Synced", "synced %s", obj.Name)
	}

	emit()
	event, ok := drainEvent(t, delegate)
	assert.True(t, ok, "expected the first no-op event to be recorded")
	assert.Equal(t, "Normal Synced synced test", event)

	for i := 0; i < 5; i++ {
		emit()
		_, ok := drainEvent(t, delegate)
		assert.False(t, ok, "did not expect a throttled no-op event on iteration %d", i)
	}

	*now = now.Add(defaultNoopSuccessEventInterval + time.Second)
	emit()
	event, ok = drainEvent(t, delegate)
	assert.True(t, ok, "expected a no-op event after the interval elapsed")
	assert.Equal(t, "Normal Synced synced test", event)
}

func TestCreateOrPatchSuccess_DistinctKeysLimitedIndependently(t *testing.T) {
	delegate := record.NewFakeRecorder(10)
	client, _ := testClient(t, delegate)
	objA := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "a"}}
	objB := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"}}

	client.CreateOrPatchSuccess(objA, controllerutil.OperationResultNone, "Synced", "synced")
	client.CreateOrPatchSuccess(objB, controllerutil.OperationResultNone, "Synced", "synced")

	_, ok := drainEvent(t, delegate)
	assert.True(t, ok)
	_, ok = drainEvent(t, delegate)
	assert.True(t, ok)
	_, ok = drainEvent(t, delegate)
	assert.False(t, ok)
}

func TestClient_RateLimitsConfiguredEventType(t *testing.T) {
	delegate := record.NewFakeRecorder(10)
	client, _ := testClient(t, delegate)
	obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	client.Eventf(obj, corev1.EventTypeNormal, "Synced", "first")
	_, ok := drainEvent(t, delegate)
	assert.True(t, ok)

	client.Eventf(obj, corev1.EventTypeNormal, "Synced", "second")
	_, ok = drainEvent(t, delegate)
	assert.False(t, ok)
}
