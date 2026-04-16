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

package waiter

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-dynamo/grove/operator/e2e/log"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultTimeout is the default polling timeout.
	DefaultTimeout = 2 * time.Minute
	// DefaultInterval is the default polling interval.
	DefaultInterval = 5 * time.Second
)

// GetFunc is the signature of a Kubernetes typed client Get method.
type GetFunc[T any] func(ctx context.Context, name string, opts metav1.GetOptions) (T, error)

// FetchFunc fetches a value of type T, returning an error if the fetch fails.
type FetchFunc[T any] func(ctx context.Context) (T, error)

// ToFetchFunc1 creates a FetchFunc by binding one argument to a function that takes (ctx, a).
func ToFetchFunc1[T, A any](fn func(context.Context, A) (T, error), a A) FetchFunc[T] {
	return func(ctx context.Context) (T, error) { return fn(ctx, a) }
}

// ToFetchFunc2 creates a FetchFunc by binding two arguments to a function that takes (ctx, a, b).
func ToFetchFunc2[T, A, B any](fn func(context.Context, A, B) (T, error), a A, b B) FetchFunc[T] {
	return func(ctx context.Context) (T, error) { return fn(ctx, a, b) }
}

func AlwaysTrue[T any](T) bool { return true }

// Predicate checks whether a fetched value satisfies the desired condition.
type Predicate[T any] func(T) bool

// IsNotZero checks whether a value is not the zero value of its type.
// For pointer types this means non-nil; for numeric types non-zero; for strings non-empty.
func IsNotZero[T comparable](v T) bool {
	var zero T
	return v != zero
}

// IsZero checks whether a value is the zero value of its type.
// For pointer types this means nil — useful for polling until a resource is deleted.
func IsZero[T comparable](v T) bool {
	var zero T
	return v == zero
}

// Waiter polls a FetchFunc and checks a Predicate at a configured interval.
type Waiter[T any] struct {
	timeout      time.Duration
	interval     time.Duration
	logger       *log.Logger
	retryOnError bool
}

// New creates a Waiter with default timeout and interval.
func New[T any]() *Waiter[T] {
	return &Waiter[T]{
		timeout:  DefaultTimeout,
		interval: DefaultInterval,
	}
}

// WithTimeout sets the polling timeout.
func (w *Waiter[T]) WithTimeout(d time.Duration) *Waiter[T] {
	w.timeout = d
	return w
}

// WithInterval sets the polling interval.
func (w *Waiter[T]) WithInterval(d time.Duration) *Waiter[T] {
	w.interval = d
	return w
}

// WithLogger sets the logger for diagnostic messages during polling.
func (w *Waiter[T]) WithLogger(l *log.Logger) *Waiter[T] {
	w.logger = l
	return w
}

// WithRetryOnError causes fetch/condition errors to be retried instead of
// causing immediate failure. Errors are logged (if a logger is set) and
// polling continues.
func (w *Waiter[T]) WithRetryOnError() *Waiter[T] {
	w.retryOnError = true
	return w
}

// WaitFor polls fetchFn at the configured interval and checks predicate.
// Returns the final T on success, or zero T + error on timeout/fetch failure.
func (w *Waiter[T]) WaitFor(ctx context.Context, fetchFn FetchFunc[T], predicate Predicate[T]) (T, error) {
	var zero T

	timeoutCtx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for first := true; ; first = false {
		if !first {
			select {
			case <-timeoutCtx.Done():
				return zero, fmt.Errorf("condition not met within timeout of %v", w.timeout)
			case <-ticker.C:
			}
		}

		result, err := fetchFn(timeoutCtx)
		if err != nil {
			if !w.retryOnError {
				return zero, fmt.Errorf("fetch failed: %w", err)
			}
			if w.logger != nil {
				w.logger.Warnf("WaitFor: fetch error (will retry): %v", err)
			}
			continue
		}
		if predicate(result) {
			return result, nil
		}
	}
}

// WaitUntil polls fetchFn at the configured interval and checks predicate.
// Unlike WaitFor, it discards the fetched value and only returns an error.
func (w *Waiter[T]) WaitUntil(ctx context.Context, fetchFn FetchFunc[T], predicate Predicate[T]) error {
	_, err := w.WaitFor(ctx, fetchFn, predicate)
	return err
}

// FetchByName returns a FetchFunc that gets a resource by name.
// NotFound errors return zero value (not error) so polling continues.
// Other errors fail immediately.
func FetchByName[T comparable](name string, getFn GetFunc[T]) FetchFunc[T] {
	return func(ctx context.Context) (T, error) {
		resource, err := getFn(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			var zero T
			return zero, nil
		}
		return resource, err
	}
}

// WaitForResource polls until a resource exists by name and returns it.
// This is a free function because it requires T comparable (for IsNotZero),
// which cannot be added as a constraint on Waiter[T any] methods.
func WaitForResource[T comparable](ctx context.Context, w *Waiter[T], name string, getFn GetFunc[T]) (T, error) {
	return w.WaitFor(ctx, FetchByName(name, getFn), IsNotZero[T])
}

// WaitForResourceDeletion polls until a resource no longer exists by name.
func WaitForResourceDeletion[T comparable](ctx context.Context, w *Waiter[T], name string, getFn GetFunc[T]) error {
	return w.WaitUntil(ctx, FetchByName(name, getFn), IsZero[T])
}
