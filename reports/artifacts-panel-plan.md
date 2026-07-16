# C3b 设计文档：会话 Artifacts 面板（产物预览与下载）

> 分支：`claude/kagent-artifacts-panel`（基于 main @ ceec195）
> 前置：PR #1 / #2 已合并（文件上传、builtin 工具、代码块预览下载均已在 main）

---

## 一、目标

Agent 用 `write_file` / `bash` 写到会话工作区 `outputs/` 目录的文件（报告、图表、生成的配置等），
目前用户完全看不到。目标是把这些产物自动呈现在聊天界面：

1. 每轮对话结束后，本轮新产生/修改的 `outputs/` 文件自动出现在会话里；
2. 可**预览**（markdown 渲染 / html 沙箱 iframe / svg / 图片直显）；
3. 可**下载**；
4. 历史会话重新打开时产物仍在（持久化）；
5. 流式过程中实时出现，不需要刷新页面。

对齐 Claude / ChatGPT 的 Artifacts 体验。

---

## 二、方案选型

文件在 **agent pod 的本地磁盘**（`/tmp/kagent/<session-id>/outputs/`），UI/controller 无法直接读取。

| 方案 | 说明 | 结论 |
|------|------|------|
| A. Agent 运行时加 HTTP 文件接口 + controller 代理 | `GET /workspace/{session}/files`，controller 转发 | ❌ 新增两层接口和鉴权面，sandbox/substrate 模式路由复杂 |
| B. controller 直接读文件 | — | ❌ 不可行，跨 pod 文件系统 |
| **C. A2A 原生 Task Artifacts（选定）** | 运行时在每轮结束时把新产物作为 A2A artifact 事件发出 | ✅ 零新增接口，见下 |

**方案 C 的依据（已调研核实）**：

- A2A 协议原生支持 Task Artifacts（`TaskArtifactUpdateEvent`，Artifact 含 `name`/`metadata`/`parts`，parts 可为 FilePart base64）；
- Go executor 已经用 artifact 事件传最终文本（`executor.go` 第 387 行 `NewArtifactEvent`），事件管道现成；
- `KAgentTaskStore.Save` 会把 `Task.Artifacts` 连同 task JSON 持久化到 DB（partial 会被清理，非 partial 保留）；
- UI `getSessionTasks` 返回的 `Task` 类型已含 `artifacts?: Artifact[]`；
- UI 流式处理已有 `handleA2ATaskArtifactUpdate`（`messageHandlers.ts`），只需分流 file parts；
- 上传链路（C2）已验证 FilePart base64 端到端可用。

**推论**：全程走已有协议与存储，历史回放、sandbox 模式、resubscribe 都自动工作。

---

## 三、详细设计

### 3.1 Go 运行时：产物扫描与发射（`go/adk/pkg/a2a/`）

新文件 `artifacts.go`：

```
snapshotOutputs(sessionID)            -> map[relpath]{size, mtime}   // 每轮 run 前
collectNewOutputs(sessionID, before)  -> []OutputFile                // run 后 diff
buildFileArtifactEvents(reqCtx, files) -> []*TaskArtifactUpdateEvent
```

- **时机**：executor 步骤 3（session path 初始化）后快照；步骤 11 发 completed 前（且非 HITL/失败路径）
  扫描 diff 并逐个发 artifact 事件；
- **判定新/改**：文件 mtime 或 size 与快照不同、或快照中不存在；只扫 `outputs/` 一层 + 子目录（WalkDir）；
- **大小上限**：单文件 ≤ 2MB 内联 base64（FilePart）；超限则发一个只带 metadata 的 artifact
  （name/size/mime，无 bytes），UI 显示"文件过大，请让 agent 压缩或拆分"；
- **数量上限**：每轮最多 10 个，超出记日志跳过（防失控循环刷爆 DB）；
- **Artifact 结构**：
  - `Name` = 文件名（相对 outputs/ 的路径）；
  - `Metadata` = `{kagent_type: "file_artifact", kagent_path: "outputs/<rel>", kagent_size: N, kagent_mime: "..."}`（走 `GetKAgentMetadataKey` 前缀规范）；
  - `Parts` = `[FilePart{FileBytes}]`（或超限时空 parts + metadata，注意 taskstore 会清掉空 parts 的 artifact —— 超限时放一个 TextPart 说明代替）；
