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

package utils

import (
	"fmt"
	"strconv"
	"strings"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPodCliqueSetReplicaIndexFromPodCliqueFQN extracts the PodCliqueSet replica index from a Pod Clique FQN name.
func GetPodCliqueSetReplicaIndexFromPodCliqueFQN(pcsName, pclqFQNName string) (int, error) {
	replicaStartIndex := len(pcsName) + 1 // +1 for the hyphen
	hyphenIndex := strings.Index(pclqFQNName[replicaStartIndex:], "-")
	if hyphenIndex == -1 {
		return -1, fmt.Errorf("PodClique FQN is not in the expected format of <pcs-name>-<pcs-replica-index>-<pclq-template-name>: %s", pclqFQNName)
	}
	replicaEndIndex := replicaStartIndex + hyphenIndex
	return strconv.Atoi(pclqFQNName[replicaStartIndex:replicaEndIndex])
}

// GetPodCliqueNameFromPodCliqueFQN extracts the unqualified PodClique name from a fully qualified name.
func GetPodCliqueNameFromPodCliqueFQN(pclqObjectMeta metav1.ObjectMeta) (string, error) {
	pclqObjectKey := client.ObjectKey{Name: pclqObjectMeta.Name, Namespace: pclqObjectMeta.Namespace}
	pcsgName, ok := pclqObjectMeta.Labels[apicommon.LabelPodCliqueScalingGroup]
	if ok {
		// get the pcsg replica index
		pcsgReplicaIndex, replicaIndexLabelFound := pclqObjectMeta.Labels[apicommon.LabelPodCliqueScalingGroupReplicaIndex]
		if !replicaIndexLabelFound {
			return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPodCliqueScalingGroupReplicaIndex, pclqObjectKey)
		}
		pclqNamePrefix := fmt.Sprintf("%s-%s-", pcsgName, pcsgReplicaIndex)
		return pclqObjectMeta.Name[len(pclqNamePrefix):], nil
	}

	pcsName, ok := pclqObjectMeta.Labels[apicommon.LabelPartOfKey]
	if !ok {
		return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPartOfKey, pclqObjectKey)
	}
	// Get the PCS replica index
	pcsReplicaIndex, ok := pclqObjectMeta.Labels[apicommon.LabelPodCliqueSetReplicaIndex]
	if !ok {
		return "", fmt.Errorf("missing label %s on PodClique: %v", apicommon.LabelPodCliqueSetReplicaIndex, pclqObjectKey)
	}
	pclqNamePrefix := fmt.Sprintf("%s-%s-", pcsName, pcsReplicaIndex)
	return pclqObjectMeta.Name[len(pclqNamePrefix):], nil
}

// ExtractPodGangNameSuffix parses the trailing integer segment from a PodGang name.
//
// Under the unified PodGang naming convention introduced for coherent updates, all PodGang
// names follow the shape <pcs>-<replica>-<unix-nano>; the suffix is the unix-nano value.
// Legacy SPG names of the form <pcsg-fqn>-<int> also have an integer trailing segment and
// parse correctly; this function does not interpret the meaning of the suffix.
//
// Returns an error if the name has no trailing '-<integer>' segment — Grove is the sole
// writer of these names so a parse failure indicates a contract violation, not a soft skip.
func ExtractPodGangNameSuffix(podGangName string) (int, error) {
	dash := strings.LastIndex(podGangName, "-")
	if dash < 0 || dash == len(podGangName)-1 {
		return 0, fmt.Errorf("PodGang name %q has no trailing '-<integer>' suffix", podGangName)
	}
	suffix, err := strconv.Atoi(podGangName[dash+1:])
	if err != nil {
		return 0, fmt.Errorf("trailing suffix of PodGang name %q is not an integer: %w", podGangName, err)
	}
	return suffix, nil
}
