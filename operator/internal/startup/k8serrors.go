// /*
// Copyright 2025 The Grove Authors.
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

package startup

import (
	"errors"
	"io"
	"net"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// IsTransientAPIError reports whether err is a transient error that is safe to retry.
// It covers both Kubernetes API-level errors (5xx, timeouts) and transport-level
// errors (connection refused, EOF, net timeout) that surface before a StatusError
// is returned — e.g. during an API-server rolling restart or network partition.
// Permanent errors (Forbidden, Unauthorized) return false.
func IsTransientAPIError(err error) bool {
	if apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsTooManyRequests(err) {
		return true
	}
	// Transport-level transient errors.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNREFUSED)
}
