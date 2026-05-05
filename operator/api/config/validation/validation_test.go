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

package validation

import (
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidatePodCliqueControllerConfiguration(t *testing.T) {
	fldPath := field.NewPath("controllers").Child("podClique")
	tests := []struct {
		name           string
		config         configv1alpha1.PodCliqueControllerConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name: "valid: concurrentSyncs set to 1",
			config: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: intPtr(1),
			},
			expectErrors: 0,
		},
		{
			name: "valid: concurrentSyncs set to a large value",
			config: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: intPtr(10),
			},
			expectErrors: 0,
		},
		{
			name: "invalid: concurrentSyncs is nil (defaults to 0, must be > 0)",
			config: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: nil,
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podClique.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: concurrentSyncs set to 0",
			config: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: intPtr(0),
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podClique.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: concurrentSyncs set to negative value",
			config: configv1alpha1.PodCliqueControllerConfiguration{
				ConcurrentSyncs: intPtr(-1),
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podClique.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validatePodCliqueControllerConfiguration(test.config, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestValidatePodGangControllerConfiguration(t *testing.T) {
	fldPath := field.NewPath("controllers").Child("podGang")
	tests := []struct {
		name           string
		config         configv1alpha1.PodGangControllerConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name: "valid: concurrentSyncs set to 1",
			config: configv1alpha1.PodGangControllerConfiguration{
				ConcurrentSyncs: intPtr(1),
			},
			expectErrors: 0,
		},
		{
			name: "valid: concurrentSyncs set to a large value",
			config: configv1alpha1.PodGangControllerConfiguration{
				ConcurrentSyncs: intPtr(10),
			},
			expectErrors: 0,
		},
		{
			name: "invalid: concurrentSyncs is nil (defaults to 0, must be > 0)",
			config: configv1alpha1.PodGangControllerConfiguration{
				ConcurrentSyncs: nil,
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podGang.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: concurrentSyncs set to 0",
			config: configv1alpha1.PodGangControllerConfiguration{
				ConcurrentSyncs: intPtr(0),
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podGang.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: concurrentSyncs set to negative value",
			config: configv1alpha1.PodGangControllerConfiguration{
				ConcurrentSyncs: intPtr(-1),
			},
			expectErrors:   1,
			expectedFields: []string{"controllers.podGang.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validatePodGangControllerConfiguration(test.config, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func TestValidateSchedulerConfiguration(t *testing.T) {
	fldPath := field.NewPath("scheduler")
	tests := []struct {
		name           string
		scheduler      *configv1alpha1.SchedulerConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		// Here we test pre-defaulting: empty profiles + empty defaultProfileName → Required for profiles, kube profile, and defaultProfileName
		{
			name: "invalid: empty profiles and empty defaultProfileName",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{},
				DefaultProfileName: "",
			},
			expectErrors:   3,
			expectedFields: []string{"scheduler.profiles", "scheduler.profiles", "scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired, field.ErrorTypeRequired, field.ErrorTypeRequired},
		},
		// empty profiles with defaultProfileName set
		{
			name: "invalid: empty profiles with defaultProfileName set",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			expectErrors:   3,
			expectedFields: []string{"scheduler.profiles", "scheduler.profiles", "scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired, field.ErrorTypeRequired, field.ErrorTypeInvalid},
		},
		// single kube
		{
			name: "valid: single kube default",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			expectErrors: 0,
		},
		// single kai without kube → invalid because default-scheduler profile is always required
		{
			name: "invalid: single kai without kube profile",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKai}},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
			},
			expectErrors:   1,
			expectedFields: []string{"scheduler.profiles"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		// kai with kube
		{
			name: "valid: kai and kube profiles with kai default",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKai},
					{Name: configv1alpha1.SchedulerNameKube},
				},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
			},
			expectErrors: 0,
		},
		// multiple schedulers, kube default
		{
			name: "valid: multiple schedulers kube default",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKube},
					{Name: configv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			expectErrors: 0,
		},
		// multiple schedulers, kai default
		{
			name: "valid: multiple schedulers kai default",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKube},
					{Name: configv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
			},
			expectErrors: 0,
		},
		// defaultProfileName omitted (pre-defaulting → Required)
		{
			name: "invalid: defaultProfileName omitted",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKube},
					{Name: configv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: "",
			},
			expectErrors:   1,
			expectedFields: []string{"scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		// invalid defaultProfileName (not in supported list; not in profiles → Invalid)
		{
			name: "invalid: defaultProfileName not in profiles (e.g. invalid-scheduler)",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKube},
					{Name: configv1alpha1.SchedulerNameKai},
				},
				DefaultProfileName: "invalid-scheduler",
			},
			expectErrors:   1,
			expectedFields: []string{"scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		// defaultProfileName is kube but kube not in profiles
		{
			name: "invalid: defaultProfileName not in profiles (default-scheduler but only kai in profiles)",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKai}},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			expectErrors:   2,
			expectedFields: []string{"scheduler.profiles", "scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired, field.ErrorTypeInvalid},
		},
		// empty name in profile
		{
			name: "invalid: profile with empty name",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: ""},
				},
				DefaultProfileName: "default-scheduler",
			},
			expectErrors:   3,
			expectedFields: []string{"scheduler.profiles[0].name", "scheduler.profiles", "scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired, field.ErrorTypeRequired, field.ErrorTypeInvalid},
		},
		// unsupported profile name
		{
			name: "invalid: unsupported profile name",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerName("volcano")},
				},
				DefaultProfileName: "volcano",
			},
			expectErrors:   3,
			expectedFields: []string{"scheduler.profiles[0].name", "scheduler.profiles", "scheduler.defaultProfileName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeNotSupported, field.ErrorTypeRequired, field.ErrorTypeInvalid},
		},
		// duplicate profile names
		{
			name: "invalid: duplicate profile names",
			scheduler: &configv1alpha1.SchedulerConfiguration{
				Profiles: []configv1alpha1.SchedulerProfile{
					{Name: configv1alpha1.SchedulerNameKube},
					{Name: configv1alpha1.SchedulerNameKube},
				},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			expectErrors:   1,
			expectedFields: []string{"scheduler.profiles[1].name"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeDuplicate},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateSchedulerConfiguration(test.scheduler, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func TestValidateLeaderElectionConfiguration(t *testing.T) {
	fldPath := field.NewPath("leaderElection")
	validCfg := configv1alpha1.LeaderElectionConfiguration{
		Enabled:       true,
		LeaseDuration: metav1.Duration{Duration: 15 * time.Second},
		RenewDeadline: metav1.Duration{Duration: 10 * time.Second},
		RetryPeriod:   metav1.Duration{Duration: 2 * time.Second},
		ResourceLock:  "leases",
		ResourceName:  "grove-operator-leader-election",
	}
	tests := []struct {
		name           string
		config         configv1alpha1.LeaderElectionConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name:         "valid: disabled - skips all checks",
			config:       configv1alpha1.LeaderElectionConfiguration{Enabled: false},
			expectErrors: 0,
		},
		{
			name:         "valid: all fields properly configured",
			config:       validCfg,
			expectErrors: 0,
		},
		{
			name: "invalid: leaseDuration is zero",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.LeaseDuration = metav1.Duration{}
				return c
			}(),
			expectErrors:   2,
			expectedFields: []string{"leaderElection.leaseDuration", "leaderElection.leaseDuration"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid, field.ErrorTypeInvalid},
		},
		{
			name: "invalid: renewDeadline is zero",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.RenewDeadline = metav1.Duration{}
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.renewDeadline"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: retryPeriod is zero",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.RetryPeriod = metav1.Duration{}
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.retryPeriod"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: leaseDuration equal to renewDeadline",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.LeaseDuration = metav1.Duration{Duration: 10 * time.Second}
				c.RenewDeadline = metav1.Duration{Duration: 10 * time.Second}
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.leaseDuration"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: leaseDuration less than renewDeadline",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.LeaseDuration = metav1.Duration{Duration: 5 * time.Second}
				c.RenewDeadline = metav1.Duration{Duration: 10 * time.Second}
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.leaseDuration"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: empty resourceLock",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.ResourceLock = ""
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.resourceLock"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		{
			name: "invalid: empty resourceName",
			config: func() configv1alpha1.LeaderElectionConfiguration {
				c := validCfg
				c.ResourceName = ""
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"leaderElection.resourceName"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeRequired},
		},
		{
			name: "invalid: all durations zero, missing resourceLock and resourceName",
			config: configv1alpha1.LeaderElectionConfiguration{
				Enabled: true,
			},
			expectErrors: 6,
			expectedFields: []string{
				"leaderElection.leaseDuration",
				"leaderElection.renewDeadline",
				"leaderElection.retryPeriod",
				"leaderElection.leaseDuration",
				"leaderElection.resourceLock",
				"leaderElection.resourceName",
			},
			expectedTypes: []field.ErrorType{
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
				field.ErrorTypeRequired,
				field.ErrorTypeRequired,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateLeaderElectionConfiguration(test.config, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func TestValidateClientConnectionConfiguration(t *testing.T) {
	fldPath := field.NewPath("clientConnection")
	tests := []struct {
		name           string
		config         configv1alpha1.ClientConnectionConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name:         "valid: zero burst",
			config:       configv1alpha1.ClientConnectionConfiguration{Burst: 0},
			expectErrors: 0,
		},
		{
			name:         "valid: positive burst",
			config:       configv1alpha1.ClientConnectionConfiguration{Burst: 120},
			expectErrors: 0,
		},
		{
			name:         "valid: positive QPS and burst",
			config:       configv1alpha1.ClientConnectionConfiguration{QPS: 100.0, Burst: 120},
			expectErrors: 0,
		},
		{
			name:           "invalid: negative burst",
			config:         configv1alpha1.ClientConnectionConfiguration{Burst: -1},
			expectErrors:   1,
			expectedFields: []string{"clientConnection.burst"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateClientConnectionConfiguration(test.config, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func TestValidateControllerConfiguration(t *testing.T) {
	fldPath := field.NewPath("controllers")
	validCfg := configv1alpha1.ControllerConfiguration{
		PodCliqueSet:          configv1alpha1.PodCliqueSetControllerConfiguration{ConcurrentSyncs: intPtr(10)},
		PodClique:             configv1alpha1.PodCliqueControllerConfiguration{ConcurrentSyncs: intPtr(10)},
		PodCliqueScalingGroup: configv1alpha1.PodCliqueScalingGroupControllerConfiguration{ConcurrentSyncs: intPtr(5)},
		PodGang:               configv1alpha1.PodGangControllerConfiguration{ConcurrentSyncs: intPtr(5)},
	}
	tests := []struct {
		name           string
		config         configv1alpha1.ControllerConfiguration
		expectErrors   int
		expectedFields []string
		expectedTypes  []field.ErrorType
	}{
		{
			name:         "valid: all controllers properly configured",
			config:       validCfg,
			expectErrors: 0,
		},
		{
			name: "invalid: all concurrentSyncs nil",
			config: configv1alpha1.ControllerConfiguration{
				PodCliqueSet:          configv1alpha1.PodCliqueSetControllerConfiguration{},
				PodClique:             configv1alpha1.PodCliqueControllerConfiguration{},
				PodCliqueScalingGroup: configv1alpha1.PodCliqueScalingGroupControllerConfiguration{},
				PodGang:               configv1alpha1.PodGangControllerConfiguration{},
			},
			expectErrors: 4,
			expectedFields: []string{
				"controllers.podCliqueSet.concurrentSyncs",
				"controllers.podCliqueScalingGroup.concurrentSyncs",
				"controllers.podClique.concurrentSyncs",
				"controllers.podGang.concurrentSyncs",
			},
			expectedTypes: []field.ErrorType{
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
				field.ErrorTypeInvalid,
			},
		},
		{
			name: "invalid: only podCliqueSet has invalid concurrentSyncs",
			config: func() configv1alpha1.ControllerConfiguration {
				c := validCfg
				c.PodCliqueSet.ConcurrentSyncs = intPtr(0)
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"controllers.podCliqueSet.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
		{
			name: "invalid: only podGang has invalid concurrentSyncs",
			config: func() configv1alpha1.ControllerConfiguration {
				c := validCfg
				c.PodGang.ConcurrentSyncs = intPtr(-1)
				return c
			}(),
			expectErrors:   1,
			expectedFields: []string{"controllers.podGang.concurrentSyncs"},
			expectedTypes:  []field.ErrorType{field.ErrorTypeInvalid},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := validateControllerConfiguration(test.config, fldPath)

			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)

			if test.expectErrors > 0 {
				for i, expectedField := range test.expectedFields {
					assert.Equal(t, expectedField, errs[i].Field, "error %d: expected field %s but got %s", i, expectedField, errs[i].Field)
					if i < len(test.expectedTypes) {
						assert.Equal(t, test.expectedTypes[i], errs[i].Type, "error %d: expected type %s but got %s", i, test.expectedTypes[i], errs[i].Type)
					}
				}
			}
		})
	}
}

func TestValidateOperatorConfiguration(t *testing.T) {
	validConfig := func() *configv1alpha1.OperatorConfiguration {
		return &configv1alpha1.OperatorConfiguration{
			LogLevel:  configv1alpha1.InfoLevel,
			LogFormat: configv1alpha1.LogFormatJSON,
			Scheduler: configv1alpha1.SchedulerConfiguration{
				Profiles:           []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKube}},
				DefaultProfileName: string(configv1alpha1.SchedulerNameKube),
			},
			LeaderElection: configv1alpha1.LeaderElectionConfiguration{
				Enabled:       true,
				LeaseDuration: metav1.Duration{Duration: 15 * time.Second},
				RenewDeadline: metav1.Duration{Duration: 10 * time.Second},
				RetryPeriod:   metav1.Duration{Duration: 2 * time.Second},
				ResourceLock:  "leases",
				ResourceName:  "grove-operator-leader-election",
			},
			ClientConnection: configv1alpha1.ClientConnectionConfiguration{
				QPS:   100.0,
				Burst: 120,
			},
			Controllers: configv1alpha1.ControllerConfiguration{
				PodCliqueSet:          configv1alpha1.PodCliqueSetControllerConfiguration{ConcurrentSyncs: intPtr(10)},
				PodClique:             configv1alpha1.PodCliqueControllerConfiguration{ConcurrentSyncs: intPtr(10)},
				PodCliqueScalingGroup: configv1alpha1.PodCliqueScalingGroupControllerConfiguration{ConcurrentSyncs: intPtr(5)},
				PodGang:               configv1alpha1.PodGangControllerConfiguration{ConcurrentSyncs: intPtr(5)},
			},
			TopologyAwareScheduling: configv1alpha1.TopologyAwareSchedulingConfiguration{Enabled: false},
		}
	}

	tests := []struct {
		name         string
		config       *configv1alpha1.OperatorConfiguration
		expectErrors int
	}{
		{
			name:         "valid: fully configured",
			config:       validConfig(),
			expectErrors: 0,
		},
		{
			name: "valid: leader election disabled",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.LeaderElection = configv1alpha1.LeaderElectionConfiguration{Enabled: false}
				return c
			}(),
			expectErrors: 0,
		},
		{
			name: "valid: empty log level and format (skipped)",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.LogLevel = ""
				c.LogFormat = ""
				return c
			}(),
			expectErrors: 0,
		},
		{
			name: "invalid: unsupported log level",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.LogLevel = "verbose"
				return c
			}(),
			expectErrors: 1,
		},
		{
			name: "invalid: unsupported log format",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.LogFormat = "yaml"
				return c
			}(),
			expectErrors: 1,
		},
		{
			name: "invalid: errors from multiple sections",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.LogLevel = "verbose"
				c.ClientConnection.Burst = -1
				c.Controllers.PodCliqueSet.ConcurrentSyncs = intPtr(0)
				return c
			}(),
			expectErrors: 3,
		},
		{
			name: "invalid: scheduler missing kube profile and controller nil syncs",
			config: func() *configv1alpha1.OperatorConfiguration {
				c := validConfig()
				c.Scheduler = configv1alpha1.SchedulerConfiguration{
					Profiles:           []configv1alpha1.SchedulerProfile{{Name: configv1alpha1.SchedulerNameKai}},
					DefaultProfileName: string(configv1alpha1.SchedulerNameKai),
				}
				c.Controllers.PodGang.ConcurrentSyncs = nil
				return c
			}(),
			expectErrors: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := ValidateOperatorConfiguration(test.config)
			assert.Len(t, errs, test.expectErrors, "expected %d validation errors but got %d: %v", test.expectErrors, len(errs), errs)
		})
	}
}
