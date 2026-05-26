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
	"sync"
	"testing"
)

// Capability is a scheduler feature the E2E suite may require (e.g. gang
// scheduling, topology-aware scheduling). Tests gate themselves with
// RequireCapability and auto-skip when the active backend does not provide it.
type Capability string

const (
	// GangScheduling indicates the active backend treats a PodGang as an
	// all-or-nothing scheduling unit.
	GangScheduling Capability = "GangScheduling"

	// TopologyAwareScheduling indicates the active backend implements the
	// scheduler.TopologyAwareBackend interface AND the operator has
	// topologyAwareScheduling.enabled=true.
	TopologyAwareScheduling Capability = "TopologyAwareScheduling"

	// AutoMNNVL indicates the operator has network.autoMNNVLEnabled=true.
	// Config-only — no backend coupling.
	AutoMNNVL Capability = "AutoMNNVL"
)

// CapabilitySet is the resolved set of capabilities for a single E2E run.
type CapabilitySet struct {
	// ActiveBackend is the value of OperatorConfiguration.scheduler.defaultProfileName.
	ActiveBackend string
	// caps is the set of capabilities present on the active backend.
	caps map[Capability]bool
}

// Has reports whether the set contains the given capability.
func (s CapabilitySet) Has(c Capability) bool {
	return s.caps[c]
}

// backendInterfaceCapabilities is the hardcoded map of backend → capabilities
// that depend on Go interface implementation in the operator. Entries here are
// what E2E cannot deduce from a live OperatorConfiguration alone (the operator
// uses Go type assertions; the test binary runs out-of-process and cannot).
//
// Capabilities derived purely from configuration flags (e.g. AutoMNNVL from
// network.autoMNNVLEnabled) are NOT listed here — they are resolved directly
// from OperatorConfiguration in DiscoverCapabilities.
//
// When adding a new backend, add a row here AND update the developer
// checklist in the design proposal. The capabilities_test.go cross-check
// fails the build if this table disagrees with the actual Go interfaces.
var backendInterfaceCapabilities = map[string]map[Capability]bool{
	"kai-scheduler": {
		GangScheduling:          true,
		TopologyAwareScheduling: true,
	},
	"default-scheduler": {
		// KubeSchedulerConfig.GangScheduling is forward-looking — the kube
		// backend does not yet read or act on it. When it does, set
		// GangScheduling: true here.
	},
}

// currentCapabilities holds the resolved CapabilitySet for the running e2e
// suite. DiscoverCapabilities (in capability_discovery.go, e2e build tag)
// populates it once at TestMain time; RequireCapability reads it on every
// gated test entry.
var (
	currentCapabilities    CapabilitySet
	currentCapabilitiesSet bool
	currentCapabilitiesMu  sync.RWMutex
)

// RequireCapability skips t when the active backend does not provide cap.
// Tests gated with RequireCapability are listed in the design proposal's
// Test Classification table as "Capability-gated".
//
// The function is no-op if capabilities have not been discovered yet (e.g. when
// running unit tests with go test ./... without an e2e cluster); the e2e build
// flow guarantees discovery runs before any test that calls this.
func RequireCapability(t *testing.T, cap Capability) {
	t.Helper()
	currentCapabilitiesMu.RLock()
	defer currentCapabilitiesMu.RUnlock()
	if !currentCapabilitiesSet {
		return
	}
	if !currentCapabilities.Has(cap) {
		t.Skipf("skipping: active backend %q does not provide capability %q",
			currentCapabilities.ActiveBackend, cap)
	}
}
