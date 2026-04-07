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

package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSetDefaults_SchedulerConfiguration(t *testing.T) {
	tests := []struct {
		name               string
		cfg                *SchedulerConfiguration
		wantProfiles       []SchedulerProfile
		wantDefaultProfile string
	}{
		{
			name:               "empty profiles: add kube and set defaultProfileName",
			cfg:                &SchedulerConfiguration{},
			wantProfiles:       []SchedulerProfile{{Name: SchedulerNameKube}},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "nil profiles (len 0): add kube and set defaultProfileName",
			cfg: &SchedulerConfiguration{
				Profiles:           nil,
				DefaultProfileName: "",
			},
			wantProfiles:       []SchedulerProfile{{Name: SchedulerNameKube}},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "only kai in profiles: append kube and set defaultProfileName",
			cfg: &SchedulerConfiguration{
				Profiles:           []SchedulerProfile{{Name: SchedulerNameKai}},
				DefaultProfileName: "",
			},
			wantProfiles:       []SchedulerProfile{{Name: SchedulerNameKai}, {Name: SchedulerNameKube}},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "only kube in profiles, defaultProfileName unset: set defaultProfileName",
			cfg: &SchedulerConfiguration{
				Profiles:           []SchedulerProfile{{Name: SchedulerNameKube}},
				DefaultProfileName: "",
			},
			wantProfiles:       []SchedulerProfile{{Name: SchedulerNameKube}},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "kube and kai in profiles, defaultProfileName unset: set defaultProfileName to kube",
			cfg: &SchedulerConfiguration{
				Profiles: []SchedulerProfile{
					{Name: SchedulerNameKube},
					{Name: SchedulerNameKai},
				},
				DefaultProfileName: "",
			},
			wantProfiles: []SchedulerProfile{
				{Name: SchedulerNameKube},
				{Name: SchedulerNameKai},
			},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "kube and kai in profiles, defaultProfileName already set to kube: no change",
			cfg: &SchedulerConfiguration{
				Profiles: []SchedulerProfile{
					{Name: SchedulerNameKube},
					{Name: SchedulerNameKai},
				},
				DefaultProfileName: string(SchedulerNameKube),
			},
			wantProfiles: []SchedulerProfile{
				{Name: SchedulerNameKube},
				{Name: SchedulerNameKai},
			},
			wantDefaultProfile: string(SchedulerNameKube),
		},
		{
			name: "kube and kai in profiles, defaultProfileName already set to kai: no change",
			cfg: &SchedulerConfiguration{
				Profiles: []SchedulerProfile{
					{Name: SchedulerNameKube},
					{Name: SchedulerNameKai},
				},
				DefaultProfileName: string(SchedulerNameKai),
			},
			wantProfiles: []SchedulerProfile{
				{Name: SchedulerNameKube},
				{Name: SchedulerNameKai},
			},
			wantDefaultProfile: string(SchedulerNameKai),
		},
		{
			name: "only kai in profiles, defaultProfileName already kai: append kube only",
			cfg: &SchedulerConfiguration{
				Profiles:           []SchedulerProfile{{Name: SchedulerNameKai}},
				DefaultProfileName: string(SchedulerNameKai),
			},
			wantProfiles:       []SchedulerProfile{{Name: SchedulerNameKai}, {Name: SchedulerNameKube}},
			wantDefaultProfile: string(SchedulerNameKai),
		},
		{
			name: "volcano profile gets default queue config",
			cfg: &SchedulerConfiguration{
				Profiles: []SchedulerProfile{
					{Name: SchedulerNameVolcano},
					{Name: SchedulerNameKube},
				},
				DefaultProfileName: string(SchedulerNameVolcano),
			},
			wantProfiles: []SchedulerProfile{
				{Name: SchedulerNameVolcano, Config: mustRawExtension(t, VolcanoSchedulerConfiguration{Queue: "default"})},
				{Name: SchedulerNameKube},
			},
			wantDefaultProfile: string(SchedulerNameVolcano),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetDefaults_SchedulerConfiguration(tt.cfg)
			assert.Equal(t, tt.wantProfiles, tt.cfg.Profiles, "Profiles after defaulting")
			assert.Equal(t, tt.wantDefaultProfile, tt.cfg.DefaultProfileName, "DefaultProfileName after defaulting")
		})
	}
}

func mustRawExtension(t *testing.T, in any) *runtime.RawExtension {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}
	return &runtime.RawExtension{Raw: raw}
}
