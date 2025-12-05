# 🚀 快速开始

5分钟内完成新调度器后端架构的集成！

## 📝 Step 1: 更新 Component Registry（2分钟）

编辑 `operator/internal/controller/podcliqueset/components/registry.go`:

```go
import (
    // ... 其他 imports ...
    podgang_unified "github.com/ai-dynamo/grove/operator/internal/controller/podcliqueset/components/podgang_unified"
)

func CreateOperatorRegistry(mgr manager.Manager, eventRecorder record.EventRecorder) component.OperatorRegistry[v1alpha1.PodCliqueSet] {
    cl := mgr.GetClient()
    reg := component.NewOperatorRegistry[v1alpha1.PodCliqueSet]()
    
    // ... 其他组件注册 ...
    
    // ✅ 使用新的统一 PodGang 组件（替换旧的）
    reg.Register(component.KindPodGang, podgang_unified.New(cl, mgr.GetScheme(), eventRecorder))
    
    // ❌ 删除这些（如果存在）:
    // reg.Register(component.KindPodGang, podgang.New(...))
    // reg.Register(component.KindWorkload, workload.New(...))
    
    return reg
}
```

## 🔧 Step 2: 注册 Backend Controllers（2分钟）

编辑 `operator/cmd/main.go`:

```go
import (
    // ... 其他 imports ...
    backendcontroller "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend/controller"
    _ "github.com/ai-dynamo/grove/operator/internal/controller/scheduler/backend" // 触发 backends 注册
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
    
    // ✅ 注册 Backend Controllers（新增这段）
    if err := backendcontroller.SetupBackendControllers(mgr, setupLog); err != nil {
        setupLog.Error(err, "unable to setup backend controllers")
        os.Exit(1)
    }
    setupLog.Info("✓ Backend controllers registered")
    
    // ... 启动 manager ...
    if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
        setupLog.Error(err, "problem running manager")
        os.Exit(1)
    }
}
```

## 🔑 Step 3: 测试（1分钟）

### 测试 KAI Scheduler

```bash
# 创建使用 KAI 的 PodCliqueSet
cat <<EOF | kubectl apply -f -
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
          schedulerName: "kai-scheduler"
          containers:
          - name: worker
            image: busybox
            command: ["sleep", "3600"]
EOF

# 验证 PodGang 创建
kubectl get podgangs
# 期望: test-kai-0

# 验证 backend 标签
kubectl get podgang test-kai-0 -o jsonpath='{.metadata.labels.grove\.io/scheduler-backend}'
# 期望: kai

# 验证 PodGroup 创建（KAI 的 CR）
kubectl get podgroups.scheduling.run.ai
# 期望: pg-test-kai-0-{uid}
```

### 测试 Default Scheduler

```bash
# 创建使用默认调度器的 PodCliqueSet
cat <<EOF | kubectl apply -f -
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
          schedulerName: ""  # 默认调度器
          containers:
          - name: worker
            image: busybox
            command: ["sleep", "3600"]
EOF

# 验证 backend 标签
kubectl get podgang test-default-0 -o jsonpath='{.metadata.labels.grove\.io/scheduler-backend}'
# 期望: default

# 验证 Workload 创建（K8s 的 CR）
kubectl get workloads.scheduling.k8s.io
# 期望: test-default-0
```

## ✅ 成功标志

如果你看到：

1. ✅ PodGang 始终被创建（无论 schedulerName）
2. ✅ 正确的 backend 标签（`kai` 或 `default`）
3. ✅ 对应的调度器 CR 被创建（PodGroup 或 Workload）
4. ✅ Operator 日志显示 "Backend controllers registered"

**恭喜！集成成功！🎉**

## 🆘 遇到问题？

### 问题 1: PodGang 未创建

```bash
# 检查 operator 日志
kubectl logs -n grove-system deployment/grove-operator | grep -i podgang

# 检查 events
kubectl get events --sort-by='.lastTimestamp' | grep PodGang
```

**可能原因**: Component registry 未正确更新

### 问题 2: Backend CR 未创建

```bash
# 检查 operator 日志
kubectl logs -n grove-system deployment/grove-operator | grep -i backend

# 检查 PodGang 标签
kubectl get podgang -o yaml | grep "grove.io/scheduler-backend"
```

**可能原因**: 
- Backend controllers 未注册
- Backend 标签不正确

### 问题 3: Backend 标签错误

检查 `podgang_unified/podgang.go` 中的 `determineBackendLabel()` 函数：

```go
func (r _resource) determineBackendLabel(pcs *PodCliqueSet) string {
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

## 📖 下一步

- 阅读 [INTEGRATION_GUIDE.md](./INTEGRATION_GUIDE.md) 了解详细集成步骤
- 查看 [COMPLETE_EXAMPLE.md](./COMPLETE_EXAMPLE.md) 了解完整示例
- 阅读 [FINAL_SUMMARY.md](./FINAL_SUMMARY.md) 了解架构总结

## 💡 提示

1. **渐进迁移**: 可以先保留旧组件，使用环境变量控制使用哪个
2. **日志调试**: 增加 `--v=2` 查看详细日志
3. **验证 RBAC**: 确保 Operator 有权限操作 PodGroup/Workload

**Happy Coding! 🚀**

