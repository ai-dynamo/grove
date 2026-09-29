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

//go:build e2e

package podgangmap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	commonapi "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/e2e/log"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Verifier provides capabilities to verify PodGangMap resources.
type Verifier struct {
	cl     client.Client
	logger *log.Logger
}

// Check verifies a single PodGangMap. It returns nil if the PodGangMap passes the check or an error
// why the check failed.
type Check func(*grovecorev1alpha1.PodGangMap) error

// NewVerifier creates a Verifier for PodGangMap resources bound to the given client.
func NewVerifier(cl client.Client, logger *log.Logger) *Verifier {
	return &Verifier{
		cl:     cl,
		logger: logger,
	}
}

// Get returns the PodGangMap for the given PodCliqueSet replica index. One PodGangMap exists per
// PodCliqueSet replica, named <pcs-name>-<pcs-replica-index>.
func (v *Verifier) Get(ctx context.Context, pcsNsName types.NamespacedName, pcsReplicaIndex int) (*grovecorev1alpha1.PodGangMap, error) {
	name := commonapi.GeneratePodGangMapName(commonapi.ResourceNameReplica{Name: pcsNsName.Name, Replica: pcsReplicaIndex})
	pgm := &grovecorev1alpha1.PodGangMap{}
	if err := v.cl.Get(ctx, types.NamespacedName{Namespace: pcsNsName.Namespace, Name: name}, pgm); err != nil {
		return nil, err
	}
	return pgm, nil
}

// Verify gets the PodGangMap for the given PodCliqueSet replica index and applies each check to it. It
// returns an error if the PodGangMap cannot be fetched or a check fails (returns on the first failure).
func (v *Verifier) Verify(ctx context.Context, pcsNsName types.NamespacedName, pcsReplicaIndex int, check ...Check) error {
	pgm, err := v.Get(ctx, pcsNsName, pcsReplicaIndex)
	if err != nil {
		return err
	}
	for _, c := range check {
		if err := c(pgm); err != nil {
			return fmt.Errorf("check failed for PodGangMap %v: %w", client.ObjectKeyFromObject(pgm), err)
		}
	}
	return nil
}

// AnchorPCSGReplicaIndicesCheckFn returns a Check that verifies the anchor entry carries the wanted
// replica indices for the given PodCliqueScalingGroup.
// NOTE: it selects the single anchor entry. After a coherent update a PodGangMap has more than one
// anchor entry, and selecting the right one is left to the coherent update tests.
func AnchorPCSGReplicaIndicesCheckFn(pcsgName string, want []int32) Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		entry, err := singleEntryByRole(pgm, grovecorev1alpha1.PodGangEntryRoleAnchor)
		if err != nil {
			return err
		}
		return assertPCSGReplicaIndices(entry, pcsgName, want)
	}
}

// ScaleOutPCSGReplicaIndicesCheckFn returns a Check that verifies the ScaleOut entry carries the wanted
// replica indices for the given PodCliqueScalingGroup.
func ScaleOutPCSGReplicaIndicesCheckFn(pcsgName string, want []int32) Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		entry, err := singleEntryByRole(pgm, grovecorev1alpha1.PodGangEntryRoleScaleOut)
		if err != nil {
			return err
		}
		return assertPCSGReplicaIndices(entry, pcsgName, want)
	}
}

// AnchorStandalonePodCliqueCountCheckFn returns a Check that verifies the anchor entry carries the
// wanted pod count for the given standalone PodClique.
func AnchorStandalonePodCliqueCountCheckFn(cliqueName string, want int32) Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		entry, err := singleEntryByRole(pgm, grovecorev1alpha1.PodGangEntryRoleAnchor)
		if err != nil {
			return err
		}
		if got := entry.PodCliques[cliqueName]; got != want {
			return fmt.Errorf("anchor PodClique %q count = %d, want %d", cliqueName, got, want)
		}
		return nil
	}
}

