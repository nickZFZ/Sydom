# 司域 端到端贯通 + 可运行 demo 设计

> 状态：设计已批准，待写实现计划。日期：2026-06-07。

## 1. 背景与目标

详细设计阶段已完结：①数据库 ②gRPC 契约 ③控制面（3 子）④Sidecar（4 子 + effect 专项）⑤SDK（A/B/C/D 四切片）+ cmd 两二进制，全部实现并入 main。但每个子项目都是**隔离测试**的——整套系统作为一个整体**从未真正一起运行过**。

本子项目要补这一环：**让整套系统端到端贯通，并产出一个真人能在浏览器里看和用的 demo**。一个名为 **demo-shop** 的订单服务把链路串起来：

```
浏览器(真人) → 订单服务(examples/orderservice, 用 SDK A/B/C/D)
            → Sidecar(cmd/sydom-sidecar) ──HMAC──> 控制面(cmd/sydom-controlplane)
            → PostgreSQL + Redis
```

两个交付物共用同一套组件与同一个业务场景：
- **In-process 集成测试**（CI 级贯通证明）：testcontainers 起 PG+Redis，同进程 goroutine 跑 `controlplane/app.Run` + `sidecar/app.Run`，真订单服务 handler 对外，断言全链逻辑。
- **可运行 demo**（compose 栈 + `make demo` 编排器 + 浏览器 UI）：三个二进制 Docker 化，`make demo` 分步拉起并捕获 app_secret，真人开浏览器照 quickstart 即可走通业务流。

**成功的单一权威定义**见 §8 准出门禁。

## 2. 决策记录

经 brainstorming 逐条拍板（每问附 2-3 方案权衡）：

| # | 决策 | 取舍 |
|---|---|---|
| D1 | 贯通测试**两者都要**：in-process testcontainers（CI 硬门禁）+ compose 栈冒烟 | in-process 快/稳/CI 友好做逻辑门禁；compose 做镜像级真栈验证，两者互补 |
| D2 | 示例 app = **带 Web UI 的 HTTP 订单服务**，exercise SDK A/B/C/D | 比最小 CLI 更贴真实、全面展示 SDK；UI 让"给人看和用"成立 |
| D3 | demo 用 **`make demo` 编排器**，app_secret 仅经内存/env 传递、绝不落文件 | 比纯 compose up 多一步编排，但解决 secret 鸡生蛋且守 secret 不落盘铁律 |
| D4 | 供应经**真 `AdminService` gRPC**（非 SQL 播种） | 这是拿到 app_secret 的唯一产品化路径，且顺带端到端验证管理 API |
| D5 | 订单服务**仅 import 公开 `sdk/go/...`**（不碰 internal），自有 `orders` 业务表 | 建模真实外部消费者，守住 SDK 公开边界 |
| D6 | **给人看和用的业务流是硬准出门禁**，用 Playwright 以真人视角验收，全程无卡点 | 用户明确要求；JSON API 不算"给人看"，故必须有可在浏览器走的 UI |
| D7 | Web UI = **服务端渲染 HTML（Go html/template），零前端构建链** | 避免给 Go 仓库引入 npm/SPA；3 个页面 + 友好错误页足够 |

## 3. 架构与组件

### 3.1 组件清单与文件布局

| 路径 | 职责 | 依赖边界 |
|---|---|---|
| `examples/orderservice/app/` | 订单服务核心：handler 工厂 `New(cfg) (http.Handler, error)`、Web UI 模板、`orders` 业务表建表/播种、启动时经 SDK `Registry` 注册并上报权限点（D） | **只** import `sdk/go/{sydom,sydomhttp,sydomsql}` + 标准库 + pq 驱动 |
| `examples/orderservice/main.go` | 薄 `os.Exit`/进程入口，读 env/flag，调 `app.New` 起 `http.Server` | 同上 |
| `examples/seed/` | 供应 seeder：以 root 身份用 `admin/v1` gRPC 客户端 `CreateApplication`（捕获 app_secret）+ 建角色/权限/授权/用户绑定/数据策略 | 可 import `internal/auth`（admin HMAC 凭据）+ `gen/sydom/admin/v1`——它是 ops 工具非外部 SDK 消费者 |
| `deploy/Dockerfile.controlplane` `.sidecar` `.orderservice` | 三个多阶段构建镜像（golang build → distroless/alpine 运行） | — |
| `deploy/docker-compose.yaml` | 服务：postgres、redis、migrate（一次性，用官方 `migrate/migrate` 镜像挂 `db/migrations`）、controlplane、sidecar、orderservice、seeder（`profiles: [tools]`，由编排器 `compose run` 拉起） | — |
| `deploy/demo.sh` | `make demo` 的编排脚本：分步拉起 + 就绪等待 + 捕获 secret 注入 sidecar（见 §3.3） | — |
| `deploy/.env.demo` | **仅 demo 占位**的非敏感配置 + 显著标注的 demo master key / root secret | 标注"生产另行注入" |
| `Makefile` 新增 `demo` / `demo-down` / `smoke` 目标 | 入口 | — |
| `test/e2e/e2e_test.go` | in-process testcontainers 贯通测试（G1），import `controlplane/app`、`sidecar/app`、`examples/orderservice/app`、`gen/sydom/admin/v1`、SDK | 模块内测试文件，允许 import internal |
| `test/e2e/browser/WALKTHROUGH.md` | Playwright 人用流走查脚本/步骤（截图留档），G2 验收依据 | — |
| `README.md` 扩写 + `docs/.../quickstart`（或并入 README） | 架构图 + 三步 quickstart | — |

