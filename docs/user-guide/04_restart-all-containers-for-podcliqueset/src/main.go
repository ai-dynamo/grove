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

// restart-watcher: sidecar that watches a ConfigMap key (restartGeneration).
// When the value increases, it exits with a configured code to trigger
// Kubernetes RestartAllContainers on the Pod (K8s 1.35+).
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	ns := getenv("CM_NAMESPACE", "default")
	cmName := getenv("CM_NAME", "grove-restart-control")
	key := getenv("KEY_NAME", "restartGeneration")
	pollSecondsStr := getenv("POLL_INTERVAL_SECONDS", "5")
	triggerExitCodeStr := getenv("TRIGGER_EXIT_CODE", "88")

	pollSeconds, err := strconv.Atoi(pollSecondsStr)
	if err != nil || pollSeconds <= 0 {
		fmt.Fprintf(os.Stderr, "invalid POLL_INTERVAL_SECONDS=%q\n", pollSecondsStr)
		os.Exit(1)
	}
	triggerExitCode, err := strconv.Atoi(triggerExitCodeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid TRIGGER_EXIT_CODE=%q\n", triggerExitCodeStr)
		os.Exit(1)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get in-cluster config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create clientset: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Initial read as baseline
	currentGen := int64(0)
	cm, err := getConfigMap(ctx, clientset, ns, cmName)
	if err == nil {
		if v, ok := cm.Data[key]; ok {
			if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
				currentGen = parsed
				fmt.Printf("initial %s/%s %s=%d\n", ns, cmName, key, currentGen)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "initial get configmap error: %v\n", err)
	}

	ticker := time.NewTicker(time.Duration(pollSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cm, err := getConfigMap(ctx, clientset, ns, cmName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "get configmap error: %v\n", err)
				continue
			}

			value, ok := cm.Data[key]
			if !ok {
				fmt.Fprintf(os.Stderr, "configmap %s/%s missing key %s\n", ns, cmName, key)
				continue
			}

			newGen, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid %s value=%q: %v\n", key, value, err)
				continue
			}

			if newGen > currentGen {
				fmt.Printf("detected %s/%s %s increased: %d -> %d, exiting with code %d\n",
					ns, cmName, key, currentGen, newGen, triggerExitCode)
				os.Exit(triggerExitCode)
			}
		}
	}
}

func getConfigMap(ctx context.Context, clientset *kubernetes.Clientset, ns, name string) (*corev1.ConfigMap, error) {
	return clientset.CoreV1().
		ConfigMaps(ns).
		Get(ctx, name, metav1.GetOptions{})
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
