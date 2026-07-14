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
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ai-dynamo/grove/operator/internal/constants"
)

const (
	defaultNoopSuccessEventInterval = 5 * time.Minute
	defaultNoopSuccessEventBurst    = 1
)

// RateLimitRule describes how events of a given type (and optional reasons) are throttled per object.
type RateLimitRule struct {
	EventType string
	Reasons   []string
	Interval  time.Duration
	Burst     int
}

// Config holds rate-limiting rules applied by a Client before delegating to the underlying recorder.
type Config struct {
	Rules []RateLimitRule
}

// DefaultConfig returns rate-limiting rules for steady-state CreateOrPatch success events.
func DefaultConfig() Config {
	return Config{
		Rules: []RateLimitRule{
			{
				EventType: corev1.EventTypeNormal,
				Reasons: []string{
					constants.ReasonPodCliqueCreateOrUpdateSuccessful,
					constants.ReasonPodCliqueScalingGroupCreateSuccessful,
					constants.ReasonPodGangCreateOrUpdateSuccessful,
					constants.ReasonComputeDomainCreateSuccessful,
				},
				Interval: defaultNoopSuccessEventInterval,
				Burst:    defaultNoopSuccessEventBurst,
			},
		},
	}
}
