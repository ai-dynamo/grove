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

package conversion

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	grovecorev1beta1 "github.com/ai-dynamo/grove/operator/api/core/v1beta1"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Service is the address of the operator's webhook server, at which the conversion webhook is served.
type Service struct {
	// Name is the name of the Service in front of the webhook server.
	Name string
	// Namespace is the namespace of the Service.
	Namespace string
	// Port is the port of the webhook server.
	Port int
}

const (
	// keepInterval bounds how long a reverted conversion webhook or a rotated CA in manual certificate mode
	// takes to be corrected in the CRDs.
	keepInterval = 30 * time.Second
)

var (
	conversionPath   = []string{"spec", "conversion"}
	clientConfigPath = []string{"spec", "conversion", "webhook", "clientConfig"}
)

// ConfigureCRDs points the conversion webhook of every converted CRD at service. The caBundle is left to its injector. It fails when a CRD does not serve grove.io/v1beta1, as
// the CRDs of an earlier release would leave the v1beta1 admission webhooks unmatched.
func ConfigureCRDs(ctx context.Context, cl client.Client, log logr.Logger, service Service) error {
	for _, name := range ConvertedCRDNames() {
		crd, err := getCRD(ctx, cl, name)
		if err != nil {
			return err
		}
		if err := checkServesHub(crd); err != nil {
			return err
		}
		if err := configureCRD(ctx, cl, log, crd, service, ""); err != nil {
			return err
		}
	}
	return nil
}

// NewCRDConversionKeeper returns a Runnable that periodically re-applies ConfigureCRDs, so that re-applying
// the CRD manifests, which carry the default Service address, does not leave conversion broken. When caFile is
// set, it also keeps the caBundle equal to the file's contents. That is needed when certificates are provided
// externally, where cert-controller does not inject the CA. Every replica runs it, since the patch is idempotent.
func NewCRDConversionKeeper(cl client.Client, log logr.Logger, service Service, caFile string) manager.Runnable {
	return &crdConversionKeeper{
		client:   cl,
		log:      log,
		service:  service,
		caFile:   caFile,
		interval: keepInterval,
	}
}

type crdConversionKeeper struct {
	client   client.Client
	log      logr.Logger
	service  Service
	caFile   string
	interval time.Duration
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (k *crdConversionKeeper) NeedLeaderElection() bool {
	return false
}

// Start implements manager.Runnable.
func (k *crdConversionKeeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()
	for {
		if err := k.keep(ctx); err != nil {
			k.log.Error(err, "Failed to keep the conversion webhook of the CRDs", "caFile", k.caFile)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (k *crdConversionKeeper) keep(ctx context.Context) error {
	caBundle := ""
	if k.caFile != "" {
		ca, err := os.ReadFile(filepath.Clean(k.caFile))
		if err != nil {
			return err
		}
		if len(ca) == 0 {
			return fmt.Errorf("CA file %s is empty", k.caFile)
		}
		caBundle = base64.StdEncoding.EncodeToString(ca)
	}
	for _, name := range ConvertedCRDNames() {
		crd, err := getCRD(ctx, k.client, name)
		if err != nil {
			return err
		}
		if err := configureCRD(ctx, k.client, k.log, crd, k.service, caBundle); err != nil {
			return err
		}
	}
	return nil
}

// configureCRD patches the conversion webhook of crd when it differs from the wanted one. An empty caBundle
// leaves the caBundle unchanged.
func configureCRD(ctx context.Context, cl client.Client, log logr.Logger, crd *unstructured.Unstructured, service Service, caBundle string) error {
	desired := crd.DeepCopy()
	if err := setWebhookConversion(desired, service); err != nil {
		return fmt.Errorf("failed to set the conversion webhook of CRD %q: %w", crd.GetName(), err)
	}
	if caBundle != "" {
		if err := unstructured.SetNestedField(desired.Object, caBundle, append(clientConfigPath, "caBundle")...); err != nil {
			return fmt.Errorf("failed to set the caBundle of CRD %q: %w", crd.GetName(), err)
		}
	}
	current, _, _ := unstructured.NestedFieldNoCopy(crd.Object, conversionPath...)
	wanted, _, _ := unstructured.NestedFieldNoCopy(desired.Object, conversionPath...)
	if equality.Semantic.DeepEqual(current, wanted) {
		return nil
	}
	if err := cl.Patch(ctx, desired, client.MergeFrom(crd)); err != nil {
		return fmt.Errorf("failed to patch the conversion webhook of CRD %q: %w", crd.GetName(), err)
	}
	log.Info("Configured conversion webhook of CRD", "name", crd.GetName(), "service", service.Name,
		"serviceNamespace", service.Namespace, "port", service.Port, "caBundleUpdated", caBundle != "")
	return nil
}

func getCRD(ctx context.Context, cl client.Client, name string) (*unstructured.Unstructured, error) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, crd); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("CRD %q is not installed: %w", name, err)
		}
		return nil, fmt.Errorf("failed to get CRD %q: %w", name, err)
	}
	return crd, nil
}

func checkServesHub(crd *unstructured.Unstructured) error {
	versions, _, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil {
		return fmt.Errorf("failed to read the versions of CRD %q: %w", crd.GetName(), err)
	}
	for _, v := range versions {
		version, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if version["name"] == grovecorev1beta1.SchemeGroupVersion.Version && version["served"] == true {
			return nil
		}
	}
	return fmt.Errorf("CRD %q does not serve %s; apply the CRDs of this release before starting the operator",
		crd.GetName(), grovecorev1beta1.SchemeGroupVersion)
}

func setWebhookConversion(crd *unstructured.Unstructured, service Service) error {
	fields := []struct {
		value any
		path  []string
	}{
		{string(apiextensionsv1.WebhookConverter), append(conversionPath, "strategy")},
		{service.Name, append(clientConfigPath, "service", "name")},
		{service.Namespace, append(clientConfigPath, "service", "namespace")},
		{WebhookPath, append(clientConfigPath, "service", "path")},
		{int64(service.Port), append(clientConfigPath, "service", "port")},
		{[]any{"v1"}, append(conversionPath, "webhook", "conversionReviewVersions")},
	}
	for _, f := range fields {
		if err := unstructured.SetNestedField(crd.Object, f.value, f.path...); err != nil {
			return err
		}
	}
	return nil
}
