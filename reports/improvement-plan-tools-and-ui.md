# Kagent 改进计划：Agent 工具能力 与 聊天 UI

> 基于 main 分支（b51b1d3）代码调研，针对两个核心痛点：
> 1. Agent 工具能力/权限不足，不能直接执行命令
> 2. 聊天界面显示内容少、输入框占用空间大、整体不好用

---

## 一、现状分析

### 1.1 工具能力现状

**Agent 能用的工具只有三类**（`go/api/v1alpha2/agent_types.go:482`）：

| 类型 | 说明 | 局限 |
|------|------|------|
| `McpServer` | 引用外部 MCP 工具服务器 | 需要额外部署和配置，门槛高 |
| `Agent` | 把另一个 Agent 当工具 | 不解决执行能力问题 |
| Skills 附带工具 | bash / read_file / write_file / edit_file | **只有配置了 `spec.skills` 才会挂载** |

关键发现：

- **bash 执行能力其实已经存在**。Go 运行时（`go/adk/pkg/tools/skills.go:99` 的 `NewSkillsTools`）和 Python 运行时（`python/packages/kagent-adk/src/kagent/adk/tools/bash_tool.py`）都实现了带沙箱的 bash 工具，但挂载条件是 `KAGENT_SKILLS_FOLDER` 环境变量非空（`go/adk/pkg/agent/agent.go:170-178`），也就是必须给 Agent 配一个 skills 镜像/git 仓库才能"顺带"获得命令执行能力。想要一个"能跑 kubectl 的 agent"，用户必须先学会 Skills 概念，这是主要的体验断层。
- 内置的 `kagent-tools` MCP 服务器（helm chart 默认开启）只提供预定义的 k8s 只读为主的封装工具，没有通用命令执行。
- 权限模型只有一个开关：`RequireApproval []string`（`go/api/v1alpha2/agent_types.go:533`），按工具名列出需要人工审批的工具。没有 allowlist/denylist、没有按参数匹配的规则（比如"kubectl get 放行、kubectl delete 审批"）、没有会话级"本次会话总是允许"。
- 沙箱网络策略已有雏形：`SandboxConfig.Network.AllowedDomains`（`agent_types.go:246-252`），可以复用。

### 1.2 聊天 UI 现状

代码位置：`ui/src/components/chat/`

**输入框区域过大**（`ChatInterface.tsx:1018-1076`）：
- Textarea 固定 `min-h-[100px]`，不会随内容收缩；
- 上方一行状态/token 统计（`mb-4`）+ 下方一行按钮（`mt-4`）+ 容器 `p-4`，合计固定占用约 220px 高度，即使只输入一个字也是这么大；
- 发送必须点按钮或按 **Ctrl/Cmd+Enter**（`handleKeyDown`，`ChatInterface.tsx:927-934`），普通 Enter 不发送，和所有主流聊天产品习惯相反。

**消息区显示内容少**：
- 每条工具调用渲染成一整张 Card（`ui/src/components/ToolDisplay.tsx:157`），带 CardHeader、"Arguments" 折叠行、"Results" 折叠行和大量 margin/padding。一轮多工具调用的对话里，屏幕大部分被卡片框架占据，实际内容（参数/结果）却默认折叠看不到；
- 消息文本使用 `break-all`（`ChatMessage.tsx:171`），英文单词会在任意字符处断行，可读性差；
- 滚动区 `py-12`（`ChatInterface.tsx:953`）上下留白过大；
- 消息区 `max-w-6xl` 全宽平铺（`ChatLayoutUI.tsx:148`），长行文字阅读体验差；
- 长会话没有虚拟化，全部消息一次渲染，滚动性能随会话变长而下降；自动滚动是无条件滚到底（`ChatInterface.tsx:217-224`），用户往上翻看历史时会被新消息强行拉回底部。

---

## 二、改进计划

### 方向 A：工具能力与权限（后端，Go）

#### A1. 内置工具作为 CRD 一等公民（P0）

让用户无需配置 Skills 或 MCP 服务器就能给 Agent 命令执行/文件能力：

```yaml
spec:
  declarative:
    tools:
      - type: Builtin
        builtin:
          name: bash            # bash | read_file | write_file | edit_file
          permissions:
            allowedCommands: ["kubectl get *", "kubectl describe *", "helm list*"]
            deniedCommands: ["kubectl delete *"]   # 拒绝优先于放行
            requireApprovalByDefault: true          # 不在 allowlist 内的命令走 HITL 审批
```

