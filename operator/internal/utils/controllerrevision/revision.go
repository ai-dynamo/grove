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

// Package controllerrevision resolves versioned PodCliqueSet templates stored in ControllerRevisions.
package controllerrevision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type revisionHeader struct {
	APIVersion string `json:"apiVersion"`
}

type revisionPayloadV1Alpha1 struct {
	revisionHeader
	Template grovecorev1alpha1.PodCliqueSetTemplateSpec `json:"podCliqueSetTemplateSpec"`
}

// Resolved contains a validated ControllerRevision and its decoded template.
type Resolved struct {
	Name     string
	Revision *appsv1.ControllerRevision
	Template *grovecorev1alpha1.PodCliqueSetTemplateSpec
}

// NormalizeTemplate returns the canonical copy used for revision comparisons.
func NormalizeTemplate(template *grovecorev1alpha1.PodCliqueSetTemplateSpec) *grovecorev1alpha1.PodCliqueSetTemplateSpec {
	return template.DeepCopy()
}

// EncodeTemplate encodes a versioned PodCliqueSet template snapshot.
func EncodeTemplate(template *grovecorev1alpha1.PodCliqueSetTemplateSpec) ([]byte, error) {
	payload := revisionPayloadV1Alpha1{
		revisionHeader: revisionHeader{APIVersion: grovecorev1alpha1.SchemeGroupVersion.String()},
		Template:       *NormalizeTemplate(template),
	}
	data, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("could not encode PodCliqueSet revision: %w", err)
	}
	return data, nil
}

// DecodeTemplate decodes a versioned PodCliqueSet template snapshot.
func DecodeTemplate(data []byte) (*grovecorev1alpha1.PodCliqueSetTemplateSpec, error) {
	var header revisionHeader
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("could not decode revision header: %w", err)
	}
	if header.APIVersion != grovecorev1alpha1.SchemeGroupVersion.String() {
		return nil, fmt.Errorf("unsupported PodCliqueSet revision API version %q", header.APIVersion)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload revisionPayloadV1Alpha1
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("could not decode PodCliqueSet revision: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("PodCliqueSet revision contains trailing data")
	}
	return NormalizeTemplate(&payload.Template), nil
}

// DataMatchesTemplate compares stored revision data with a normalized template.
func DataMatchesTemplate(data []byte, normalizedTemplate *grovecorev1alpha1.PodCliqueSetTemplateSpec) (bool, error) {
	storedTemplate, err := DecodeTemplate(data)
	if err != nil {
		return false, err
	}
	return apiequality.Semantic.DeepEqual(storedTemplate, normalizedTemplate), nil
}

// FindReplicaRevision returns the revision selected for a replica.
func FindReplicaRevision(pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int) (string, bool) {
	if pcs.Status.UpdateProgress == nil {
		return "", false
	}
	for i := range pcs.Status.UpdateProgress.ReplicaRevisions {
		binding := &pcs.Status.UpdateProgress.ReplicaRevisions[i]
		if int(binding.ReplicaIndex) == replicaIndex {
			return binding.Revision, binding.Revision != ""
		}
	}
	return "", false
}

// SetReplicaRevision changes an existing replica binding.
func SetReplicaRevision(pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int, revisionName string) error {
	if pcs.Status.UpdateProgress == nil {
		return fmt.Errorf("PodCliqueSet %s/%s has no update progress", pcs.Namespace, pcs.Name)
	}
	for i := range pcs.Status.UpdateProgress.ReplicaRevisions {
		if int(pcs.Status.UpdateProgress.ReplicaRevisions[i].ReplicaIndex) == replicaIndex {
			pcs.Status.UpdateProgress.ReplicaRevisions[i].Revision = revisionName
			return nil
		}
	}
	return fmt.Errorf("PodCliqueSet %s/%s has no revision binding for replica %d", pcs.Namespace, pcs.Name, replicaIndex)
}

// ResolveForReplica resolves the ControllerRevision selected in PCS status for a replica.
// A stable PodCliqueSet has no active per-replica rollout, so every replica uses UpdateRevision.
// During a rollout, the active replica uses UpdateRevision while all other replicas use their
// durable binding. The active replica's binding is committed after its update completes.
func ResolveForReplica(ctx context.Context, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet, replicaIndex int) (*Resolved, error) {
	if pcs.Status.UpdateProgress == nil || pcs.Status.UpdateProgress.UpdateEndedAt != nil {
		if pcs.Status.UpdateRevision == "" {
			return nil, fmt.Errorf("PodCliqueSet %s/%s has no update ControllerRevision", pcs.Namespace, pcs.Name)
		}
		return Resolve(ctx, cl, pcs, pcs.Status.UpdateRevision)
	}
	for _, progress := range pcs.Status.UpdateProgress.CurrentlyUpdating {
		if int(progress.ReplicaIndex) == replicaIndex && progress.UpdateEndedAt == nil {
			if pcs.Status.UpdateRevision == "" {
				return nil, fmt.Errorf("PodCliqueSet %s/%s has no update ControllerRevision", pcs.Namespace, pcs.Name)
			}
			return Resolve(ctx, cl, pcs, pcs.Status.UpdateRevision)
		}
	}

	revisionName, ok := FindReplicaRevision(pcs, replicaIndex)
	if !ok {
		return nil, fmt.Errorf("PodCliqueSet %s/%s has no ControllerRevision binding for replica %d", pcs.Namespace, pcs.Name, replicaIndex)
	}
	return Resolve(ctx, cl, pcs, revisionName)
}

// Resolve validates ownership and decodes a named ControllerRevision.
func Resolve(ctx context.Context, cl client.Client, pcs *grovecorev1alpha1.PodCliqueSet, revisionName string) (*Resolved, error) {
	revision := &appsv1.ControllerRevision{}
	key := client.ObjectKey{Namespace: pcs.Namespace, Name: revisionName}
	if err := cl.Get(ctx, key, revision); err != nil {
		return nil, fmt.Errorf("could not get ControllerRevision %s: %w", key, err)
	}
	if !metav1.IsControlledBy(revision, pcs) {
		return nil, fmt.Errorf("ControllerRevision %s is not controlled by PodCliqueSet %s/%s", key, pcs.Namespace, pcs.Name)
	}
	template, err := DecodeTemplate(revision.Data.Raw)
	if err != nil {
		return nil, fmt.Errorf("could not decode ControllerRevision %s: %w", key, err)
	}
	return &Resolved{Name: revision.Name, Revision: revision, Template: template}, nil
}

// PodCliqueSetWithTemplate returns a copy of pcs with the supplied revision template.
func PodCliqueSetWithTemplate(pcs *grovecorev1alpha1.PodCliqueSet, template *grovecorev1alpha1.PodCliqueSetTemplateSpec) *grovecorev1alpha1.PodCliqueSet {
	copy := pcs.DeepCopy()
	copy.Spec.Template = *template.DeepCopy()
	return copy
}
