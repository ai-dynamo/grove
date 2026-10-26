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

package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"
	schedulertest "github.com/ai-dynamo/grove/operator/test/utils/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestBackend_PreparePod(t *testing.T) {
	cl := testutils.CreateDefaultFakeClient(nil)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube}
	b := New(cl, cl.Scheme(), recorder, profile)

	pod := testutils.NewPodBuilder("test-pod", "default").Build()

	require.NoError(t, b.PreparePod(pod))

	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
}

func TestBackend_Init(t *testing.T) {
	workloadUnavailable := &meta.NoKindMatchError{
		GroupKind: schema.GroupKind{Group: "scheduling.k8s.io", Kind: "Workload"},
	}
	podGroupUnavailable := apierrors.NewNotFound(schema.GroupResource{Group: "scheduling.k8s.io", Resource: "podgroups"}, "")
	compositeUnavailable := &meta.NoKindMatchError{
		GroupKind: schema.GroupKind{Group: "scheduling.k8s.io", Kind: "CompositePodGroup"},
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "scheduling.k8s.io", Resource: "workloads"}, "", errors.New("list denied"))
	tests := []struct {
		name      string
		config    *runtime.RawExtension
		failKind  string
		cause     error
		wantCalls []string
	}{
		{name: "omitted config"},
		{name: "empty config", config: &runtime.RawExtension{}},
		{name: "disabled", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":false}`)}},
		{
			name:      "enabled",
			config:    &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			wantCalls: []string{"WorkloadList", "PodGroupList", "CompositePodGroupList"},
		},
		{
			name: "Workload unavailable", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			failKind: "WorkloadList", cause: workloadUnavailable, wantCalls: []string{"WorkloadList"},
		},
		{
			name: "PodGroup unavailable", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			failKind: "PodGroupList", cause: podGroupUnavailable, wantCalls: []string{"WorkloadList", "PodGroupList"},
		},
		{
			name: "CompositePodGroup unavailable", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			failKind: "CompositePodGroupList", cause: compositeUnavailable, wantCalls: []string{"WorkloadList", "PodGroupList", "CompositePodGroupList"},
		},
		{
			name: "RBAC denied", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			failKind: "WorkloadList", cause: forbidden, wantCalls: []string{"WorkloadList"},
		},
		{
			name: "request timeout", config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			failKind: "WorkloadList", cause: context.DeadlineExceeded, wantCalls: []string{"WorkloadList"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			var calls []string
			directClient := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					deadline, ok := ctx.Deadline()
					require.True(t, ok, "startup probes must have a deadline")
					require.Positive(t, time.Until(deadline))
					require.LessOrEqual(t, time.Until(deadline), workloadAPICheckTimeout)
					listOptions := &client.ListOptions{}
					listOptions.ApplyOptions(opts)
					assert.Equal(t, int64(1), listOptions.Limit)
					gvk, err := cl.GroupVersionKindFor(list)
					require.NoError(t, err)
					calls = append(calls, gvk.Kind)
					if gvk.Kind == tt.failKind {
						return tt.cause
					}
					return cl.List(ctx, list, opts...)
				},
			}).Build()
			b := New(nil, scheme, nil, configv1alpha1.SchedulerProfile{
				Name: configv1alpha1.SchedulerNameKube, Config: tt.config,
			})
			err := b.Init(directClient)
			require.ErrorIs(t, err, tt.cause)
			assert.Equal(t, tt.cause == nil && len(tt.wantCalls) > 0, b.(*schedulerBackend).gangSchedulingEnabled)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestBackend_InitRejectsInvalidConfig(t *testing.T) {
	b := New(nil, runtime.NewScheme(), nil, configv1alpha1.SchedulerProfile{
		Name:   configv1alpha1.SchedulerNameKube,
		Config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":`)},
	})
	var syntaxError *json.SyntaxError
	require.ErrorAs(t, b.Init(nil), &syntaxError)
}

func TestValidatePodCliqueSet_AcceptsTopologyConstraints(t *testing.T) {
	b := &schedulerBackend{gangSchedulingEnabled: true}
	pcs := &grovecorev1alpha1.PodCliqueSet{ObjectMeta: metav1.ObjectMeta{Name: "pcs"}}
	require.NoError(t, b.ValidatePodCliqueSet(t.Context(), pcs))
	constraint := &grovecorev1alpha1.TopologyConstraint{
		Pack: &grovecorev1alpha1.TopologyPackConstraint{RequiredDomain: "rack", PreferredDomain: "host"},
	}
	pcs.Spec.Template.TopologyConstraint = constraint
	pcs.Spec.Template.Cliques = []*grovecorev1alpha1.PodCliqueTemplateSpec{{Name: "worker", TopologyConstraint: constraint}}
	pcs.Spec.Template.PodCliqueScalingGroupConfigs = []grovecorev1alpha1.PodCliqueScalingGroupConfig{{Name: "workers", TopologyConstraint: constraint}}
	require.NoError(t, b.ValidatePodCliqueSet(t.Context(), pcs), "required and preferred constraints are accepted at every level")
}

func TestValidatePodCliqueSet_PriorityClassUpdates(t *testing.T) {
	for _, strategy := range []grovecorev1alpha1.UpdateStrategyType{
		grovecorev1alpha1.RollingRecreateStrategy, grovecorev1alpha1.OnDeleteStrategy, grovecorev1alpha1.CoherentStrategy,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			backend := &schedulerBackend{gangSchedulingEnabled: true}
			pcs := &grovecorev1alpha1.PodCliqueSet{Spec: grovecorev1alpha1.PodCliqueSetSpec{
				UpdateStrategy: &grovecorev1alpha1.PodCliqueSetUpdateStrategy{Type: strategy},
				Template:       grovecorev1alpha1.PodCliqueSetTemplateSpec{PriorityClassName: "original"},
			}}
			require.NoError(t, backend.ValidatePodCliqueSet(t.Context(), pcs), "creation remains supported")
			old := pcs.DeepCopy()
			pcs.Annotations = map[string]string{"example.com/note": "updated"}
			require.NoError(t, backend.ValidatePodCliqueSet(t.Context(), pcs, scheduler.WithOldPodCliqueSet(old)))
			for _, priority := range []string{"replacement", ""} {
				pcs.Spec.Template.PriorityClassName = priority
				err := backend.ValidatePodCliqueSet(t.Context(), pcs, scheduler.WithOldPodCliqueSet(old))
				require.Error(t, err)
				aggregate, ok := err.(utilerrors.Aggregate)
				require.True(t, ok)
				require.Len(t, aggregate.Errors(), 1)
				fieldErr, ok := aggregate.Errors()[0].(*field.Error)
				require.True(t, ok)
				assert.Equal(t, field.ErrorTypeInvalid, fieldErr.Type)
				assert.Equal(t, "spec.template.priorityClassName", fieldErr.Field)
			}
			backend.gangSchedulingEnabled = false
			require.NoError(t, backend.ValidatePodCliqueSet(t.Context(), pcs, scheduler.WithOldPodCliqueSet(old)),
				"plain default-scheduler must not inherit WAS restrictions")
		})
	}
}