实现要点：
- `Tool` 结构体新增 `Builtin *BuiltinTool` 字段（v1alpha2），CEL 校验与现有 `McpServer`/`Agent` 互斥；
- 复用现有 `NewSkillsTools` 里的 bash/文件工具实现，把"是否挂载"与 Skills 解耦：无 skills 目录时以会话工作目录为根运行；
- 沙箱：复用现有 srt 沙箱与 `SandboxConfig.Network`；
- Translator（controller）把 CRD 配置翻译为运行时 AgentConfig，Go/Python 两个运行时都消费；
- 按 CLAUDE.md 要求补 E2E 测试（新 CRD 字段必须有）。

#### A2. 细粒度审批规则（P1）

现有 `RequireApproval` 只能按工具名整体开关。扩展为规则式：

```yaml
mcpServer:
  toolServer: kagent-tools
  toolNames: [kubectl]
  approvalRules:
    - match: { argsPattern: { command: "get|describe|logs.*" } }
      action: allow
    - match: {}
      action: ask        # 默认审批
```

- 保留 `requireApproval` 兼容旧配置（等价于 `action: ask` 全匹配）；
- UI 审批卡片上增加"本次会话总是允许此工具/此命令模式"，写入会话级别的临时放行表（存 DB session metadata，不改 CRD）。

#### A3. kagent-tools 内置 MCP 服务器扩展（P1）

- 增加可选的写操作工具组（`kubectl apply/scale/rollout` 等），helm values 默认关闭，开启时文档强制建议配合 `requireApproval`；
- 为 agent 部署提供 per-agent ServiceAccount + 最小 RBAC 的 helm 模板和文档，避免用户为了"能用"直接给 cluster-admin。

#### A4. 文档（P0，随 A1 出）

- 新增 "给 Agent 执行能力" 指南：Builtin bash vs Skills vs MCP 三种方式的选型表；
- 权限/审批配置示例集。

### 方向 B：聊天 UI（前端，Next.js）

> **进度**：B1 / B3 / Enter 发送已实现（`ui/src/components/chat/ChatInterface.tsx`、`ChatMessage.tsx`）：
> 输入区空闲高度从 ~220px 降到 ~60px，textarea 随内容自动伸缩（上限约 8 行）；
> Enter 发送（带中文输入法 composition 保护，Shift+Enter 换行，Ctrl/Cmd+Enter 兼容）；
> 消息列 `max-w-3xl` 居中、`break-words`、贴底跟随滚动 + "Latest" 回底悬浮按钮。

#### B1. 输入框紧凑化（P0，收益最大、改动小）

`ChatInterface.tsx` 底部 composer 重构：
- Textarea 改为自动伸缩：初始单行（~40px），随内容增长，上限约 8 行后内部滚动；
- 状态指示（thinking/streaming）与 token 统计合并为输入框上沿一行小字（仅活动时显示），去掉常驻的 `mb-4` 状态行；
- 发送/语音/取消按钮内嵌到输入框右下角，去掉独立按钮行；
- **Enter 发送，Shift+Enter 换行**（保留 Ctrl/Cmd+Enter 兼容）；
- 预期效果：空闲时 composer 高度从 ~220px 降到 ~60px，消息可视区多出约 160px。

#### B2. 工具调用显示紧凑化（P0）

`ToolDisplay.tsx` 改为单行可展开条目：
- 收起态：一行显示 状态图标 + 工具名 + 参数摘要（如 `bash: kubectl get pods -n kagent`）+ 耗时/状态，类似 Claude Code / Cursor 的工具调用行；
- 点击整行展开完整 Arguments / Results（保留现有折叠面板与复制按钮）；
- 连续多个工具调用聚合为一组，组头显示 "N tool calls"，可整组展开；
- 审批态（pending_approval）保持醒目卡片样式不变——这是需要用户操作的场景。

#### B3. 消息区排版修复（P0，都是小改）

- `ChatMessage.tsx:171` 的 `break-all` 改为 `break-words`；
- 滚动区 `py-12` 减为 `py-6`；
- 消息内容列限宽 `max-w-3xl` 居中（工具卡片可保持稍宽），改善长行可读性；
- 自动滚动改为"贴底跟随"：仅当用户本就在底部附近时才自动滚动，向上翻阅时显示"跳到最新"悬浮按钮。

#### B4. 会话体验（P1）

