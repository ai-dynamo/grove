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

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyedMutexSerializesSameKeyAndCleansEntry(t *testing.T) {
	var mutex KeyedMutex[string]
	unlockFirst := mutex.Lock("shared")

	acquired := make(chan struct{})
	release := make(chan struct{})
	go func() {
		unlock := mutex.Lock("shared")
		close(acquired)
		<-release
		unlock()
	}()

	require.Eventually(t, func() bool {
		mutex.mu.Lock()
		defer mutex.mu.Unlock()
		return mutex.entries["shared"].refs == 2
	}, time.Second, time.Millisecond)
	assert.Never(t, func() bool {
		select {
		case <-acquired:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)

	unlockFirst()
	require.Eventually(t, func() bool {
		select {
		case <-acquired:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	close(release)
	require.Eventually(t, func() bool {
		mutex.mu.Lock()
		defer mutex.mu.Unlock()
		return len(mutex.entries) == 0
	}, time.Second, time.Millisecond)
}

func TestKeyedMutexAllowsDifferentKeysConcurrently(t *testing.T) {
	var mutex KeyedMutex[string]
	unlockFirst := mutex.Lock("first")

	acquiredSecond := make(chan struct{})
	releaseSecond := make(chan struct{})
	go func() {
		unlock := mutex.Lock("second")
		close(acquiredSecond)
		<-releaseSecond
		unlock()
	}()

	require.Eventually(t, func() bool {
		select {
		case <-acquiredSecond:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	close(releaseSecond)
	unlockFirst()
	require.Eventually(t, func() bool {
		mutex.mu.Lock()
		defer mutex.mu.Unlock()
		return len(mutex.entries) == 0
	}, time.Second, time.Millisecond)
}
