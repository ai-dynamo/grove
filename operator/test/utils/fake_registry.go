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

package utils

import "github.com/ai-dynamo/grove/operator/internal/scheduler"

// FakeRegistry is a test helper implementing scheduler.Registry.
// Set Backends and DefaultBackend fields directly for the desired test scenario.
type FakeRegistry struct {
	Backends       map[string]scheduler.Backend
	DefaultBackend scheduler.Backend
}

var _ scheduler.Registry = (*FakeRegistry)(nil)

// Get returns the backend registered under name, or nil if not found.
func (r *FakeRegistry) Get(name string) scheduler.Backend {
	if r.Backends == nil {
		return nil
	}
	return r.Backends[name]
}

// GetDefault returns the default backend.
func (r *FakeRegistry) GetDefault() scheduler.Backend {
	return r.DefaultBackend
}
