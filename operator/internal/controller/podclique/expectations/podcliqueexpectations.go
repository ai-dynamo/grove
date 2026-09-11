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
	"strings"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/expect"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

// PodCliqueExpectationsIndexName is the name of the index that groups PodGang-scoped expectations by
// their owning PodClique key.
const PodCliqueExpectationsIndexName = "byPodClique"

// PodGangScopedExpectationsStoreKey returns the storage key expectations for a PodClique's pods that
// belong to podGangName are recorded under. A pod belongs to exactly one PodGang, so scoping the key
// to the PodGang lets a PodClique whose pods span multiple PodGangs track each PodGang's create and
// delete expectations separately. The key has the form "<namespace>/<pclq-name>/<podGangName>".
func PodGangScopedExpectationsStoreKey(pclqObjMeta metav1.ObjectMeta, podGangName string) (string, error) {
	pclqKey, err := cache.MetaNamespaceKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: pclqObjMeta})
	if err != nil {
		return "", err
	}
	return pclqKey + "/" + podGangName, nil
}

// PodCliqueExpectationsIndexers returns the indexers registered on the expectations store so every
// PodGang-scoped expectation is grouped under its owning PodClique key. This lets a PodClique's
// expectations be cleared in one call regardless of how many PodGangs its pods span.
func PodCliqueExpectationsIndexers() cache.Indexers {
	return cache.Indexers{
		PodCliqueExpectationsIndexName: indexExpectationByPodClique,
	}
}

// ClearPodCliqueExpectations removes every PodGang-scoped expectation recorded for the PodClique.
func ClearPodCliqueExpectations(logger logr.Logger, store *expect.ExpectationsStore, pclqObjMeta metav1.ObjectMeta) error {
	groupKey, err := cache.MetaNamespaceKeyFunc(&grovecorev1alpha1.PodClique{ObjectMeta: pclqObjMeta})
	if err != nil {
		return err
	}
	return store.DeleteExpectationsByIndex(logger, PodCliqueExpectationsIndexName, groupKey)
}

// ObservePodDeletion lowers the delete expectation for a deleted pod under the PodGang-scoped key its
// owning PodClique recorded it under. podGangName is the pod's grove.io/podgang label value.
func ObservePodDeletion(logger logr.Logger, store *expect.ExpectationsStore, pclqObjMeta metav1.ObjectMeta, podGangName string, podUID types.UID) error {
	key, err := PodGangScopedExpectationsStoreKey(pclqObjMeta, podGangName)
	if err != nil {
		return err
	}
	store.ObserveDeletions(logger, key, podUID)
	return nil
}

// indexExpectationByPodClique derives the owning PodClique key of a PodGang-scoped expectation by
// dropping the trailing PodGang segment of its storage key. A key without a PodGang segment is
// returned unchanged.
func indexExpectationByPodClique(obj any) ([]string, error) {
	exp, ok := obj.(*expect.ControlleeExpectations)
	if !ok {
		return nil, fmt.Errorf("unexpected object type %T in PodClique expectations indexer", obj)
	}
	key := exp.Key()
	if idx := strings.LastIndex(key, "/"); idx >= 0 {
		return []string{key[:idx]}, nil
	}
	return []string{key}, nil
}