// ScaleOutEpochCheckFn returns a Check that verifies the ScaleOut entry carries the wanted epoch. It is
// used to confirm a rebuilt PodGangMap reuses the epoch of the existing scale-out PodGang rather than
// minting a new one, so the scale-out PodGang keeps its name.
func ScaleOutEpochCheckFn(want string) Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		entry, err := singleEntryByRole(pgm, grovecorev1alpha1.PodGangEntryRoleScaleOut)
		if err != nil {
			return err
		}
		if entry.Epoch != want {
			return fmt.Errorf("ScaleOut epoch = %q, want %q", entry.Epoch, want)
		}
		return nil
	}
}

// AnchorEpochCheckFn returns a Check that verifies the anchor entry carries the wanted epoch. It is used
// to confirm a rebuilt PodGangMap reuses the epoch of the existing anchor PodGang rather than minting a
// new one, so the anchor PodGang keeps its name.
func AnchorEpochCheckFn(want string) Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		entry, err := singleEntryByRole(pgm, grovecorev1alpha1.PodGangEntryRoleAnchor)
		if err != nil {
			return err
		}
		if entry.Epoch != want {
			return fmt.Errorf("anchor epoch = %q, want %q", entry.Epoch, want)
		}
		return nil
	}
}

// NoEntriesCheckFn returns a Check that verifies the PodGangMap has no entries. This is the transient
// state after every entry drains to empty, for example when a PodCliqueScalingGroup is scaled below
// MinAvailable on an all-PodCliqueScalingGroup PodCliqueSet.
func NoEntriesCheckFn() Check {
	return func(pgm *grovecorev1alpha1.PodGangMap) error {
		if len(pgm.Spec.Entries) != 0 {
			return fmt.Errorf("expected no entries, found %d", len(pgm.Spec.Entries))
		}
		return nil
	}
}

// WaitUntilVerified polls the replica's PodGangMap until it satisfies all checks or the timeout
// elapses. Verification is delegated to Verifier.Verify and runs immediately, then repeats every
// interval. It returns nil once all checks pass. On timeout or context cancellation it returns an error
// that joins the poll outcome with the last verification failure.
func WaitUntilVerified(ctx context.Context, v *Verifier, pcsNsName types.NamespacedName, pcsReplicaIndex int, timeout, interval time.Duration, checks ...Check) error {
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		lastErr = v.Verify(ctx, pcsNsName, pcsReplicaIndex, checks...)
		return lastErr == nil, nil
	})
	if pollErr == nil {
		return nil
	}
	return fmt.Errorf("PodGangMap for PodCliqueSet %v replica %d not verified within %s: %w", pcsNsName, pcsReplicaIndex, timeout, errors.Join(pollErr, lastErr))
}

// singleEntryByRole returns the entry with the given role, or an error unless there is exactly one.
func singleEntryByRole(pgm *grovecorev1alpha1.PodGangMap, role grovecorev1alpha1.PodGangEntryRole) (grovecorev1alpha1.PodGangEntry, error) {
	var found []grovecorev1alpha1.PodGangEntry
	for i := range pgm.Spec.Entries {
		if pgm.Spec.Entries[i].Role == role {
			found = append(found, pgm.Spec.Entries[i])
		}
	}
	if len(found) != 1 {
		return grovecorev1alpha1.PodGangEntry{}, fmt.Errorf("expected exactly one %s entry, found %d", role, len(found))
	}
	return found[0], nil
}

// assertPCSGReplicaIndices reports whether the entry carries exactly the wanted replica indices for the
// given PodCliqueScalingGroup.
func assertPCSGReplicaIndices(entry grovecorev1alpha1.PodGangEntry, pcsgName string, want []int32) error {
	got := entry.PCSGReplicaIndices[pcsgName]
	if !slices.Equal(got, want) {
		return fmt.Errorf("%s PodCliqueScalingGroup %q replica indices = %v, want %v", entry.Role, pcsgName, got, want)
	}
	return nil
}