func TestBackend_InitWithWorkloadAPIs(t *testing.T) {
	tests := []struct {
		name    string
		gates   string
		enabled bool
	}{
		{
			name:  "WAS disabled",
			gates: "GenericWorkload=false,CompositePodGroup=false,TopologyAwareWorkloadScheduling=false",
		},
		{
			name:  "composite support disabled",
			gates: "GenericWorkload=true,CompositePodGroup=false,TopologyAwareWorkloadScheduling=true",
		},
		{
			name:    "hierarchical WAS enabled",
			gates:   "GenericWorkload=true,CompositePodGroup=true,TopologyAwareWorkloadScheduling=true",
			enabled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := schedulertest.StartWorkloadEnvtest(t, tt.gates)
			scheme := runtime.NewScheme()
			directClient, err := client.New(config, client.Options{Scheme: scheme})
			require.NoError(t, err)

			disabled := New(nil, scheme, nil, configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameKube})
			require.NoError(t, disabled.Init(directClient))
			enabled := New(nil, scheme, nil, configv1alpha1.SchedulerProfile{
				Name:   configv1alpha1.SchedulerNameKube,
				Config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
			})
			err = enabled.Init(directClient)
			if tt.enabled {
				require.NoError(t, err)
			} else {
				require.True(t, meta.IsNoMatchError(err), "missing APIs must fail initialization, got %v", err)
			}
		})
	}
}

func TestSyncPodGang_TemplateListLimitsWithWorkloadAPIs(t *testing.T) {
	const testNamespace = "test-ns"
	config := schedulertest.StartWorkloadEnvtest(t, "GenericWorkload=true,CompositePodGroup=true,TopologyAwareWorkloadScheduling=true")
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	directClient, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, directClient.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}))
	backend := New(directClient, scheme, nil, configv1alpha1.SchedulerProfile{
		Name:   configv1alpha1.SchedulerNameKube,
		Config: &runtime.RawExtension{Raw: []byte(`{"gangScheduling":true}`)},
	})
	require.NoError(t, backend.Init(directClient))
	podGang := testutils.NewPodGangBuilder("was-limits", testNamespace).Build()
	podGang.UID = "was-limits"
	for i := range 8 {
		direct, grouped := fmt.Sprintf("direct-%d", i), fmt.Sprintf("grouped-%d", i)
		podGang.Spec.PodGroups = append(podGang.Spec.PodGroups,
			groveschedulerv1alpha1.PodGroup{Name: direct, MinReplicas: 1},
			groveschedulerv1alpha1.PodGroup{Name: grouped, MinReplicas: 1})
		podGang.Spec.TopologyConstraintGroupConfigs = append(podGang.Spec.TopologyConstraintGroupConfigs,
			groveschedulerv1alpha1.TopologyConstraintGroupConfig{Name: fmt.Sprintf("group-%d", i), PodGroupNames: []string{grouped}})
	}
	require.NoError(t, backend.SyncPodGang(t.Context(), podGang))
	composites := &schedulingv1alpha3.CompositePodGroupList{}
	require.NoError(t, directClient.List(t.Context(), composites, client.InNamespace(testNamespace)))
	require.Len(t, composites.Items, 9)
	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, directClient.List(t.Context(), leaves, client.InNamespace(testNamespace)))
	require.Len(t, leaves.Items, 16)
}
