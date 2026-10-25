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

// Package storageversion moves the objects of the grove.io CRDs from one stored API version to another.
// The API server encodes an object at the storage version of its CRD only when the object is written, so
// objects written before the storage version changed stay encoded at the earlier version until they are
// rewritten. Once every object is rewritten, the earlier version can be removed from status.storedVersions,
// which the API server requires before that version can be removed from the CRD.
package storageversion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	k8sutils "github.com/ai-dynamo/grove/operator/internal/utils/kubernetes"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// listPageSize bounds the number of objects read per list request, so that large installations are not
// read in a single response.
var listPageSize int64 = 500

// CRDNames lists the grove.io CRDs whose objects are migrated.
var CRDNames = []string{
	"podcliquesets.grove.io",
	"podcliques.grove.io",
	"podcliquescalinggroups.grove.io",
	"clustertopologybindings.grove.io",
	"podgangmaps.grove.io",
}

// ErrIncomplete is returned when some objects were rejected when rewritten, for example by an admission
// webhook. Their CRDs keep the earlier version in status.storedVersions, so no data becomes unreadable.
var ErrIncomplete = errors.New("some objects could not be rewritten at the storage version")

var (
	crdGVK         = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
	rewriteBackoff = wait.Backoff{Steps: 5, Duration: 100 * time.Millisecond, Factor: 2.0, Jitter: 0.1}
)

// Migrate rewrites every object of the CRDs named in crdNames so that it is stored at version, then sets the
// status.storedVersions of each CRD to [version]. version must already be the storage version of the CRDs. A
// CRD whose status.storedVersions is already [version] is skipped, so Migrate is idempotent. Errors of one
// CRD do not stop the migration of the others. An error wraps ErrIncomplete when the only failures are
// objects that were rejected when rewritten, which retrying does not fix.
func Migrate(ctx context.Context, cl client.Client, log logr.Logger, crdNames []string, version string) error {
	var errs, rejections []error
	for _, name := range crdNames {
		if err := migrateCRD(ctx, cl, log, name, version); err != nil {
			if errors.Is(err, errRejected) {
				rejections = append(rejections, err)
			} else {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(append(errs, rejections...)...)
	}
	if len(rejections) > 0 {
		return fmt.Errorf("%w: %w", ErrIncomplete, errors.Join(rejections...))
	}
	return nil
}

// errRejected marks the error of a CRD whose only failures are rejected objects.
var errRejected = errors.New("objects were rejected")

func migrateCRD(ctx context.Context, cl client.Client, log logr.Logger, name, version string) error {
	crd, err := getCRD(ctx, cl, name)
	if err != nil {
		return err
	}
	storage := storageVersion(crd)
	if storage != version {
		return fmt.Errorf("CRD %q stores %q, not %q", name, storage, version)
	}
	if slices.Equal(crd.Status.StoredVersions, []string{version}) {
		log.V(1).Info("Objects of CRD are already stored at the storage version", "crd", name, "version", version)
		setMigrated(name, version, true)
		return nil
	}
	setMigrated(name, version, false)
	log.Info("Migrating the objects of CRD to the storage version", "crd", name, "version", version,
		"storedVersions", crd.Status.StoredVersions)

	gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: version, Kind: crd.Spec.Names.Kind}
	rewritten, rejected, err := rewriteAll(ctx, cl, log, gvk)
	objectsRewrittenTotal.WithLabelValues(name, version).Add(float64(rewritten))
	objectsRejectedTotal.WithLabelValues(name, version).Add(float64(len(rejected)))
	if err != nil {
		return fmt.Errorf("failed to rewrite the objects of CRD %q: %w", name, err)
	}
	if len(rejected) > 0 {
		return fmt.Errorf("%w: CRD %q keeps storedVersions %v, rejected objects: %v", errRejected, name,
			crd.Status.StoredVersions, rejected)
	}

	if err := setStoredVersions(ctx, cl, name, version); err != nil {
		return err
	}
	setMigrated(name, version, true)
	log.Info("Migrated the objects of CRD to the storage version", "crd", name, "version", version, "rewritten", rewritten)
	return nil
}

// rewriteAll writes every object of gvk back unchanged, which makes the API server encode it at the storage
// version. It returns the number of objects rewritten and the keys of the objects that were rejected.
func rewriteAll(ctx context.Context, cl client.Client, log logr.Logger, gvk schema.GroupVersionKind) (int, []string, error) {
	rewritten := 0
	var rejected []string
	continueToken := ""
	for {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := cl.List(ctx, list, client.Limit(listPageSize), client.Continue(continueToken)); err != nil {
			return rewritten, rejected, fmt.Errorf("failed to list %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			key := client.ObjectKeyFromObject(obj).String()
			if err := rewrite(ctx, cl, obj); err != nil {
				if k8sutils.IsRetriableAPIError(err) {
					return rewritten, rejected, fmt.Errorf("failed to rewrite %s %s: %w", gvk.Kind, key, err)
				}
				log.Error(err, "Object was rejected when rewritten at the storage version", "kind", gvk.Kind, "object", key)
				rejected = append(rejected, key)
				continue
			}
			rewritten++
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return rewritten, rejected, nil
		}
	}
}

// rewrite updates obj without changes. On a conflict the object was written since it was listed, which
// already encoded it at the storage version, but it is read and written again to be independent of when the
// storage version change reached every API server. An object deleted since it was listed is skipped.
func rewrite(ctx context.Context, cl client.Client, obj *unstructured.Unstructured) error {
	key := client.ObjectKeyFromObject(obj)
	current := obj
	err := retry.OnError(rewriteBackoff, k8sutils.IsRetriableAPIError, func() error {
		if current == nil {
			latest := &unstructured.Unstructured{}
			latest.SetGroupVersionKind(obj.GroupVersionKind())
			if err := cl.Get(ctx, key, latest); err != nil {
				return err
			}
			current = latest
		}
		err := cl.Update(ctx, current)
		if apierrors.IsConflict(err) {
			current = nil
		}
		return err
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// setStoredVersions sets status.storedVersions of the CRD to [version].
func setStoredVersions(ctx context.Context, cl client.Client, name, version string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		crd, err := getCRD(ctx, cl, name)
		if err != nil {
			return err
		}
		if storage := storageVersion(crd); storage != version {
			return fmt.Errorf("CRD %q stores %q, not %q", name, storage, version)
		}
		crd.Status.StoredVersions = []string{version}
		u, err := toUnstructured(crd)
		if err != nil {
			return err
		}
		if err := cl.Status().Update(ctx, u); err != nil {
			return fmt.Errorf("failed to set status.storedVersions of CRD %q: %w", name, err)
		}
		return nil
	})
}

func storageVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return v.Name
		}
	}
	return ""
}

// getCRD reads the CRD as unstructured, so that the client scheme does not need the apiextensions types.
func getCRD(ctx context.Context, cl client.Client, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, u); err != nil {
		return nil, fmt.Errorf("failed to get CRD %q: %w", name, err)
	}
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, crd); err != nil {
		return nil, fmt.Errorf("failed to decode CRD %q: %w", name, err)
	}
	return crd, nil
}

func toUnstructured(crd *apiextensionsv1.CustomResourceDefinition) (*unstructured.Unstructured, error) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(crd)
	if err != nil {
		return nil, fmt.Errorf("failed to encode CRD %q: %w", crd.Name, err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(crdGVK)
	return u, nil
}
