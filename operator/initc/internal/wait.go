// /*
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
// */

package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/constants"
	groveerr "github.com/ai-dynamo/grove/operator/internal/errors"

	"github.com/go-logr/logr"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// Constants for error codes.
const (
	errCodeClientCreation               grovecorev1alpha1.ErrorCode = "ERR_CLIENT_CREATION"
	errCodeLabelSelectorCreationForPods grovecorev1alpha1.ErrorCode = "ERR_LABEL_SELECTOR_CREATION_FOR_PODS"
	errCodeRegisterEventHandler         grovecorev1alpha1.ErrorCode = "ERR_REGISTER_EVENT_HANDLER"
)

const (
	operationWaitForParentPodClique = "WaitForParentPodClique"
)

// ParentPodCliqueDependencies contains the last known (readiness) state of all parent PodCliques.
type ParentPodCliqueDependencies struct {
	mutex                 sync.Mutex                  // Protects concurrent access to state
	namespace             string                      // Kubernetes namespace to watch for pods
	podGang               string                      // PodGang name used for pod label selection
	pclqFQNToMinAvailable map[string]int              // Required minimum available pods per PodClique
	currentPCLQReadyPods  map[string]sets.Set[string] // Currently ready pods per PodClique
	allReadyCh            chan struct{}               // Signals when all dependencies are satisfied
}

// NewPodCliqueState creates and initializes all parent PodCliques with an unready state.
func NewPodCliqueState(podCliqueDependencies map[string]int, log logr.Logger) (*ParentPodCliqueDependencies, error) {
	namespace, podGang, err := readPodInfo()
	if err != nil {
		log.Error(err, "Failed to read pod information from files")
		return nil, err
	}

	return newPodCliqueStateWithInfo(podCliqueDependencies, namespace, podGang), nil
}

// readPodInfo reads namespace and podgang information from mounted files.
func readPodInfo() (string, string, error) {
	podNamespaceFilePath := filepath.Join(constants.VolumeMountPathPodInfo, constants.PodNamespaceFileName)
	podNamespace, err := os.ReadFile(podNamespaceFilePath)
	if err != nil {
		return "", "", err
	}

	podGangNameFilePath := filepath.Join(constants.VolumeMountPathPodInfo, constants.PodGangNameFileName)
	podGangName, err := os.ReadFile(podGangNameFilePath)
	if err != nil {
		return "", "", err
	}

	return string(podNamespace), string(podGangName), nil
}

// newPodCliqueStateWithInfo creates a ParentPodCliqueDependencies with provided info.
// This function is more testable as it doesn't depend on file system operations.
func newPodCliqueStateWithInfo(podCliqueDependencies map[string]int, namespace, podGang string) *ParentPodCliqueDependencies {
	// Initialize tracking for each parent PodClique's ready pods
	currentlyReadyPods := make(map[string]sets.Set[string])
	for parentPodCliqueName := range podCliqueDependencies {
		currentlyReadyPods[parentPodCliqueName] = sets.New[string]()
	}

	return &ParentPodCliqueDependencies{
		namespace:             namespace,
		podGang:               podGang,
		pclqFQNToMinAvailable: podCliqueDependencies,
		currentPCLQReadyPods:  currentlyReadyPods,
		allReadyCh:            make(chan struct{}, 1),
	}
}

