# 司域 (Sydom) M1.5 — 最小可托管运维底座（TLS / 健康探针 / 可部署） 设计

> 类型：单功能 spec（M1 多租户基座第 5 子项目，M1 收尾）。
> 日期：2026-06-15
> 前置：M1.1 租户隔离基座 `437b28d` / M1.2 自助账户 `48ecae8` / M1.3 有效权限视图 `588ae54` / M1.4 薄运营台 `1c6fb29` 均已并入 main。
> 路线图定位：`2026-06-13-sydom-production-readiness-roadmap.md` §4 M1 «含「最小可托管」运维底座（TLS、健康探针、可部署）»；§2D «运维就绪» 的最薄首切片。完整硬化（mTLS / 证书轮换 / k8s·helm / metrics·tracing / HA / 限流·备份）明确归 **M5**。

## 1. 背景与目标

司域功能已端到端可跑（数据面 + 管理/接入面 + 双层 UX 薄种子），但运维底座为零：**服务侧全明文 gRPC/HTTP，无任何健康/就绪探针，部署 `depends_on` 非健康门控、容器跑 root**。这三项缺失使其无法作为「可托管」产品交付给设计客户做私有 Beta。

现状盘点（实查，非臆测）：
- **TLS**：服务侧零 TLS。但 HMAC 层已为 TLS 预留钩子——`auth.NewPerRPCCredentials(appID, secret, secure)` 的 `secure` 控制 `RequireTransportSecurity()`（`internal/auth/credentials.go:61`）；`syncclient.Config{Secure, DialOptions}` 可注入 TLS 凭据（`internal/sidecar/syncclient/config.go:24`、`client.go:32`）。缺的是各监听器真正 serve TLS + sidecar dial 用 TLS。
- **健康探针**：完全没有。`/healthz`、`/readyz` 一个都没有。`deploy/docker-compose.yaml` 只有 PG/Redis 有 healthcheck，controlplane/sidecar 用 `service_started`（弱依赖，不等真正就绪）。
- **可部署**：`deploy/` 有 docker-compose 全栈 + 各 Dockerfile + `migrate/migrate` 一次性迁移；无 k8s/helm；`Dockerfile.controlplane` 等跑 root（alpine 无 `USER`）。

**目标**：交付「最小可托管」运维底座，使司域能在单机 / 单 compose 下以**加密、健康门控、非 root** 的姿态自托管，且**一切新增默认关闭、向后兼容**——存量测试 / demo / SDK 零改可跑。

**本轮 brainstorm 锁定的范围决策**：

| 维度 | 决策 |
|---|---|
| TLS 终结模型 | **应用原生可选 server-TLS**：每监听器 cert/key 可选，缺省=明文（本地/demo），配置=TLS；sidecar→控制面 dial 走 TLS，复用既有 `secure`/`DialOptions` 钩子。**mTLS / 证书轮换留 M5**。 |
| 健康探针形态 | **专用明文 HTTP 健康口**：两二进制各加独立明文小监听器，只含 `/healthz`（活性）+ `/readyz`（就绪，fail-close 语义），免鉴权、不走 TLS，缺省不起。 |
| 可部署边界 | **硬化现有 docker-compose 为可托管 + 部署 runbook**：健康门控 `depends_on`、非 root 容器、密钥走 env/挂载、TLS 证书可选挂载、迁移沿用 `migrate`。**k8s/helm 留 M5**。 |

## 2. 范围

**纳入**：
- **支柱 1 全链可选 server-TLS**：控制面 4 监听器（admin/sync gRPC + REST/Console HTTP）+ sidecar auth 口可选 server-TLS；sidecar→控制面 sync dial 可选 TLS；配置驱动 cert/key/CA；fail-close 校验。
- **支柱 2 健康探针**：共享 `internal/health` 小包；控制面与 sidecar 各起一个明文 HTTP 健康口；`/healthz` 恒活、`/readyz` 忠实 fail-close 语义。
- **支柱 3 可托管部署**：compose 健康门控、四 Dockerfile 非 root、TLS 证书可选挂载、密钥纪律文档化、`deploy/README` 部署 runbook。

**不纳入（M1.5↔M5 边界，YAGNI 红线）**：
- **mTLS / 客户端证书认证**——HMAC 仍是应用层认证；mTLS 归 M5。
- **证书热轮换 / reload**——M1.5 证书在进程启动时加载，轮换=重启；热轮换归 M5。
- **k8s manifests / helm chart / 零停机 / 备份恢复**——归 M5。
- **Prometheus metrics / tracing / 结构化日志大改**——归 M5。
- **HA（控制面多实例 / relay 去单点 / PG·Redis HA）/ 登录限流防爆破 / 依赖扫描**——归 M5。
- **SDK 新增公开 TLS 帮手 API**——SDK→sidecar TLS 经既有 `WithDialOptions` 注入即可，**公开契约零改动**，不新增 `WithTLS`。

