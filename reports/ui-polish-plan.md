# UI 布局与信息密度打磨计划

> 来源：用户实测反馈（2026-07）。逐项给出根因定位与修法，按工作量分两批。
> 分支规划：`claude/kagent-ui-polish`（独立于 Jenkins provider 分支）。

## 问题清单与方案

### 1. 左侧会话栏宽度不可调节
- **现状**：shadcn Sidebar，宽度是固定 CSS 变量 `--sidebar-width`（sidebar.tsx），只有展开/收起两态；
- **方案**：侧栏右缘加拖拽把手（pointer events 改写 CSS 变量），宽度范围 200–480px，
  存 localStorage（`kagent.sidebar.width`）跨会话保持；双击把手恢复默认。

### 2. 顶部留白太多
- **现状**：Header（layout 顶栏）+ 聊天滚动区自身 padding 叠加；
- **方案**：审计 Header 高度与 chat 容器 padding：Header 压到 h-12；滚动区顶部 padding
  从 py-6 减为 pt-2（底部保留，视觉锚定 composer）；空状态垂直居中改为 40% 高度处。

### 3. 右侧 Agent 详情栏留白多、不可调节
- **方案**：与 #1 同一套拖拽把手组件（左缘拖拽）；默认宽度调窄；
  区块间距减半（AgentDetailsSidebar 内 space-y 审计）；支持完全收起（图标条模式已有则接通）。

### 4. 滚动条常驻显示（应滚动时才出现）
- **现状**：`components/ui/scroll-area.tsx` 的 Radix ScrollArea 默认 `type="always"` 风格样式；
- **方案**：Radix Root 改 `type="scroll"`（滚动时浮现、600ms 后淡出），scrollbar 宽度收窄为 6px、
  透明轨道 + 圆角 thumb，加 opacity 过渡；全局所有 ScrollArea 受益，一处改动。

### 5. Token 显示单位应为 k
- **现状**：TokenStats/TokenStatsTooltip 直接显示原始整数（如 123456）；
- **方案**：新增 `formatTokens(n)`：≥1000 显示 `123.5k`，≥1M 显示 `1.2M`，保留一位小数；
  Tooltip 内仍显示精确值。单测覆盖边界（999/1000/999949/1M）。

### 6. 缺少上下文窗口限制显示（如 12.3k / 200k）
- **现状**：会话只有累计 token 统计，没有模型上下文窗口对照；后端 ModelConfig 不存窗口大小；
- **方案（分两步）**：
  - **MVP（前端）**：内置常见模型 → 窗口大小映射表（gpt-4o 128k、claude-* 200k、gemini-1.5 1M 等，
    前缀匹配），composer 状态行显示 `12.3k / 200k` + 细进度条，>80% 变琥珀色、>95% 变红提示压缩；
    未知模型只显示已用量；映射表独立文件便于维护；
  - **正解（后端，后续批）**：`ModelConfig.spec.contextWindow`（可选字段，创建页可填），
    API 透出到 AgentResponse，前端优先用 CRD 值、映射表兜底。

## 批次划分

| 批 | 内容 | 性质 |
|----|------|------|
| polish-1 | #4 滚动条、#5 token 单位、#6 MVP、#2 顶部留白 | ✅ 已实现 |
| polish-2 | #1/#3 双侧栏拖拽调宽（共享 Resizable 组件 + localStorage） | ✅ 已实现 |
| polish-3 | #6 正解（CRD 字段 + 表单 + API） | 前后端，随下个 CRD 批次 |


## 实现记录（claude/kagent-ui-polish）

- #4：`ui/scroll-area.tsx` Radix Root 改 `type="scroll"`（600ms 淡出），scrollbar 收窄 6px、透明轨道、opacity 过渡——全局生效；
- #5：`lib/tokenUtils.ts` `formatTokens`（k/M，一位小数去尾 0），Usage 行与消息 tooltip 均改用，title/tooltip 保留精确值；
- #6 MVP：`getModelContextWindow` 前缀映射表（OpenAI/Anthropic/Gemini/Llama 等），
  composer 状态行显示 `ctx 12.3k/200k` + 进度条（>80% 琥珀、>95% 红），
  当前上下文取最近一条带 tokenStats 消息的 prompt 值（最接近真实 context 大小）；未知模型自动隐藏；
- #2：聊天滚动区 `py-6` → `pt-2 pb-6`；
- #1/#3：`Sidebar` 组件新增 `width` prop（设在最外层，布局占位与固定面板同步，拖拽时禁用宽度过渡），
  `useSidebarWidth` hook（200–480px 钳制 + localStorage 持久化 + 双击复原），
  `SidebarResizeHandle` 拖拽把手（pointer capture），左右侧栏各自独立记忆宽度；
- 测试：tokenUtils 11 个单测；全套 282 个 jest 通过；tsc/eslint/生产构建全绿。

剩余：polish-3（ModelConfig.contextWindow CRD 字段，随下个 CRD 批次做）。
