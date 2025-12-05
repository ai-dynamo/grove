# 📚 Scheduler Backend 文档索引

## 🚀 快速开始

**时间**: 5分钟  
**文件**: [QUICKSTART.md](./QUICKSTART.md)

如果你想立即开始集成，这是最佳起点！

## 📖 完整文档

### 1. 架构理解

| 文档 | 用途 | 适合人群 |
|------|------|----------|
| [NEW_ARCHITECTURE.md](./NEW_ARCHITECTURE.md) | 新架构详细设计 | 架构师、开发者 |
| [ARCHITECTURE_V2.md](./ARCHITECTURE_V2.md) | V2 概念说明 | 所有人 |
| [README.md](./README.md) | 整体概述 | 新手 |

### 2. 实施指南

| 文档 | 用途 | 适合人群 |
|------|------|----------|
| [INTEGRATION_GUIDE.md](./INTEGRATION_GUIDE.md) | ⭐ 集成步骤详解 | 实施者 |
| [QUICKSTART.md](./QUICKSTART.md) | ⚡ 5分钟快速集成 | 快速上手者 |

### 3. 使用示例

| 文档 | 用途 | 适合人群 |
|------|------|----------|
| [COMPLETE_EXAMPLE.md](./COMPLETE_EXAMPLE.md) | ⭐ 端到端完整示例 | 学习者 |
| [USAGE.md](./USAGE.md) | 使用指南和最佳实践 | 用户 |
| [examples_test.go](./examples_test.go) | 代码示例 | 开发者 |

### 4. 实现总结

| 文档 | 用途 | 适合人群 |
|------|------|----------|
| [FINAL_SUMMARY.md](./FINAL_SUMMARY.md) | ⭐ 最终实现总结 | 所有人 |
| [IMPLEMENTATION_SUMMARY.md](./IMPLEMENTATION_SUMMARY.md) | 实现细节 | 开发者 |

## 📋 推荐阅读路径

### 对于新用户

```
1. README.md (5分钟) - 理解是什么
   ↓
2. NEW_ARCHITECTURE.md (10分钟) - 理解为什么和怎么做
   ↓
3. QUICKSTART.md (5分钟) - 立即开始
```

### 对于实施者

```
1. NEW_ARCHITECTURE.md (10分钟) - 理解设计
   ↓
2. INTEGRATION_GUIDE.md (15分钟) - 详细集成步骤
   ↓
3. COMPLETE_EXAMPLE.md (10分钟) - 看完整示例
   ↓
4. 开始集成！
```

### 对于架构师/Review者

```
1. FINAL_SUMMARY.md (10分钟) - 快速理解全貌
   ↓
2. NEW_ARCHITECTURE.md (15分钟) - 深入设计细节
   ↓
3. 代码审查
```

## 🗂️ 代码组织

```
backend/
├── types.go                           # Backend接口定义
├── registry.go                        # Backend注册表
├── builder.go                         # Gang info构建器
├── init.go                            # Backend导入
│
├── kai/                               # KAI Backend
│   └── backend.go                     # PodGang → run.ai PodGroup
│
├── workload/                          # Workload Backend  
│   └── backend.go                     # PodGang → K8s Workload
│
├── koordinator/                       # Koordinator Backend
│   └── backend.go                     # (框架已完成)
│
├── controller/                        # Backend Controllers
│   ├── reconciler.go                  # Backend reconciler
│   └── manager.go                     # Controller管理器
│
└── (docs)/                            # 本目录的文档
```

## 💡 核心概念速查

### PodGang - 统一中间表示

```yaml
apiVersion: scheduler.grove.io/v1alpha1
kind: PodGang
metadata:
  labels:
    grove.io/scheduler-backend: "kai"  # 关键标签！
spec:
  podgroups: [...]
```

### Backend 选择

| schedulerName | Backend Label | 生成的 CR |
|---------------|---------------|-----------|
| `""` or `"default-scheduler"` | `default` | `Workload` |
| `"kai-scheduler"` | `kai` | `PodGroup` (run.ai) |
| `"koord-scheduler"` | `koordinator` | `PodGroup` (koordinator) |

### 工作流程

```
PodCliqueSet
    ↓ (Operator 始终创建)
PodGang (with backend label)
    ↓ (Backend Controller 监听)
调度器特定 CR (PodGroup/Workload)
```

## ❓ 常见问题

### Q: 我应该从哪里开始？

A: 阅读 [QUICKSTART.md](./QUICKSTART.md)，5分钟内完成集成！

### Q: PodGang 什么时候创建？

A: **始终创建**！无论使用哪个调度器。

### Q: 如何决定使用哪个 Backend？

A: 通过 PodGang 上的 `grove.io/scheduler-backend` 标签。

### Q: 如何添加新的调度器支持？

A: 查看 [USAGE.md](./USAGE.md) 的 "Adding a New Backend" 部分。

### Q: 旧的实现怎么办？

A: 可以渐进迁移，参见 [INTEGRATION_GUIDE.md](./INTEGRATION_GUIDE.md) 的迁移策略。

## 🆘 获取帮助

1. **文档查找**: 使用本索引文件找到相关文档
2. **示例代码**: 查看 [COMPLETE_EXAMPLE.md](./COMPLETE_EXAMPLE.md)
3. **集成问题**: 参考 [INTEGRATION_GUIDE.md](./INTEGRATION_GUIDE.md) 的故障排除部分
4. **架构问题**: 阅读 [NEW_ARCHITECTURE.md](./NEW_ARCHITECTURE.md)

## ✅ 验证清单

集成完成后，确保：

- [ ] 阅读了至少 3 个核心文档
- [ ] 理解了 PodGang 作为中间表示的概念
- [ ] 了解 Backend 标签的作用
- [ ] 知道如何验证集成是否成功
- [ ] 测试了至少一个调度器路径

## 🎯 下一步

选择你的路径：

- **快速上手**: → [QUICKSTART.md](./QUICKSTART.md)
- **深入学习**: → [NEW_ARCHITECTURE.md](./NEW_ARCHITECTURE.md)
- **开始集成**: → [INTEGRATION_GUIDE.md](./INTEGRATION_GUIDE.md)
- **查看示例**: → [COMPLETE_EXAMPLE.md](./COMPLETE_EXAMPLE.md)

**Happy Coding! 🚀**

