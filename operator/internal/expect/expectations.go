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

package expect

import (
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
)

// ExpectationsStore is a cache where add and delete expectations are captured.
// `controller-runtime` serves the read requests from an informer cache and will query the kube-apiserver
// during startup or resyncs. The informer cache does not provide read-your-writes consistency which leads to stale
// caches. Informer caches are eventually consistent but if your controller gets quick events then it is possible
// that the controller operates on a stale state, thus leading to side effects which could include creation of adding
// resource replicas or deletion of more than desired replicas of a resource.
// NOTE: Expectations is an existing pattern already used in kubernetes.
// See [ControllerExpectationsInterface]: https://github.com/kubernetes/kubernetes/blob/e6161070d4416f6d9c1ac9961029fdceef5c9286/pkg/controller/controller_utils.go#L157
// where expectations are used to provide a barrier when reconciling resources. If the previous expectations are not fulfilled, or they have not yet expired
// no further resource creation/deletion will be done.
// This implementation takes inspiration from it but takes a different take on how resources are tracked.
// We propose to not use expectations as a barrier but only as an additional in-memory store of expectations that help
// the reconciler correctly compute the desired number of creates or deletes.
// It also attempts to resolve the 2 issues that are listed in https://github.com/kubernetes/kubernetes/issues/129795#issuecomment-2657716713
type ExpectationsStore struct {
	cache.Indexer
	mu sync.RWMutex
}

// ControlleeExpectations tracks expectations for the reconciled/controlled resource.
// It will track the object UIDs for expected creations and deletions.
// NOTE: When creating a resource, UID of an object is only available post the Create call. It is expected that the
// consumers will capture the expectations after every Create call. For deletes the UID is already available before the
// call to delete is made.
type ControlleeExpectations struct {
	// key is the unique identifier for the resource for which expectations are captured.
	key string
	// uidsToDelete are the set of resource UIDs that are expected to be deleted.
	uidsToDelete sets.Set[types.UID]
	// uidsToAdd are the set of resource UIDs that are expected to be created.
	uidsToAdd sets.Set[types.UID]
}

// Key returns the storage key this expectation is recorded under. It is the same key
// the consumer supplied when raising create/delete expectations. The store treats the key
// as an opaque string and imposes no structure on it.
//
// This accessor exists because a cache.IndexFunc registered on the store is invoked with
// the stored expectation as an opaque value. A consumer that defines its cache.IndexFunc
// in its own package can only read the exported surface of this type. This accessor allows
// cache.IndexFunc to read the key.
// As an example: A consumer that keys by "<namespace>/<name>/<sub-scope>", for instance,
// reads Key in its cache.IndexFunc and returns the "<namespace>/<name>" prefix as the group,
// then clears a whole group with DeleteExpectationsByIndex.
//
// Keeping the derivation in the consumer keeps the store agnostic to the key's structure.
func (e *ControlleeExpectations) Key() string {
	return e.key
}

// deepCopy returns a copy of the expectations with cloned UID sets, so callers can read the returned
// value without holding the store lock and cannot mutate the store's live sets.
func (e *ControlleeExpectations) deepCopy() *ControlleeExpectations {
	return &ControlleeExpectations{
		key:          e.key,
		uidsToDelete: e.uidsToDelete.Clone(),
		uidsToAdd:    e.uidsToAdd.Clone(),
	}
}

// NewExpectationsStore creates a new expectations store.
func NewExpectationsStore() *ExpectationsStore {
	return &ExpectationsStore{
		Indexer: cache.NewIndexer(getControlleeExpectationsKeyFunc(), cache.Indexers{}),
	}
}

// ExpectCreations records resource creation expectations for uids for a controlled resource identified by a controlleeKey.
func (s *ExpectationsStore) ExpectCreations(logger logr.Logger, controlleeKey string, uids ...types.UID) error {
	return s.createOrRaiseExpectations(logger, controlleeKey, uids, nil)
}

