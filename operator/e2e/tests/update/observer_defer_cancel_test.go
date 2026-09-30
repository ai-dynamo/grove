// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// This file intentionally has NO `//go:build e2e` tag so it runs under
// plain `go test`. It exists to demonstrate — with a runnable proof — why
// changing
//
//	timeoutCtx, cancel := context.WithTimeout(tc.Ctx, tc.Timeout)
//
// followed by `defer cancel()` inside newOrdinalUpdateObserver would break
// the observer's contract with its caller. See PR #734 review discussion
// r3803206103.
//
// The real observer's Wait() reads from a k8s watch channel that is bound
// to `timeoutCtx`. If `cancel()` fires at the moment newOrdinalUpdateObserver
// returns, the derived context is Done before the caller ever calls Wait(),
// and Wait() returns a "context canceled" error instead of observing the
// ordinal transition.
//
// The tests below model that pattern with a minimal reproducer: a fake
// downstream that only feeds events into a channel while its context stays
// alive. Two builder variants exist:
//
//   - buildObserver_deferCancel:      the pattern Ronkahn21 suggested.
//   - buildObserver_cancelOnStop:     the pattern currently on main.
//
// The event producer that feeds the observer stops as soon as the observer's
// context is Done, mirroring how apimachinery's watch shuts its channel when
// the parent ctx is canceled.

package update

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeObserver is a stand-in for ordinalUpdateObserver. It carries only the
// two fields relevant to the review argument: the derived timeout ctx and
// the cancel func attached to it.
type fakeObserver struct {
	ctx    context.Context
	cancel context.CancelFunc
	events <-chan int32
}

// buildObserver_cancelOnStop reproduces the current PR #734 code path:
// cancel is captured into the observer struct and only runs when Stop() is
// called by the caller. The returned ctx stays alive across the caller's
// Wait().
func buildObserver_cancelOnStop(parent context.Context, timeout time.Duration) *fakeObserver {
	timeoutCtx, cancel := context.WithTimeout(parent, timeout)
	events := startFakeWatcher(timeoutCtx)
	return &fakeObserver{
		ctx:    timeoutCtx,
		cancel: cancel,
		events: events,
	}
}

// buildObserver_deferCancel reproduces the code Ronkahn21 suggested:
//
//	timeoutCtx, cancel := context.WithTimeout(tc.Ctx, tc.Timeout)
//	defer cancel()
//
// Because cancel() runs before the function returns, the ctx handed back to
// the caller is already Done, and the watcher chan is closed almost
// immediately by the producer's ctx.Done() branch.
func buildObserver_deferCancel(parent context.Context, timeout time.Duration) *fakeObserver {
	timeoutCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	events := startFakeWatcher(timeoutCtx)
	return &fakeObserver{
		ctx:    timeoutCtx,
		cancel: cancel,
		events: events,
	}
}

// startFakeWatcher models tc.Client.Watch(): as long as ctx is alive, it
// emits update-progress events on a delay. When ctx is Done, it closes the
// channel, exactly like apimachinery's watcher does when the parent ctx is
// canceled.
func startFakeWatcher(ctx context.Context) <-chan int32 {
	ch := make(chan int32, 4)
	go func() {
		defer close(ch)
		// Emulate a small pipeline delay before the ordinal transitions.
		// The real operator takes ~5–7s in observed L20 runs; we shorten it
		// so the unit test is quick, but the shape is identical.
		producerDelay := 50 * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(producerDelay):
		}
		// Emit the "ordinal 1 currently updating" event.
		select {
		case <-ctx.Done():
			return
		case ch <- 1:
		}
	}()
	return ch
}

// waitForOrdinal is a stand-in for ordinalUpdateObserver.Wait(). It mirrors
// waiter.WaitForWatchEvent's select loop exactly.
func (o *fakeObserver) waitForOrdinal(target int32) error {
	for {
		select {
		case <-o.ctx.Done():
			return errWatchConditionNotMet(o.ctx.Err())
		case v, ok := <-o.events:
			if !ok {
				return errWatchClosed
			}
			if v == target {
				return nil
			}
		}
	}
}

func (o *fakeObserver) Stop() { o.cancel() }

var errWatchClosed = errors.New("watch closed before condition was met")

func errWatchConditionNotMet(err error) error {
	return &conditionErr{cause: err}
}

type conditionErr struct{ cause error }

func (e *conditionErr) Error() string { return "watch condition not met: " + e.cause.Error() }
func (e *conditionErr) Unwrap() error { return e.cause }

// TestObserver_CancelOnStop_ExhibitsCorrectBehavior demonstrates that the
// pattern currently on PR #734 lets Wait() see the transition, because ctx
// stays alive between constructor return and Wait().
func TestObserver_CancelOnStop_ExhibitsCorrectBehavior(t *testing.T) {
	ctx := context.Background()
	obs := buildObserver_cancelOnStop(ctx, 5*time.Second)
	defer obs.Stop()

	// Simulate the real caller pattern: build observer, THEN kick off the
	// mutation, THEN Wait. A short pause here models the time spent in
	// triggerPodCliqueUpdate() before Wait() runs.
	time.Sleep(20 * time.Millisecond)

	if err := obs.waitForOrdinal(1); err != nil {
		t.Fatalf("cancel-on-Stop observer failed to see ordinal transition: %v", err)
	}
}

// TestObserver_DeferCancel_BreaksWait is the "actual proof" that the
// PR #734 review suggestion (defer cancel inside the constructor) would
// break the observer.
func TestObserver_DeferCancel_BreaksWait(t *testing.T) {
	ctx := context.Background()
	obs := buildObserver_deferCancel(ctx, 5*time.Second)
	// Note: no need to defer obs.Stop(); cancel already fired.

	// Real caller would trigger update here and then Wait(). We do the same.
	time.Sleep(20 * time.Millisecond)

	err := obs.waitForOrdinal(1)
	if err == nil {
		t.Fatalf("defer-cancel observer unexpectedly succeeded; this contradicts the review-suggestion critique")
	}

	// The failure mode should be one of:
	//   - context canceled (ctx.Done fires before any event arrives), OR
	//   - watch closed (producer saw ctx.Done and closed the channel).
	// Both are equivalent proofs that Wait() cannot function.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, errWatchClosed) {
		t.Fatalf("expected context.Canceled or errWatchClosed, got: %v", err)
	}
	t.Logf("defer-cancel variant failed as expected: %v", err)
}

// TestObserver_DeferCancel_ContextAlreadyDoneOnReturn asserts the exact
// mechanism: after the constructor returns, ctx.Err() is already non-nil.
// This is the single-line justification for keeping cancel on Stop().
func TestObserver_DeferCancel_ContextAlreadyDoneOnReturn(t *testing.T) {
	obs := buildObserver_deferCancel(context.Background(), 5*time.Second)
	if obs.ctx.Err() == nil {
		t.Fatalf("expected obs.ctx to be Done immediately after constructor return, but ctx.Err() is nil")
	}
	t.Logf("as expected, obs.ctx.Err() = %v right after constructor return", obs.ctx.Err())
}

// TestObserver_CancelOnStop_ContextAliveOnReturn is the counter-assertion:
// current code leaves ctx alive across the caller's mutation window.
func TestObserver_CancelOnStop_ContextAliveOnReturn(t *testing.T) {
	obs := buildObserver_cancelOnStop(context.Background(), 5*time.Second)
	defer obs.Stop()
	if err := obs.ctx.Err(); err != nil {
		t.Fatalf("expected obs.ctx to be alive after constructor return, got err=%v", err)
	}
}
