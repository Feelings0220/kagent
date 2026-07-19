# 端到端验证：Jenkins → 根因分析（审计 + 手动 runbook）

> 分支：`claude/kagent-e2e-audit`
> 背景：沙箱内无 Docker（`rootless Docker not found`），无法真起 kind 集群跑 E2E。本文分两部分：① 我做的**静态集成审计**（追踪整条调用链、修掉真实 bug）；② 你在**自己集群**上跑的手动验证步骤 + 排障表。

---

## 第一部分：静态集成审计结果

我把「粘贴 Jenkins 链接 → 根因分析」的完整调用链逐跳追踪了一遍：

```
UI 发消息 → agent(Go runtime) 的 jenkins_console_log 工具
  → HTTP GET KAGENT_URL/api/context/jenkins/resources/context   [controller]
  → controller 拉 Jenkins master console log（脱敏）
agent 提取线索 → k8s_events / k8s_pod_logs / k8s_get_resource 工具
  → HTTP GET KAGENT_URL/api/cluster/query/*                     [controller]
  → controller 用 uncached client / clientset → API Server
agent 汇总 → 结构化根因报告
```

### 发现并修复的 bug（本分支）

| # | 严重度 | 问题 | 影响面 | 修复 |
|---|--------|------|--------|------|
| **B1** | 中 | `k8s_*` / `jenkins_*` 工具用 `http.DefaultClient`（无鉴权 token）调 controller，而 controller 所有路由都在 `AuthnMiddleware` 之后 | **仅 `trusted-proxy` 鉴权模式**下每次工具调用 401；`unsecure`（默认）模式因无 token 兜底为 admin 而侥幸可用 | 把运行时的**已鉴权 httpClient**（带 service token，和 session/task 持久化用的是同一个）从 `main.go` 一路穿到 `NewK8sTools`/`NewJenkinsTools`，工具现在与 session 调用同样携带 token，任何鉴权模式都通过 |

### 审计确认「没问题」的点（避免误改）

- **KAGENT_URL 注入**：translator 的 `manifest_builder.go` 始终给 agent pod 注入 `KAGENT_URL=http://<controller>.<ns>:8083`，工具一定能创建。✓
- **查询参数名匹配**：逐个核对了工具发的 query key 与 handler 的 `r.URL.Query().Get(...)`（`kind/name/namespace/job/labelSelector/...`）——全部对齐。✓
- **RBAC**：getter ClusterRole 对 core `*` 有 `get/list/watch`，K8s RBAC 里 `resources:["*"]` **通配符覆盖子资源** `pods/log`，events 也在 core 组内。读链路 RBAC 无缺项。✓
- **uncached client + 字段选择器**：查询走绕过 cache 的直连 client，events 的 `involvedObject.name/kind` 字段选择器由 API Server 原生支持（不需要 informer 索引）。✓
- **响应信封**：工具解析 `{error,message,data}`，controller 用 `api.NewResponse` 产出同结构。✓
- **超时**：token client 30s 超时，pod 日志按 64KB 上限截断、拉取很快，充足。✓

### 已知非阻塞行为（记录、暂不改）

- **Jenkins 未配置时**：`jenkins_*` 在默认工具包里，但若 controller 没设 `JENKINS_URL`，这三个工具调用会返回 “Jenkins context provider is not configured”（400 文案）。模型看到后会跳过，不崩溃。要用 Jenkins 场景**必须**给 controller 配 Jenkins 环境变量（见下）。

---

## 第二部分：在你的集群上手动验证

### 前置条件

1. **模型**：kagent 需要一个可用的 ModelConfig（OpenAI/Anthropic/…）。`sre-investigator` 默认用 `default-model-config`，务必先配好 provider 和 API key。
2. **Jenkins 凭据**（用 Jenkins 场景才需要）：controller 需要以下环境变量（helm 里给 controller deployment 加 env，或参考 `JenkinsConfigFromEnv` 支持的键）：
   - `JENKINS_URL`（如 `https://jenkins.example.com`）
   - `JENKINS_USER`、`JENKINS_API_TOKEN`
