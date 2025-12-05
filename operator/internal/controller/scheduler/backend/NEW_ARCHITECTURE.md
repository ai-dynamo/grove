# 调度器后端新架构说明

## 🎯 核心设计

### PodGang 作为统一的中间表示

**核心理念**：无论使用哪个调度器，Operator 始终创建 `scheduler.grove.io/v1alpha1/PodGang` 作为 gang scheduling 的统一抽象。Backend 负责将 PodGang 转换成各自调度器所需的 CR。

## 📊 架构图

```
┌────────────────────────────────────────────────────────────────┐
│ 用户层：创建 PodCliqueSet                                       │
└────────────────────────────────────────────────────────────────┘
                            ↓
┌────────────────────────────────────────────────────────────────┐
│ Phase 1: Operator 创建 PodGang (统一中间表示)                  │
│                                                                │
│ PodCliqueSet → PodGang (scheduler.grove.io/v1alpha1)          │
│                                                                │
│ Labels:                                                        │
│   grove.io/scheduler-backend: "kai" or "default"              │
└────────────────────────────────────────────────────────────────┘
                            ↓
┌────────────────────────────────────────────────────────────────┐
│ Phase 2: Backend 转换 PodGang → 调度器特定 CR                  │
└────────────────────────────────────────────────────────────────┘
                            ↓
        ┌───────────────────┴───────────────────┐
        │                                       │
   ┌────▼────┐                          ┌──────▼─────┐
   │   KAI   │                          │  Default   │
   │ Backend │                          │  Backend   │
   └────┬────┘                          └──────┬─────┘
        │                                      │
    PodGang →                              PodGang →
    PodGroup                                Workload
    (run.ai)                          (scheduling.k8s.io)
        │                                      │
  ┌─────▼──────┐                      ┌───────▼────────┐
  │ PodGroup   │                      │   Workload     │
  │ APIVersion:│                      │   APIVersion:  │
  │ scheduling │                      │   scheduling   │
  │ .run.ai/   │                      │   .k8s.io/     │
  │ v2alpha2   │                      │   v1alpha1     │
  └────────────┘                      └────────────────┘
```

## 💡 实现细节

### Step 1: Operator 创建 PodGang

无论 `schedulerName` 是什么，Operator 始终创建 PodGang：

```go
// In PodCliqueSet controller
func (r *Reconciler) reconcilePodGang(pcs *PodCliqueSet) error {
    for i := 0; i < pcs.Spec.Replicas; i++ {
        podGang := &PodGang{
            ObjectMeta: metav1.ObjectMeta{
                Name: fmt.Sprintf("%s-%d", pcs.Name, i),
                Namespace: pcs.Namespace,
                Labels: map[string]string{
                    // 根据 schedulerName 设置 backend 标签
                    "grove.io/scheduler-backend": determineBackend(pcs),
                },
            },
            Spec: PodGangSpec{
                PodGroups: buildPodGroups(pcs, i),
                // ... 其他字段
            },
        }
        createOrUpdate(podGang)
    }
}

func determineBackend(pcs *PodCliqueSet) string {
    schedulerName := pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName
    if schedulerName == "" || schedulerName == "default-scheduler" {
        return "default"
    }
    return "kai"  // 或其他 backend
}
```

### Step 2: Backend Controller 监听并转换

每个 backend 作为独立的 controller 运行：

```go
// KAI Backend Controller
type KAIBackendReconciler struct {
    client.Client
    Scheme  *runtime.Scheme
    Backend backend.SchedulerBackend
}

func (r *KAIBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) {
    // 1. 获取 PodGang
    podGang := &PodGang{}
    if err := r.Get(ctx, req.NamespacedName, podGang); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }
    
    // 2. 检查是否由此 backend 处理
    if !r.Backend.Matches(podGang) {
        return ctrl.Result{}, nil  // 跳过
    }
    
    // 3. 转换 PodGang → PodGroup
    if err := r.Backend.Sync(ctx, logger, podGang); err != nil {
        return ctrl.Result{}, err
    }
    
    return ctrl.Result{}, nil
}
```

## 📝 Backend 实现示例

### KAI Backend（生成 run.ai PodGroup）

```go
func (b *KAIBackend) Sync(ctx context.Context, logger logr.Logger, podGang *PodGang) error {
    // 转换成 run.ai PodGroup (类似 posgroups.yaml)
    podGroup := &unstructured.Unstructured{}
    podGroup.SetGroupVersionKind(schema.GroupVersionKind{
        Group:   "scheduling.run.ai",
        Version: "v2alpha2",
        Kind:    "PodGroup",
    })
    
    podGroup.SetName(fmt.Sprintf("pg-%s-%s", podGang.Name, podGang.UID))
    podGroup.SetNamespace(podGang.Namespace)
    
    // 设置 ownerReference 指向 PodGang
    controllerutil.SetOwnerReference(podGang, podGroup, b.scheme)
    
    // 构建 spec
    spec := map[string]interface{}{
        "minMember": calculateTotalMinMember(podGang),
        "queue": "default-queue",
        "subGroups": buildSubGroups(podGang.Spec.PodGroups),
    }
    unstructured.SetNestedMap(podGroup.Object, spec, "spec")
    
    // 创建或更新
    return b.client.Patch(ctx, podGroup, client.Apply)
}
```

