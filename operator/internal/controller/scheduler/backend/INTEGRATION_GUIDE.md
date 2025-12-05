# Backend 集成指南

## 🎯 目标

将新的调度器后端架构集成到 Grove Operator 中，实现：
1. Operator **始终创建 PodGang**（无论使用哪个调度器）
2. Backend Controllers 监听 PodGang 并转换成调度器特定 CR
3. 完全解耦的架构

## 📋 集成步骤

### Step 1: 更新 Component Registry

在 `operator/internal/controller/podcliqueset/components/registry.go` 中注册新的统一 PodGang 组件：

```go
import (
    // ... 其他imports
    podgang_unified "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podgang_unified"
)

func CreateOperatorRegistry(mgr manager.Manager, eventRecorder record.EventRecorder) component.OperatorRegistry[v1alpha1.PodCliqueSet] {
    cl := mgr.GetClient()
    reg := component.NewOperatorRegistry[v1alpha1.PodCliqueSet]()
    
    // ... 其他组件注册 ...
    
    // 使用新的统一 PodGang 组件（始终创建 PodGang）
    reg.Register(component.KindPodGang, podgang_unified.New(cl, mgr.GetScheme(), eventRecorder))
    
    // 移除旧的条件性 PodGang/Workload 组件
    // reg.Register(component.KindPodGang, podgang.New(...))  // DELETE
    // reg.Register(component.KindWorkload, workload.New(...)) // DELETE
    
    return reg
}
```

### Step 2: 在 Main 中注册 Backend Controllers

在 `operator/cmd/main.go` 中添加 backend controllers：

```go
import (
    // ... 其他imports
    backendcontroller "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/controller"
    _ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend" // 触发backends注册
)

func main() {
    // ... 现有的 manager 设置 ...
    
    mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
        // ... options ...
    })
    if err != nil {
        setupLog.Error(err, "unable to start manager")
        os.Exit(1)
    }
    
    // ... 注册现有的 reconcilers ...
    
    // 注册 Backend Controllers（新增）
    if err := backendcontroller.SetupBackendControllers(mgr, setupLog); err != nil {
        setupLog.Error(err, "unable to setup backend controllers")
        os.Exit(1)
    }
    setupLog.Info("Backend controllers registered successfully")
    
    // ... 启动 manager ...
    if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
        setupLog.Error(err, "problem running manager")
        os.Exit(1)
    }
}
```

### Step 3: 更新 RBAC 权限

确保 Operator 有权限操作 PodGang 和各个调度器的 CR。

在 `operator/charts/templates/clusterrole.yaml` 中：

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .Values.clusterRole.name }}
rules:
# PodGang (始终需要)
- apiGroups:
  - scheduler.grove.io
  resources:
  - podgangs
  - podgangs/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete

# KAI Scheduler - PodGroup
- apiGroups:
  - scheduling.run.ai
  resources:
  - podgroups
  - podgroups/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete

# Default Scheduler - Workload
- apiGroups:
  - scheduling.k8s.io
  resources:
  - workloads
  - workloads/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete

# Koordinator - PodGroup (如果使用)
- apiGroups:
  - scheduling.koordinator.sh
  resources:
  - podgroups
  - podgroups/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
```

### Step 4: 验证集成

#### 4.1 创建测试 PodCliqueSet (KAI Scheduler)

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: test-kai
spec:
  replicas: 1
  template:
    cliques:
    - name: workers
      spec:
        replicas: 2
        minAvailable: 2
        podSpec:
          schedulerName: "kai-scheduler"  # 使用 KAI
          containers:
          - name: worker
            image: busybox
            command: ["sleep", "3600"]
```

#### 4.2 验证资源创建

```bash
# 1. 检查 PodGang 是否创建（应该始终创建）
kubectl get podgangs
# 应该看到: test-kai-0

# 2. 检查 PodGang 的 backend 标签
kubectl get podgang test-kai-0 -o yaml | grep "grove.io/scheduler-backend"
# 应该看到: grove.io/scheduler-backend: kai

# 3. 检查 KAI PodGroup 是否创建
kubectl get podgroups.scheduling.run.ai
# 应该看到: pg-test-kai-0-{uid}

# 4. 检查 ownerReference
kubectl get podgroup pg-test-kai-0-{uid} -o yaml | grep -A5 ownerReferences
# 应该看到指向 PodGang 的引用
```

#### 4.3 创建测试 PodCliqueSet (Default Scheduler)

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: test-default
spec:
  replicas: 1
  template:
    cliques:
    - name: workers
      spec:
        replicas: 2
        podSpec:
          schedulerName: ""  # 使用默认调度器
          containers:
          - name: worker
            image: busybox
            command: ["sleep", "3600"]
```

#### 4.4 验证 Default Backend

```bash
# 1. 检查 PodGang
kubectl get podgang test-default-0 -o yaml | grep "grove.io/scheduler-backend"
# 应该看到: grove.io/scheduler-backend: default

# 2. 检查 Workload
kubectl get workloads.scheduling.k8s.io
# 应该看到: test-default-0
```

## 🔍 调试

### 查看 Backend Controller 日志

```bash
# 查看 operator 日志，筛选 backend 相关
kubectl logs -n grove-system deployment/grove-operator | grep backend

