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

package utils

import (
	"testing"
	"time"

	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFindScalingGroupConfigForClique(t *testing.T) {
	// Create test scaling group configurations
	scalingGroupConfigs := []grovecorev1alpha1.PodCliqueScalingGroupConfig{
		{
			Name:        "sga",
			CliqueNames: []string{"pca", "pcb"},
		},
		{
			Name:        "sgb",
			CliqueNames: []string{"pcc", "pcd", "pce"},
		},
		{
			Name:        "sgc",
			CliqueNames: []string{"pcf"},
		},
	}

	tests := []struct {
		name               string
		configs            []grovecorev1alpha1.PodCliqueScalingGroupConfig
		cliqueName         string
		expectedFound      bool
		expectedConfigName string
	}{
		{
			name:               "clique found in first scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pca",
			expectedFound:      true,
			expectedConfigName: "sga",
		},
		{
			name:               "clique found in second scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcd",
			expectedFound:      true,
			expectedConfigName: "sgb",
		},
		{
			name:               "clique found in third scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "pcf",
			expectedFound:      true,
			expectedConfigName: "sgc",
		},
		{
			name:               "clique not found in any scaling group",
			configs:            scalingGroupConfigs,
			cliqueName:         "nonexistent",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty clique name",
			configs:            scalingGroupConfigs,
			cliqueName:         "",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty configs",
			configs:            []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			cliqueName:         "anyClique",
			expectedFound:      false,
			expectedConfigName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := FindScalingGroupConfigForClique(tt.configs, tt.cliqueName)
			assert.Equal(t, tt.expectedFound, config != nil)
			if tt.expectedFound {
				assert.Equal(t, tt.expectedConfigName, config.Name)
			} else {
				// When not found, config should be nil
				assert.Nil(t, config)
			}
		})
	}
}

func TestFindScalingGroupConfigByName(t *testing.T) {
	scalingGroupConfigs := []grovecorev1alpha1.PodCliqueScalingGroupConfig{
		{
			Name:        "sga",
			CliqueNames: []string{"pca", "pcb"},
		},
		{
			Name:        "sgb",
			CliqueNames: []string{"pcc", "pcd"},
		},
	}

	tests := []struct {
		name               string
		configs            []grovecorev1alpha1.PodCliqueScalingGroupConfig
		searchName         string
		expectedFound      bool
		expectedConfigName string
	}{
		{
			name:               "config found by name",
			configs:            scalingGroupConfigs,
			searchName:         "sga",
			expectedFound:      true,
			expectedConfigName: "sga",
		},
		{
			name:               "config found by name - second config",
			configs:            scalingGroupConfigs,
			searchName:         "sgb",
			expectedFound:      true,
			expectedConfigName: "sgb",
		},
		{
			name:               "config not found",
			configs:            scalingGroupConfigs,
			searchName:         "nonexistent",
			expectedFound:      false,
			expectedConfigName: "",
		},
		{
			name:               "empty configs",
			configs:            []grovecorev1alpha1.PodCliqueScalingGroupConfig{},
			searchName:         "sga",
			expectedFound:      false,
			expectedConfigName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := FindScalingGroupConfigByName(tt.configs, tt.searchName)
			assert.Equal(t, tt.expectedFound, config != nil)
			if tt.expectedFound {
				assert.Equal(t, tt.expectedConfigName, config.Name)
			} else {
				assert.Nil(t, config)
			}
		})
	}
}

func TestGetEffectiveTerminationDelay(t *testing.T) {
	pcsDelay := metav1.Duration{Duration: 4 * time.Hour}
	pcsgDelay := metav1.Duration{Duration: 2 * time.Hour}

	tests := []struct {
		name                  string
		pcsTerminationDelay   *metav1.Duration
		pcsgConfig            *grovecorev1alpha1.PodCliqueScalingGroupConfig
		expectedDelay         *time.Duration
		expectedDelayDuration time.Duration
	}{
		{
			name:                "nil PCS delay - gang termination disabled",
			pcsTerminationDelay: nil,
			pcsgConfig:          nil,
			expectedDelay:       nil,
		},
		{
			name:                  "PCS delay set, no PCSG config - uses PCS delay",
			pcsTerminationDelay:   &pcsDelay,
			pcsgConfig:            nil,
			expectedDelayDuration: 4 * time.Hour,
		},
		{
			name:                "PCS delay set, PCSG config with nil override - uses PCS delay",
			pcsTerminationDelay: &pcsDelay,
			pcsgConfig: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:             "test-pcsg",
				TerminationDelay: nil,
			},
			expectedDelayDuration: 4 * time.Hour,
		},
		{
			name:                "PCS delay set, PCSG config with override - uses PCSG delay",
			pcsTerminationDelay: &pcsDelay,
			pcsgConfig: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:             "test-pcsg",
				TerminationDelay: &pcsgDelay,
			},
			expectedDelayDuration: 2 * time.Hour,
		},
		{
			name:                "nil PCS delay, PCSG config with override - still disabled (PCS nil takes precedence)",
			pcsTerminationDelay: nil,
			pcsgConfig: &grovecorev1alpha1.PodCliqueScalingGroupConfig{
				Name:             "test-pcsg",
				TerminationDelay: &pcsgDelay,
			},
			expectedDelay: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetEffectiveTerminationDelay(tt.pcsTerminationDelay, tt.pcsgConfig)
			if tt.expectedDelay == nil && tt.expectedDelayDuration == 0 {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, tt.expectedDelayDuration, *result)
			}
		})
	}
}
