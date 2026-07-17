# A2 设计文档：会话级"总是允许"工具审批

> 分支：`claude/kagent-session-approvals`（基于 main @ 42e597d）
> 背景：原始痛点"权限/工具控制"的收尾。目前 `requireApproval` 每次调用都要人工点批准，
> 重复审批很快让用户疲劳。目标是 Claude Code 式的 "Always allow for this session"。

## 一、目标

1. 审批卡片新增 **"Always allow (session)"** 按钮；
2. 点击后：本次批准执行，且**该会话内**后续同名工具调用不再弹审批、直接执行；
3. 只影响当前会话——同 agent 的其他会话、新会话仍走审批（Agent CRD 的 requireApproval 不变）；
4. 记忆随会话持久化（存 ADK session state，落 DB），刷新页面/重开会话仍有效。

## 二、链路调研结论（已核实）

- 审批闸门在 Go 运行时 `MakeApprovalCallback`（`go/adk/pkg/agent/approval.go`）——
  BeforeToolCallback，首调 `ctx.RequestConfirmation()` 中断，恢复时 `ctx.ToolConfirmation()` 带决定；
- UI 决定以 DataPart `{decision_type: approve|reject|batch, ...}` 发回，
  `BuildResumeHITLMessage` → `ProcessHitlDecision`（`hitl.go`）把它转成 ToolConfirmation payload；
- `agent.ToolContext` 暴露 `ReadonlyState()` / `State()`（ADK session state，
  Set 走 EventActions.StateDelta 随事件持久化到 session service/DB）——**会话级记忆的天然存放处**；
- 无前缀 state key 即会话作用域（`app:`/`user:` 前缀才是更大作用域）。

## 三、实现

### 3.1 协议（`go/adk/pkg/a2a/hitl.go`）
- UI 决定 DataPart 增加可选 `always_allow: true`（uniform approve 时）；
- `HitlConfirmationPayload` 增加 `AlwaysAllow bool`（ToMap/Parse 对应）；
- `ProcessHitlDecision`：approve 且消息带 always_allow → 每个 pending confirmation 的
  payload 置 `AlwaysAllow=true`（多工具同时 pending 时语义 = 全部批准并全部记忆，UI 有文案说明）。

### 3.2 运行时（`go/adk/pkg/agent/approval.go`）
- state key：`kagent_approved_tools`（[]string，session 作用域）；
- 闸门顺序：不在 requireApproval → 放行；**在会话放行表 → 放行**；有 confirmation →
  批准时若 payload.AlwaysAllow 则把工具名写入放行表，然后放行；否则按原逻辑；
- 单测：fake ToolContext（含 state map + confirmation 注入）覆盖
  首调中断 / 批准 / always_allow 记忆 / 记忆后跳过 / 拒绝 等路径。

### 3.3 UI
- `ToolDisplay` 审批卡片按钮排布：`Approve` `Always allow` `Reject`，
  Always allow 带 tooltip "本会话内不再询问该工具"；
- `ChatInterface`：新 `handleApproveAlways` → `sendApprovalDecision({decision_type:"approve", always_allow:true}, "Approved (always for this session)")`；
- 多工具 pending 时按钮文案自动带数量提示（"Always allow all (N)"）。

### 3.4 明确不做（后续）
- 按参数模式的规则（allowedCommands 通配）——需要规则引擎，独立批次；
- Python 运行时 parity（Go 为默认运行时；python 的 approval 流在 kagent-adk `_hitl_utils`，结构对应，留跟进项）；
- "总是允许（保存到 Agent）"按钮——等价于改 CRD requireApproval，工具面板（C1a）已能做，不重复。

## 四、验收

Go：approval 回调单测 + hitl payload 往返单测全绿；UI：jest / tsc / eslint / build 全绿。
