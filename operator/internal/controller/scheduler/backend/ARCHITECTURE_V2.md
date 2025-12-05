# Scheduler Backend Architecture V2

## 核心理念

**PodGang 作为统一的中间表示**：无论使用哪个调度器，Operator 始终创建 `scheduler.grove.io/v1alpha1/PodGang` 作为 gang scheduling 的统一抽象。不同的 backend 负责将 PodGang 转换成各自调度器所需的 CR。

## 架构流程

```
┌─────────────────────────────────────────────────────────────┐
│ Phase 1: Operator 创建统一的 PodGang (中间表示)              │
└─────────────────────────────────────────────────────────────┘
                            ↓
         PodCliqueSet → PodGang (scheduler.grove.io)
                            ↓
┌─────────────────────────────────────────────────────────────┐
│ Phase 2: Backend 转换 PodGang → 调度器特定 CR               │
└─────────────────────────────────────────────────────────────┘
                            ↓
        ┌───────────────────┴───────────────────┐
        │                                       │
   KAI Backend                          Default Backend
        │                                       │
   PodGang →                               PodGang →
   PodGroup                                Workload
   (类似 posgroups.yaml)              (K8s Workload API)
```

## 设计优势

### 1. PodGang 作为统一接口
- **单一真相来源**: PodGang 包含所有 gang scheduling 信息
- **解耦**: Operator 不需要知道具体使用哪个调度器
- **可观测性**: 用户可以直接查看 PodGang 了解调度状态

### 2. Backend 作为转换器
- **职责清晰**: Backend 只负责转换和同步
- **易于扩展**: 添加新调度器只需实现转换逻辑
- **独立演进**: Backend 可以独立于 Operator 更新

### 3. 两阶段优势
- **容错性**: 即使 Backend 失败，PodGang 仍然存在
- **调试友好**: 可以检查 PodGang 和目标 CR 的对应关系
- **灵活性**: 可以暂时禁用 Backend，只使用 PodGang

## 组件职责

### Operator (Phase 1)
```go
// 始终创建 PodGang，无论 schedulerName 是什么
func (r *PodCliqueSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) {
    // 1. 获取 PodCliqueSet
    pcs := &grovecorev1alpha1.PodCliqueSet{}
    
    // 2. 创建/更新 PodGang (统一的中间表示)
    podGang := buildPodGang(pcs)
    createOrUpdate(podGang)
    
    // 3. Backend 会自动监听 PodGang 并转换
}
```

### Backend (Phase 2)
```go
// Backend 监听 PodGang 并转换成调度器特定 CR
func (b *KAIBackend) Sync(ctx context.Context, podGang *PodGang) {
    // 将 PodGang 转换成 PodGroup (类似 posgroups.yaml)
    podGroup := b.ConvertPodGangToPodGroup(podGang)
    createOrUpdate(podGroup)
}

func (b *DefaultBackend) Sync(ctx context.Context, podGang *PodGang) {
    // 将 PodGang 转换成 Workload
    workload := b.ConvertPodGangToWorkload(podGang)
    createOrUpdate(workload)
}
```

## 工作流程详解

### Step 1: 用户创建 PodCliqueSet
```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: ml-training
spec:
  replicas: 2
  template:
    cliques:
    - name: master
      spec:
        replicas: 1
        minAvailable: 1
        podSpec:
          schedulerName: "kai-scheduler"  # 指定调度器
    - name: workers
      spec:
        replicas: 4
        minAvailable: 3
```

### Step 2: Operator 创建 PodGang (统一)
```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  name: ml-training-0
  namespace: default
  labels:
    grove.io/scheduler: "kai-scheduler"  # 标记目标调度器
spec:
  podgroups:
  - name: ml-training-0-master
    minReplicas: 1
    podReferences:
    - namespace: default
      name: ml-training-0-master-abc
  - name: ml-training-0-workers
    minReplicas: 3
    podReferences:
    - namespace: default
      name: ml-training-0-workers-xyz
    - namespace: default
      name: ml-training-0-workers-def
    - namespace: default
      name: ml-training-0-workers-ghi
```

### Step 3a: KAI Backend 转换 → PodGroup
```yaml
# Backend 根据 PodGang 生成 (类似 posgroups.yaml)
apiVersion: scheduling.x-k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: ml-training-0
  namespace: default
  ownerReferences:
  - apiVersion: scheduler.grove.io/v1alpha1
    kind: PodGang
    name: ml-training-0
spec:
  minMember: 4  # 1 master + 3 workers
  scheduleTimeoutSeconds: 600
  podGroups:
  - name: master
    minMember: 1
  - name: workers
    minMember: 3
```

### Step 3b: Default Backend 转换 → Workload
```yaml
# Backend 根据 PodGang 生成
apiVersion: scheduling.k8s.io/v1alpha1
kind: Workload
metadata:
  name: ml-training-0
  namespace: default
  ownerReferences:
  - apiVersion: scheduler.grove.io/v1alpha1
    kind: PodGang
    name: ml-training-0
spec:
  podSets:
  - name: master
    count: 1
  - name: workers
    count: 3
```

## Backend 接口更新