// WaitForReady waits for all upstream start-up dependencies to be ready.
func (c *ParentPodCliqueDependencies) WaitForReady(ctx context.Context, log logr.Logger) error {
	defer close(c.allReadyCh) // Close the channel the informers write to *after* the context they use is cancelled.

	log.Info("Parent PodClique(s) being waited on", "pclqFQNToMinAvailable", c.pclqFQNToMinAvailable)

	client, err := createClient()
	if err != nil {
		return err
	}

	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: getLabelSelectorForPods(c.podGang),
	})
	if err != nil {
		return groveerr.WrapError(
			err,
			errCodeLabelSelectorCreationForPods,
			operationWaitForParentPodClique,
			"failed to convert labels required for the PodGang to selector",
		)
	}

	eventHandlerContext, cancel := context.WithCancel(ctx)

	// Create informer factory to watch pods matching the PodGang label
	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		time.Second,
		informers.WithNamespace(c.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = selector.String()
		}),
	)
	defer func() {
		cancel() // Cancel the context used by the informers if the wait is successful, or an err occurs.
		factory.Shutdown()
	}()

	if err := c.registerEventHandler(factory, log); err != nil {
		return groveerr.WrapError(
			err,
			errCodeRegisterEventHandler,
			operationWaitForParentPodClique,
			"failed to register the Pod event handler",
		)
	}

	factory.WaitForCacheSync(eventHandlerContext.Done())
	factory.Start(eventHandlerContext.Done())

	select {
	case <-c.allReadyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// createClient creates a Kubernetes client using in-cluster configuration.
func createClient() (*kubernetes.Clientset, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, groveerr.WrapError(
			err,
			errCodeClientCreation,
			operationWaitForParentPodClique,
			"failed to fetch the in cluster config",
		)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, groveerr.WrapError(
			err,
			errCodeClientCreation,
			operationWaitForParentPodClique,
			"failed to create clientSet with the fetched restConfig",
		)
	}
	return client, nil
}

// registerEventHandler sets up pod event handlers to track readiness state changes.
func (c *ParentPodCliqueDependencies) registerEventHandler(factory informers.SharedInformerFactory, log logr.Logger) error {
	typedInformer := factory.Core().V1().Pods().Informer()
	_, err := typedInformer.AddEventHandlerWithOptions(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}

			c.refreshReadyPodsOfPodClique(pod, false)
			c.notifyIfAllParentsReady()
		},
		UpdateFunc: func(_, newObj any) {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			pod, ok := newObj.(*corev1.Pod)
			if !ok {
				return
			}

			c.refreshReadyPodsOfPodClique(pod, false)
			c.notifyIfAllParentsReady()
		},
		DeleteFunc: func(obj any) {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}

			c.refreshReadyPodsOfPodClique(pod, true)
			c.notifyIfAllParentsReady()
		},
	}, cache.HandlerOptions{Logger: &log})
	return err
}

// refreshReadyPodsOfPodClique updates the ready pod count for the PodClique that owns the given pod.
func (c *ParentPodCliqueDependencies) refreshReadyPodsOfPodClique(pod *corev1.Pod, deletionEvent bool) {
	// Find which parent PodClique this pod belongs to by name prefix matching
	podCliqueName, ok := lo.Find(lo.Keys(c.pclqFQNToMinAvailable), func(podCliqueFQN string) bool {
		return strings.HasPrefix(pod.Name, podCliqueFQN)
	})
	if !ok {
		return // Pod doesn't belong to any tracked parent PodClique
	}

	if deletionEvent {
		c.currentPCLQReadyPods[podCliqueName].Delete(pod.Name)
		return
	}

	// Check if pod is in Ready state
	readyCondition, ok := lo.Find(pod.Status.Conditions, func(podCondition corev1.PodCondition) bool {
		return podCondition.Type == corev1.PodReady
	})
	podReady := ok && readyCondition.Status == corev1.ConditionTrue

	if podReady {
		c.currentPCLQReadyPods[podCliqueName].Insert(pod.Name)
	} else {
		c.currentPCLQReadyPods[podCliqueName].Delete(pod.Name)
	}
}

// notifyIfAllParentsReady will notify consumers if all PCLQ pods are ready.
func (c *ParentPodCliqueDependencies) notifyIfAllParentsReady() {
	for cliqueName, readyPods := range c.currentPCLQReadyPods {
		if len(readyPods) < c.pclqFQNToMinAvailable[cliqueName] {
			return
		}
	}

	select {
	case c.allReadyCh <- struct{}{}:
	default: // already notified
	}
}

// getLabelSelectorForPods returns the label selector to filter pods belonging to the specified PodGang.
func getLabelSelectorForPods(podGangName string) map[string]string {
	return map[string]string{
		apicommon.LabelPodGang: podGangName,
	}
}
