# M5.3a 配置管理硬化 + 生产模式 fail-close 设计规格

**里程碑：** M5.3 部署硬化 · 首片（M5.3a）
**日期：** 2026-07-11
**BASE：** `main` @ `b8e4a0f`（M5.2b mTLS 完结之后）

---

## 1. 目标与范围

给控制面/边车两进程的**配置装载**加一层部署硬化，围绕两件事：

1. **生产模式 fail-close** —— 显式 `environment` 信号；`production` 下若传输未启用 TLS 即拒绝启动（绝不静默明文降级）。
2. **文件式密钥加载** —— 三个敏感密钥支持 `SYDOM_X_FILE` 约定（k8s/Docker secret 挂载惯例），使密钥不必经环境变量传递（`/proc/<pid>/environ` 不泄露）。

**明确不在本片范围**（留 M5.3 其它切片）：容器镜像/运行时硬化、K8s/Helm 清单、迁移自动化+零停机、备份恢复。

**零触碰铁律：** 仅动 `internal/deploycfg`（新建）、`internal/controlplane/app/config.go`、`internal/sidecar/app/config.go` 及其测试、两处 `config.example.yaml`。`casbin/`、`adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`、`internal/auth/`、`internal/obs/`、`internal/tlsconfig/` 内容 diff 必须为 0。

---

## 2. 架构

新增一个小的**共享内部包 `internal/deploycfg`**（单一真相源、独立可测；沿用 `tlsconfig`/`health`/`secheaders`/`certtest` 的共享内部包模式）。它提供两个纯原语，被控制面与边车 `LoadConfig` 各自消费；**进程特定**的 TLS fail-close 检查因字段不同留在各自 `config.go`。

```
internal/deploycfg/           ← 新建：部署配置硬化原语（无状态、无全局态、全离线可测）
  deploycfg.go                  Environment 枚举 + ParseEnvironment + ResolveSecret
  deploycfg_test.go             两原语各分支单测

internal/controlplane/app/config.go   ← 改：environment 装载 + _FILE 密钥解析 + 生产 TLS 硬校验
internal/sidecar/app/config.go        ← 改：同构（生产需 ControlPlaneTLS）
cmd/sydom-controlplane/config.example.yaml  ← 改：补 environment + _FILE 说明
cmd/sydom-sidecar/config.example.yaml       ← 改：同上
```

---

## 3. `internal/deploycfg` 接口

```go
package deploycfg

// Environment 是进程运行环境。零值 = Development（向后兼容默认）。
type Environment int

const (
	Development Environment = iota
	Production
)

func (e Environment) IsProduction() bool { return e == Production }
func (e Environment) String() string     // "development" / "production"

// ParseEnvironment 解析环境字符串（大小写敏感，取自 yaml/env）：
//   ""            → Development（未设=向后兼容默认）
//   "development" → Development
//   "production"  → Production
//   其它任意值     → 错误（fail-close：拼写错误如 "prod"/"prd" 绝不静默降级为 dev）
func ParseEnvironment(s string) (Environment, error)

// ResolveSecret 从环境变量 name 或其 name+"_FILE" 变体解析一个密钥值：
//   仅 name 设            → 返回 getenv(name)（今天的行为，逐字节不变）
//   仅 name+"_FILE" 设     → 读该路径文件，去尾部空白（\r\n\t 及空格），返回内容
//   两者同设              → 错误（歧义 fail-close：拒绝两个来源同时给值）
//   皆空                  → 返回 ""（调用方按既有必填校验处理）
// getenv 注入便于测试；文件经 os.ReadFile 读取（测试写真实临时文件）。
func ResolveSecret(getenv func(string) string, name string) (string, error)
```

**去尾部空白而非全部空白的理由：** 仅裁剪尾部（`strings.TrimRight(s, " \t\r\n")`），保守——不动密钥前导字节或内部内容；只消除 `echo secret > file` / 编辑器补的尾换行这一最常见 footgun。base64 主密钥尾换行会破坏解码，此裁剪正好覆盖。

---

## 4. 行为契约（精确 fail-close 矩阵）

### 4.1 环境信号

新增 yaml `environment`，可被 `SYDOM_ENVIRONMENT` env 覆盖（`firstNonEmpty(getenv("SYDOM_ENVIRONMENT"), fc.Environment)`，与 `database_dsn`/`redis_addr` 的 env 覆盖一致）。解析经 `deploycfg.ParseEnvironment`：

| 输入 | 结果 |
|---|---|
| 空 / 未设 | `development`（现有配置零改，行为与今天逐字节一致） |
| `development` | development |
| `production` | production（启用 §4.2 硬校验） |
| 其它任意值 | **报错拒启动**（fail-close） |

