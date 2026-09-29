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

package kubernetes

import apierrors "k8s.io/apimachinery/pkg/api/errors"

// IsRetriableAPIError reports whether an API server error is worth retrying. It returns true for
// optimistic-lock conflicts and transient server-side failures such as timeouts, throttling,
// unavailability and internal errors. Permanent errors such as NotFound, Forbidden and Invalid
// return false so callers surface them immediately.
func IsRetriableAPIError(err error) bool {
	return apierrors.IsConflict(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err)
}
