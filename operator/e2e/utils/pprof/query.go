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

package pprof

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// buildQuery returns the Pyroscope /render URL for the given profile type and time window.
//
// Pure function — no I/O, no state. Unit-testable by value.
//
// Example output:
//
//	http://localhost:4040/pyroscope/render?
//	  query=process_cpu%3A...%7Bservice_name%3D%22grove-operator%22%7D
//	  &from=1741000000&until=1741003600&format=pprof
func buildQuery(baseURL, appName string, p ProfileType, from, to time.Time) string {
	query := fmt.Sprintf(`%s{service_name="%s"}`, p.QueryPrefix(), appName)

	v := url.Values{}
	v.Set("query", query)
	v.Set("from", strconv.FormatInt(from.Unix(), 10))
	v.Set("until", strconv.FormatInt(to.Unix(), 10))
	v.Set("format", "pprof")

	return baseURL + "/pyroscope/render?" + v.Encode()
}
