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

package kubernetes

import (
	"github.com/ai-dynamo/grove/operator/internal/utils"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// CreateObjectKeyForCreateWebhooks creates an object key for an object handled by webhooks registered for CREATE verbs.
func CreateObjectKeyForCreateWebhooks(obj client.Object, req admission.Request) client.ObjectKey {
	namespace := obj.GetNamespace()
	// In webhooks the namespace is not always set in objects due to https://github.com/kubernetes/kubernetes/issues/88282,
	// so try to get the namespace information from the request directly, otherwise the object is presumably not namespaced.
	if utils.IsEmptyStringType(namespace) && len(req.Namespace) != 0 {
		namespace = req.Namespace
	}
	name := obj.GetName()
	if len(name) == 0 {
		name = obj.GetGenerateName()
	}
	return client.ObjectKey{Namespace: namespace, Name: name}
}
