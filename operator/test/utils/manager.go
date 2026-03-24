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
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// FakeManager is a fake controller-runtime manager
type FakeManager struct {
	manager.Manager
	Client        client.Client
	Scheme        *runtime.Scheme
	Logger        logr.Logger
	WebhookServer webhook.Server
}

// GetClient returns the client registered with the fake manager
func (f *FakeManager) GetClient() client.Client {
	return f.Client
}

// GetLogger returns the logger registered with the fake manager
func (f *FakeManager) GetLogger() logr.Logger {
	return f.Logger
}

// GetScheme returns the scheme registered with the fake manager
func (f FakeManager) GetScheme() *runtime.Scheme {
	return f.Scheme
}

// GetAPIReader returns a client.Reader backed by the fake manager's client.
func (f *FakeManager) GetAPIReader() client.Reader {
	return f.Client
}

// GetWebhookServer returns the webhook server registered with the fake manager
func (f *FakeManager) GetWebhookServer() webhook.Server {
	return f.WebhookServer
}
