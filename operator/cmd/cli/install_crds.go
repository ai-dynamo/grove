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
	"fmt"

	"github.com/spf13/pflag"
)

const (
	crdInstallerName             = "grove-install-crds"
	defaultConversionWebhookPort = 9443
)

// CRDInstallerOptions defines options for launching the CRD installer.
type CRDInstallerOptions struct {
	// UpgradeCRDs indicates whether to apply the bundled CRDs.
	UpgradeCRDs bool
	// ConversionWebhookPort is the port of the conversion webhook Service.
	ConversionWebhookPort int
	// ConversionWebhookCAFile contains the conversion webhook CA certificate.
	ConversionWebhookCAFile string
	// Version indicates whether to print the installer version and exit.
	Version bool
}

// ParseCRDInstallerOptions parses the CRD installer's CLI arguments.
func ParseCRDInstallerOptions(cliArgs []string) (*CRDInstallerOptions, error) {
	options := &CRDInstallerOptions{}
	flagSet := pflag.NewFlagSet(crdInstallerName, pflag.ContinueOnError)
	options.mapFlags(flagSet)
	if err := flagSet.Parse(cliArgs); err != nil {
		return nil, fmt.Errorf("%w: %w", errParseCLIArgs, err)
	}
	if flagSet.NArg() != 0 {
		return nil, fmt.Errorf("%w: unexpected arguments %q", errParseCLIArgs, flagSet.Args())
	}
	return options, nil
}

func (o *CRDInstallerOptions) mapFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.UpgradeCRDs, "upgrade-crds", false, "Upgrade CRDs from the bundle shipped with this release.")
	fs.IntVar(&o.ConversionWebhookPort, "conversion-webhook-port", defaultConversionWebhookPort, "Port of the conversion webhook Service.")
	fs.StringVar(&o.ConversionWebhookCAFile, "conversion-webhook-ca-file", "", "File containing the conversion webhook CA certificate.")
	fs.BoolVarP(&o.Version, "version", "v", false, "Print the version and exit.")
}
