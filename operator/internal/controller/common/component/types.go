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

package component

import (
	"context"
	"fmt"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Constants for operations that an Operator can perform.
const (
	// OperationGetExistingResourceNames represents an operation to get existing resource names.
	OperationGetExistingResourceNames = "GetExistingResourceNames"
	// OperationSync represents a sync operation.
	OperationSync = "Sync"
	// OperationDelete represents a delete operation.
	OperationDelete = "Delete"
)

// GroveCustomResourceType defines a type bound for generic types.
type GroveCustomResourceType interface {
	grovecorev1alpha1.PodCliqueSet | grovecorev1alpha1.PodClique | grovecorev1alpha1.PodCliqueScalingGroup
}

// EmptyState is used by reconcilers that do not share reconcile-scoped input with component operators.
type EmptyState struct{}

// NoResult is used by component operators that do not return observations to their reconciler.
type NoResult struct{}

// Operator manages one kind of component resource for a Grove custom resource.
// - T is the custom resource being reconciled.
// - S is reconciler-specific, reconcile-scoped input shared with the component-operators participating in that
// reconciliation.
// - R is the reconciler-specific result produced by an operator after synchronization. Reconcilers that need no
// shared input or result should use EmptyState and NoResult.
type Operator[T GroveCustomResourceType, S any, R any] interface {
	// GetExistingResourceNames returns the names of all the existing resources that this Operator manages.
	GetExistingResourceNames(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) ([]string, error)
	// Sync synchronizes the resources managed by this Operator and returns what it observed. State is
	// supplied by the reconciler and is valid only for the current reconciliation. Implementations may
	// create, update, or delete managed resources to converge them with obj.
	Sync(ctx context.Context, logger logr.Logger, obj *T, state *S) (R, error)
	// Delete triggers the deletion of all resources that this Operator manages.
	Delete(ctx context.Context, logger logr.Logger, objMeta metav1.ObjectMeta) error
}

// Kind represents kind of a resource.
type Kind string

const (
	// KindPodClique indicates that the resource is a PodClique.
	KindPodClique Kind = "PodClique"
	// KindServiceAccount indicates that the resource is a ServiceAccount.
	KindServiceAccount Kind = "ServiceAccount"
	// KindRole indicates that the resource is a Role.
	KindRole Kind = "Role"
	// KindRoleBinding indicates that the resource is a RoleBinding.
	KindRoleBinding Kind = "RoleBinding"
	// KindServiceAccountTokenSecret indicates that the resource is a Secret to generate ServiceAccount token.
	KindServiceAccountTokenSecret Kind = "ServiceAccountTokenSecret"
	// KindHeadlessService indicates that the resource is a headless Service.
	KindHeadlessService Kind = "HeadlessService"
	// KindHorizontalPodAutoscaler indicates that the resource is a HorizontalPodAutoscaler.
	KindHorizontalPodAutoscaler Kind = "HorizontalPodAutoscaler"
	// KindPod indicates that the resource is a Pod.
	KindPod Kind = "Pod"
	// KindPodCliqueScalingGroup indicates that the resource is a PodCliqueScalingGroup.
	KindPodCliqueScalingGroup Kind = "PodCliqueScalingGroup"
	// KindPodGang indicates that the resource is a PodGang.
	KindPodGang Kind = "PodGang"
	// KindPodGangMap indicates that the resource is a PodGangMap.
	KindPodGangMap Kind = "PodGangMap"
	// KindPodCliqueSetReplica indicates that the resource is a PodCliqueSet replica.
	KindPodCliqueSetReplica Kind = "PodCliqueSetReplica"
	// KindComputeDomain indicates that the resource is a ComputeDomain.
	KindComputeDomain Kind = "ComputeDomain"
	// KindResourceClaim indicates that the resource is a DRA ResourceClaim.
	KindResourceClaim Kind = "ResourceClaim"
	// KindPodGangMigrator is an action component (not a managed resource) that migrates a PodCliqueSet
	// from the legacy PodGang naming to the epoch-based PodGang naming and PodGangMap scheme.
	KindPodGangMigrator Kind = "PodGangMigrator"
)

// OperatorRegistry stores the component operators used by one reconciler. T is the reconciled custom resource, S is the
// reconcile-scoped input passed to every registered operator, and R is the result returned by every registered
// operator.
type OperatorRegistry[T GroveCustomResourceType, S any, R any] interface {
	// Register registers a components operator against the kind of components it operates on.
	Register(kind Kind, operator Operator[T, S, R])
	// GetOperator gets a components operator that operates on the given kind.
	GetOperator(kind Kind) (Operator[T, S, R], error)
	// GetAllOperators returns all components operators.
	GetAllOperators() map[Kind]Operator[T, S, R]
}

type _registry[T GroveCustomResourceType, S any, R any] struct {
	operators map[Kind]Operator[T, S, R]
}

// NewOperatorRegistry creates an empty registry whose operators share the same T, S, and R contract.
func NewOperatorRegistry[T GroveCustomResourceType, S any, R any]() OperatorRegistry[T, S, R] {
	return &_registry[T, S, R]{
		operators: make(map[Kind]Operator[T, S, R]),
	}
}

// Register registers an operator with its associated kind in the registry.
func (r *_registry[T, S, R]) Register(kind Kind, operator Operator[T, S, R]) {
	r.operators[kind] = operator
}

// GetOperator gets the operator associated with a kind from the registry.
func (r *_registry[T, S, R]) GetOperator(kind Kind) (Operator[T, S, R], error) {
	operator, ok := r.operators[kind]
	if !ok {
		return nil, fmt.Errorf("operator for kind %s not found", kind)
	}
	return operator, nil
}

// GetAllOperators gets all operators registered.
func (r *_registry[T, S, R]) GetAllOperators() map[Kind]Operator[T, S, R] {
	return r.operators
}