// ExpectDeletions records resource deletion expectations for uids for a controlled resource identified by a controlleeKey.
func (s *ExpectationsStore) ExpectDeletions(logger logr.Logger, controlleeKey string, uids ...types.UID) error {
	return s.createOrRaiseExpectations(logger, controlleeKey, nil, uids)
}

// ObserveDeletions lowers the delete expectations removing the uids passed.
func (s *ExpectationsStore) ObserveDeletions(logger logr.Logger, controlleeKey string, uids ...types.UID) {
	s.lowerExpectations(logger, controlleeKey, nil, uids)
}

// DeleteExpectations removes all expectations stored against a controlleeKey from the store.
func (s *ExpectationsStore) DeleteExpectations(logger logr.Logger, controlleeKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, exists, err := s.GetByKey(controlleeKey)
	if err != nil {
		return err
	}
	if exists {
		if err = s.Delete(exp); err != nil {
			return fmt.Errorf("%w: could not delete expectations for controlleeKey %s", err, controlleeKey)
		}
	}
	logger.Info("Successfully deleted expectations", "controlleeKey", controlleeKey)
	return nil
}

// DeleteExpectationsByIndex removes every expectation grouped under indexedValue in the named index.
func (s *ExpectationsStore) DeleteExpectationsByIndex(logger logr.Logger, indexName, indexedValue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exps, err := s.ByIndex(indexName, indexedValue)
	if err != nil {
		return fmt.Errorf("%w: could not list expectations for index %s=%s", err, indexName, indexedValue)
	}
	for _, exp := range exps {
		if err = s.Delete(exp); err != nil {
			return fmt.Errorf("%w: could not delete expectations for index %s=%s", err, indexName, indexedValue)
		}
	}
	logger.Info("Successfully deleted expectations by index", "indexName", indexName, "indexedValue", indexedValue)
	return nil
}

// GetExpectations returns a copy of the recorded expectations against a controlleeKey. A copy is
// returned so callers never read the store's live UID sets without holding the lock.
func (s *ExpectationsStore) GetExpectations(controlleeKey string) (*ControlleeExpectations, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp, exists, err := s.getExpectations(controlleeKey)
	if err != nil || !exists {
		return nil, false, err
	}
	return exp.deepCopy(), true, nil
}

// getExpectations returns the stored expectations against a controlleeKey. It returns the live object
// whose UID sets are guarded by s.mu, so callers must already hold the lock (read or write).
func (s *ExpectationsStore) getExpectations(controlleeKey string) (*ControlleeExpectations, bool, error) {
	exp, exists, err := s.GetByKey(controlleeKey)
	if err != nil || !exists {
		return nil, false, err
	}
	return exp.(*ControlleeExpectations), true, nil
}

// SyncExpectations allows the expectations store to sync up expectations against the existingNonTerminatingUIDs.
// Informer caches are either updated via watch events or via an explicit sync to the kube-apiserver. Thus, an informer
// cache is a single source of truth. Consumers while computing pending work anyway need to make a LIST call which will
// most probably hit the informer cache. This function removes the need to have `ObserveCreation`
// and there is no longer a need to reduce the expectations from within the event handlers. This function takes in
// an existing resource type.UIDs as seen by the informer cache and removes the expectations that are no longer
// valid in the store.
func (s *ExpectationsStore) SyncExpectations(controlleeKey string, existingNonTerminatingUIDs []types.UID, existingTerminatingUIDs []types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, exists, _ := s.getExpectations(controlleeKey); exists {
		// Remove the UIDs from `uidsToAdd` if the informer cache is already up-to-date and certain events have
		// been missed/dropped by the watch resulting in missed calls to `CreationObserved`.
		exp.uidsToAdd.Delete(existingNonTerminatingUIDs...)
		// Remove stale entries in `uidsToDelete` if the `existingUIDS` no longer has those UIDs.
		staleUIDs := exp.uidsToDelete.Difference(sets.New(existingNonTerminatingUIDs...))
		exp.uidsToDelete.Delete(staleUIDs.UnsortedList()...)
		// Re-add existing termination UIDs if they are not already present in uidsToDelete.
		// This can happen if a resource was terminated (deletionTimestamp is set) but the resource continues to exist
		// due to TerminationGracePeriodSeconds not being reached yet. In the meantime, the operator dies and comes back up.
		// In this case, the expectations store will not have the termination UIDs in uidsToDelete. During the sync
		// we re-add the termination UIDs to uidsToDelete.
		exp.uidsToDelete.Insert(existingTerminatingUIDs...)
	}
}