### Default Backend（生成 K8s Workload）

```go
func (b *DefaultBackend) Sync(ctx context.Context, logger logr.Logger, podGang *PodGang) error {
    // 转换成 K8s Workload
    workload := &schedulingv1alpha1.Workload{
        ObjectMeta: metav1.ObjectMeta{
            Name:      podGang.Name,
            Namespace: podGang.Namespace,
        },
        Spec: schedulingv1alpha1.WorkloadSpec{
            // 从 PodGang 提取信息
        },
    }
    
    // 设置 ownerReference 指向 PodGang
    controllerutil.SetOwnerReference(podGang, workload, b.scheme)
    
    // 创建或更新
    return b.client.Patch(ctx, workload, client.Apply)
}
```

## 🔑 关键特性

### 1. 标签驱动的 Backend 选择

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  labels:
    grove.io/scheduler-backend: "kai"  # 或 "default", "koordinator"
```

### 2. OwnerReference 链

```
PodCliqueSet (owns) → PodGang (owns) → PodGroup/Workload
```

删除 PodCliqueSet 会级联删除所有资源。

### 3. 双向同步

- **Forward**: PodGang 变化 → Backend 同步到调度器 CR
- **Status**: 调度器 CR 状态 → 反馈到 PodGang.Status

## 📋 完整示例

### 用户创建 PodCliqueSet

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
          schedulerName: "kai-scheduler"  # 触发 KAI backend
```

### Operator 创建 PodGang

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  name: ml-training-0
  namespace: default
  labels:
    grove.io/scheduler-backend: "kai"  # 根据 schedulerName 设置
  ownerReferences:
  - apiVersion: grove.io/v1alpha1
    kind: PodCliqueSet
    name: ml-training
spec:
  podgroups:
  - name: ml-training-0-master
    minReplicas: 1
    podReferences: [...]
```

### KAI Backend 生成 PodGroup

```yaml
apiVersion: scheduling.run.ai/v2alpha2
kind: PodGroup
metadata:
  name: pg-ml-training-0-{uid}
  namespace: default
  ownerReferences:
  - apiVersion: scheduler.grove.io/v1alpha1
    kind: PodGang
    name: ml-training-0
spec:
  minMember: 1
  queue: default-queue
  subGroups:
  - name: ml-training-0-master
    minMember: 1
```

## 🆚 对比旧架构

| 特性 | 旧架构 | 新架构 |
|------|--------|--------|
| 中间表示 | ❌ 无 | ✅ PodGang |
| Backend 耦合 | ❌ 耦合在 Operator | ✅ 独立 Controller |
| 可观测性 | ❌ 低 | ✅ 高（可直接查看 PodGang） |
| 扩展性 | ⚠️ 中等 | ✅ 优秀 |
| 调试友好性 | ⚠️ 困难 | ✅ 容易 |

## 🚀 部署方式

### 方式 1: 集成在 Operator 中

Backend controllers 作为 Operator 的一部分运行：

```go
func main() {
    mgr, _ := ctrl.NewManager(...)
    
    // Register backend controllers
    kaiBE, _ := kai.NewBackend(...)
    if err := (&KAIBackendReconciler{
        Client: mgr.GetClient(),
        Backend: kaiBE,
    }).SetupWithManager(mgr); err != nil {
        panic(err)
    }
    
    mgr.Start(...)
}
```

### 方式 2: 独立部署

每个 backend 可以作为独立的 deployment：

```bash
# 部署 KAI backend
kubectl apply -f kai-backend-deployment.yaml

# 部署 Default backend  
kubectl apply -f default-backend-deployment.yaml
```

## 📊 监控和可观测性

### 查看 PodGang

```bash
# 查看所有 PodGang
kubectl get podgangs

# 查看详细信息
kubectl describe podgang ml-training-0
```

### 查看生成的 CR

```bash
# 查看 KAI PodGroup
kubectl get podgroups.scheduling.run.ai

# 查看 Workload
kubectl get workloads.scheduling.k8s.io
```

### 跟踪转换关系

```bash
# 查看 ownerReferences
kubectl get podgroup pg-ml-training-0-{uid} -o yaml | grep -A5 ownerReferences
```

## ✅ 优势总结

1. **解耦**: Operator 不需要知道具体调度器实现
2. **统一**: PodGang 作为单一真相来源
3. **灵活**: 可以动态启用/禁用 backend
4. **可观测**: 清晰的资源层次和转换关系
5. **可扩展**: 轻松添加新的调度器支持
6. **容错**: 即使 backend 失败，PodGang 仍存在

这个架构完全符合 Kubernetes 的声明式设计理念，提供了更好的解耦、可观测性和可维护性！

