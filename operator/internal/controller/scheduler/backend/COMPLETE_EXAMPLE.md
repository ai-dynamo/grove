# 完整使用示例

本文档提供完整的端到端示例，展示新的调度器后端架构如何工作。

## 🎯 场景：ML 训练任务

我们将部署一个分布式 ML 训练任务，包含：
- 1 个 Master 节点
- 4 个 Worker 节点
- 使用 KAI Scheduler

## 📝 Step 1: 创建 PodCliqueSet

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: ml-training
  namespace: ml-workspace
spec:
  replicas: 2  # 创建 2 个训练集群
  template:
    cliques:
    # Master 节点
    - name: master
      spec:
        replicas: 1
        minAvailable: 1
        podSpec:
          schedulerName: "kai-scheduler"  # 使用 KAI
          priorityClassName: "high-priority"
          containers:
          - name: master
            image: training/master:v1.0
            resources:
              requests:
                nvidia.com/gpu: "1"
                cpu: "4"
                memory: "16Gi"
    
    # Worker 节点
    - name: workers
      spec:
        replicas: 4
        minAvailable: 3  # 至少 3 个 worker 才能开始
        podSpec:
          schedulerName: "kai-scheduler"
          priorityClassName: "high-priority"
          containers:
          - name: worker
            image: training/worker:v1.0
            resources:
              requests:
                nvidia.com/gpu: "2"
                cpu: "8"
                memory: "32Gi"
```

应用配置：
```bash
kubectl apply -f ml-training.yaml
```

## 🔍 Step 2: 观察资源创建过程

### 2.1 PodCliqueSet 创建

```bash
$ kubectl get pcs -n ml-workspace
NAME          REPLICAS   READY   AGE
ml-training   2          0/2     5s
```

### 2.2 Operator 创建 PodGang

Operator 检测到 `schedulerName: "kai-scheduler"`，创建 PodGang 并设置 backend 标签：

```bash
$ kubectl get podgangs -n ml-workspace
NAME             AGE
ml-training-0    10s
ml-training-1    10s
```

查看 PodGang 详情：
```bash
$ kubectl get podgang ml-training-0 -n ml-workspace -o yaml
```

<details>
<summary>PodGang YAML 输出</summary>

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  name: ml-training-0
  namespace: ml-workspace
  labels:
    app.kubernetes.io/managed-by: grove-operator
    app.kubernetes.io/part-of: ml-training
    app.kubernetes.io/component: podgang
    grove.io/scheduler-backend: "kai"  # ← 关键：Backend 标签
  ownerReferences:
  - apiVersion: grove.io/v1alpha1
    kind: PodCliqueSet
    name: ml-training
    uid: abc-123-def
spec:
  priorityClassName: high-priority
  podgroups:
  - name: ml-training-0-master
    minReplicas: 1
    podReferences:
    - namespace: ml-workspace
      name: ml-training-0-master-xyz
  - name: ml-training-0-workers
    minReplicas: 3
    podReferences:
    - namespace: ml-workspace
      name: ml-training-0-workers-abc
    - namespace: ml-workspace
      name: ml-training-0-workers-def
    - namespace: ml-workspace
      name: ml-training-0-workers-ghi
    - namespace: ml-workspace
      name: ml-training-0-workers-jkl
```
</details>

### 2.3 KAI Backend 创建 PodGroup

KAI Backend Controller 监听到 PodGang，检查标签匹配，转换成 PodGroup：

```bash
$ kubectl get podgroups.scheduling.run.ai -n ml-workspace
NAME                                    AGE
pg-ml-training-0-abc-123-def           15s
pg-ml-training-1-def-456-ghi           15s
```

查看 PodGroup 详情：
```bash
$ kubectl get podgroup pg-ml-training-0-abc-123-def -n ml-workspace -o yaml
```

<details>
<summary>PodGroup YAML 输出</summary>

```yaml
apiVersion: scheduling.run.ai/v2alpha2
kind: PodGroup
metadata:
  name: pg-ml-training-0-abc-123-def
  namespace: ml-workspace
  annotations:
    kai.scheduler/top-owner-metadata: |
      name: ml-training-0
      uid: abc-123-def
      group: scheduler.grove.io
      version: v1alpha1
      kind: PodGang
  labels:
    app.kubernetes.io/managed-by: grove-operator
    app.kubernetes.io/part-of: ml-training
    app.kubernetes.io/component: podgang
    grove.io/scheduler-backend: "kai"
  ownerReferences:
  - apiVersion: scheduler.grove.io/v1alpha1
    kind: PodGang
    name: ml-training-0
    uid: abc-123-def  # ← 指向 PodGang
spec:
  minMember: 4  # 1 master + 3 workers (minAvailable)
  priorityClassName: high-priority
  queue: default-queue
  subGroups:
  - name: ml-training-0-master
    minMember: 1
  - name: ml-training-0-workers
    minMember: 3
  topologyConstraint: {}
status:
  schedulingConditions:
  - type: Scheduled
    status: "True"
    lastTransitionTime: "2025-12-04T10:30:00Z"
```
</details>

## 🎬 Step 3: 调度过程

### 3.1 Pods 创建

```bash
$ kubectl get pods -n ml-workspace
NAME                            READY   STATUS    RESTARTS   AGE
ml-training-0-master-xyz        0/1     Pending   0          20s
ml-training-0-workers-abc       0/1     Pending   0          20s
ml-training-0-workers-def       0/1     Pending   0          20s
ml-training-0-workers-ghi       0/1     Pending   0          20s
ml-training-0-workers-jkl       0/1     Pending   0          20s
```

