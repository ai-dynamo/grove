//go:build e2e

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

// Package kwok provides helpers to manage KWOK resources used by e2e tests, such as the Stage
// custom resources that shape simulated pod lifecycle behaviour.
package kwok

import (
	"context"
	"fmt"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// stageGVK is the GroupVersionKind of a KWOK Stage.
var stageGVK = schema.GroupVersionKind{Group: "kwok.x-k8s.io", Version: "v1alpha1", Kind: "Stage"}

// ApplyStage applies a KWOK Stage manifest file to the cluster. An existing stage of the same name
// is left in place.
func ApplyStage(ctx context.Context, cl client.Client, yamlPath string) error {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("failed to read KWOK stage %s: %w", yamlPath, err)
	}
	stage := &unstructured.Unstructured{}
	if err := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096).Decode(stage); err != nil {
		return fmt.Errorf("failed to decode KWOK stage %s: %w", yamlPath, err)
	}
	if err := cl.Create(ctx, stage); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to apply KWOK stage %s: %w", yamlPath, err)
	}
	return nil
}

// DeleteStage deletes a KWOK Stage by name. A missing stage is not treated as an error so a caller
// can delete the stage explicitly and still defer a cleanup delete.
func DeleteStage(ctx context.Context, cl client.Client, stageName string) error {
	stage := &unstructured.Unstructured{}
	stage.SetGroupVersionKind(stageGVK)
	stage.SetName(stageName)
	if err := cl.Delete(ctx, stage); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