- 长会话消息虚拟化（`react-virtuoso` 或按需分页加载历史 task），解决长会话卡顿；
- 会话侧栏：搜索/过滤、按时间分组（今天/本周/更早）；
- 空状态从"Start a conversation"卡片升级为：显示该 Agent 的描述、可用工具列表和 2-3 个建议提示词（数据已在 `AgentDetailsSidebar` 可得）；
- 流式输出中断后的"继续/重试"按钮。

#### B5. 视觉打磨（P2）

- 用户/助手消息的视觉区分（目前只有左边框颜色 blue/violet 之差）：用户消息右对齐浅色气泡，助手消息保持文档流；
- 深色模式下 `prose` 与代码块的对比度检查；
- Storybook 已有基础（`*.stories.tsx`），新组件补齐 story 以便回归。

---

### 方向 C：对话内工具控制、上下文管理与富输出（新需求设计）

> 目标：让工具/权限控制"就在对话框旁边"，支持上传文件补充上下文，
> 并让 Agent 输出的 md/html 文档可预览、可下载 —— 对齐 Claude/ChatGPT 类产品的会话体验。

#### C1. 对话框旁的工具与权限快捷控制（P0）

**交互设计**：在输入框左侧加一个"工具"按钮（滑块图标），点开 Popover 面板：

```
┌─ Tools & Permissions ──────────────────┐
│ ✓ kubectl (kagent-tools)    [Ask ▾]    │   Ask / Always allow (session) /
│ ✓ bash                      [Allow ▾]  │   Always allow (saved) / Disable
│ ✗ helm                      [off]      │
│ ────────────────────────────────────── │
│ + Add tool…  (从已发现的 MCP 服务器选择) │
└────────────────────────────────────────┘
```

- 数据已具备：`ChatLayoutUI` 已把 `allTools`（所有已发现的 MCP 工具）传给 `AgentDetailsSidebar`，当前 Agent 的工具列表在 `currentAgent.agent.spec.declarative.tools`，复用即可；
- **持久修改**（增删工具、保存审批策略）：走已有的 Agent 更新 server action（等价于 PATCH Agent CRD），面板里改完即生效，无需进 Agent 编辑页；
- **会话级修改**（"本会话总是允许"）：新增 session 级 policy，存 DB session metadata，HITL 审批层在弹审批前先查会话放行表——依赖方向 A2 的审批规则落地；
- 注意权限边界：修改 Agent CRD 影响所有使用者，面板上要显式区分"仅本会话 / 保存到 Agent"两种作用域。

**实现拆分**：
1. C1a（✅ 已实现，`ui/src/components/chat/ChatToolsPanel.tsx`）：输入框旁扳手按钮弹出面板，
   列出 Agent 全部工具（按 MCP 服务器分组），每个工具可切换 **Ask（需审批）/ Auto（自动执行）**
   （落到 CRD 已有的 `requireApproval` 字段）、可移除，可从已发现的 MCP 服务器添加工具；
   草稿式编辑 + Save 一次性保存（新 server action `updateAgentTools`）；面板明确标注"作用于该 Agent 的所有会话"；
2. C1b（依赖 A2）：会话级"总是允许"与按参数规则。

#### C2. 上下文管理：文件上传（P0，✅ 第一期已实现）

会话工作区已有 `uploads/`、`outputs/` 目录约定（bash/file 工具都能读写），缺的只是"把文件送进去"的通道。

**已实现方案（A2A 原生，无需新增 HTTP 接口）**：
- 上传走 A2A 协议 `FilePart`（base64），控制器无需代理文件——文件直达 agent 运行时；
- **Go 运行时**（`go/adk/pkg/a2a/uploads.go` + executor 步骤 5b）：收到带 FilePart 的消息时，
  把文件写入会话工作区 `uploads/`（文件名清洗防路径穿越、重名自动 -1/-2 后缀），
  并把该 part 替换为一条文本注记（"File uploaded: ... use the file or bash tools to read it"），
  避免把任意二进制塞进 chat-completion 请求；图片额外保留 inline，vision 模型仍能看图；
  无 skills 工作区时保持原样（inline 兜底）。单测覆盖清洗/去重/落盘/图片/异常路径；
- **前端**（ChatInterface）：回形针按钮 + 剪贴板粘贴文件，输入框上方显示可删除的文件 chips
  （限 5 个/条、10MB/个），发送时作为 FilePart 附在消息里，失败自动恢复草稿与附件；
  已发送消息在会话里显示附件 chips（ChatMessage）。