3. **写操作**（可选）：破坏性工具默认关，需 `--set clusterTools.write.enabled=true`。

### 部署（kind 示例）

```bash
# 1. 建 kind 集群
make create-kind-cluster

# 2. 构建并加载镜像（controller / app / ui）
make build
make kind-load-images        # 或按项目实际的 load 目标

# 3. 装 CRD 再装主 chart
helm install kagent-crds ./helm/kagent-crds -n kagent --create-namespace
helm install kagent ./helm/kagent -n kagent \
  --set providers.default=openAI \
  --set providers.openAI.apiKey=$OPENAI_API_KEY

# 4.（Jenkins 场景）给 controller 注入 Jenkins 凭据后重启
kubectl -n kagent set env deploy/kagent-controller \
  JENKINS_URL=$JENKINS_URL JENKINS_USER=$JENKINS_USER JENKINS_API_TOKEN=$JENKINS_API_TOKEN

# 5. 等就绪
kubectl -n kagent rollout status deploy/kagent-controller
kubectl -n kagent get agent sre-investigator
```

### 冒烟测试（不依赖 Jenkins，先验证集群查询链路）

打开 UI（`kubectl port-forward -n kagent svc/kagent-ui 3000:8080`），进 `sre-investigator`，逐条问：

1. `列出 kube-system 里最近的几个 pod` → 期望走 `k8s_list_resources`，返回真实 pod 列表。
2. `kube-system 里有哪些 Warning 事件？` → 期望走 `k8s_events`，返回真实事件。
3. `给我 <某个pod> 的最近日志` → 期望走 `k8s_pod_logs`，返回真实日志。

**若这三步通过，说明 B1 修复生效、RBAC/路由/参数全部打通。**

### 目标场景（Jenkins → RCA）

在 `sre-investigator` 里粘贴一条真实失败流水线链接：

```
帮我查这条流水线为什么失败：https://<jenkins>/job/<folder>/job/<app>/<build>/
```

**期望**：agent 依次调用 `jenkins_console_log`（自动从 URL 解析 job+build）→ 从日志提取线索 → `k8s_events`/`k8s_pod_logs`/`k8s_get_resource` 关联集群侧 → 输出含「结论 / 证据 / 失败链 / 建议 / 存疑」的结构化报告，且**引用了它实际查到的日志行和事件**。

### 排障表

| 症状 | 可能原因 | 处理 |
|------|---------|------|
| 工具调用全部 401 / Unauthorized | `trusted-proxy` 模式且用了**未合入 B1 修复**的镜像 | 用本分支重建 app 镜像；或临时 `auth.mode: unsecure` 验证 |
| 工具报 “Jenkins context provider is not configured” | controller 无 Jenkins env | 按前置步骤 4 注入 `JENKINS_URL/USER/API_TOKEN` |
| pod 日志报 500 “Pod log access is not configured” | controller 的 uncached clientset 未建成（RBAC/config 异常） | 看 controller 日志里 `unable to create clientset`；确认 SA 绑定了 getter-role |
| 列表/日志 403 Forbidden | RBAC 未覆盖目标资源，或装了 `rbac.namespaces` 使权限被限定命名空间 | 检查 getter ClusterRole 是否 cluster-scoped；目标命名空间是否在 watch 列表内 |
| agent 没有 k8s/jenkins 工具 | 该 agent 关了默认工具包，或 `clusterTools.defaultBuiltinTools` 被设空 | 工具面板打开「Default cluster access」开关；或检查 helm 值 |
| agent 老是反问用户而不查 | 用了未合入 ask_user 克制指令的镜像 | 用含 PR #10 的镜像重建 |

---

## 结论

静态审计发现并修复了 1 个真实 bug（B1 鉴权客户端），其余链路（KAGENT_URL 注入、参数、RBAC、信封、超时）经逐跳核对无阻塞问题。**在默认 `unsecure` 模式下，整条 Jenkins→RCA 链路预期可跑通**；真正的 live 验证需你在自己集群上按上面 runbook 执行（沙箱无 Docker 无法代跑）。B1 修复让它在 `trusted-proxy` 安全模式下也成立。
