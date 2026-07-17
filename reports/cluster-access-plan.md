# 集群查询与操作能力计划（Cluster Access Toolpack）

> 分支：`claude/kagent-cluster-access`
> 状态：设计定稿，开始实施
> 前置依赖：@-mention 上下文注入（已合并）、内置工具机制 A1（已合并）、会话级 Always-Allow 审批 A2（PR #7）

## 1. 需求与问题

用户反馈的三个问题：

1. **@-mention 只是"一次性快照"**：@ 一个 pod 只在发送时注入当时的 YAML/事件，agent 没有权限/工具去**主动**读取该 pod 的日志、最新 YAML、关联事件。
2. **@ 选择器资源不全**：命名空间下拉只看到少数几个（如 cattle-*），资源列表只看到 pod，没有 deployment/svc 等。
3. **最终目标**：agent 能查询集群**绝大多数资源**——分析 pod 日志、流水线日志、把事件关联到节点和 pod、得出错误分析结论；同时"运行测试 pod、更新 deployment 配置"等**破坏性操作权限**能在会话里很方便地按需添加。

## 2. 根因分析

### 2.1 @ 选择器不全的根因

- `resources.go` 的 `HandleListResources` 默认 `limit=20`，且按 `namespace, name` **字母序**排序后截断。在"全部命名空间"浏览时，字母序靠前的命名空间（`cattle-*` 以 c 开头）垄断了前 20 条 —— 这正是"只看到 cattle 的几个命名空间和 pod"的现象。kind 下拉其实已支持 11 种资源，但列表内容偏斜让人以为只有 pod。
- 命名空间下拉来自 `/api/namespaces`：若部署时配置了 `WATCH_NAMESPACES`，controller 只返回 watch 列表；且 manager **cache 也按命名空间裁剪**（`configureNamespaceWatching`），此时经 cache 的 List 根本读不到范围外的资源。
- 结论：a) 排序/截断策略要改；b) 集群浏览/查询要用**绕过 cache 的直连 client**（uncached），让 RBAC（getter ClusterRole 对 core/apps/batch 全读）成为唯一边界。

### 2.2 agent 无主动查询能力的根因

- agent pod 使用自己的 ServiceAccount，没有集群 RBAC；controller 的 getter-role 才有全集群读权限。
- 运行时已注入 `KAGENT_URL`（controller 地址，session service 在用）。
- 结论：**工具走 controller 代理**——运行时内置 `k8s_*` 工具通过 `KAGENT_URL` 调 controller 新增的 `/api/cluster/query/*` 端点，零 agent RBAC 变更即可获得与 controller 等同的读能力；写操作单独收口、单独开关。

## 3. 方案设计

### 3.1 Controller：集群查询/操作 API（`cluster_tools.go`）

新端点（读，默认启用）：

| 端点 | 说明 |
|------|------|
| `GET /api/cluster/query/kinds` | discovery 列出可查询的资源种类（RESTMapper） |
| `GET /api/cluster/query/list` | 任意 kind 列表：`kind, namespace?, labelSelector?, fieldSelector?, limit?`；返回名称/命名空间/状态摘要 |
| `GET /api/cluster/query/resource` | 任意 kind 单对象：净化后的 YAML + 最近事件（复用 sanitize/事件逻辑，从固定 allowlist 扩展为动态 kind 解析） |
| `GET /api/cluster/query/logs` | Pod 日志：`name, namespace, container?, tailLines?, previous?, sinceSeconds?`（client-go clientset 子资源；getter-role 的 core `*` 已覆盖 `pods/log`） |
| `GET /api/cluster/query/events` | 事件检索：`namespace?, name?, kind?, limit?`，按时间倒序 |

安全边界：

- **Secret 永远拒绝**（kind 黑名单，与 @-mention 一致）；其余 kind 交给 API Server RBAC 判定，Forbidden 原样透传给工具结果。
- 全部使用**独立 uncached client + clientset**（`client.New(restConfig)`），不受 cache 命名空间裁剪影响，也避免任意 kind 进入 informer cache 造成内存膨胀。