解析出的 `Environment` 存入 `Config.Environment` 字段（供 §4.2 检查 + `Run` 启动日志 `environment=...`）。

### 4.2 生产 fail-close（唯一检查 = 传输 TLS）

在各自 `LoadConfig` 末尾、既有必填校验之后：

- **控制面**：`env.IsProduction() && (TLSCertFile == "" || TLSKeyFile == "")` → 报错拒启动（admin/REST/Console/sync 皆不得走明文）。
- **边车**：`env.IsProduction() && !ControlPlaneTLS` → 报错拒启动（dial 控制面不得走明文）。
- development 下两检查都不生效（行为与今天一致）。

> 注：不校验 mTLS（`SyncClientCAFile`/客户端证书）——那是 M5.2b 的 opt-in 纵深，非生产基线要求。不校验安全 Cookie / 密钥强度 / 边车 serve TLS（本片刻意收窄，见 §1）。

### 4.3 文件式密钥 `_FILE`

三密钥经 `deploycfg.ResolveSecret` 解析（替换现有裸 `getenv`）：

| 密钥 env | 进程 |
|---|---|
| `SYDOM_MASTER_KEY` / `SYDOM_MASTER_KEY_FILE` | 控制面（base64 → 32 字节） |
| `SYDOM_ROOT_SECRET` / `SYDOM_ROOT_SECRET_FILE` | 控制面（原始字节） |
| `SYDOM_APP_SECRET` / `SYDOM_APP_SECRET_FILE` | 边车（原始字节） |

解析后既有的必填/尺寸校验不变（MasterKey 解码须 32 字节、RootSecret 非空、AppSecret 非空）。`_FILE` 与裸 env 同设 → 报错。文件不可读 → 报错（fail-close）。

---

## 5. 测试策略（TDD，全离线）

- **`internal/deploycfg` 单测**：
  - `ParseEnvironment`：空→Development、`development`→Development、`production`→Production、未知值→err。
  - `ResolveSecret`：仅 env→值；仅 `_FILE`→文件内容；`_FILE` 尾换行→被 trim；两者同设→err；皆空→""；`_FILE` 指向不存在文件→err。
- **控制面 `config_test`**：`environment` 装载（含 env 覆盖 + 未知值 err）；prod 无 TLS→err、prod 有 TLS→ok、dev 无 TLS→ok（向后兼容）；三态密钥（`MASTER_KEY_FILE`/`ROOT_SECRET_FILE` 装载、与 env 冲突→err）。
- **边车 `config_test`**：`environment` 装载；prod 无 `ControlPlaneTLS`→err、prod 有→ok、dev 无→ok；`APP_SECRET_FILE` 装载、冲突→err。
- 每个 fail-close 分支先写失败测试（红），再落实现（绿）。

---

## 6. 验收关卡（M53A-1..7）

- **M53A-1 零触碰授权核心**：`git diff --numstat <BASE>..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/sidecar/authz/ internal/auth/ internal/obs/ internal/tlsconfig/` = 空。
- **M53A-2 向后兼容**：既有 config 测试全绿；environment 默认 development、`_FILE` 纯加法 → 现有 yaml/demo compose 零改仍可启动。
- **M53A-3 环境解析**：四类值行为如 §4.1，未知值 fail-close 报错（有齿：拼写 `prod` 不静默降级）。
- **M53A-4 生产 TLS fail-close**：控制面 prod 无 TLS→err、边车 prod 无 ControlPlaneTLS→err；对应 dev/有 TLS→ok（有齿：撤掉检查则 prod 无 TLS 测试转 FAIL）。
- **M53A-5 `_FILE` 密钥**：三密钥各自 file 装载 + trim + 冲突报错。
- **M53A-6 全量回归**：`go test ./...` EXIT 0 全绿。
- **M53A-7 文档**：两 `config.example.yaml` 补 `environment` 与 `_FILE` 用法说明（commented 示例，风格一致）。

---

## 7. 不变量总述

1. **零触碰授权核心**（M53A-1）。
2. **向后兼容加法**：`environment` 默认 dev、`_FILE` 与生产检查皆 opt-in；所有新增皆空 ⟹ 装配行为与 BASE 逐字节一致。
3. **fail-close 一致性**：未知环境值、`_FILE`/env 冲突、生产缺 TLS、文件不可读——一律返错拒启动，绝不静默降级（呼应项目「一致性优先/fail-close」传统）。
4. **单一真相源**：环境解析与密钥来源解析只在 `internal/deploycfg` 定义一处，两进程共享。
