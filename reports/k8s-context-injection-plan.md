# 设计文档：K8s 上下文注入（@-提及集群资源）与 CI 平台上下文规划

> 分支：`claude/kagent-k8s-context`（基于 main @ 9d71de3）
> 愿景定位：这是"深度集成 Kubernetes 的 Claude 网页版"的核心差异化能力——
> 聊天时直接引用集群里的真实资源，agent 回答自动带上资源现状。

---

## 一、目标与用户故事

1. 用户在输入框点 "@"（或加号按钮）→ 选资源类型（Pod/Deployment/Service…）→
   搜索选中某个资源 → 输入框上方出现 `@pod nginx-xxx` 上下文 chip；
2. 发送时，该资源的**清洗后 YAML + 关联 Events** 作为上下文随消息注入，
   模型直接基于资源现状回答（"这个 pod 为什么 CrashLoopBackOff？"）；
3. 会话历史里该消息显示为文字 + 上下文 chip（不刷屏几十行 YAML）；
4. 对所有 agent 生效（Declarative Go/Python、BYO），无需给 agent 配任何工具。

## 二、方案要点（已调研核实）

### 2.1 权限：零新增 RBAC
控制器 `getter-role`（helm/kagent/templates/rbac/getter-role.yaml）已含：
- `apiGroups: [""] resources: ["*"] verbs: [get,list,watch]`（core：pod/service/node/event/…）
- `apps/*`、`batch/*`、`rbac/*`、`gateway.networking.k8s.io/*` 同为只读。
支持 `rbac.namespaces` 时自动退化为 namespace 级 Role——资源接口天然跟随部署时的权限边界。

### 2.2 后端：ResourcesHandler（沿用 NamespacesHandler 先例）
新增 `go/core/internal/httpserver/handlers/resources.go`：

| 接口 | 用途 |
|------|------|
| `GET /api/cluster/resources?kind=pod&namespace=ns&query=ngx&limit=20` | 资源列表（供 @ 搜索补全），返回 name/namespace/摘要状态 |
| `GET /api/cluster/resources/context?kind=pod&namespace=ns&name=x` | 上下文文本：清洗 YAML（去 managedFields/last-applied）+ 该对象最近 Events，上限 ~16KB |

- **kind 白名单**（安全边界，显式排除 Secret）：pod、deployment、statefulset、daemonset、
  replicaset、job、cronjob、service、configmap、node、namespace、ingress；
  映射到 GVK + 是否 namespaced，经 controller-runtime `unstructured` List/Get；
- 超白名单 kind → 400；对象不存在 → 404；list 默认 limit 20、按名字 contains 过滤；
- ConfigMap 注入时对 data 值做逐条截断（防超长）；
- Events 用 fieldSelector `involvedObject.name/namespace/kind` 取最近 10 条。

### 2.3 注入格式：带 metadata 的 TextPart（零运行时改动）
发送消息时，每个上下文 chip 变成一个额外 TextPart：

```json
{ "kind": "text",
  "text": "=== Kubernetes context: pod ns/nginx-xxx ===\n<yaml+events>",
  "metadata": { "kagent_context": { "provider": "kubernetes", "kind": "pod", "namespace": "ns", "name": "nginx-xxx" } } }
```

- Go/Python 转换器对 TextPart 一律透传给模型 → 双运行时 + BYO 全兼容；
- UI 渲染时把带 `kagent_context` 的 part 从正文剔除、改渲染成 chip；
- 历史消息（task JSON 持久化 parts+metadata）重新打开时 chip 依旧。

对比否决的方案：FilePart（无工作区的 agent 会把非图片 blob 直塞 provider 报错）；
DataPart（转换器会丢弃无类型标记的 DataPart）。

### 2.4 前端
- composer 左侧新增 **@ 按钮**（AtSign 图标）+ 输入框内键入 `@` 也可唤起；
- Popover：kind 下拉 + namespace 下拉（复用 NamespacesCombobox 数据源）+ 搜索框 + 结果列表；
- 选中即拉取 context 文本存入 `pendingContexts` state，chip 显示在附件行（可删除）；
- 发送时转成 TextPart 附加；错误恢复（restoreDraft）同附件一并恢复；
- ChatMessage：`kagent_context` part → chip（`@pod nginx-xxx`），点击可弹窗查看注入的原文。

## 三、CI 平台上下文注入规划（Jenkins 等，后续批次）

@-提及体系按 **provider** 抽象设计，Kubernetes 只是第一个 provider：

### 3.1 架构：Context Provider 接口
后端定义统一接口（本批就按此组织代码，方便扩展）：

```
type ContextProvider interface {
    Name() string                                   // "kubernetes" | "jenkins" | ...
    Kinds() []KindInfo                              // 该 provider 可引用的对象类型
    List(ctx, kind, scope, query, limit) []Item     // 搜索补全
    Context(ctx, kind, scope, name) (string, error) // 注入文本
}
```

前端 @ 菜单第一级即 provider（Kubernetes / Jenkins / …），选择器与 chip 均带 provider 标识
（`kagent_context.provider`），UI 无需为新 provider 改结构。

### 3.2 Jenkins provider（第二批）
- **配置**：helm values `contextProviders.jenkins: {url, credentialsSecretRef}`（用户名 + API token 存 Secret）；
- **kinds**：`job`（列任务）、`build`（某 job 最近构建，含结果/时长）、`console`（某次构建的控制台日志尾部 N KB）；
- **接口映射**：Jenkins REST（`/api/json`、`/job/<name>/<n>/consoleText`），只读；
- **典型场景**："@jenkins-build my-app#123 为什么失败？" → 注入构建元数据 + 失败 stage 日志尾部；
- **安全**：日志注入前做长度截断 + 可选敏感词遮蔽（token/password 正则）。

### 3.3 其他候选 provider（按需排期）
| Provider | kinds | 数据源 |
|----------|-------|--------|
| GitLab CI / GitHub Actions | pipeline、job、log | 各自 REST API（PAT Secret） |
| Prometheus | query（PromQL 即时值）、alert | Prometheus HTTP API |
| ArgoCD | application、sync-status | ArgoCD API |

### 3.4 与 MCP 工具的关系
MCP 工具是 **agent 主动调用**；上下文注入是**用户主动圈定事实**再提问——
两者互补：注入保证"模型第一时间看到用户所指的对象"，工具用于后续追查。

## 四、实施步骤与验收（本批）

| 步骤 | 内容 | 验收 |
|------|------|------|
| 1 | Go：provider 接口 + kubernetes provider + ResourcesHandler + 路由 | fake client 单测全绿 |
| 2 | UI：server actions + 类型 | tsc |
| 3 | UI：@ 按钮 + Popover 选择器 + 上下文 chips + 发送注入 | jest + build |
| 4 | UI：历史消息 context chip 渲染（含查看原文弹窗） | jest |
| 5 | 文档：docs/ 增补使用说明；本文档记进度 | — |

超出本批：`@` 光标内联联想（textarea 内浮层，交互复杂，Popover 先行）；Jenkins provider。