## 3. 方案选型与决策

三个主架构决策（均经 brainstorm 选定，记录裁决理由）：

### 3.1 TLS 终结模型

| | 方案 | 裁决 |
|---|---|---|
| ✅ | **应用原生可选 TLS**（每监听器 serve TLS，缺省明文） | **选**。全链加密、无外部代理依赖、自包含可托管，复用既有 `secure`/`DialOptions` 钩子，最贴合「一致性优先」与路线图「全链 TLS」。 |
| | 反代/Ingress 终结（应用明文，前置 TLS 代理） | 否。内部跳明文、依赖外部组件、与「全托管 SaaS 全链」有差。 |
| | 仅对外边缘 TLS（REST/Console，内部 gRPC 明文） | 否。admin gRPC 与 sync 跳仍明文，隐患留 M5。 |

### 3.2 健康探针暴露

| | 方案 | 裁决 |
|---|---|---|
| ✅ | **专用明文 HTTP 健康口** | **选**。docker/k8s 原生 HTTP 探针零额外依赖；探针免鉴权、不撞 TLS 自签证书；与业务 TLS 口物理隔离，不泄业务面。 |
| | gRPC 健康协议（grpc.health.v1） | 否。compose 需 `grpc_health_probe` 二进制；探针走业务 TLS 口会撞证书/鉴权；REST/Console 这些 HTTP 面不适用。 |
| | 捆现有 HTTP 面（REST/Console） | 否。探针走 TLS+需穿透鉴权；两面可选（不启则无探针）；sidecar 根本无 HTTP 面。 |

### 3.3 可部署边界

| | 方案 | 裁决 |
|---|---|---|
| ✅ | **硬化现有 compose + runbook** | **选**。最小可托管闭环，与路线图「M1.5 最小、M5 完整硬化」边界一致。 |
| | 额外加 k8s/helm | 否。超出「最小」且与 M5 范围重叠，领域蔓延。 |
| | 仅文档 runbook 不动 compose | 否。「可托管」成色打折，健康门控/非 root 无落地。 |

## 4. 支柱 1 — 全链可选 server-TLS

### 4.1 配置面

**控制面 `Config`（`internal/controlplane/app/config.go`）新增**：
- `tls_cert_file` / `TLSCertFile`、`tls_key_file` / `TLSKeyFile`：单证书对，复用于 admin/sync gRPC + REST/Console 四监听器（同进程同主机，SAN 覆盖对外主机名）。
- 校验（fail-close）：两者**同设**=全链 TLS；**同空**=明文；**只设一个**→ `LoadConfig` 返错，启动失败，**绝不静默明文降级**。

**sidecar `Config`（`internal/sidecar/app/config.go`）新增**：
- `tls_cert_file` / `tls_key_file`：serve auth 口（SDK→sidecar）的证书对，校验同上。
- `control_plane_tls` / `ControlPlaneTLS`（bool）：dial 控制面 sync 是否走 TLS。
- `control_plane_ca_file` / `ControlPlaneCAFile`（可选）：信任的 CA；空=系统根证书。

### 4.2 装配面

在 `app.Run` 内**一次性**构造 `*tls.Config`（`tls.LoadX509KeyPair` 失败 → 返错 fail-close）：
- **gRPC 穿入**：`mgmt.NewGRPCServer` / `policysync.NewGRPCServer` / `authz.NewGRPCServer` 三构造各加**变参 `...grpc.ServerOption`**（additive，向后兼容）；TLS 开时 `Run` 传 `grpc.Creds(credentials.NewTLS(serverCfg))`。
- **HTTP 穿入**：REST/Console 监听器在 `Serve` 前包 `tls.NewListener(lis, serverCfg)`，`srv.Serve(lis)` 调用不变。
- **sidecar→CP dial**：TLS 开时 `syncclient.Config{Secure: true, DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(clientCfg))}}`（复用既有钩子，client.go 现有 `if !cfg.Secure { insecure }` 分支天然兼容）。

### 4.3 HMAC × TLS 组合正确性

TLS 开 ⇒ sidecar 侧 `secure=true` ⇒ `perRPC.RequireTransportSecurity()=true`（`credentials.go:61`），HMAC 凭据**拒绝在明文上传输**；TLS 关 ⇒ `secure=false` 维持现状明文。开关须一致由同一 `ControlPlaneTLS` 驱动，避免 secure/transport 错配。SDK→sidecar 路径本就不加 HMAC（`sdk/go/sydom/client.go:36`「本地回环不加 HMAC」），无 secure 交互。

