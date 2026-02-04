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

// Package mnnvl provides utilities and constants for Multi-Node NVLink (MNNVL) support.
package mnnvl

import "k8s.io/apimachinery/pkg/runtime/schema"

// Constants for NVIDIA ComputeDomain (used by MNNVL support)
const (
	// ComputeDomainGroup is the API group for NVIDIA ComputeDomain resources.
	ComputeDomainGroup = "resource.nvidia.com"
	// ComputeDomainVersion is the API version for NVIDIA ComputeDomain resources.
	ComputeDomainVersion = "v1beta1"
	// ComputeDomainKind is the Kind for NVIDIA ComputeDomain resources.
	ComputeDomainKind = "ComputeDomain"
	// ComputeDomainResource is the plural resource name for ComputeDomain.
	ComputeDomainResource = "computedomains"
	// ComputeDomainCRDName is the full CRD name for ComputeDomain.
	ComputeDomainCRDName = ComputeDomainResource + "." + ComputeDomainGroup
)

// ComputeDomainGVK is the GroupVersionKind for NVIDIA ComputeDomain resources.
var ComputeDomainGVK = schema.GroupVersionKind{
	Group:   ComputeDomainGroup,
	Version: ComputeDomainVersion,
	Kind:    ComputeDomainKind,
}

// MNNVL annotation, finalizer, and resource claim constants
const (
	// AnnotationAutoMNNVL is the annotation key used to indicate whether automatic MNNVL
	// support should be enabled for a PodCliqueSet. Valid values are AnnotationAutoMNNVLEnabled
	// and AnnotationAutoMNNVLDisabled.
	AnnotationAutoMNNVL = "grove.io/auto-mnnvl"

	// AnnotationAutoMNNVLEnabled is the value for AnnotationAutoMNNVL indicating that
	// automatic MNNVL support should be enabled. The operator will automatically create
	// and manage ComputeDomain resources for the workload.
	AnnotationAutoMNNVLEnabled = "enabled"

	// AnnotationAutoMNNVLDisabled is the value for AnnotationAutoMNNVL indicating that
	// automatic MNNVL support should be disabled.
	AnnotationAutoMNNVLDisabled = "disabled"

	// FinalizerComputeDomain is the finalizer added to ComputeDomains to prevent accidental
	// deletion while workloads are using them. This finalizer is removed by the PCS controller
	// during scale-in or PCS deletion.
	FinalizerComputeDomain = "grove.io/computedomain-finalizer"

	// MNNVLClaimName is the name used for the MNNVL resource claim in pod specs.
	MNNVLClaimName = "mnnvl-claim"
)
