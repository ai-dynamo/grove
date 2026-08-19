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

package utils

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
)

// Task is a named closure.
type Task struct {
	// Name is the name of the task, used for logging and result reporting.
	Name string
	// Fn is the function that will be executed.
	Fn func(ctx context.Context) error
}

// RunResult holds the results of running tasks concurrently.
type RunResult struct {
	// SuccessfulTasks are the names of tasks that executed successfully.
	SuccessfulTasks []string
	// FailedTasks are the names of tasks that failed during execution.
	FailedTasks []string
	// SkippedTasks are the names of tasks that were skipped.
	SkippedTasks []string
	// Errors contains all errors encountered during task execution.
	Errors []error
}

// HasErrors returns true if any tasks encountered errors.
func (r *RunResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// GetAggregatedError returns all task errors joined into a single error.
func (r *RunResult) GetAggregatedError() error {
	if !r.HasErrors() {
		return nil
	}
	return errors.Join(r.Errors...)
}

// GetSummary returns a summary of successful, failed, and skipped tasks.
func (r *RunResult) GetSummary() string {
	return fmt.Sprintf("RunResult{SuccessfulTasks: %v, FailedTasks: %v, SkippedTasks: %v}",
		r.SuccessfulTasks, r.FailedTasks, r.SkippedTasks)
}

// RunConcurrentlyWithSlowStart executes tasks in exponentially growing batches starting at initialBatchSize.
// Each successful batch doubles the size of the next batch. On any batch failure, execution halts immediately
// and remaining tasks are marked as skipped. This prevents overwhelming kube-apiserver with concurrent requests.
func RunConcurrentlyWithSlowStart(ctx context.Context, logger logr.Logger, initialBatchSize int, tasks []Task) RunResult {
	remaining := len(tasks)
	aggregatedRunResult := RunResult{}
	nextRunStartIndex := 0
	for batchSize := min(remaining, initialBatchSize); batchSize > 0; batchSize = min(2*batchSize, remaining) {
		logger.V(4).Info("Triggering batch of taskConfigs with slow start", "batchSize", batchSize, "remainingTasks", remaining)
		runEndIndex := nextRunStartIndex + batchSize
		batchRunResult := RunConcurrently(ctx, logger, tasks[nextRunStartIndex:runEndIndex])
		updateWithBatchRunResult(&aggregatedRunResult, batchRunResult)
		if batchRunResult.HasErrors() {
			logger.V(4).Info("Batch of taskConfigs failed, halting further execution", "batchSize", batchSize, "remainingTasks", remaining)
			computeAndUpdateSkippedTasks(&aggregatedRunResult, tasks)
			return aggregatedRunResult
		}
		remaining -= batchSize
		nextRunStartIndex = runEndIndex
	}
	return aggregatedRunResult
}

// RunConcurrently executes a slice of Tasks concurrently.
func RunConcurrently(ctx context.Context, logger logr.Logger, tasks []Task) RunResult {
	return RunConcurrentlyWithBounds(ctx, logger, tasks, len(tasks))
}

// RunConcurrentlyWithBounds executes a slice of Tasks with at most `bound` taskConfigs running concurrently.
func RunConcurrentlyWithBounds(ctx context.Context, logger logr.Logger, tasks []Task, bound int) RunResult {
	rg := newRunGroup(bound, logger)
	for _, task := range tasks {
		rg.trigger(ctx, task)
	}
	tasksInError := rg.waitAndCollectErroneousTasks()
	return createRunResult(tasks, tasksInError)
}

// createRunResult builds a RunResult from all tasks, separating successful and failed tasks.
func createRunResult(allTasks []Task, tasksInError []lo.Tuple2[string, error]) RunResult {
	result := RunResult{
		SuccessfulTasks: make([]string, 0, len(allTasks)),
		FailedTasks:     make([]string, 0, len(tasksInError)),
		SkippedTasks:    make([]string, 0, len(allTasks)-len(tasksInError)),
		Errors:          make([]error, 0, len(tasksInError)),
	}
	for _, task := range allTasks {
		foundErrTask, ok := lo.Find(tasksInError, func(errTask lo.Tuple2[string, error]) bool {
			return errTask.A == task.Name
		})
		if ok {
			result.FailedTasks = append(result.FailedTasks, foundErrTask.A)
			result.Errors = append(result.Errors, foundErrTask.B)
		} else {
			result.SuccessfulTasks = append(result.SuccessfulTasks, task.Name)
		}
	}
	return result
}

// updateWithBatchRunResult merges a batch result into the aggregated result.
func updateWithBatchRunResult(aggregatedRunResult *RunResult, batchRunResult RunResult) {
	aggregatedRunResult.SuccessfulTasks = append(aggregatedRunResult.SuccessfulTasks, batchRunResult.SuccessfulTasks...)
	aggregatedRunResult.FailedTasks = append(aggregatedRunResult.FailedTasks, batchRunResult.FailedTasks...)
	aggregatedRunResult.Errors = append(aggregatedRunResult.Errors, batchRunResult.Errors...)
}

// computeAndUpdateSkippedTasks identifies tasks that were neither successful nor failed and marks them as skipped.
func computeAndUpdateSkippedTasks(result *RunResult, allTasks []Task) {
	allTaskNames := lo.Map(allTasks, func(task Task, _ int) string {
		return task.Name
	})
	skippedTaskNames := lo.Filter(allTaskNames, func(taskName string, _ int) bool {
		return !lo.Contains(result.SuccessfulTasks, taskName) && !lo.Contains(result.FailedTasks, taskName)
	})
	result.SkippedTasks = append(result.SkippedTasks, skippedTaskNames...)
}

// runGroup coordinates concurrent task execution with wait group and error collection.
type runGroup struct {
	logger    logr.Logger
	wg        sync.WaitGroup
	errTaskCh chan lo.Tuple2[string, error]
}

// newRunGroup creates a new runGroup with a buffered error channel sized for the number of tasks.
func newRunGroup(numTasks int, logger logr.Logger) *runGroup {
	return &runGroup{
		logger:    logger,
		wg:        sync.WaitGroup{},
		errTaskCh: make(chan lo.Tuple2[string, error], numTasks),
	}
}

// trigger starts a task in a new goroutine, capturing panics and errors.
func (rg *runGroup) trigger(ctx context.Context, task Task) {
	rg.wg.Add(1)
	rg.logger.V(4).Info("triggering concurrent execution of task", "taskName", task.Name)
	go func(task Task) {
		defer rg.wg.Done()
		defer func() {
			if v := recover(); v != nil {
				stack := debug.Stack()
				panicErr := fmt.Errorf("task: %s execution panicked: %v\n, stack-trace: %s", task.Name, v, stack)
				rg.errTaskCh <- lo.T2(task.Name, panicErr)
			}
		}()
		rg.logger.V(5).Info("executing task", "taskName", task.Name)
		if err := task.Fn(ctx); err != nil {
			rg.errTaskCh <- lo.T2(task.Name, err)
		}
	}(task)
}

// waitAndCollectErroneousTasks waits for all tasks to complete and returns tasks that encountered errors.
func (rg *runGroup) waitAndCollectErroneousTasks() []lo.Tuple2[string, error] {
	rg.wg.Wait()
	close(rg.errTaskCh)
	var tasksInError []lo.Tuple2[string, error]
	for err := range rg.errTaskCh {
		tasksInError = append(tasksInError, err)
	}
	return tasksInError
}
