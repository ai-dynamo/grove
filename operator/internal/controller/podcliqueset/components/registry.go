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
	"github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/controller/common/component"
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

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// CreateOperatorRegistry initializes the operator registry for the PodCliqueSet reconciler
func CreateOperatorRegistry(mgr manager.Manager, eventRecorder record.EventRecorder) component.OperatorRegistry[v1alpha1.PodCliqueSet] {
	cl := mgr.GetClient()
	reg := component.NewOperatorRegistry[v1alpha1.PodCliqueSet]()
	reg.Register(component.KindPodClique, podclique.New(cl, mgr.GetScheme(), eventRecorder))
	reg.Register(component.KindHeadlessService, service.New(cl, mgr.GetScheme()))
	reg.Register(component.KindRole, role.New(cl, mgr.GetScheme()))
	reg.Register(component.KindRoleBinding, rolebinding.New(cl, mgr.GetScheme()))
	reg.Register(component.KindServiceAccount, serviceaccount.New(cl, mgr.GetScheme()))
	reg.Register(component.KindServiceAccountTokenSecret, satokensecret.New(cl, mgr.GetScheme()))
	reg.Register(component.KindPodCliqueScalingGroup, podcliquescalinggroup.New(cl, mgr.GetScheme(), eventRecorder))
	reg.Register(component.KindHorizontalPodAutoscaler, hpa.New(cl, mgr.GetScheme()))
	reg.Register(component.KindPodGang, podgang.New(cl, mgr.GetScheme(), eventRecorder))
	// NOTE: Workload is NOT registered here - it's managed by Backend Controllers
	// Backend Controllers watch PodGang and convert to scheduler-specific CRs (Workload/PodGroup)
	reg.Register(component.KindPodCliqueSetReplica, podcliquesetreplica.New(cl, eventRecorder))
	return reg
}
