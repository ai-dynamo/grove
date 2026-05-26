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

package tests

import (
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kube"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// backendConstructors mirrors the switch in
// operator/internal/scheduler/manager/manager.go newBackendForProfile.
// Adding a new backend means adding a row here AND in
// backendInterfaceCapabilities (in capabilities.go); TestCapabilityTableMatchesBackends
// fails the build if the two disagree.
var backendConstructors = map[configv1alpha1.SchedulerName]func() scheduler.Backend{
	configv1alpha1.SchedulerNameKai: func() scheduler.Backend {
		return kai.New(
			fake.NewClientBuilder().Build(),
			runtime.NewScheme(),
			record.NewFakeRecorder(1),
			configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKai},
		)
	},
	configv1alpha1.SchedulerNameKube: func() scheduler.Backend {
		return kube.New(
			fake.NewClientBuilder().Build(),
			runtime.NewScheme(),
			record.NewFakeRecorder(1),
			configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube},
		)
	},
}

// TestCapabilityTableCoversAllSupportedBackends ensures the hardcoded
// capability table has a row for every backend the operator can construct.
// Catches the failure mode where a contributor adds a backend to
// SupportedSchedulerNames + manager.newBackendForProfile but forgets the
// capability table — without this, the new backend's capability-gated tests
// would silently skip rather than fail.
func TestCapabilityTableCoversAllSupportedBackends(t *testing.T) {
	for _, name := range configv1alpha1.SupportedSchedulerNames {
		if _, ok := backendInterfaceCapabilities[string(name)]; !ok {
			t.Errorf("backend %q is in SupportedSchedulerNames but missing from "+
				"backendInterfaceCapabilities; add a row to "+
				"operator/e2e/tests/capabilities.go", name)
		}
		if _, ok := backendConstructors[name]; !ok {
			t.Errorf("backend %q is in SupportedSchedulerNames but missing from "+
				"backendConstructors; add a row to "+
				"operator/e2e/tests/capabilities_test.go", name)
		}
	}
}

// TestCapabilityTableMatchesBackends cross-checks the hardcoded capability
// table against actual Go interface implementation for each backend. Catches
// the failure mode where a backend's interface set changes (e.g. KAI drops
// TopologyAwareBackend) but the table is not updated — without this, the
// E2E suite would either skip valid TAS tests or run them against a backend
// that no longer supports TAS.
func TestCapabilityTableMatchesBackends(t *testing.T) {
	for name, ctor := range backendConstructors {
		t.Run(string(name), func(t *testing.T) {
			b := ctor()
			table := backendInterfaceCapabilities[string(name)]

			// TopologyAwareScheduling: tied to the Go interface assertion
			// the operator itself uses (clustertopology.go L46–54).
			_, gotTAS := b.(scheduler.TopologyAwareBackend)
			wantTAS := table[TopologyAwareScheduling]
			if gotTAS != wantTAS {
				t.Errorf("backend %q: TopologyAwareScheduling table=%v but "+
					"interface assertion=%v; update either the backend "+
					"or backendInterfaceCapabilities", name, wantTAS, gotTAS)
			}
		})
	}
}
