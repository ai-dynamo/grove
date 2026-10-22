//go:build e2e

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
	"context"
	"fmt"

	"github.com/ai-dynamo/grove/operator/e2e/grove/config"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DiscoverCapabilities resolves the active backend and its capabilities from
// the live OperatorConfiguration plus the hardcoded interface table, stores
// the result in the package-level currentCapabilities so RequireCapability
// can read it, and returns the resolved set so callers may log/inspect it.
//
// Called once from TestMain before any test runs.
func DiscoverCapabilities(ctx context.Context, crClient client.Client) (CapabilitySet, error) {
	md, err := config.NewOperatorConfig(crClient).ReadGroveMetadata(ctx)
	if err != nil {
		return CapabilitySet{}, fmt.Errorf("read OperatorConfiguration: %w", err)
	}

	backend := md.Config.Scheduler.DefaultProfileName
	table, ok := backendInterfaceCapabilities[backend]
	if !ok {
		return CapabilitySet{}, fmt.Errorf(
			"active backend %q has no entry in backendInterfaceCapabilities; "+
				"please update operator/e2e/tests/capabilities.go", backend)
	}

	set := CapabilitySet{
		ActiveBackend: backend,
		caps:          map[Capability]bool{},
	}

	// Backend-coupled capability: present iff backend is in the table for it.
	if table[GangScheduling] {
		set.caps[GangScheduling] = true
	}

	// Backend-coupled capability gated by an additional config flag.
	if md.Config.TopologyAwareScheduling.Enabled && table[TopologyAwareScheduling] {
		set.caps[TopologyAwareScheduling] = true
	}

	// Config-only capability: no interface-table lookup.
	if md.Config.Network.AutoMNNVLEnabled {
		set.caps[AutoMNNVL] = true
	}

	currentCapabilitiesMu.Lock()
	currentCapabilities = set
	currentCapabilitiesSet = true
	currentCapabilitiesMu.Unlock()

	return set, nil
}