> 订单服务拆 `app` 包（handler 工厂）+ 薄 `main.go`，使 in-process 测试能直接挂载 handler、无需起子进程（镜像 main.go 自然复用同一 `app` 包）。

### 3.2 订单服务（examples/orderservice）行为

- **配置**（env/flag）：`SIDECAR_ADDR`（sidecar 本地 AuthService 地址）、`DATABASE_DSN`（订单服务自有库，demo 复用同一 PG）、`LISTEN_ADDR`、`DOMAIN`（仅作展示，域由 Sidecar pin）。
- **启动序**：① 连自有 DB，建 `orders` 表 if not exists + 播种几行（跨部门）；② `sydom.New(SIDECAR_ADDR)` 连 Sidecar；③ 建 `sydom.Registry`，注册 3 个权限点，`Report` 上报（D，**fail-soft**：失败记日志不阻塞启动）；④ 装路由。
- **身份（demo 简化）**：司域只管授权不管认证。落地页给「以 alice 进入」「以 bob 进入」，选择写 cookie（user + department）；中间件 resolver 从 cookie 取 subject 与 department。**诚实呈现**：README 点明"真实系统的身份认证在业务侧/网关，司域只接收 subject"。
- **路由**：
  - `GET /` 落地页（选用户）。
  - `GET /login?user=alice`（或 POST）：set cookie，302 到 `/orders`。`GET /logout` 清 cookie。
  - `GET /orders`：sydomhttp 中间件 Check `order:read`（A+B）→ handler 调 `client.FilterSQL(subject, "order", {department})`→`sydomsql` 注入行级 WHERE（C）→ 渲染订单表（含「删除」按钮）。
  - `POST /orders/{id}/delete`：中间件 Check `order:delete` → 删行 → 302 回 `/orders`；无权时渲染**友好 403 页**。
  - （可选）`POST /orders`：Check `order:write` 创建。
- **resolver**（sydomhttp 注入点）：`(method,path) → (object,action)`：`GET /orders`→`(order,read)`、`POST /orders/{id}/delete`→`(order,delete)`、`POST /orders`→`(order,write)`；subject 取自 cookie。落地页/login/静态资源经 `ErrSkipAuth` 放行。
- **错误呈现（无卡点的含义）**：无权→友好 403 页（非堆栈）；`ErrUnavailable`（Sidecar 未就绪）→友好 503 页（提示稍后重试）；默认 **fail-close**，demo 不开 fail-open（演示底线）。

### 3.3 demo 编排（deploy/demo.sh，解 secret 鸡生蛋）

1. `docker compose up -d postgres redis`，等 PG healthy。
2. `docker compose run --rm migrate`（官方 migrate 镜像跑 `db/migrations`）。
3. `docker compose up -d controlplane`（env：demo master key、root principal/secret），等 admin 端口就绪。
4. `APP_SECRET=$(docker compose run --rm seeder)`：seeder 以 root 调 AdminService 建 app + 全套授权/数据策略，**只把 app_secret 明文打到 stdout**（日志走 stderr），编排器捕获到 shell 变量。
5. `SYDOM_APP_SECRET=$APP_SECRET docker compose up -d sidecar orderservice`，等 sidecar bootstrap + 订单服务就绪。
6. 打印「demo 就绪：开浏览器访问 http://localhost:8080」。

secret 全程 stdout→shell 变量→env，**绝不落文件**。`make demo-down` = `docker compose down -v`（清容器+卷）。

## 4. Demo 业务场景（seeder 配置）

