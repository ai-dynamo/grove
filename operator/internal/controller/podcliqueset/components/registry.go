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

package components

import (
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/hash"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/hpa"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podclique"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podcliquescalinggroup"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podcliquesetreplica"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podgang"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/role"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/rolebinding"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/satokensecret"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/service"
	"github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/serviceaccount"
	"github.com/ai-dynamo/grove/operator/internal/mnnvl/computedomain"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// CreateOperatorRegistry initializes the operator registry for the PodCliqueSet reconciler
func CreateOperatorRegistry(mgr manager.Manager, eventRecorder record.EventRecorder, topologyAwareSchedulingConfig configv1alpha1.TopologyAwareSchedulingConfiguration, networkConfig configv1alpha1.NetworkAcceleration, podTemplateSpecHashCache *hash.PodTemplateSpecHashCache) component.OperatorRegistry[v1alpha1.PodCliqueSet] {
	cl := mgr.GetClient()
	reg := component.NewOperatorRegistry[v1alpha1.PodCliqueSet]()
	reg.Register(component.KindPodClique, podclique.New(cl, mgr.GetScheme(), eventRecorder, podTemplateSpecHashCache))
	reg.Register(component.KindHeadlessService, service.New(cl, mgr.GetScheme()))
	reg.Register(component.KindRole, role.New(cl, mgr.GetScheme()))
	reg.Register(component.KindRoleBinding, rolebinding.New(cl, mgr.GetScheme()))
	reg.Register(component.KindServiceAccount, serviceaccount.New(cl, mgr.GetScheme()))
	reg.Register(component.KindServiceAccountTokenSecret, satokensecret.New(cl, mgr.GetScheme()))
	reg.Register(component.KindPodCliqueScalingGroup, podcliquescalinggroup.New(cl, mgr.GetScheme(), eventRecorder))
	reg.Register(component.KindHorizontalPodAutoscaler, hpa.New(cl, mgr.GetScheme()))
	reg.Register(component.KindPodGang, podgang.New(cl, mgr.GetScheme(), eventRecorder, topologyAwareSchedulingConfig))
	reg.Register(component.KindPodCliqueSetReplica, podcliquesetreplica.New(cl, eventRecorder))

	// Only register ComputeDomain operator if MNNVL is enabled
	if networkConfig.AutoMNNVLEnabled {
		reg.Register(component.KindComputeDomain, computedomain.New(cl, mgr.GetScheme(), eventRecorder))
	}

	return reg
}
