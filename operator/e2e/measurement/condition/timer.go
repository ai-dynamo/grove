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

package condition

import (
	"context"
	"fmt"
	"time"
)

// TimerCondition is met once Duration has elapsed since the first call to Met. It is used
// to keep a measurement window open for a fixed duration after a trigger phase fires.
type TimerCondition struct {
	Duration time.Duration

	startOnce bool
	startedAt time.Time
}

// Met implements measurement.MilestoneCondition. It returns true once Duration has
// elapsed since the first Met call. The ctx and error return are unused but required
// by the interface.
func (c *TimerCondition) Met(_ context.Context) (bool, error) {
	if !c.startOnce {
		c.startedAt = time.Now()
		c.startOnce = true
	}
	return time.Since(c.startedAt) >= c.Duration, nil
}

// Progress implements measurement.ProgressReporter and reports the time remaining
// until the timer fires. The ctx is unused but required by the interface.
func (c *TimerCondition) Progress(_ context.Context) string {
	if !c.startOnce {
		return fmt.Sprintf("%s remaining", c.Duration)
	}
	remaining := c.Duration - time.Since(c.startedAt)
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("%s remaining", remaining.Round(time.Second))
}