### 4.4 SDK→sidecar TLS

SDK 默认 `insecure`（`client.go:48`）但有 `WithDialOptions(...)` 逃生口（`options.go:14`）。sidecar 开 TLS 时，运营者经 `sydom.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(...)))` 注入即可，**公开 SDK 契约零改动**。sidecar TLS 缺省关，存量 SDK 用户零影响。runbook 文档化该用法。

## 5. 支柱 2 — 健康探针（专用明文 HTTP 健康口）

### 5.1 共享包 `internal/health`

单一职责、可独测：
```go
// Checker 返回 nil 表示就绪；返回 err 表示未就绪（fail-close）。
type Checker func(ctx context.Context) error

// Handler /healthz 恒 200（活性=进程在，不连依赖，避免抖动误重启）;
//         /readyz 跑 ready，nil→200 "ok"，否则 503 "not ready"。
func Handler(ready Checker) http.Handler
```
响应体仅 `ok` / `not ready`，**零业务、零 secret、零内部错误细节**（避免泄露依赖拓扑）。

### 5.2 控制面就绪

`/readyz` checker：DB `PingContext` + Redis `Ping`（各带短超时 context）皆通 → 就绪；任一失败 → 503。

### 5.3 sidecar 就绪（复用唯一 fail-close 条件，不复制逻辑）

执行路径的 fail-close 条件唯一存于 `authz.Authorizer.checkFresh`（`internal/sidecar/authz/authorizer.go:51`：`!fresh.Ready()` → `ErrNotReady`；`MaxStaleness>0` 且超 `LastSyncAt` 阈 → `ErrTooStale`）。

**新增导出方法 `func (a *Authorizer) Ready() error`**，方法体即复用 `checkFresh` 现逻辑（把 `checkFresh` 提为可导出或令 `Ready` 调它）。sidecar `/readyz` checker = `authzr.Ready()`。如此 readiness 与执行拒绝**同源同条件**：sidecar 会因快照未就绪/陈旧而 fail-close 拒绝判定时，`/readyz` 同步报 503，流量被摘除。**绝不另写第二份新鲜度判定**（一致性优先）。

### 5.4 装配与生命周期

两二进制 `Config` 各加 `health_addr` / `HealthAddr`，**缺省空=不起**（向后兼容）。健康口为明文 `http.Server`，随 `Run` 的 `launch` 协程起、随整体 `Shutdown` 优雅关闭（与 REST/Console 同模式）。compose 中显式开启。

## 6. 支柱 3 — 可托管部署（硬化 compose + runbook）

- **健康门控**：cp/sidecar config 补 `health_addr`；`docker-compose.yaml` 给 controlplane/sidecar 加 `healthcheck`（探 `/readyz`，用 `wget`/`curl` 或内建探活）；下游 `depends_on` 从 `service_started` → `service_healthy`（seeder/sidecar 等真正等就绪）。
- **非 root**：四 Dockerfile（controlplane/sidecar/seed/orderservice）补非 root user（如 alpine `adduser -D` + `USER`），二进制 `CGO_ENABLED=0` 静态产物对非 root 友好。
- **TLS 可选挂载**：证书经卷挂载 + 配置项指向挂载路径，env 开关；**默认无证书=明文 demo 路径不变**；补一条「prod-ish」开 TLS 的注释/overlay。
- **密钥纪律**：已走 env（`SYDOM_MASTER_KEY` / `SYDOM_ROOT_SECRET` / `SYDOM_APP_SECRET`），runbook 文档化经 `.env`/secret 文件注入，绝不入镜像。
- **迁移**：沿用 `migrate/migrate` 一次性（已 `service_healthy` 门控 PG），不变。
- **部署 runbook**（`deploy/README.md`）：生证（自签测试步骤 + 真 CA 说明）、配密钥、健康门控起栈、验就绪（curl `/readyz`）、TLS 开/关切换、非 root 说明、fail-close 运维注记。

## 7. 不变量（验收逐条核验，file:line 证据）

