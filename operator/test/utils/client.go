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

	groveclientscheme "github.com/ai-dynamo/grove/operator/internal/client"

	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ClientMethod is a name of the method on client.Client for which an error is recorded.
type ClientMethod string

const (
	// ClientMethodGet is the name of the Get method on client.Client.
	ClientMethodGet ClientMethod = "Get"
	// ClientMethodList is the name of the List method on client.Client.
	ClientMethodList ClientMethod = "List"
	// ClientMethodCreate is the name of the Create method on client.Client.
	ClientMethodCreate ClientMethod = "Create"
	// ClientMethodDelete is the name of the Delete method on client.Client.
	ClientMethodDelete ClientMethod = "Delete"
	// ClientMethodDeleteAll is the name of the DeleteAllOf method on client.Client.
	ClientMethodDeleteAll ClientMethod = "DeleteAll"
	// ClientMethodPatch is the name of the Patch method on client.Client.
	ClientMethodPatch ClientMethod = "Patch"
	// ClientMethodUpdate is the name of the Update method on client.Client.
	ClientMethodUpdate ClientMethod = "Update"
	// ClientMethodStatusPatch is the name of the Patch method on the status subresource writer.
	ClientMethodStatusPatch ClientMethod = "StatusPatch"
	// ClientMethodStatusUpdate is the name of the Update method on the status subresource writer.
	ClientMethodStatusUpdate ClientMethod = "StatusUpdate"
)

// TestClientBuilder is a builder for creating a test client.Client which is capable of recording and replaying errors.
type TestClientBuilder struct {
	delegatingClientBuilder *fake.ClientBuilder
	delegatingClient        client.Client
	scheme                  *runtime.Scheme
	errorRecords            []errorRecord
}

type errorRecord struct {
	method      ClientMethod
	objectKey   client.ObjectKey
	labels      labels.Set
	resourceGVK schema.GroupVersionKind
	err         error
	// remainingFailures is nil for an error that fires on every matching call. When non-nil it is the
	// number of leading consecutive matching calls that still return the error. It decrements per
	// matching call and once it reaches zero the call succeeds.
	remainingFailures *int
}

// CreateDefaultFakeClient creates a default client.Client without any configured reactions to errors.
func CreateDefaultFakeClient(existingObjects []client.Object) client.Client {
	clientBuilder := NewTestClientBuilder()
	if len(existingObjects) > 0 {
		clientBuilder.WithObjects(existingObjects...)
	}
	return clientBuilder.Build()
}

// CreateFakeClientForObjects creates a fake client.Client with initial set of existing objects
// along with any expected errors for the Get, Create, Patch, and Delete client methods.
func CreateFakeClientForObjects(getErr, createErr, patchErr, deleteErr *apierrors.StatusError, existingObjects []client.Object) {
	clientBuilder := NewTestClientBuilder()
	if len(existingObjects) > 0 {
		clientBuilder.WithObjects(existingObjects...)
	}

	for _, obj := range existingObjects {
		objKey := client.ObjectKeyFromObject(obj)
		clientBuilder.RecordErrorForObjects(ClientMethodGet, getErr, objKey)
		clientBuilder.RecordErrorForObjects(ClientMethodCreate, createErr, objKey)
		clientBuilder.RecordErrorForObjects(ClientMethodPatch, patchErr, objKey)
		clientBuilder.RecordErrorForObjects(ClientMethodDelete, deleteErr, objKey)
	}
}

// CreateFakeClientForObjectsMatchingLabels creates a fake client.Client with initial set of existing objects
// along with any expected list and/or deleteAll errors for objects matching the given matching labels for the given GVK.
// If the matchingLabels is nil or empty then the deleteAll/list error will be recorded for all objects matching the GVK in the given namespace.
func CreateFakeClientForObjectsMatchingLabels(deleteErr, listErr *apierrors.StatusError, namespace string, targetObjectsGVK schema.GroupVersionKind, matchingLabels map[string]string, existingObjects ...client.Object) client.Client {
	clientBuilder := NewTestClientBuilder()
	for _, existingObject := range existingObjects {
		// Errors recorded for individual object delete
		clientBuilder.WithObjects(existingObject).
			RecordErrorForObjectsMatchingLabels(ClientMethodDelete, client.ObjectKeyFromObject(existingObject), targetObjectsGVK, matchingLabels, deleteErr)
	}
	return clientBuilder.
		RecordErrorForObjectsMatchingLabels(ClientMethodList, client.ObjectKey{Namespace: namespace}, targetObjectsGVK, matchingLabels, listErr).
		RecordErrorForObjectsMatchingLabels(ClientMethodDelete, client.ObjectKey{Namespace: namespace}, targetObjectsGVK, matchingLabels, deleteErr).
		Build()
}

