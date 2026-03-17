/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopologyDomainIsFreeFormString(t *testing.T) {
	// TopologyDomain is a free-form string type. Administrators define domain names
	// that describe their infrastructure hierarchy. The ordering is determined by
	// the position in the ClusterTopology's levels array, not by the domain name itself.
	tests := []struct {
		name   string
		domain TopologyDomain
	}{
		{name: "well-known domain: zone", domain: "zone"},
		{name: "well-known domain: rack", domain: "rack"},
		{name: "well-known domain: host", domain: "host"},
		{name: "custom domain: nvl-block", domain: "nvl-block"},
		{name: "custom domain: blade", domain: "blade"},
		{name: "custom domain: switch-group", domain: "switch-group"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEmpty(t, string(tc.domain))
		})
	}
}