// GetCreateExpectations is a convenience method which gives a slice of resource UIDs for which creation has not yet been synced
// in the informer cache.
func (s *ExpectationsStore) GetCreateExpectations(controlleeKey string) []types.UID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if exp, exists, _ := s.getExpectations(controlleeKey); exists {
		return exp.uidsToAdd.UnsortedList()
	}
	return nil
}

// GetDeleteExpectations is a convenience method which gives a slice of resource UIDs for which deletion has not yet been synced
// in the informer cache.
func (s *ExpectationsStore) GetDeleteExpectations(controlleeKey string) []types.UID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if exp, exists, _ := s.getExpectations(controlleeKey); exists {
		return exp.uidsToDelete.UnsortedList()
	}
	return nil
}

// HasDeleteExpectation returns true if a delete expectation has been recorded for the controlleeKey and the target resource UID.
func (s *ExpectationsStore) HasDeleteExpectation(controlleeKey string, uid types.UID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if exp, exists, _ := s.getExpectations(controlleeKey); exists {
		return exp.uidsToDelete.Has(uid)
	}
	return false
}

// createOrRaiseExpectations creates or raises create/delete expectations for the given controlleeKey.
func (s *ExpectationsStore) createOrRaiseExpectations(logger logr.Logger, controlleeKey string, uidsToAdd, uidsToDelete []types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, exists, err := s.getExpectations(controlleeKey)
	if err != nil {
		return fmt.Errorf("%w: could not capture expectations [uidsToAdd: %v, uidsTodelete:%v] for resource: %v", err, uidsToAdd, controlleeKey, controlleeKey)
	}
	if !exists {
		exp = &ControlleeExpectations{
			key:          controlleeKey,
			uidsToAdd:    sets.New(uidsToAdd...),
			uidsToDelete: sets.New(uidsToDelete...),
		}
		logger.Info("created expectations for controller resource", "controlleeKey", controlleeKey, "uidsToAdd", uidsToAdd, "uidsToDelete", uidsToDelete)
	} else {
		exp.uidsToAdd.Insert(uidsToAdd...)
		// If there are UIDs in uidsToDelete that also have a presence in uidsToAdd then remove these UIDs from uidsToAdd
		// as those add expectations are now no longer valid.
		exp.uidsToAdd.Delete(uidsToDelete...)
		exp.uidsToDelete.Insert(uidsToDelete...)
		logger.Info("raised expectations for controller resource", "controlleeKey", controlleeKey, "uidsToAdd", uidsToAdd, "uidsToDelete", uidsToDelete)
	}
	return s.Add(exp)
}

// lowerExpectations lowers create/delete expectations for the given controlleeKey.
func (s *ExpectationsStore) lowerExpectations(logger logr.Logger, controlleeKey string, addUIDs, deleteUIDs []types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, exists, _ := s.getExpectations(controlleeKey); exists {
		exp.uidsToAdd.Delete(addUIDs...)
		exp.uidsToAdd.Delete(deleteUIDs...)
		exp.uidsToDelete.Delete(deleteUIDs...)
		logger.Info("lowered expectations for controlled resource", "controlleeKey", controlleeKey, "addUIDs", addUIDs, "deleteUIDs", deleteUIDs)
	}
}

// getControlleeExpectationsKeyFunc creates a cache.KeyFunc required for the cache.Store
// This function is internally used to fetch the ControlleeExpectations object by its key.
func getControlleeExpectationsKeyFunc() cache.KeyFunc {
	return func(obj any) (string, error) {
		if exp, ok := obj.(*ControlleeExpectations); ok {
			return exp.key, nil
		}
		return "", fmt.Errorf("could not find key for obj %#v", obj)
	}
}