所有 Pods 都有 scheduling gate：
```bash
$ kubectl get pod ml-training-0-master-xyz -o jsonpath='{.spec.schedulingGates}'
[{"name":"grove.io/podgang"}]
```

### 3.2 KAI Scheduler 处理

KAI Scheduler 看到 PodGroup，检查资源：
- ✓ 找到足够资源满足 minMember=4
- ✓ 执行 gang 调度
- ✓ 移除 scheduling gates
- ✓ Pods 开始运行

```bash
$ kubectl get pods -n ml-workspace
NAME                            READY   STATUS    RESTARTS   AGE
ml-training-0-master-xyz        1/1     Running   0          45s
ml-training-0-workers-abc       1/1     Running   0          45s
ml-training-0-workers-def       1/1     Running   0          45s
ml-training-0-workers-ghi       1/1     Running   0          45s
ml-training-0-workers-jkl       1/1     Running   0          45s
```

## 🔄 Step 4: 扩容场景

扩展到 3 个训练集群：

```bash
$ kubectl patch pcs ml-training -n ml-workspace --type=merge -p '{"spec":{"replicas":3}}'
```

### 4.1 自动创建新的 PodGang

```bash
$ kubectl get podgangs -n ml-workspace
NAME             AGE
ml-training-0    2m
ml-training-1    2m
ml-training-2    5s   # ← 新创建
```

### 4.2 KAI Backend 自动创建对应 PodGroup

```bash
$ kubectl get podgroups.scheduling.run.ai -n ml-workspace
NAME                                    AGE
pg-ml-training-0-abc-123-def           2m
pg-ml-training-1-def-456-ghi           2m
pg-ml-training-2-ghi-789-jkl           10s  # ← 新创建
```

## 🗑️ Step 5: 删除和清理

删除 PodCliqueSet：

```bash
$ kubectl delete pcs ml-training -n ml-workspace
```

### 5.1 级联删除过程

由于 ownerReference 设置，资源自动级联删除：

```
PodCliqueSet 删除
    ↓
PodGangs 被删除（ownerRef）
    ↓
PodGroups 被删除（ownerRef）
    ↓
Pods 被删除（ownerRef）
```

验证：
```bash
# PodGangs 应该被删除
$ kubectl get podgangs -n ml-workspace
No resources found in ml-workspace namespace.

# PodGroups 应该被删除
$ kubectl get podgroups.scheduling.run.ai -n ml-workspace
No resources found in ml-workspace namespace.

# Pods 应该被删除
$ kubectl get pods -n ml-workspace
No resources found in ml-workspace namespace.
```

## 🔀 Step 6: 对比 - 使用 Default Scheduler

创建使用默认调度器的 PodCliqueSet：

```yaml
apiVersion: grove.io/v1alpha1
kind: PodCliqueSet
metadata:
  name: inference-service
  namespace: default
spec:
  replicas: 1
  template:
    cliques:
    - name: servers
      spec:
        replicas: 3
        podSpec:
          schedulerName: ""  # 使用默认调度器
          containers:
          - name: server
            image: inference/server:v1.0
```

### 6.1 PodGang 仍然被创建

```bash
$ kubectl get podgang inference-service-0
NAME                    AGE
inference-service-0     5s
```

但是 backend 标签不同：

```bash
$ kubectl get podgang inference-service-0 -o jsonpath='{.metadata.labels.grove\.io/scheduler-backend}'
default  # ← 注意是 "default" 而不是 "kai"
```

### 6.2 Workload 被创建（而不是 PodGroup）

```bash
$ kubectl get workloads.scheduling.k8s.io
NAME                    AGE
inference-service-0     10s
```

查看 Workload：
```yaml
apiVersion: scheduling.k8s.io/v1alpha1
kind: Workload
metadata:
  name: inference-service-0
  namespace: default
  ownerReferences:
  - apiVersion: scheduler.grove.io/v1alpha1
    kind: PodGang
    name: inference-service-0  # ← 指向 PodGang
spec:
  # Workload spec (根据 K8s 1.35+ API)
```

## 📊 资源关系图

```
┌──────────────────┐
│ PodCliqueSet     │
│ ml-training      │
└────────┬─────────┘
         │ owns (via ownerReference)
         ↓
┌────────────────────────────────────────┐
│ PodGangs                               │
│ ┌────────────────┐ ┌────────────────┐ │
│ │ ml-training-0  │ │ ml-training-1  │ │
│ │ backend: kai   │ │ backend: kai   │ │
│ └────────┬───────┘ └────────┬───────┘ │
└──────────┼──────────────────┼─────────┘
           │ owns              │ owns
           ↓                   ↓
┌──────────────────┐  ┌──────────────────┐
│ PodGroup (KAI)   │  │ PodGroup (KAI)   │
│ pg-ml-training-0 │  │ pg-ml-training-1 │
└──────────────────┘  └──────────────────┘
```

## 🎓 关键要点

1. **统一的 PodGang**
   - 无论使用哪个调度器，都会创建 PodGang
   - PodGang 是单一真相来源

2. **Backend 标签驱动**
   - `grove.io/scheduler-backend` 标签决定使用哪个 backend
   - KAI: `kai`
   - Default: `default`
   - Koordinator: `koordinator`

3. **自动转换**
   - Backend controllers 自动监听并转换
   - 用户无需关心转换细节

4. **级联删除**
   - ownerReference 确保正确的删除顺序
   - 删除 PodCliqueSet 自动清理所有资源

5. **可观测性**
   - 可以查看 PodGang 了解 gang 状态
   - 可以查看 backend CR 了解调度器状态
   - 清晰的资源层次关系

## 🚀 下一步

- 尝试不同的调度器组合
- 实现自定义 backend
- 添加监控和告警
- 性能调优

