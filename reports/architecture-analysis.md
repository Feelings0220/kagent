# kagent 架构分析与演进计划：从"能提问的 ChatGPT"到真正的集群 Agent

> 日期：2026-07-17
> 背景：基于两条核心反馈——①"贴一个 Jenkins 流水线链接就能得到完整根因分析"的端到端场景；②对 kagent 架构（权限提供方式、MCP 依赖、UI 交互）的整体质疑。
> 性质：分析 + 路线图。P0 条目可直接开工。

---

## 一、目标场景拆解：Jenkins 链接 → 根因分析

理想交互：

```
用户: 帮我查一下这条流水线为什么挂了
      https://jenkins.example.com/job/deploy-svc-a/1234/

agent: (自动执行，全程只读)
  1. jenkins_console_log(url)        → 拉取 console log，定位报错段落
  2. 从日志提取线索: 镜像名 / namespace / deployment / 报错关键词
  3. k8s_events(namespace, ...)      → 找到 FailedScheduling / BackOff / OOMKilled 等事件
  4. k8s_list_resources + get        → 定位相关 pod / deployment / svc / endpoints
  5. k8s_pod_logs(pod, previous=true)→ 崩溃容器的上一次日志
  6. k8s_get_resource(node)          → 节点状态 / 资源压力 / kubelet conditions
  7. 输出结构化根因分析报告（可下载 md 工件）
```

### 1.1 现状盘点（截至 PR #8）

| 环节 | 状态 | 缺口 |
|------|------|------|
| K8s 只读查询工具（9 个 `k8s_*`） | ✅ 已实现 | **默认没有**——需逐 agent 在 CRD/工具面板里添加 |
| Jenkins console log | ⚠️ 仅 @-mention UI 注入 | agent 没有 `jenkins_*` 工具，无法从消息里的 URL 自主拉日志 |
| 事件↔pod↔节点↔svc 关联 | ✅ 工具已具备 | 缺少引导 agent 做系统性排查的提示词/预置 agent |
| kubelet / 节点深度信息 | ⚠️ 部分 | node YAML 含 conditions/capacity；kubelet summary（nodes/proxy）未接 |
| 根因报告产出 | ✅ 工件面板已支持 md 输出 | 无 |

### 1.2 核心论点："查询权限应该是 agent 的出厂配置"

**用户判断是对的。** 一个部署在集群里、以 SRE 助手为定位的 agent，连"看"的能力都要逐个手工授予，那它默认状态下确实只是一个能提问的聊天框。反观 kubectl：任何拿到 kubeconfig 的人天然能 `get/describe/logs`。只读（去 Secret）+ 应用层审计是业界公认的安全默认值——**默认可读、写需授权** 才是正确的权限分层，而不是"读也要逐个申请"。

安全边界不变：Secret 永远拒绝；写操作维持"helm 开关 + 每次 HITL 审批 + 会话级 always-allow"三层门禁；controller 是唯一出口，天然是审计点（后续可加查询日志）。

### 1.3 P0 实施项（按依赖排序）

**P0-1 默认工具包（default toolpack）**
- Helm 值 `agents.defaultBuiltinTools`，默认 `[k8s_get_resource, k8s_list_resources, k8s_pod_logs, k8s_events, k8s_api_resources]`。
- Translator 在编译 AgentConfig 时把默认工具合并进每个 declarative agent（与显式声明去重；`agents.defaultBuiltinTools: []` 即回到旧行为）。
- CRD 增加 `spec.declarative.disableDefaultTools: true` 供单个 agent 退出。
- 同时给 `BuiltinTool` 增加组别名：`k8s-read` / `k8s-write` / `workspace`，展开在 translator 做，避免 CRD 里罗列 9 个名字。

**P0-2 `jenkins_*` agent 工具**
- 复用已有的 Jenkins 上下文提供方（`JenkinsConfigFromEnv`、console log 脱敏 regex 都已存在），把能力从"UI 注入"升级为"agent 工具"：
  - `jenkins_console_log(url_or_job, build?)`——接受完整流水线 URL（直接解析 job path + build number）或 job+build 参数；
  - `jenkins_build_info(job, build?)`——结果/时长/参数/上游触发；
  - `jenkins_list_builds(job)`。
- 走同一模式：controller 端点（凭据只存在于 controller）→ 运行时 functiontool 经 `KAGENT_URL` 代理。你提到的 cictl 底层同样是 HTTP 调 Jenkins master，此路径与其等价且免去 agent 容器装 CLI。
- 纳入默认工具包（当 Jenkins provider 已配置时）。

**P0-3 预置 SRE 排查 agent**
- Helm 附带一个开箱即用的 `sre-investigator` Agent 清单：默认工具包 + 精心写的系统提示词（排查方法论：先日志定位线索 → 事件面 → 工作负载面 → 节点面 → 网络面 → 输出结构化结论，结论必须引用查到的证据）。
- 这是把"工具可用"变成"排查靠谱"的关键——工具齐了之后，质量差距主要在提示词。

**P1 延伸**
- `k8s_node_summary`：经 API server `nodes/{name}/proxy/stats/summary` 拉 kubelet 摘要（磁盘/内存/PID 压力），getter RBAC 需补 `nodes/proxy`。
- 查询审计日志（controller 记录 agent 的每次集群查询，UI 可回看）。
- GitLab CI / Argo 等更多流水线 provider，同一 `*_console_log` 模式复制。