- **MO-1 一致性/fail-close**：TLS 配不全（只设 cert 或只设 key）或证书不可读 → `LoadConfig`/`Run` 返错，进程不起，**绝不静默明文**；readiness checker 失败 → `/readyz` 503。
- **MO-2 加密可组合**：TLS 开 ⇒ `secure=true` ⇒ `RequireTransportSecurity()=true`（HMAC 拒明文）；TLS 关 ⇒ 维持现状明文。开关由同一配置驱动，无 secure/transport 错配。
- **MO-3 授权真相零触碰**：不新增 / 不旁路任何鉴权判定；`AuthorizeRule` / `CheckStatusWrite` / `ruleTable` / M1.1 matcher（`internal/controlplane/adminauthz/`）**一字不改**（diff 0 行）；health 口免鉴权但只暴露 liveness/readiness 布尔。
- **MO-4 探针忠实 fail-close**：sidecar `/readyz` 503 ⟺ `authzr.Ready()!=nil` ⟺ 执行路径会 fail-close（同一 `checkFresh`）；CP `/readyz` 503 ⟺ DB/Redis 不可达。
- **MO-5 不泄露**：探针响应体仅 `ok`/`not ready`，无 secret、无业务数据、无内部错误细节 / 依赖拓扑；TLS 私钥走挂载 secret，绝不入镜像 / 日志。
- **MO-6 向后兼容**：cert/key 缺省=明文、`health_addr` 缺省=不起、`control_plane_tls` 缺省=false；现有测试 / demo / SDK 零改可跑（`go test ./...` 全绿）。
- **MO-7 可托管闭环**：compose `depends_on` 走 `service_healthy`、容器非 root、密钥经 env/挂载、runbook 覆盖生证→密钥→起栈→验就绪→TLS 开关全流程。

## 8. 错误处理 / fail-close

- 证书加载失败、cert/key 部分配置、CA 文件不可读 → 启动期返错，进程退出码非零，**不降级明文**。
- 健康 checker 内部任何错误（DB ping 超时、Redis 断、快照陈旧）→ 503，不抛 500、不回显错误细节。
- 健康口本身起不来（端口占用）→ 同其它监听器，触发整体级联关闭（`launch` 的 `defer cancel()`）。

## 9. 测试策略（TDD）

- **配置校验**：cert/key 部分配置 → 返错；CA 不可读 → 返错；同空 → 明文路径；同设 → TLS 路径。
- **TLS 往返**：起 TLS 服务 + 带 CA 客户端 dial → 成功；明文客户端 dial TLS 服务 → 失败（证明非静默降级）。
- **HMAC × TLS 组合**：`secure=true` 下明文传输被拒；`secure=false` 维持明文通。
- **health handler**：`/healthz` 恒 200；`/readyz` checker nil→200、err→503；响应体不含 secret/业务。
- **sidecar `Ready()` 映射 checkFresh**：未就绪→err、超阈→err、新鲜→nil 三态，与 `checkFresh` 同结果。
- **CP readiness**：DB 断→503、Redis 断→503、皆通→200。
- **非 root 镜像**：构建后 `USER` 非 root（构建校验 / 文档断言）。
- **adminauthz diff 0 行**：核验 M1.1 matcher 与 ruleTable 未碰。

## 10. 子项目任务分解（交 writing-plans 细化）

1. `internal/health` 共享包 + handler 测试（/healthz、/readyz、不泄露）。
2. sidecar `Authorizer.Ready()` 导出（复用 checkFresh）+ 三态测试。
3. 控制面 TLS 配置项 + 校验（fail-close 部分配置）+ 装配（gRPC 变参 ServerOption + HTTP `tls.NewListener`）+ 往返测试。
4. sidecar TLS 配置项（serve + dial）+ 装配（syncclient Secure/DialOptions）+ HMAC×TLS 组合测试。
5. 控制面 / sidecar 健康口装配（health_addr，缺省不起，优雅关闭）+ readiness 接线（CP DB/Redis、sidecar Ready）。
6. 四 Dockerfile 非 root + compose 健康门控（healthcheck + service_healthy）+ TLS 可选挂载 + config 补 health_addr。
7. `deploy/README.md` 部署 runbook（生证 / 密钥 / 起栈 / 验就绪 / TLS 开关 / 非 root）。
8. 全仓 `go vet ./...` + `go test ./...` 兜底 + adminauthz diff 0 行核验 + opus 整体安全评审。

## 11. 假设与未决

- **单证书对覆盖控制面 4 监听器**：假设同主机同 SAN 足够；若 admin / 对外面需不同证书，留 M5 拆分。
- **健康口明文**：假设健康口绑内网 / 受 compose 网络隔离；公网暴露健康口非预期用法（探针不含敏感信息，但仍建议内网）。
- **证书轮换=重启**：M1.5 不做热 reload，接受轮换需滚动重启；热轮换归 M5。
- **compose healthcheck 探活工具**：alpine 镜像需含 `wget`（busybox 自带）或改用二进制内建探活子命令；实现时定夺（倾向 busybox `wget -q -O- /readyz`，零额外依赖）。
