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

	"google.golang.org/protobuf/encoding/protowire"
)

const selectMergeProfilePath = "/querier.v1.QuerierService/SelectMergeProfile"

// marshalSelectMergeRequest encodes a SelectMergeProfileRequest as binary protobuf.
// The message has only four scalar fields, so protowire encoding is used directly
// instead of a full descriptor/dynamic-message stack.
func marshalSelectMergeRequest(appName string, p ProfileType, from, to time.Time) []byte {
	labelSelector := fmt.Sprintf(`{service_name="%s"}`, appName)

	var b []byte
	// field 1: profile_typeID (string)
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, p.QueryPrefix())
	// field 2: label_selector (string)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, labelSelector)
	// field 3: start (int64, milliseconds)
	b = protowire.AppendTag(b, 3, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(from.UnixMilli()))
	// field 4: end (int64, milliseconds)
	b = protowire.AppendTag(b, 4, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(to.UnixMilli()))
	return b
}

// buildURL returns the full SelectMergeProfile endpoint URL.
func buildURL(baseURL string) string {
	return baseURL + selectMergeProfilePath
}