- **mime 推断**：按扩展名（`mime.TypeByExtension`），兜底 `application/octet-stream`；
- **失败容忍**：扫描/读文件出错只记日志，不影响本轮回复。

### 3.2 UI：数据提取

`messageHandlers.ts`：

- 新增 `extractFileArtifactsFromTasks(tasks: Task[]): FileArtifact[]` —— 遍历 `task.artifacts`，
  取 `metadata.kagent_type === "file_artifact"` 的条目（按 name 去重，后者覆盖前者=最新版本）；
- `FileArtifact = { name, path, mimeType, size, bytes?: string, taskId }`；
- 流式：`createMessageHandlers` 增加可选 `onFileArtifact(artifact: FileArtifact)` 回调，
  `handleA2ATaskArtifactUpdate` 里识别 file_artifact 类型的事件时调用（不再拼进文本流）。

### 3.3 UI：Artifacts 面板（`ui/src/components/chat/ArtifactsPanel.tsx`）

- **位置**：消息区与 composer 之间的一条可折叠横条：`📄 Artifacts (3) ▾`，
  展开后是 chip 行（复用 `AttachmentChip` 风格，扩展出预览/下载动作）；
- **交互**：
  - 点 chip → 预览：md/html/svg 走现成 `DocumentPreviewDialog`；`image/*` 用 data-URI `<img>` 弹窗
    （给 DocumentPreviewDialog 加 `image` kind）；其余类型只提供下载；
  - 每个 chip 带下载按钮（Blob + a[download]，同 CodeBlock 的实现，抽公共 util `downloadTextFile` → 本次改成 `downloadFile(bytes|text)`）；
  - 新 artifact 到达时 toast 提示 "Agent 生成了文件 xxx"；
- **状态**：`ChatInterface` 持有 `artifacts: FileArtifact[]`：
  - 初始加载：`extractFileArtifactsFromTasks(getSessionTasks(...))`（已有调用点，顺路提取）；
  - 流式：`onFileArtifact` 回调 append（按 name 去重）；
  - 会话切换：随 initializeChat 重置。

### 3.4 Python 运行时

后续跟进（本期不做）：在 `KAgentApp`/executor 侧做同样的 run 前快照 + run 后 diff + artifact 事件。
计划文档记为 P1 跟进项；Go 是默认运行时，先覆盖主路径。

---

## 四、边界与风险

| 风险 | 处理 |
|------|------|
| 大文件撑爆 DB（task JSON 里存 base64） | 2MB/文件 + 10 文件/轮 上限；超限降级为说明文本 |
| 每轮重复发同一文件 | run 前快照 + mtime/size diff，只发变化的 |
| agent 反复改同一文件 | UI 按 name 去重取最新；历史 task 里旧版本仍在（可接受） |
| taskstore 清理空 parts artifact | 超限文件用 TextPart 占位而不是空 parts |
| HITL 中断轮（input_required） | 该轮不扫描（HITL 恢复后的下一轮结束时会补上 diff） |
| 无工作区的 agent（无 skills/builtin） | outputs/ 目录不存在 → 扫描直接返回空，零开销 |

---

## 五、实施步骤与验收

| 步骤 | 内容 | 验收 |
|------|------|------|
| 1 | Go `artifacts.go`：快照/diff/事件构建 + 单测（新文件、修改、超限、上限、无目录） | `go test ./adk/pkg/a2a/` 全绿 |
| 2 | executor 接线（快照 + completed 前发射） | 单测 + `go build`/`go vet` |
| 3 | UI `extractFileArtifactsFromTasks` + `onFileArtifact` + 单测 | jest 全绿 |
| 4 | `ArtifactsPanel` + `DocumentPreviewDialog` image kind + 下载 util | tsc/eslint/build 全绿 |
| 5 | 文档：`docs/builtin-tools-and-uploads.md` 增补 Artifacts 一节 | — |
| 6 | （跟进）Python 运行时对等、E2E | 独立排期 |
