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

package expectations

import (
	"fmt"
	"sync"

	"github.com/ai-dynamo/grove/operator/internal/utils"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GenerationHashes tracks generation hashes that must appear in the informer cache
// before the next PodCliqueSet reconciliation proceeds. Its zero value is ready to use.
// It is safe for concurrent use and must not be copied after first use.
type GenerationHashes struct {
	expectations sync.Map
}

// Record stores the generation hashes that must be observed through the informer cache.
// It copies the optional applied scheduling hash. Nil expects that status field to be absent.
func (e *GenerationHashes) Record(pcsObjectMeta metav1.ObjectMeta, currentGenerationHash string, appliedSchedulingConfigurationHash *string) {
	expected := &generationHashesExpectation{currentGenerationHash: currentGenerationHash}
	if appliedSchedulingConfigurationHash != nil {
		expected.appliedSchedulingConfigurationHash = new(*appliedSchedulingConfigurationHash)
	}
	e.expectations.Store(generationHashesExpectationKey(pcsObjectMeta), expected)
}

// ClearIfSatisfied reports whether no expectation exists or the cached hashes match it.
// A matching expectation is removed only if no newer Record has replaced it.
func (e *GenerationHashes) ClearIfSatisfied(pcsObjectMeta metav1.ObjectMeta, currentGenerationHash, appliedSchedulingConfigurationHash *string) bool {
	expectationKey := generationHashesExpectationKey(pcsObjectMeta)
	storedExpectation, found := e.expectations.Load(expectationKey)
	if !found {
		return true
	}
	expected := storedExpectation.(*generationHashesExpectation)
	if currentGenerationHash == nil || *currentGenerationHash != expected.currentGenerationHash ||
		!utils.EqualOptional(appliedSchedulingConfigurationHash, expected.appliedSchedulingConfigurationHash) {
		return false
	}
	return e.expectations.CompareAndDelete(expectationKey, storedExpectation)
}

// Forget removes the generation-hash expectation for the PodCliqueSet.
func (e *GenerationHashes) Forget(pcsObjectMeta metav1.ObjectMeta) {
	e.expectations.Delete(generationHashesExpectationKey(pcsObjectMeta))
}

// generationHashesExpectation is an immutable snapshot. Pointer identity prevents an acknowledgment
// of an older snapshot from removing a newer expectation, even when the hashes are equal.
type generationHashesExpectation struct {
	currentGenerationHash              string
	appliedSchedulingConfigurationHash *string
}

// generationHashesExpectationKey includes the UID so a deleted PCS cannot block a new PCS with the same name.
// See https://github.com/ai-dynamo/grove/issues/782 for context.
func generationHashesExpectationKey(pcsObjectMeta metav1.ObjectMeta) string {
	return fmt.Sprintf("%s/%s/%s", pcsObjectMeta.Namespace, pcsObjectMeta.Name, pcsObjectMeta.UID)
}
