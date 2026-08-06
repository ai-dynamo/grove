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

package utils

import "sync"

// KeyedMutex serializes work sharing the same comparable key while allowing work on different keys to proceed concurrently.
// Its zero value is ready for use.
type KeyedMutex[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

// Lock acquires the mutex associated with key and returns its unlock function.
// The returned function must be called exactly once.
func (m *KeyedMutex[K]) Lock(key K) func() {
	m.mu.Lock()
	if m.entries == nil {
		m.entries = make(map[K]*keyedMutexEntry)
	}
	entry := m.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		m.entries[key] = entry
	}
	// Count both the current holder and waiters so an entry cannot be removed
	// while another goroutine is waiting to acquire it.
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		m.mu.Lock()
		defer m.mu.Unlock()
		entry.refs--
		if entry.refs == 0 && m.entries[key] == entry {
			delete(m.entries, key)
		}
	}
}