**后续增强**：
- Python 运行时补齐同样的 materialize 逻辑（目前仅默认的 Go 运行时支持落盘）；
- 侧栏 "Context" 区块列出 uploads/ 与 outputs/ 内容（需要 agent 运行时暴露列表/下载接口或经 A2A 扩展）；
- 拖拽上传、目录支持。

#### C3. 富输出：文档预览与下载（P0 前端部分，P1 Artifacts）

**C3a. 代码块工具栏（纯前端，立即可做）**：
- 现有 `CodeBlock.tsx` 已有复制，`HTMLPreviewDialog` 已支持 html 预览；统一为代码块右上角工具栏：**Copy / Preview / Download**；
- Preview 支持：`html`（现有 iframe 沙箱预览）、`markdown`（渲染视图切换）、`svg`、`mermaid`（可引入 mermaid 渲染）；
- Download 按语言映射扩展名（md/html/yaml/json/csv/py…），文件名取代码块首行注释或时间戳；
- 长代码块默认折叠显示前 N 行 + "展开"，缓解"一坨输出撑满屏幕"。

#### C4. 会话内切换模型（P0，已实现）

> 需求：在会话框附近直接切换模型 —— 不只是模型名，还包括 baseUrl 等 endpoint 配置。

kagent 的模型抽象是 ModelConfig CRD（provider + model + baseUrl/endpoint + API key secret 等），
Agent 通过 `spec.declarative.modelConfig` 按名字引用。因此"切换模型（含 baseUrl）" = 切换 ModelConfig 引用：

- **输入框左侧新增模型选择器**（`ui/src/components/chat/ChatModelSwitcher.tsx`）：
  显示当前模型名，点开列出 Agent 所在 namespace 的全部 ModelConfig，
  每项展示 名称 / provider / model / baseUrl（OpenAI、Anthropic、Gemini、Ollama host、Azure endpoint）；
- 选择后通过新增 server action `updateAgentModelConfig`（`ui/src/app/actions/agents.ts`）
  更新 Agent 的 `spec.declarative.modelConfig`（后端 `PUT /api/agents` 是"取现有对象、只替换 spec"，无并发冲突问题）；
- 面板底部 "New model" / "Manage" 直达 ModelConfig 创建/管理页（那里可配置 baseUrl、密钥等全部字段）；
- 作用域说明：修改的是 Agent CRD（对所有会话生效）。会话级模型覆盖需要运行时支持 per-session model，
  作为后续增强（可与 A2 会话级策略一起设计）；
- 仅对 Declarative 非 sandbox agent 显示（BYO 自带模型，SandboxAgent 更新路径不同）。

**C3b. Artifacts 面板（依赖 C2 的文件 API）**：
- Agent 用 write_file/bash 写到 `outputs/` 的产物，右侧新增 "Artifacts" 面板自动列出；
- 点击预览（md 渲染 / html 沙箱 iframe / 图片直显 / 其他给下载），一键下载；
- 流式过程中收到 function_response 里出现 outputs/ 写入时，实时刷新面板并 toast 提示"生成了文档 xxx"。

---

## 三、落地顺序建议

| 阶段 | 内容 | 预估工作量 | 状态 |
|------|------|-----------|------|
| 第 1 批 | B1 输入框 + B3 排版修复 + Enter 发送 | 1-2 天，纯前端 | ✅ 已实现 |
| 第 2 批 | C3a 代码块 Preview/Download + B2 工具调用紧凑化 + C4 模型切换器 | 3-4 天，前端 + 现有 API | ✅ 已实现 |
| 第 3 批 | C1a 对话框旁工具面板（增删工具 + Ask/Auto 审批切换） | 2-3 天，前端 + 现有 API | ✅ 已实现 |
| 第 4 批 | C2 文件上传（A2A FilePart → 会话工作区落盘 + 附件 UI） | 1 周，Go + 前端 | ✅ 已实现 |
| 第 5 批 | A1 Builtin 工具 CRD 字段 + 运行时挂载 + E2E + 文档 | 1-2 周，Go 为主 | |
| 第 6 批 | A2 审批规则 + C1b 会话级放行 UI | 1 周 | |
| 第 7 批 | C3b Artifacts 面板、B4 会话体验、A3 kagent-tools 扩展 | 按需排期 | |

第 2-3 批零后端风险快速见效；A1 是解决"agent 不能执行命令"的根本改动，涉及 CRD 变更需走 `make -C go generate` + E2E 流程（参见 kagent-dev skill）；C2 的上传 API 与 A1 无耦合，可并行开发。