写端点（POST，破坏性）：

| 端点 | 说明 |
|------|------|
| `POST /api/cluster/apply` | Server-Side Apply 一段 YAML（可用于"跑测试 pod"“改 deployment 配置”） |
| `POST /api/cluster/delete` | 删除指定对象 |
| `POST /api/cluster/scale` | 修改副本数 |
| `POST /api/cluster/rollout-restart` | 滚动重启（patch template 注解） |

- Helm 新增 `clusterTools.write.enabled`（默认 **false**）：为 controller SA 增加 core/apps/batch 写 RBAC；未开启时 API Server 直接 Forbidden。
- Secret 同样拒绝 apply/delete。

### 3.2 运行时：内置 `k8s_*` 工具（go/adk）

`go/adk/pkg/tools/k8s.go`，functiontool 包装 HTTP 调用（`KAGENT_URL`）：

- 读：`k8s_get_resource`、`k8s_list_resources`、`k8s_pod_logs`、`k8s_events`、`k8s_api_resources`
- 写：`k8s_apply`、`k8s_delete`、`k8s_scale`、`k8s_rollout_restart`

审批策略（核心安全设计）：

- 4 个写工具在运行时**硬编码加入 approvalSet**（不依赖配置），每次调用先弹 HITL 审批卡片；
- 配合 A2 的 **Always allow (session)**，用户第一次批准后本会话不再打扰 —— 即"破坏性权限在会话里很方便地添加"。

CRD/翻译器/UI：

- `BuiltinToolName` 枚举扩展 9 个名字；CRD/deepcopy 重新生成并同步 helm/kagent-crds；
- 翻译器照旧去重合并进 `AgentConfig.BuiltinTools`；
- UI `ChatToolsPanel` 的内置工具目录加入 k8s 工具（写工具标注 "requires approval"），沿用已有的会话内添加/移除交互。

### 3.3 @-mention 联动与选择器修复

- 注入的上下文文本末尾追加提示行：`(Tip: use the k8s_* tools to fetch live logs, YAML, and events for this resource.)`，把"快照"与"实时查询"衔接起来；
- `HandleListResources`：默认 limit 20 → 50；pod/job/replicaset 按 `creationTimestamp` **倒序**（最近活动优先，天然跨命名空间散布），其余 kind 保持字母序；listing 改用 uncached client；
- UI 选择器 kind 下拉保持 allowlist（@-mention 注入场景不变），agent 的动态 kind 能力由 `k8s_*` 工具承担。

### 3.4 Python 运行时

暂缓（与 A1/A2 一致，Go 运行时先行）；在本文档记录 TODO。

## 4. 实施步骤

1. ✅ 计划文档（本文件）
2. 选择器修复：resources.go 排序/limit/uncached client + 单测
3. `cluster_tools.go` 读端点 + RESTMapper kind 解析 + 单测（fake client）
4. 运行时 `k8s.go` 工具 + 硬编码审批 + 单测
5. CRD 枚举 + `make -C go generate` + helm CRD 同步 + 翻译器测试
6. 写端点 + helm `clusterTools.write.enabled` RBAC
7. UI ChatToolsPanel 目录 + 上下文提示行
8. 全量验证（go build/test/vet、tsc/jest/eslint/build）、提交、推送

## 5. 验收标准

- @ 选择器在"全部命名空间"下能看到跨命名空间的最近 pod；kind 切换 deployment/service 等正常列出；
- agent 被授予 `k8s_*` 读工具后，能回答"X 命名空间里哪个 pod 在重启？给我看它最近 50 行日志和相关事件"这类问题（多步工具调用）；
- `k8s_apply` 等写工具每次调用弹审批；点 "Always allow (session)" 后同会话内不再弹；
- 未开启 `clusterTools.write.enabled` 时写工具返回明确的 Forbidden 报错文本；
- Secret 读写一律被拒绝。