### 新的 SchedulerBackend 接口
```go
type SchedulerBackend interface {
    // Name returns the backend name
    Name() string
    
    // Matches checks if this backend should handle the PodGang
    // Based on labels or annotations on PodGang
    Matches(podGang *PodGang) bool
    
    // Sync converts PodGang to scheduler-specific CR
    Sync(ctx context.Context, logger logr.Logger, podGang *PodGang) error
    
    // Delete removes scheduler-specific CRs for this PodGang
    Delete(ctx context.Context, logger logr.Logger, podGang *PodGang) error
    
    // GetSchedulingGateName returns the gate name for this backend
    GetSchedulingGateName() string
    
    // CheckReady checks if the scheduler-specific CR is ready
    CheckReady(ctx context.Context, podGang *PodGang) (bool, error)
}
```

### Backend 实现示例

#### KAI Backend
```go
func (b *KAIBackend) Sync(ctx context.Context, logger logr.Logger, podGang *PodGang) error {
    // 转换 PodGang → PodGroup (类似 posgroups.yaml)
    podGroup := &schedulingv1alpha1.PodGroup{
        ObjectMeta: metav1.ObjectMeta{
            Name:      podGang.Name,
            Namespace: podGang.Namespace,
        },
        Spec: schedulingv1alpha1.PodGroupSpec{
            MinMember: calculateTotalMinMember(podGang),
            PodGroups: convertToPodGroupList(podGang.Spec.PodGroups),
        },
    }
    
    // 设置 owner reference
    controllerutil.SetOwnerReference(podGang, podGroup, b.scheme)
    
    // 创建或更新
    return b.client.Patch(ctx, podGroup, client.Apply)
}

func convertToPodGroupList(gangGroups []PodGroup) []PodGroupSpec {
    result := make([]PodGroupSpec, len(gangGroups))
    for i, gg := range gangGroups {
        result[i] = PodGroupSpec{
            Name:      gg.Name,
            MinMember: gg.MinReplicas,
        }
    }
    return result
}
```

#### Default Backend
```go
func (b *DefaultBackend) Sync(ctx context.Context, logger logr.Logger, podGang *PodGang) error {
    // 转换 PodGang → Workload
    workload := &schedulingv1alpha1.Workload{
        ObjectMeta: metav1.ObjectMeta{
            Name:      podGang.Name,
            Namespace: podGang.Namespace,
        },
        Spec: schedulingv1alpha1.WorkloadSpec{
            PodSets: convertToPodSets(podGang.Spec.PodGroups),
        },
    }
    
    // 设置 owner reference
    controllerutil.SetOwnerReference(podGang, workload, b.scheme)
    
    // 创建或更新
    return b.client.Patch(ctx, workload, client.Apply)
}
```

## Backend Controller

每个 backend 可以作为独立的 controller 运行，监听 PodGang 变化：

```go
// KAI Backend Controller
func (r *KAIBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) {
    // 1. 获取 PodGang
    podGang := &schedulerv1alpha1.PodGang{}
    if err := r.Get(ctx, req.NamespacedName, podGang); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // 2. 检查是否应该由此 backend 处理
    if !r.backend.Matches(podGang) {
        return ctrl.Result{}, nil
    }
    
    // 3. 同步转换
    if err := r.backend.Sync(ctx, r.logger, podGang); err != nil {
        return ctrl.Result{}, err
    }
    
    // 4. 更新 PodGang 状态
    return r.updatePodGangStatus(ctx, podGang)
}

// 设置 watch
func (r *KAIBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&schedulerv1alpha1.PodGang{}).
        Owns(&schedulingv1alpha1.PodGroup{}).
        Complete(r)
}
```

## 对比：旧架构 vs 新架构

### 旧架构 (V1)
```
PodCliqueSet
     ↓
Backend 直接选择
     ↓
创建 Workload 或 PodGang
```

**问题**:
- ❌ PodGang 只在使用 KAI 时创建
- ❌ 没有统一的中间表示
- ❌ Backend 耦合在 operator 中

### 新架构 (V2)
```
PodCliqueSet
     ↓
Operator 创建 PodGang (统一)
     ↓
Backend Controller 监听并转换
     ↓
创建 PodGroup/Workload/etc
```

**优势**:
- ✅ PodGang 始终存在，作为统一接口
- ✅ Backend 解耦，可独立部署
- ✅ 更好的可观测性
- ✅ 容错性更强

## 标签约定

在 PodGang 上使用标签来标识目标调度器：

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  labels:
    grove.io/scheduler-backend: "kai"  # 或 "default", "koordinator"
```

Backend 根据此标签决定是否处理。

## 迁移路径

### Phase 1: 保持兼容
- Operator 同时支持旧逻辑（直接创建 CR）和新逻辑（创建 PodGang）
- 通过 feature gate 控制

### Phase 2: 逐步迁移
- 默认启用 PodGang 作为中间表示
- Backend 作为独立 controller 部署

### Phase 3: 完全切换
- 移除旧逻辑
- Backend 成为标准组件

## 总结

新架构的核心是：
1. **PodGang 作为统一的 gang scheduling 抽象**
2. **Backend 作为转换器和同步器**
3. **解耦 Operator 和调度器实现**

这样的设计更符合 Kubernetes 的最佳实践，提供了更好的扩展性和可维护性。

