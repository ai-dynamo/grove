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

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// QueueAnnotationKey is the workload-scoped annotation used to select a Volcano queue.
	QueueAnnotationKey = "scheduling.grove.io/volcano-queue"
	// DefaultQueue is the default Volcano queue used when no queue annotation is specified.
	DefaultQueue = "default"
)

// errConflictingQueueAnnotations indicates that global and clique-level queue annotations disagree.
var errConflictingQueueAnnotations = errors.New("conflicting volcano queue annotations")

// effectiveQueueFromAnnotations returns the effective Volcano queue for the given annotations.
// If the queue annotation is missing or empty, it defaults to "default".
func effectiveQueueFromAnnotations(annotations map[string]string) string {
	if annotations == nil {
		return DefaultQueue
	}
	queue := strings.TrimSpace(annotations[QueueAnnotationKey])
	if queue == "" {
		return DefaultQueue
	}
	return queue
}

// resolvePodCliqueQueue returns the effective queue after applying the
// global object queue value and the clique-local queue value.
// If both queue values are set, they must match.
func resolvePodCliqueQueue(globalQueue, cliqueQueue string) (string, error) {
	switch {
	case globalQueue == "" && cliqueQueue == "":
		return DefaultQueue, nil
	case globalQueue == "":
		return cliqueQueue, nil
	case cliqueQueue == "":
		return globalQueue, nil
	case globalQueue != cliqueQueue:
		return "", fmt.Errorf("%w: PodCliqueSet has %q, PodClique has %q", errConflictingQueueAnnotations, globalQueue, cliqueQueue)
	default:
		return globalQueue, nil
	}
}

// validateQueueName validates the effective Volcano queue name.
func validateQueueName(queue string) []string {
	return validation.IsDNS1123Subdomain(queue)
}

func validateQueueAnnotations(ctx context.Context, k8sClient client.Reader, pcs *grovecorev1alpha1.PodCliqueSet) error {
	var errs []error
	pcsQueue := strings.TrimSpace(pcs.Annotations[QueueAnnotationKey])
	if pcsQueue != "" {
		if msgs := validateQueueName(pcsQueue); len(msgs) > 0 {
			return fmt.Errorf("metadata.annotations[%s]: %s", QueueAnnotationKey, strings.Join(msgs, "; "))
		}
	}
	if len(pcs.Spec.Template.Cliques) == 0 {
		return validateQueueExistsAndIsOpen(ctx, k8sClient, effectiveQueueFromAnnotations(pcs.Annotations))
	}

	var resolvedQueue string
	for i, cliqueTemplateSpec := range pcs.Spec.Template.Cliques {
		cliqueQueue := strings.TrimSpace(cliqueTemplateSpec.Annotations[QueueAnnotationKey])
		if cliqueQueue != "" {
			if msgs := validateQueueName(cliqueQueue); len(msgs) > 0 {
				errs = append(errs, fmt.Errorf("spec.template.cliques[%d].annotations[%s]: %s", i, QueueAnnotationKey, strings.Join(msgs, "; ")))
			}
		}

		queue, err := resolvePodCliqueQueue(pcsQueue, cliqueQueue)
		if err != nil {
			errs = append(errs, fmt.Errorf("spec.template.cliques[%d].annotations[%s]: %w", i, QueueAnnotationKey, err))
			continue
		}
		if resolvedQueue == "" {
			resolvedQueue = queue
			continue
		}
		if resolvedQueue != queue {
			errs = append(errs, fmt.Errorf("spec.template.cliques[%d].annotations[%s]: all PodCliques in a PodCliqueSet using volcano scheduler must resolve to the same queue", i, QueueAnnotationKey))
		}
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	return validateQueueExistsAndIsOpen(ctx, k8sClient, resolvedQueue)
}

// validateQueueExistsAndIsOpen verifies that the resolved Volcano queue exists
// in the cluster and that its status is Open before admitting the workload.
func validateQueueExistsAndIsOpen(ctx context.Context, k8sClient client.Reader, queue string) error {
	if queue == "" {
		queue = DefaultQueue
	}

	volcanoQueue := &volcanov1beta1.Queue{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: queue}, volcanoQueue); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("volcano queue %q does not exist", queue)
		}
		return fmt.Errorf("failed to get volcano queue %q: %w", queue, err)
	}

	if volcanoQueue.Status.State != volcanov1beta1.QueueStateOpen {
		return fmt.Errorf("volcano queue %q is not Open", queue)
	}

	return nil
}
