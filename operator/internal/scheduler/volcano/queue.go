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

package volcano

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// QueueAnnotationKey is the workload-scoped annotation used to select a Volcano queue.
	QueueAnnotationKey = "scheduling.grove.io/volcano-queue"
	// SubGroupAnnotationKey is the pod-scoped annotation used to identify the Volcano subgroup/task within a gang.
	SubGroupAnnotationKey = "scheduling.grove.io/volcano-subgroup"
	// DefaultQueue is the default Volcano queue used when no queue annotation is specified.
	DefaultQueue = "default"
)

// ErrConflictingQueueAnnotations indicates that global and clique-level queue annotations disagree.
var ErrConflictingQueueAnnotations = errors.New("conflicting volcano queue annotations")

// EffectiveQueueFromAnnotations returns the effective Volcano queue for the given annotations.
// If the queue annotation is missing or empty, it defaults to "default".
func EffectiveQueueFromAnnotations(annotations map[string]string) string {
	if annotations == nil {
		return DefaultQueue
	}
	queue := strings.TrimSpace(annotations[QueueAnnotationKey])
	if queue == "" {
		return DefaultQueue
	}
	return queue
}

// ResolvePodCliqueQueue returns the effective queue for a PodClique template/object after
// applying the global object annotations and the clique-local annotations.
// If both define the queue annotation, they must match.
func ResolvePodCliqueQueue(globalAnnotations, cliqueAnnotations map[string]string) (string, error) {
	globalQueue := strings.TrimSpace(globalAnnotations[QueueAnnotationKey])
	cliqueQueue := strings.TrimSpace(cliqueAnnotations[QueueAnnotationKey])

	switch {
	case globalQueue == "" && cliqueQueue == "":
		return DefaultQueue, nil
	case globalQueue == "":
		return cliqueQueue, nil
	case cliqueQueue == "":
		return globalQueue, nil
	case globalQueue != cliqueQueue:
		return "", ErrConflictingQueueAnnotations
	default:
		return globalQueue, nil
	}
}

// ValidateQueueName validates the effective Volcano queue name.
func ValidateQueueName(queue string) []string {
	return validation.IsDNS1123Subdomain(strings.TrimSpace(queue))
}

// ValidateQueueExistsAndIsOpen verifies that the referenced Volcano queue exists and is Open.
func ValidateQueueExistsAndIsOpen(ctx context.Context, k8sClient client.Reader, queue string) error {
	if k8sClient == nil {
		return nil
	}

	queueName := strings.TrimSpace(queue)
	if queueName == "" {
		queueName = DefaultQueue
	}

	volcanoQueue := &volcanov1beta1.Queue{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: queueName}, volcanoQueue); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("volcano queue %q does not exist", queueName)
		}
		return fmt.Errorf("failed to get volcano queue %q: %w", queueName, err)
	}

	if volcanoQueue.Status.State != volcanov1beta1.QueueStateOpen {
		return fmt.Errorf("volcano queue %q is not Open", queueName)
	}

	return nil
}
