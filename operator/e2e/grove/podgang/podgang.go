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

package podgang

import (
	"context"
	"errors"
	"fmt"
	"time"

	commonapi "github.com/ai-dynamo/grove/operator/api/common"
	"github.com/ai-dynamo/grove/operator/e2e/log"
	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Verifier provides capabilities to verify PodGang resources.
type Verifier struct {
	cl     client.Client
	logger *log.Logger
}

// Check verifies a single PodGang. It returns nil if the PodGang passes the check
// or an error why the check failed.
type Check func(*groveschedulerv1alpha1.PodGang) error

// NewVerifier creates a Verifier for PodGang resources bound to the given client.
func NewVerifier(cl client.Client, logger *log.Logger) *Verifier {
	return &Verifier{
		cl:     cl,
		logger: logger,
	}
}

// List returns the PodGangs for the given PodCliqueSet name.
func (v *Verifier) List(ctx context.Context, pcsNsName types.NamespacedName) ([]groveschedulerv1alpha1.PodGang, error) {
	pgList := &groveschedulerv1alpha1.PodGangList{}
	if err := v.cl.List(ctx, pgList,
		client.InNamespace(pcsNsName.Namespace),
		client.MatchingLabels(commonapi.GetDefaultLabelsForPodCliqueSetManagedResources(pcsNsName.Name))); err != nil {
		return nil, err
	}
	return pgList.Items, nil
}

// Get returns the named PodGang.
func (v *Verifier) Get(ctx context.Context, namespace, name string) (*groveschedulerv1alpha1.PodGang, error) {
	pg := &groveschedulerv1alpha1.PodGang{}
	if err := v.cl.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pg); err != nil {
		return nil, err
	}
	return pg, nil
}

// Verify lists the PodGang(s) for the given PodCliqueSet namespace/name and applies the given
// check to each of the PodGang. It returns an error if one of the below conditions is met:
//  1. An error while listing PodGang resources.
//  2. No PodGang is found
//  3. Check fails for a PodGang (returns on the first failure).
//
// If there are no errors then it will return nil signaling that the verification has succeeded.
func (v *Verifier) Verify(ctx context.Context, pcsNsName types.NamespacedName, check ...Check) error {
	podGangs, err := v.List(ctx, pcsNsName)
	if err != nil {
		return err
	}
	if len(podGangs) == 0 {
		return fmt.Errorf("no PodGang(s) found for PodCliqueSet %v", pcsNsName)
	}
	for i := range podGangs {
		pg := &podGangs[i]
		for _, c := range check {
			if err := c(pg); err != nil {
				return fmt.Errorf("check failed for PodGang %v: %w", client.ObjectKeyFromObject(pg), err)
			}
		}
	}
	return nil
}

// VerifyByName gets the named PodGang and applies each check to it. It returns an error if the PodGang
// cannot be fetched or a check fails (returns on the first failure).
func (v *Verifier) VerifyByName(ctx context.Context, namespace, name string, check ...Check) error {
	pg, err := v.Get(ctx, namespace, name)
	if err != nil {
		return err
	}
	for _, c := range check {
		if err := c(pg); err != nil {
			return fmt.Errorf("check failed for PodGang %s/%s: %w", namespace, name, err)
		}
	}
	return nil
}

// SameUIDCheckFn returns a Check that verifies the PodGang still carries the given UID. A PodGang that
// was deleted and recreated gets a new UID, so an unchanged UID proves the same object survived.
// UID comparison is used rather than watching for delete events, because the recreation happens within
// a very short window and watch events observed over such a short interval are unreliable.
func SameUIDCheckFn(want types.UID) Check {
	return func(pg *groveschedulerv1alpha1.PodGang) error {
		if pg.UID != want {
			return fmt.Errorf("UID = %s, want %s (PodGang was recreated)", pg.UID, want)
		}
		return nil
	}
}

// ConditionStatusCheckFn returns a Check that verifies the PodGang has the given condition type set
// to the wanted status.
func ConditionStatusCheckFn(condType groveschedulerv1alpha1.PodGangConditionType, want metav1.ConditionStatus) Check {
	return func(pg *groveschedulerv1alpha1.PodGang) error {
		cond := meta.FindStatusCondition(pg.Status.Conditions, string(condType))
		if cond == nil {
			return fmt.Errorf("condition %s not found", condType)
		}
		if cond.Status != want {
			return fmt.Errorf("condition %s = %s, want %s", condType, cond.Status, want)
		}
		return nil
	}
}

// LastScheduledSetCheckFn returns a Check that verifies whether Status.LastScheduled is set.
func LastScheduledSetCheckFn(want bool) Check {
	return func(pg *groveschedulerv1alpha1.PodGang) error {
		if got := pg.Status.LastScheduled != nil; got != want {
			return fmt.Errorf("LastScheduled set = %t, want %t", got, want)
		}
		return nil
	}
}

// LastReadySetCheckFn returns a Check that verifies whether Status.LastReady is set.
func LastReadySetCheckFn(want bool) Check {
	return func(pg *groveschedulerv1alpha1.PodGang) error {
		if got := pg.Status.LastReady != nil; got != want {
			return fmt.Errorf("LastReady set = %t, want %t", got, want)
		}
		return nil
	}
}

// WaitUntilVerified polls the named PodCliqueSet's PodGangs until every one satisfies all checks or
// the timeout elapses. Verification is delegated to Verifier.Verify and runs immediately, then
// repeats every interval. It returns nil once all checks pass. On timeout or context cancellation it
// returns an error that joins the poll outcome with the last verification failure, so the message
// names both why polling stopped and which check was not satisfied.
func WaitUntilVerified(ctx context.Context, v *Verifier, pcsNsName types.NamespacedName, timeout, interval time.Duration, checks ...Check) error {
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		lastErr = v.Verify(ctx, pcsNsName, checks...)
		return lastErr == nil, nil
	})
	if pollErr == nil {
		return nil
	}
	return fmt.Errorf("PodGangs for PodCliqueSet %v not verified within %s: %w", pcsNsName, timeout, errors.Join(pollErr, lastErr))
}
