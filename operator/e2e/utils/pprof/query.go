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
	"time"
)

const selectMergeProfilePath = "/querier.v1.QuerierService/SelectMergeProfile"

// selectMergeRequest is the JSON body for the Pyroscope SelectMergeProfile Connect RPC endpoint.
type selectMergeRequest struct {
	ProfileTypeID string `json:"profileTypeID"`
	LabelSelector string `json:"labelSelector"`
	Start         int64  `json:"start"`
	End           int64  `json:"end"`
}

// buildRequest returns a selectMergeRequest for the given profile type and time window.
//
// Pure function — no I/O, no state. Unit-testable by value.
// Filters by service_name which Alloy sets as "namespace/pod-name" on scraped targets.
func buildRequest(appName string, p ProfileType, from, to time.Time) selectMergeRequest {
	return selectMergeRequest{
		ProfileTypeID: p.QueryPrefix(),
		LabelSelector: fmt.Sprintf(`{service_name="%s"}`, appName),
		Start:         from.UnixMilli(),
		End:           to.UnixMilli(),
	}
}

// buildURL returns the full SelectMergeProfile endpoint URL.
func buildURL(baseURL string) string {
	return baseURL + selectMergeProfilePath
}
