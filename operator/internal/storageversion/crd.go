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

package storageversion

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetStorageVersion makes version the storage version of the CRDs named in crdNames. Every version stays
// served, and the conversion webhook is left in place, so objects remain readable at every version.
func SetStorageVersion(ctx context.Context, cl client.Client, log logr.Logger, crdNames []string, version string) error {
	for _, name := range crdNames {
		if err := updateCRD(ctx, cl, name, func(crd *apiextensionsv1.CustomResourceDefinition) (bool, error) {
			found := false
			changed := false
			for i := range crd.Spec.Versions {
				v := &crd.Spec.Versions[i]
				wanted := v.Name == version
				if wanted {
					found = true
					if !v.Served {
						return false, fmt.Errorf("CRD %q does not serve %q", name, version)
					}
				}
				if v.Storage != wanted {
					v.Storage = wanted
					changed = true
				}
			}
			if !found {
				return false, fmt.Errorf("CRD %q has no version %q", name, version)
			}
			return changed, nil
		}); err != nil {
			return err
		}
		log.Info("Set the storage version of CRD", "crd", name, "version", version)
	}
	return nil
}

// RemoveVersion removes version from the CRDs named in crdNames. A CRD left with a single version no longer
// needs conversion, so its conversion webhook is removed as well. version must no longer be listed in the
// status.storedVersions of the CRDs; Migrate to another version first.
func RemoveVersion(ctx context.Context, cl client.Client, log logr.Logger, crdNames []string, version string) error {
	for _, name := range crdNames {
		if err := updateCRD(ctx, cl, name, func(crd *apiextensionsv1.CustomResourceDefinition) (bool, error) {
			if slices.Contains(crd.Status.StoredVersions, version) {
				return false, fmt.Errorf("CRD %q still has objects stored at %q", name, version)
			}
			versions := slices.DeleteFunc(slices.Clone(crd.Spec.Versions), func(v apiextensionsv1.CustomResourceDefinitionVersion) bool {
				return v.Name == version
			})
			changed := len(versions) != len(crd.Spec.Versions)
			crd.Spec.Versions = versions
			if len(versions) == 1 && crd.Spec.Conversion != nil && crd.Spec.Conversion.Strategy != apiextensionsv1.NoneConverter {
				crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}
				changed = true
			}
			return changed, nil
		}); err != nil {
			return err
		}
		log.Info("Removed version from CRD", "crd", name, "version", version)
	}
	return nil
}

// updateCRD applies mutate to the CRD and updates it when mutate reports a change, retrying on conflicts.
func updateCRD(ctx context.Context, cl client.Client, name string, mutate func(*apiextensionsv1.CustomResourceDefinition) (bool, error)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		crd, err := getCRD(ctx, cl, name)
		if err != nil {
			return err
		}
		changed, err := mutate(crd)
		if err != nil || !changed {
			return err
		}
		u, err := toUnstructured(crd)
		if err != nil {
			return err
		}
		if err := cl.Update(ctx, u); err != nil {
			return fmt.Errorf("failed to update CRD %q: %w", name, err)
		}
		return nil
	})
}
