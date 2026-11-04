// Copyright 2024 The Grove Authors.
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

// +k8s:deepcopy-gen=package
// +k8s:defaulter-gen=TypeMeta
// +k8s:conversion-gen=github.com/ai-dynamo/grove/operator/api/config/v1beta1
// +groupName=operator.config.grove.io

// Package v1alpha1 is the deprecated operator.config.grove.io/v1alpha1 configuration API. The operator still
// accepts it and converts it to operator.config.grove.io/v1beta1.
package v1alpha1
