# 模型主动请求权限：会话级能力授予（capabilities）

> 分支：`claude/kagent-capabilities`
> 状态：设计定稿，开始实施
> 依据：架构分析 P2；用户愿景「默认有查询权限、可随时关；其他权限可扩展添加**或模型主动请求**」

## 1. 目标

把权限从「工具是否被加进 agent 规格」升级为「**能力是否在本会话被授予**」：
- 模型发现自己需要某项权限（如写集群）时，能**主动请求**；
- 用户在对话里一次批准，即为整个会话**授予该能力组**（覆盖组内所有工具，不再逐次弹窗）；
- 默认能力（集群只读、CI 只读）开箱即有；破坏性能力（集群写）默认关，按需授予。

这统一了两条路径——用户侧开关（已有 default-access 开关）与模型侧请求——都落到同一个「会话已授予能力」状态。

## 2. 能力模型

**能力（capability）= 一组工具 + 元信息。** 运行时内置注册表：

| 能力 | 工具 | 默认授予 | 说明 |
|------|------|---------|------|
| `cluster-read` | k8s_get_resource / list / logs / events / api_resources | ✅ | 集群只读查询 |
| `ci-read` | jenkins_console_log / job_info / list_builds | ✅ | CI 只读 |
| `cluster-write` | k8s_apply / delete / scale / rollout_restart | ❌（需授予） | 破坏性写操作 |

- 会话授予状态存于 session state 键 `kagent_granted_capabilities`（能力名列表，无 app:/user: 前缀 ⇒ 严格会话作用域）。
- 工具「可执行」判定：其能力默认授予 **或** 已在本会话授予。

## 3. 授予流程（复用现有 HITL 机制）

**两个触发入口，同一套授予逻辑：**

1. **模型主动请求**：内置 `request_capability(capability, reason)` 工具。模型调用 → BeforeToolCallback 拦截 → `RequestConfirmation`（弹审批卡片）→ 用户批准 → 写入会话授予状态 → 返回「已授予」。
2. **直接调用被门控的工具**：模型直接调 `k8s_scale`（未授予 cluster-write）→ 同一个 BeforeToolCallback 检测到能力未授予 → 弹「授予 cluster-write 能力？」卡片 → 批准后**授予整个能力组**并放行本次调用；此后组内工具不再弹窗。

关键点：批准一次 = 授予**整个能力组**（不是单个工具），这就是用户要的「在会话里很方便地添加」。

## 4. 与现有机制的关系

- **替换** PR #8 里「写工具硬编码进 approvalSet 逐次弹窗」为「写工具归入 cluster-write 能力，按能力组授予」。UX 从「每个写工具各弹一次」变成「整组授予一次」。
- **保留** `MakeApprovalCallback`：用户在工具面板对 MCP 工具配置的 `require_approval` 仍走逐工具审批（与能力无关）。
- **保留** controller 侧 `clusterTools.write.enabled` 部署级开关：纵深防御。写工具现在是**三重门控**——① 运行时被 `MakeCapabilityCallback` 按能力门控（未授予不可执行）；② 会话须授予 cluster-write 能力（用户批准）；③ 部署须开 `clusterTools.write.enabled`（否则 controller 403）。因此「能读即挂载写工具」是安全的：工具存在于声明里但三关不过就动不了。

## 5. 实施清单

1. ✅ 计划文档（本文件）
2. `go/adk/pkg/agent/capabilities.go`：能力注册表、`capabilityForTool`、会话授予读写、`request_capability` 工具构造。
3. `capabilities.go`：`MakeCapabilityCallback` —— 处理 request_capability 与被门控工具的授予流程。
4. `agent.go`：写工具移出 approvalSet；接入能力回调；**「能读即可请求写」——agent 若挂载了集群只读工具，则自动挂载（被门控的）写工具 + request_capability 工具**（写工具三重门控下挂载是安全的，见下）；系统提示词补一句「需要更高权限时用 request_capability 请求」。
5. HITL payload（`hitl.go`）：可选带 `capability` 字段，便于 UI 卡片显示能力名。
6. 单测：授予后放行、未授予拦截、request_capability 授予、组内工具一次授予全解锁、拒绝路径。

### 暂缓（下一批）

- UI 工具面板「已授予能力」展示 + 撤销开关（本批先靠模型请求 + 现有 default-access 开关；能力可视化管理下批做）。
- Python 运行时 parity。
- 能力组自定义（CRD 层声明 agent 允许请求哪些能力）。

## 6. 验收标准

- 全新会话里模型调 `k8s_scale` → 弹「授予 cluster-write？」→ 批准后该调用执行，且后续 `k8s_apply` 不再弹；
- 模型可先调 `request_capability("cluster-write", "要修复 deployment 配置")` 主动请求；
- 未授予时组内工具被拦截并提示模型去请求；
- `cluster-read`/`ci-read` 默认可用无需请求；
- 部署未开 `clusterTools.write.enabled` 时，即便能力已授予，写调用仍返回明确 403。