// ------------------- Functions to explicitly create and configure a test client builder -------------------

// NewTestClientBuilder creates a new TestClientBuilder with a default scheme.
// Tests that use scheduler-specific types must provide a scheme containing
// those types with WithScheme.
func NewTestClientBuilder() *TestClientBuilder {
	return &TestClientBuilder{
		delegatingClientBuilder: fake.NewClientBuilder(),
		scheme:                  groveclientscheme.Scheme,
	}
}

// WithClient sets the delegating client for the TestClientBuilder.
func (b *TestClientBuilder) WithClient(cl client.Client) *TestClientBuilder {
	b.delegatingClient = cl
	return b
}

// WithScheme sets the scheme used by the delegating fake client.
func (b *TestClientBuilder) WithScheme(scheme *runtime.Scheme) *TestClientBuilder {
	b.scheme = scheme
	return b
}

// WithObjects initializes the delegating fake client builder with objects.
func (b *TestClientBuilder) WithObjects(objects ...client.Object) *TestClientBuilder {
	if len(objects) > 0 {
		b.delegatingClientBuilder.WithObjects(objects...)
	}
	return b
}

// WithStatusSubresource registers types that have status subresources so that Status().Patch() works with the fake client.
func (b *TestClientBuilder) WithStatusSubresource(objs ...client.Object) *TestClientBuilder {
	if len(objs) > 0 {
		b.delegatingClientBuilder.WithStatusSubresource(objs...)
	}
	return b
}

// WithIndex registers a field index on the delegating fake client so that List calls using a
// field selector on that field are served.
func (b *TestClientBuilder) WithIndex(obj client.Object, field string, extractValue client.IndexerFunc) *TestClientBuilder {
	b.delegatingClientBuilder.WithIndex(obj, field, extractValue)
	return b
}

// RecordErrorForObjects records an error for a specific client.Client method and object keys.
func (b *TestClientBuilder) RecordErrorForObjects(method ClientMethod, err *apierrors.StatusError, objectKeys ...client.ObjectKey) *TestClientBuilder {
	// this method records error, so if nil error is passed then there is no need to create any error record.
	if err == nil {
		return b
	}
	for _, objectKey := range objectKeys {
		b.errorRecords = append(b.errorRecords, errorRecord{
			method:    method,
			objectKey: objectKey,
			err:       err,
		})
	}
	return b
}

// RecordErrorForObjectsNTimes records an error that is returned for the first consecutiveFailures
// matching calls of the given method on the given object keys. Every call after that succeeds and
// delegates to the underlying client. A consecutiveFailures less than or equal to zero records
// nothing.
func (b *TestClientBuilder) RecordErrorForObjectsNTimes(method ClientMethod, err *apierrors.StatusError, consecutiveFailures int, objectKeys ...client.ObjectKey) *TestClientBuilder {
	if err == nil || consecutiveFailures <= 0 {
		return b
	}
	for _, objectKey := range objectKeys {
		remaining := consecutiveFailures
		b.errorRecords = append(b.errorRecords, errorRecord{
			method:            method,
			objectKey:         objectKey,
			err:               err,
			remainingFailures: &remaining,
		})
	}
	return b
}

// RecordErrorForObjectsMatchingLabels records an error for a specific client.Client method and object keys matching the given labels for a given GVK.
func (b *TestClientBuilder) RecordErrorForObjectsMatchingLabels(method ClientMethod, objectKey client.ObjectKey, targetObjectsGVK schema.GroupVersionKind, matchingLabels map[string]string, err *apierrors.StatusError) *TestClientBuilder {
	if err == nil {
		return b
	}
	b.errorRecords = append(b.errorRecords, errorRecord{
		method:      method,
		objectKey:   objectKey,
		resourceGVK: targetObjectsGVK,
		labels:      matchingLabels,
		err:         err,
	})
	return b
}

// Build creates a test client.Client with the configured reactions to errors.
func (b *TestClientBuilder) Build() client.Client {
	return &testClient{
		delegate:     b.getClient(),
		errorRecords: b.errorRecords,
	}
}

func (b *TestClientBuilder) getClient() client.Client {
	if b.delegatingClient != nil {
		return b.delegatingClient
	}
	return b.delegatingClientBuilder.WithScheme(b.scheme).Build()
}

// ---------------------------------- Implementation of client.Client ----------------------------------

// testClient is a client.Client implementation which reacts to the configured errors.
type testClient struct {
	delegate     client.Client
	errorRecords []errorRecord
}

func (c *testClient) Get(ctx context.Context, objKey client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.getRecordedObjectError(ClientMethodGet, objKey); err != nil {
		return err
	}
	return c.delegate.Get(ctx, objKey, obj, opts...)
}

