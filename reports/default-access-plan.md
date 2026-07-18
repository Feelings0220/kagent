# 默认集群访问与 P0 体验批次（default-access）

> 分支：`claude/kagent-default-access`
> 状态：已实施并通过验证
> 依据：`reports/architecture-analysis.md` 的 P0 路线（默认可读、写需授权；对话框附近可开关）

## 目标（用户原话对应）

- "agent 应该初始就具备对集群绝大部分资源（不含 Secret）的查询权限" → **默认工具包**；
- "像沙箱基础权限一样，可以直接在对话框附近修改；默认有、可随时关" → **工具面板顶部的 Default cluster access 开关**；
- "模型主动请求权限允许" → 写权限已由 HITL 审批卡片承担"模型请求→用户批准"（配合会话级 always-allow）；挂载新工具组的主动请求见"后续"；
- "贴 Jenkins 链接直接拉 console log" → **`jenkins_*` 内置工具**；
- ask_user 滥用、↑/↓ 历史消息、@ 选择器白字看不见 → 本批一并修复。

## 已实施

### 1. 默认工具包（默认可读）
- controller 新 flag `--default-builtin-tools`（env `DEFAULT_BUILTIN_TOOLS`；helm `clusterTools.defaultBuiltinTools`），默认值：5 个 `k8s_*` 只读 + 3 个 `jenkins_*` 只读。
- translator 编译时合并进每个 declarative agent（与显式声明去重）。
- CRD 新字段 `spec.declarative.disableDefaultTools` 单 agent 退出；CRD 已重新生成并同步 helm/kagent-crds。
- 单测：合并/去重/退出三种情形（`builtin_tools_test.go`）。

### 2. 对话框附近的权限开关
- ChatToolsPanel 顶部新增 "Default cluster access" 开关（绿盾图标 + 说明文案），即时保存到 Agent 资源（`updateAgentDefaultTools` action），开/关立即生效于后续消息。
- 面板同时新增 CI—Jenkins 工具目录（可显式添加）。

### 3. `jenkins_*` 内置工具（controller 代理，凭据不出 controller）
- `jenkins_console_log(url | job+build)`：**直接接受流水线 URL**，运行时解析 `/job/a/job/b/123/console` → job `a/b` + build `123`（支持 lastBuild/lastFailedBuild 等符号引用）；返回构建结果、触发原因、脱敏后的 console 尾部。
- `jenkins_job_info(url | job)`：状态/健康度/最近构建。
- `jenkins_list_builds(job)`。
- 复用既有 `/api/context/jenkins/*` 端点与脱敏逻辑，零新增后端面。
- CRD 枚举 +3；URL 解析器 6 用例单测。

### 4. 预置 SRE 排查 agent
- helm 新模板 `sre-agent.yaml`（`sreAgent.enabled`，默认 true）：`sre-investigator`，系统提示词内置排查方法论（CI 日志 → 事件 → 工作负载 → pod 日志 → 节点/网络），要求证据引用与结构化报告（结论/证据/失败链/建议/存疑）。

### 5. UX 修复
- **ask_user 克制**：运行时在系统提示词后追加约束——能用工具查到的不许问用户，仅偏好/授权/缺失凭据时提问。
- **↑/↓ 历史消息**：composer 光标在首行按 ↑ 逐条回溯本会话已发消息，↓ 前进并最终恢复未发送草稿；多行编辑中不误触发；IME 组合期间不触发；手动编辑即退出回溯态。
- **@ 选择器白字**：根因是原生 `<select>` 下拉列表不继承主题色（暗色下白底白字）。globals.css base 层为 `select`/`option` 固定主题前景/背景色，全站生效。

## 验证

- Go：`go build ./...` ✅；`go vet` ✅；`gofmt` ✅；adk/tools、adk/agent、translator 测试全过（新增 default-toolpack 3 用例、jenkins URL 解析 6 用例）。
- UI：tsc（无新增源码错误）、eslint（改动文件 0 问题）、jest 282 通过、`next build` ✅。
- helm：本环境无 helm 二进制，模板所用 helper（kagent.namespace/labels/defaultModelConfigName）已核对存在；建议合并后 `helm template` 复核。

## 后续（未含在本批）

- 模型主动请求**挂载新工具组**（request_permission 工具 → HITL 卡片 → 会话内临时挂载）：需要运行时动态工具集支持，列入 P2 capability profile 一并设计。
- kubelet summary（nodes/proxy）、查询审计日志、GitLab/Argo provider、Python parity。