- **App**：tenant=`demo`，domain=`shop`，app_key=`demo-shop`。
- **功能权限点（app 自声明目录，订单服务启动时经 SDK 注册并上报）**：`order:read` / `order:write` / `order:delete` / `order:export`。
  - 关键事实（已回源核实 `store.UpsertPermission` / `UpsertAutoPermission` / migration 000004）：`permission.source` 默认 `'manual'`；admin 路径 `UpsertPermission` 建行为 `manual` 且冲突时**不改 source**；auto 上报 `UpsertAutoPermission` 仅写/更 `source='auto'` 行、**跳过 manual 行**。
  - 因 `GrantPermission` 要求权限点先存在，**被授权的 read/write/delete 由 seeder 经 `UpsertPermission` 预建为 `source='manual'`**；app 启动 auto 上报同 code → 命中 manual → **Skipped（不覆盖人工配置，§8 头条不变量）**。
  - `order:export` 是 app 声明但**尚未授权/未接路由**的能力点（真实场景常见）：admin 未预建 → auto 上报落 `source='auto'`，演示**新增 auto 插入**。
- **角色与功能授权**：`manager`→read+write+delete（allow）；`clerk`→read（allow）。export 不授权（仅目录声明）。
- **用户绑定**：`alice`→manager；`bob`→clerk（bob.department=`shanghai`）。
- **数据策略（行级，resource=order）**：
  - `clerk`：allow `{"field":"dept","op":"EQ","value":"$user.department"}` → 只见本部门订单。
  - `manager`：allow `{"field":"dept","op":"IN","value":["shanghai","beijing"]}` → 覆盖全部播种部门=看全部。
    > 注：因 `order` 资源已被 clerk 配置，按 dataperm 语义"配了但无 allow 命中→deny-all"，manager 必须显式给 allow 才看得到行；用 IN 覆盖全部播种部门实现"看全部"（demo 取舍，已注释）。
- **订单服务自有 `orders` 表播种**：若干行跨 `shanghai`/`beijing` 两部门（如各 3 行）。

## 5. 数据流与断言（贯通要证明的事）

1. **功能权限（A+B）**：alice 删订单成功；bob 删订单被拒。
2. **数据权限（C）**：bob 列表只见 `shanghai` 行；alice 见全部；**deny-all 负向**：一个有 `order:read` 功能权限但无任何 allow 数据策略的主体 → 列表 0 行（绝不退化为全表）。
3. **权限点上报（D）**：订单服务启动后——(a) admin 预建并授权的 `order:read/write/delete` 仍为 `source='manual'`、name/resource 未被 auto 上报覆盖（auto 绝不覆盖人工配置）；(b) app 自声明的 `order:export` 落为 `source='auto'`（新增 auto 插入贯通）。
4. **实时同步贯通**：revoke alice 的 `order:delete` 授权 → sidecar 经 PolicySync 同步、版本推进 → alice 删订单由"成功"翻为"被拒"。
5. **就绪前 fail-close**：sidecar bootstrap 完成前，业务请求得 503（`ErrUnavailable`），绝不放行。

## 6. 错误处理 / fail-close / secret

- 订单服务用 SDK 默认 **fail-close**；中间件对 `ErrUnavailable` 默认 503 友好页（demo 不开 fail-open）；硬错误 500 友好页 + 记日志。
- 编排器每步带**就绪等待**（端口可连、CP admin 可达、sidecar `Ready`、订单服务 200），**禁定长 sleep**。
- secret（app_secret / master key / root secret）只经 env 传递；demo 占位值在 `deploy/.env.demo` 且显著标注"生产另行注入"。

## 7. 测试策略

- **G1 in-process（`test/e2e`，需 Docker）**：testcontainers 起 PG+Redis；跑 sydom migrations；起 CP `app.Run`（随机端口、demo master key、`EnsureRootOperator`）；seeder 逻辑经 AdminService 建 app+授权+数据策略并捕获 secret；起 sidecar `app.Run`（捕获的 secret、domain=shop、app_key=demo-shop、连 CP sync 端口）；挂 `orderservice/app` handler（连 sidecar auth 端口）；以 alice/bob cookie 驱动 HTTP，断言 §5 全部五条；实时同步用**轮询**（非 sleep）等版本推进。
- **G2 浏览器人用流（Playwright，需 compose 栈）**：`make demo` 起栈后，用 Playwright/superpowers-chrome MCP 以真人视角走 §8-G2 三步，截图留档；`test/e2e/browser/WALKTHROUGH.md` 记可重复步骤；README quickstart 同款步骤供真人照走。
- **非 flaky**：贯通测试 `-count=2` 稳定；沿用既有教训——后台投递 + 轮询，禁非测试 goroutine 内 `require.*`。

