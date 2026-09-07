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

package kai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const aggregatePrefix = "grove-"

func aggregatePodGroupKey(pcs *grovecorev1alpha1.PodCliqueSet, replica int) client.ObjectKey {
	return client.ObjectKey{Namespace: pcs.Namespace, Name: aggregatePodGroupName(pcs.Name, replica)}
}

func aggregatePodGroupName(pcsName string, replica int) string {
	return stableKAIName(fmt.Sprintf("%s%s-%d", aggregatePrefix, pcsName, replica))
}

func stableKAIName(value string) string {
	lower := strings.ToLower(value)
	var sanitized strings.Builder
	lastDash := false
	for _, char := range lower {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
		if !valid {
			char = '-'
		}
		if char == '-' {
			if lastDash {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		sanitized.WriteRune(char)
	}
	name := strings.Trim(sanitized.String(), "-")
	if name == value && len(name) <= 63 {
		return name
	}
	hash := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(hash[:])[:10]
	if name == "" {
		return "kai-" + suffix
	}
	if len(name) > 52 {
		name = strings.TrimRight(name[:52], "-")
	}
	return name + "-" + suffix
}
