//go:build e2e

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

// Package gvk defines canonical GroupVersionKind constants for all CRDs used in e2e tests.
package gvk

import (
	"github.com/ai-dynamo/grove/operator/api/common/constants"
	corev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Grove CRDs — derived from the API package's SchemeGroupVersion and Kind constants.
var (
	PodCliqueSet = corev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodCliqueSet)

	PodClique = corev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodClique)

	PodCliqueScalingGroup = corev1alpha1.SchemeGroupVersion.WithKind(constants.KindPodCliqueScalingGroup)

	// PodGang belongs to the scheduler API group which has no register.go in this repo.
	PodGang = schema.GroupVersionKind{
		Group:   "scheduler.grove.io",
		Version: "v1alpha1",
		Kind:    "PodGang",
	}
)

// ComputeDomain Third-party CRDs used in e2e tests.
var (
	ComputeDomain = schema.GroupVersionKind{
		Group:   mnnvl.ComputeDomainGroup,
		Version: mnnvl.ComputeDomainVersion,
		Kind:    mnnvl.ComputeDomainKind,
	}
)
