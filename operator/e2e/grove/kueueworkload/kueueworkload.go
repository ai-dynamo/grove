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

package kueueworkload

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/log"
	"github.com/ai-dynamo/grove/operator/e2e/waiter"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// Verifier provides Kueue Workload verification using a controller-runtime client.
type Verifier struct {
	cl     client.Client
	logger *log.Logger
}

// NewVerifier creates a Verifier bound to the given client.
func NewVerifier(cl client.Client, logger *log.Logger) *Verifier {
	return &Verifier{cl: cl, logger: logger}
}

// Get fetches the prebuilt Kueue Workload for the given namespace/name. Workload.Name always equals
// PodGang.Name (see the kueue scheduler backend's buildPrebuiltWorkload), so this is a direct Get
// rather than a List-by-label: the prebuilt Workload carries no Grove labels.
func (v *Verifier) Get(ctx context.Context, namespace, name string) (*kueuev1beta2.Workload, error) {
	workload := &kueuev1beta2.Workload{}
	if err := v.cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, workload); err != nil {
		return nil, fmt.Errorf("failed to get Kueue Workload %s/%s: %w", namespace, name, err)
	}
	return workload, nil
}

// WaitUntilVerified polls the Kueue Workload at namespace/name until predicate reports it satisfied,
// or the timeout elapses. Fetch errors (including NotFound, e.g. while the backend's ensureWorkload
// delete-and-recreate repair is in flight) are retried rather than failing the wait immediately.
func (v *Verifier) WaitUntilVerified(ctx context.Context, namespace, name string, timeout, interval time.Duration, predicate waiter.Predicate[*kueuev1beta2.Workload]) (*kueuev1beta2.Workload, error) {
	w := waiter.New[*kueuev1beta2.Workload]().
		WithTimeout(timeout).
		WithInterval(interval).
		WithRetryOnError().
		WithLogger(v.logger)
	workload, err := w.WaitFor(ctx, waiter.ToFetchFunc2(v.Get, namespace, name), predicate)
	if err != nil {
		return nil, fmt.Errorf("Kueue Workload %s/%s not verified within %s: %w", namespace, name, timeout, err)
	}
	return workload, nil
}

// Admitted is a waiter.Predicate that reports whether wl carries an Admitted=True condition.
func Admitted(wl *kueuev1beta2.Workload) bool {
	return wl != nil && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueuev1beta2.WorkloadAdmitted)
}

// Finished is a waiter.Predicate that reports whether wl carries a Finished=True condition.
func Finished(wl *kueuev1beta2.Workload) bool {
	return wl != nil && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueuev1beta2.WorkloadFinished)
}

// PodsReady is a waiter.Predicate that reports whether wl carries a PodsReady=True condition, which Kueue
// sets (only with waitForPodsReady configured) once every pod of the group exists and is Ready.
func PodsReady(wl *kueuev1beta2.Workload) bool {
	return wl != nil && apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueuev1beta2.WorkloadPodsReady)
}

// EvictedByPodsReadyTimeout is a waiter.Predicate that reports whether wl carries an Evicted=True
// condition with reason PodsReadyTimeout, proving Kueue evicted it for not reaching PodsReady=true
// within the configured waitForPodsReady timeout.
func EvictedByPodsReadyTimeout(wl *kueuev1beta2.Workload) bool {
	if wl == nil {
		return false
	}
	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueuev1beta2.WorkloadEvicted)
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueuev1beta2.WorkloadEvictedByPodsReadyTimeout
}

// UIDChanged returns a waiter.Predicate that reports whether wl's UID differs from want. A Workload
// that was deleted and recreated (see ensureWorkload) gets a new UID, so a changed UID proves the
// repair ran.
func UIDChanged(want types.UID) waiter.Predicate[*kueuev1beta2.Workload] {
	return func(wl *kueuev1beta2.Workload) bool {
		return wl != nil && wl.UID != want && wl.UID != ""
	}
}
