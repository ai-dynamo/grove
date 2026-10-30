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

package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCRDInstallerOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    *CRDInstallerOptions
		wantErr bool
	}{
		{
			name: "defaults",
			want: &CRDInstallerOptions{ConversionWebhookPort: defaultConversionWebhookPort},
		},
		{
			name: "all options",
			args: []string{"--upgrade-crds", "--conversion-webhook-port=8443", "--conversion-webhook-ca-file=/etc/certs/ca.crt"},
			want: &CRDInstallerOptions{
				UpgradeCRDs:             true,
				ConversionWebhookPort:   8443,
				ConversionWebhookCAFile: "/etc/certs/ca.crt",
			},
		},
		{
			name: "version",
			args: []string{"--version"},
			want: &CRDInstallerOptions{ConversionWebhookPort: defaultConversionWebhookPort, Version: true},
		},
		{
			name:    "unknown flag",
			args:    []string{"--unknown"},
			wantErr: true,
		},
		{
			name:    "positional argument",
			args:    []string{"unexpected"},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseCRDInstallerOptions(test.args)
			if test.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, errParseCLIArgs))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}