func (c *testClient) List(ctx context.Context, objList client.ObjectList, opts ...client.ListOption) error {
	listOpts := client.ListOptions{}
	listOpts.ApplyOptions(opts)
	gvk, err := apiutil.GVKForObject(objList, c.delegate.Scheme())
	if err != nil {
		return err
	}
	if err = c.getRecordedObjectCollectionError(ClientMethodList, listOpts.Namespace, listOpts.LabelSelector, gvk); err != nil {
		return err
	}
	return c.delegate.List(ctx, objList, opts...)
}

func (c *testClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.getRecordedObjectError(ClientMethodCreate, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return c.delegate.Create(ctx, obj, opts...)
}

func (c *testClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := c.getRecordedObjectError(ClientMethodDelete, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return c.delegate.Delete(ctx, obj, opts...)
}

func (c *testClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	deleteOpts := client.DeleteAllOfOptions{}
	deleteOpts.ApplyOptions(opts)
	gvk, err := apiutil.GVKForObject(obj, c.delegate.Scheme())
	if err != nil {
		return err
	}
	if err = c.getRecordedObjectCollectionError(ClientMethodDeleteAll, deleteOpts.Namespace, deleteOpts.LabelSelector, gvk); err != nil {
		return err
	}
	return c.delegate.DeleteAllOf(ctx, obj, opts...)
}

func (c *testClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.getRecordedObjectError(ClientMethodPatch, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return c.delegate.Patch(ctx, obj, patch, opts...)
}

func (c *testClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.getRecordedObjectError(ClientMethodUpdate, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return c.delegate.Update(ctx, obj, opts...)
}

func (c *testClient) Apply(ctx context.Context, applyConfig runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	// For apply operations, we need to get the object key from the apply configuration
	// Since ApplyConfiguration doesn't have an easy way to get object key, we'll delegate to the underlying client
	return c.delegate.Apply(ctx, applyConfig, opts...)
}

func (c *testClient) Status() client.StatusWriter {
	return &testStatusWriter{testClient: c, delegate: c.delegate.Status()}
}

func (c *testClient) SubResource(subResource string) client.SubResourceClient {
	return c.delegate.SubResource(subResource)
}

func (c *testClient) Scheme() *runtime.Scheme {
	return c.delegate.Scheme()
}

func (c *testClient) RESTMapper() meta.RESTMapper {
	return c.delegate.RESTMapper()
}

func (c *testClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.delegate.GroupVersionKindFor(obj)
}

func (c *testClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return c.delegate.IsObjectNamespaced(obj)
}

// testStatusWriter wraps the delegate status writer and reacts to errors recorded for the
// StatusPatch and StatusUpdate methods before delegating to the underlying fake client.
type testStatusWriter struct {
	testClient *testClient
	delegate   client.SubResourceWriter
}

func (w *testStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return w.delegate.Create(ctx, obj, subResource, opts...)
}

func (w *testStatusWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return w.delegate.Apply(ctx, obj, opts...)
}

func (w *testStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if err := w.testClient.getRecordedObjectError(ClientMethodStatusUpdate, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return w.delegate.Update(ctx, obj, opts...)
}

func (w *testStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if err := w.testClient.getRecordedObjectError(ClientMethodStatusPatch, client.ObjectKeyFromObject(obj)); err != nil {
		return err
	}
	return w.delegate.Patch(ctx, obj, patch, opts...)
}

// ---------------------------------- Helper methods ----------------------------------

func (c *testClient) getRecordedObjectError(method ClientMethod, objKey client.ObjectKey) error {
	for i := range c.errorRecords {
		rec := &c.errorRecords[i]
		if rec.method != method || rec.objectKey != objKey {
			continue
		}
		if rec.remainingFailures == nil {
			return rec.err
		}
		if *rec.remainingFailures > 0 {
			*rec.remainingFailures--
			return rec.err
		}
		return nil
	}
	return nil
}

func (c *testClient) getRecordedObjectCollectionError(method ClientMethod, namespace string, labelSelector labels.Selector, objGVK schema.GroupVersionKind) error {
	foundErrRecord, ok := lo.Find(c.errorRecords, func(errRecord errorRecord) bool {
		if errRecord.method == method && errRecord.objectKey.Namespace == namespace && errRecord.resourceGVK == objGVK {
			// check if the error record has labels defined and passed labelSelector is not nil, then check for match.
			if len(errRecord.labels) > 0 && labelSelector != nil {
				return labelSelector.Matches(errRecord.labels)
			}
			return true
		}
		return false
	})
	return lo.Ternary(ok, foundErrRecord.err, nil)
}