---

## 二、架构评估：kagent 的问题与应对

### 2.1 权限/能力提供链路——确实太繁琐

现状一条工具从"存在"到"agent 能用"的路径：

```
MCP 路线: 部署 tool server pod → ToolServer/RemoteMCPServer CR → Agent CRD 引用
          → translator → AgentConfig → runtime 连接 MCP → LLM 可见
内置路线: Agent CRD builtin 声明 → translator → AgentConfig → runtime 挂载
```

**判断：** 链路长的根源是"一切能力皆外部资源"的初始设计。它对第三方扩展是对的，对核心能力（读集群、读 CI、读写文件）是错的——核心能力应该是运行时自带、配置一行开关的事。

**应对（多数已在 fork 落地，方向延续）：**
1. **controller 即能力网关**：集群查询、Jenkins 代理都做成 controller API，agent 零 RBAC、零凭据、零 sidecar——这是最轻量的提权方式（已落地）。
2. **默认能力 + 组别名 + 单点退出**（P0-1）：把"配置 N 个工具"压缩成"默认就有/一个组名"。
3. **能力档位（capability profile，P2）**：CRD 演进为 `spec.capabilities: {cluster: read-only | read-write | none, ci: read-only, workspace: full}` 这种声明式档位，translator 展开成工具集。比逐工具列举更接近用户心智。

### 2.2 MCP 依赖——趋势判断正确，但不必全盘否定

**用户观点**：行业在减少对独立 MCP server 的依赖，太重。
**分析**：同意大方向。MCP 的价值在**第三方生态互操作**（社区工具、跨厂商复用），代价是每个 server 一个部署、一条网络路径、一份运维负担。行业（包括各大模型厂商的 agent 产品）的普遍走法是：**高频核心工具内置化（进程内 function call），MCP 留作扩展位**。

**kagent 的对应策略（即本 fork 已在执行的路线）：**
- 核心能力内置：workspace（bash/文件）、k8s 查询/操作、CI 日志——全部是运行时 functiontool，零额外部署（A1、PR #8、P0-2）；
- MCP 定位为社区/私有工具的接入协议，保留但不再是默认路径；
- 文档层面明确这个分层，避免新用户一上来就被引导去部署 tool server。

### 2.3 UI/交互——逐项回应

| 问题 | 分析 | 计划 |
|------|------|------|
| `ask_user` 滥用 | 该工具无条件暴露给 LLM，且无使用约束提示，模型自然倾向"问人"而不是"查证" | P0-4：①系统提示词注入约束——"能用工具查到的信息不要问用户；只在偏好/授权类决策时提问"；②Agent CRD 加 `askUserEnabled: false` 可整体关闭；③默认工具包就绪后模型"有得查"，提问频率会自然下降 |
| 无上下键历史消息 | 纯前端缺失 | P0-5：composer 空白时 ↑/↓ 翻阅本会话历史用户消息（shell 风格，含编辑后重发；与光标在多行文本中的移动不冲突：仅在光标位于首/末行时触发） |
| 响应/交互反馈 | 已改善（紧凑工具卡、流式、跟随滚动、上下文表） | P1：工具调用进行中的分步进度指示；长回答折叠 |
| 整体信息密度 | 已改善（可调侧边栏、留白压缩、滚动条） | 持续 |

### 2.4 架构总评（客观结论）

**合理的部分**：CRD 声明式 agent、controller/runtime 分离、A2A 协议统一消息面、Go/Python 双运行时、会话/任务持久化——这些骨架是对的，也是我们所有 fork 功能能快速落地的原因。

**不合理/过时的部分**：
1. 能力供给默认走外部 MCP（重）→ 应内置优先；
2. 权限模型"逐工具、逐 agent、白手起家" → 应默认可读、档位化、单点退出；
3. UI 是功能薄层，交互细节（历史、提问约束、密度）长期欠账 → 持续按批次补齐。

一句话：**骨架保留，能力供给和默认值反转。** 不需要推翻重来，需要把"agent 默认无能力"翻转成"agent 默认能查、写需授权"。

---

## 三、路线图汇总

| 优先级 | 事项 | 规模 |
|--------|------|------|
| P0-1 | 默认工具包 + 组别名 + disableDefaultTools | 中（translator+CRD+helm） |
| P0-2 | `jenkins_*` agent 工具（URL 直达 console log） | 中（controller 端点+运行时工具） |
| P0-3 | 预置 sre-investigator agent（提示词+清单） | 小 |
| P0-4 | ask_user 约束（提示词注入 + CRD 开关） | 小 |
| P0-5 | composer ↑/↓ 历史消息 | 小 |
| P1 | kubelet summary 工具、查询审计、进行中进度指示、@ 选择器动态 kind | 中 |
| P2 | capability profile CRD、GitLab/Argo provider、Python 运行时 parity、E2E | 大 |

验收标准（对应你的场景）：**全新安装 kagent → 打开预置 SRE agent → 粘贴 Jenkins 流水线链接 → 不做任何工具/权限配置，得到引用了日志、事件、pod、节点证据的根因分析报告（可下载 md）。**