# 应该看到类似：
# "Setting up backend controllers"
# "Registered backend controller" backend="kai"
# "Registered backend controller" backend="workload"
# "Processing PodGang with backend" backend="kai" podgang="default/test-kai-0"
```

### 常见问题排查

#### 问题 1: PodGang 未创建

**排查**:
```bash
kubectl get events --sort-by='.lastTimestamp' | grep PodGang
```

**可能原因**:
- Component registry 未正确注册
- RBAC 权限不足

#### 问题 2: Backend CR 未创建

**排查**:
```bash
# 检查 PodGang 的 backend 标签
kubectl get podgang {name} -o jsonpath='{.metadata.labels.grove\.io/scheduler-backend}'

# 查看 backend controller 日志
kubectl logs deployment/grove-operator | grep "Processing PodGang"
```

**可能原因**:
- Backend label 不正确
- Backend controller 未启动
- RBAC 权限不足

#### 问题 3: 标签不匹配

**检查逻辑**:
```go
// 在 podgang_unified/podgang.go 中
func (r _resource) determineBackendLabel(pcs *grovecorev1alpha1.PodCliqueSet) string {
    schedulerName := pcs.Spec.Template.Cliques[0].Spec.PodSpec.SchedulerName
    
    if schedulerName == "" || schedulerName == "default-scheduler" {
        return "default"
    }
    if schedulerName == "kai-scheduler" || schedulerName == "grove-scheduler" {
        return "kai"
    }
    return "kai" // fallback
}
```

## 📊 架构流程图

```
用户创建 PodCliqueSet
  schedulerName: "kai-scheduler"
         ↓
┌─────────────────────────────────────────┐
│ PodCliqueSet Controller                 │
│                                         │
│ 调用: podgang_unified.Sync()            │
│   - 始终创建 PodGang                    │
│   - 设置 label:                         │
│     grove.io/scheduler-backend: "kai"   │
└─────────────────────────────────────────┘
         ↓
   PodGang 资源创建
   (scheduler.grove.io/v1alpha1)
         ↓
┌─────────────────────────────────────────┐
│ Backend Controllers (并行运行)           │
│                                         │
│ ┌─────────────┐  ┌──────────────────┐  │
│ │ KAI Backend │  │ Default Backend  │  │
│ │ Reconciler  │  │ Reconciler       │  │
│ └─────────────┘  └──────────────────┘  │
│       │                    │            │
│       │ Matches?           │ Matches?   │
│       │ (label=kai) ✓      │ (label=   │
│       │                    │  default)✗ │
│       ↓                    ↓            │
│   处理此 PodGang        跳过           │
└─────────────────────────────────────────┘
         ↓
   KAI Backend.Sync()
         ↓
   创建 PodGroup
   (scheduling.run.ai/v2alpha2)
     - ownerRef → PodGang
```

## ✅ 验证清单

- [ ] Component registry 已更新使用 `podgang_unified`
- [ ] Main.go 已添加 backend controller 注册
- [ ] RBAC 权限已更新
- [ ] 测试 KAI scheduler 路径
  - [ ] PodGang 创建成功
  - [ ] Backend label 正确 (kai)
  - [ ] PodGroup 创建成功
  - [ ] OwnerReference 正确
- [ ] 测试 Default scheduler 路径
  - [ ] PodGang 创建成功
  - [ ] Backend label 正确 (default)
  - [ ] Workload 创建成功
  - [ ] OwnerReference 正确
- [ ] 删除测试
  - [ ] 删除 PodCliqueSet
  - [ ] PodGang 被级联删除
  - [ ] Backend CR 被级联删除

## 🎯 迁移策略

### Phase 1: 并行运行（推荐）

保留旧的组件，新增 backend controllers：

```go
// 同时注册旧组件和新组件
reg.Register(component.KindPodGang, podgang.New(...))      // 旧
reg.Register(component.KindWorkload, workload.New(...))    // 旧
reg.Register("podgang-unified", podgang_unified.New(...))  // 新

// 同时启动 backend controllers
backendcontroller.SetupBackendControllers(mgr, setupLog)
```

通过环境变量控制使用哪个：
```go
if os.Getenv("USE_UNIFIED_PODGANG") == "true" {
    reg.Register(component.KindPodGang, podgang_unified.New(...))
} else {
    // 使用旧逻辑
}
```

### Phase 2: 完全切换

移除旧组件，只使用新架构：

```go
// 只注册新组件
reg.Register(component.KindPodGang, podgang_unified.New(...))
```

### Phase 3: 清理

删除旧的 podgang/workload 组件代码。

## 📚 相关文档

- [Backend 架构说明](./NEW_ARCHITECTURE.md)
- [Backend API 文档](./README.md)
- [使用指南](./USAGE.md)

## 🆘 获取帮助

如遇问题，请提供以下信息：

1. PodCliqueSet YAML
2. PodGang 资源状态: `kubectl get podgang {name} -o yaml`
3. Operator 日志: `kubectl logs deployment/grove-operator`
4. Events: `kubectl get events --sort-by='.lastTimestamp'`