## 8. 准出门禁（成功标准）

**权威定义：贯通"通过" ≡ G0 + G1 + G3 全绿（全自动、CI 可门禁）+ G2 人用业务流无卡点（Playwright 真人视角验收）。** G2 因依赖镜像构建相对慢，但按用户要求是**硬门禁**，不得卡点。

### G0 — 环境卫生（硬，不需 Docker）
- `gofmt -l` / `go vet ./...` / `go build ./...` / `make proto-check` 全干净。`go vet ./...` 为强制项（编译所有测试文件，杜绝跨包签名漂移——D 切片教训）。
- `go list -deps ./examples/orderservice/...` 依赖图**不含任何 `internal/`**（机器可断言，证明订单服务是合法外部消费者）。

### G1 — In-process 贯通测试（硬，CI 准出，需 Docker）
一个 `test/e2e` 测试全绿，断言 §5 五条（功能权限 allow/deny、数据权限行级过滤 + **deny-all 0 行负向**、权限点上报 **auto 不覆盖 manual（read/write/delete 仍 manual）+ order:export 落 auto**、revoke 实时翻转、就绪前 503 fail-close）。

### G2 — 人用业务流（硬，Playwright 真人视角，需 compose 栈）
1. 开 `/` →「以 alice 进入」→ `/orders` 见**全部**订单 → 点删除**成功**。
2. 切「以 bob 进入」→ `/orders` **只见 shanghai** 订单 → 点删除 → **友好 403 页**（非堆栈）。
3. 全程无 5xx 异常页、无空白页、无需手工补参数；一个不懂内部的人照 quickstart 三步（`make demo` → 开浏览器 → 照着点）即可跑通。

### G3 — 不变量守门（硬）
- secret 全程只经 env：脚本化 grep demo 产物与日志**无明文 secret 落盘**。
- 贯通测试 `-count=2` 稳定、非 flaky。

## 9. 范围与非目标（YAGNI）

- **不做**生产可运维性（metrics / health 探针 / TLS）——属"生产可运维性"独立方向。
- **不改**产品 API（不给 `CreateApplication` 加传入 secret 字段；secret 仍服务端生成一次性返回）。
- **不上** k8s 清单与 CI 流水线接入（compose 足够；CI 化留后续）。
- **不引** npm/前端构建链；UI 仅服务端模板 + 上述页面 + 友好错误页，无样式框架/登录系统/分页。
- **不做** 自动化浏览器回归测试的 CI 固化（G2 由 Playwright MCP 走查 + 文档化步骤承载；committed 浏览器自动化测试列为后续可选）。
- 订单服务身份用"选人"代替真实认证（司域不管认证）。

## 10. 关键风险与缓解

| 风险 | 缓解 |
|---|---|
| 跨包/跨测试编译漂移（D 切片踩过） | G0 强制 `go vet ./...` 编译全部测试文件 |
| 时序 flaky（bufconn/PolicySync/Redis pub-sub，③-2 踩过） | 就绪轮询替代定长 sleep；实时同步断言轮询版本；`-count=2` 守门 |
| secret 鸡生蛋 / 落盘 | 编排器 stdout→env 注入；G3 grep 守门 |
| dataperm "配了即 deny-all" 误配致 manager 看不到行 | §4 已给 manager 显式 allow（IN 全部门）；G1 精确断言行数 |
| 订单服务误依赖 internal 破坏公开边界 | G0 `go list -deps` 断言无 internal |
| compose 镜像构建慢/环境差异 | G2 为镜像级，G0/G1/G3 提供快速自动门禁；demo master key/root secret 用占位值 |

## 11. 自检

- **占位符扫描**：无 TODO/待定；"可选 `POST /orders` 创建"与"committed 浏览器自动化测试"均明确标为可选/后续，非占位。
- **内部一致性**：§4 场景与 §5 断言、§8 门禁逐条对应；dataperm 语义经源码核实（`filter.go buildPlan`：未配置→MatchAll、配了无 allow→MatchNone）已落实到 manager 需显式 allow。
- **范围检查**：聚焦"贯通 + demo"，可由一个实现计划覆盖（订单服务 → seeder → 部署/编排 → in-process 测试 → 浏览器走查 → 文档）。
- **模糊性检查**：身份处理（选人非认证）、manager 数据策略（IN 全部门）、secret 传递（env 不落盘）、G2 验收方式（Playwright MCP + 文档步骤）均已明确单一口径。
