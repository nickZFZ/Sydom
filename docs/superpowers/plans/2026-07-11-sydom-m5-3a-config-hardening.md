# M5.3a 配置管理硬化 + 生产模式 fail-close 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面/边车配置装载加部署硬化——显式 `environment` 信号 + 生产模式传输 TLS fail-close + 三密钥 `_FILE` 文件式加载。

**架构：** 新增无状态共享包 `internal/deploycfg`（`Environment` 解析 + `ResolveSecret` env/`_FILE` 二选一），被两进程 `LoadConfig` 消费；进程特定的生产 TLS 硬校验留在各自 `config.go`。三项全 opt-in 加法，皆空 ⟹ 行为与 BASE 逐字节一致。

**技术栈：** Go、`crypto/x509`（无）、标准库 `os`/`strings`、testify、yaml.v3。

**BASE：** `main` @ `b8e4a0f`（M5.2b 之后）；规格 `docs/superpowers/specs/2026-07-11-sydom-m5-3a-config-hardening-design.md`。

**零触碰铁律：** 仅动 `internal/deploycfg`(新)、`internal/controlplane/app/config.go`、`internal/sidecar/app/config.go` 及其 `_test.go`、两 `config.example.yaml`。`casbin/`、`adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`、`internal/auth/`、`internal/obs/`、`internal/tlsconfig/` 内容 diff 必须为 0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/deploycfg/deploycfg.go`（创建） | `Environment` 枚举 + `ParseEnvironment` + `ResolveSecret`（env/`_FILE`）。 |
| `internal/deploycfg/deploycfg_test.go`（创建） | 两原语各分支单测（全离线，真实临时文件）。 |
| `internal/controlplane/app/config.go`（修改） | `Config.Environment` 字段 + yaml `environment` + LoadConfig 装 environment/`_FILE` 密钥/生产 TLS 硬校验。 |
| `internal/controlplane/app/config_test.go`（修改） | environment 装载/env 覆盖/未知值 err、生产需 TLS、master/root `_FILE` + 冲突。 |
| `internal/sidecar/app/config.go`（修改） | 同构（生产需 `ControlPlaneTLS`；`APP_SECRET` `_FILE`）。 |
| `internal/sidecar/app/config_test.go`（修改） | environment、生产需 ControlPlaneTLS、`APP_SECRET_FILE` + 冲突。 |
| `cmd/sydom-controlplane/config.example.yaml`（修改） | 补 `environment` + `_FILE` 说明。 |
| `cmd/sydom-sidecar/config.example.yaml`（修改） | 同上。 |

---

## 任务 1：`internal/deploycfg` 共享包

**文件：**
- 创建：`internal/deploycfg/deploycfg.go`
- 创建：`internal/deploycfg/deploycfg_test.go`

- [ ] **步骤 1：编写失败的测试** — `internal/deploycfg/deploycfg_test.go`：

```go
package deploycfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nickZFZ/Sydom/internal/deploycfg"
)

func TestParseEnvironment(t *testing.T) {
	cases := []struct {
		in      string
		want    deploycfg.Environment
		wantErr bool
	}{
		{"", deploycfg.Development, false},
		{"development", deploycfg.Development, false},
		{"production", deploycfg.Production, false},
		{"prod", deploycfg.Development, true},       // 拼写错误 fail-close
		{"PRODUCTION", deploycfg.Development, true},  // 大小写敏感
	}
	for _, c := range cases {
		got, err := deploycfg.ParseEnvironment(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseEnvironment(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseEnvironment(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseEnvironment(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEnvironmentIsProduction(t *testing.T) {
	if deploycfg.Development.IsProduction() {
		t.Fatal("Development.IsProduction() 应为 false")
	}
	if !deploycfg.Production.IsProduction() {
		t.Fatal("Production.IsProduction() 应为 true")
	}
}

func TestResolveSecret_EnvOnly(t *testing.T) {
	getenv := func(k string) string {
		if k == "SYDOM_X" {
			return "env-value"
		}
		return ""
	}
	got, err := deploycfg.ResolveSecret(getenv, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "env-value" {
		t.Fatalf("want env-value, got %q", got)
	}
}

func TestResolveSecret_FileOnlyTrimsTrailing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "SYDOM_X_FILE" {
			return p
		}
		return ""
	}
	got, err := deploycfg.ResolveSecret(getenv, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-value" {
		t.Fatalf("want file-value（尾换行应被 trim）, got %q", got)
	}
}

func TestResolveSecret_BothSetFailsClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("file-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "SYDOM_X":
			return "env-value"
		case "SYDOM_X_FILE":
			return p
		}
		return ""
	}
	if _, err := deploycfg.ResolveSecret(getenv, "SYDOM_X"); err == nil {
		t.Fatal("env 与 _FILE 同设应报错")
	}
}

func TestResolveSecret_NeitherSetReturnsEmpty(t *testing.T) {
	got, err := deploycfg.ResolveSecret(func(string) string { return "" }, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("皆空应返回空串, got %q", got)
	}
}

func TestResolveSecret_UnreadableFileFailsClose(t *testing.T) {
	getenv := func(k string) string {
		if k == "SYDOM_X_FILE" {
			return "/no/such/secret/file"
		}
		return ""
	}
	if _, err := deploycfg.ResolveSecret(getenv, "SYDOM_X"); err == nil {
		t.Fatal("文件不可读应报错")
	}
}
```

- [ ] **步骤 2：运行验证失败** — `go test ./internal/deploycfg/ -v`，预期编译失败（`undefined: deploycfg.*`）。

- [ ] **步骤 3：编写实现** — `internal/deploycfg/deploycfg.go`：

```go
// Package deploycfg 提供部署配置硬化原语：运行环境解析与密钥来源（env 或 _FILE 文件）解析。
// 无状态、无全局态、全离线可测；由控制面/边车 LoadConfig 共享（单一真相源）。
package deploycfg

import (
	"fmt"
	"os"
	"strings"
)

// Environment 是进程运行环境。零值 = Development（向后兼容默认）。
type Environment int

const (
	Development Environment = iota
	Production
)

// IsProduction 报告是否生产环境（触发 fail-close 硬校验）。
func (e Environment) IsProduction() bool { return e == Production }

// String 返回环境的规范名。
func (e Environment) String() string {
	if e == Production {
		return "production"
	}
	return "development"
}

// ParseEnvironment 解析环境字符串（取自 yaml/env，大小写敏感）：
//
//	""            → Development（未设=向后兼容默认）
//	"development" → Development
//	"production"  → Production
//	其它           → 错误（fail-close：拼写错误如 "prod"/"prd" 绝不静默降级为 dev）
func ParseEnvironment(s string) (Environment, error) {
	switch s {
	case "", "development":
		return Development, nil
	case "production":
		return Production, nil
	default:
		return Development, fmt.Errorf("deploycfg: 无法识别的 environment %q（仅接受 development/production）", s)
	}
}

// ResolveSecret 从环境变量 name 或其 name+"_FILE" 变体解析一个密钥值：
//
//	仅 name 设            → 返回 getenv(name)（今天的行为，逐字节不变）
//	仅 name+"_FILE" 设     → 读该路径文件，去尾部空白，返回内容
//	两者同设              → 错误（歧义 fail-close）
//	皆空                  → 返回 ""（调用方按既有必填校验处理）
//
// getenv 注入便于测试；文件经 os.ReadFile 读取。
func ResolveSecret(getenv func(string) string, name string) (string, error) {
	val := getenv(name)
	fileVal := getenv(name + "_FILE")
	if val != "" && fileVal != "" {
		return "", fmt.Errorf("deploycfg: %s 与 %s_FILE 不可同设（歧义）", name, name)
	}
	if fileVal == "" {
		return val, nil
	}
	b, err := os.ReadFile(fileVal)
	if err != nil {
		return "", fmt.Errorf("deploycfg: 读取 %s_FILE 失败: %w", name, err)
	}
	return strings.TrimRight(string(b), " \t\r\n"), nil
}
```

- [ ] **步骤 4：运行验证通过** — `go test ./internal/deploycfg/ -v`，预期全 PASS；`go build ./...`；`gofmt -l internal/deploycfg/`（应无输出）。

- [ ] **步骤 5：Commit**

```bash
git add internal/deploycfg/
git commit -m "feat(deploycfg): M5.3a 部署配置硬化原语 Environment 解析(未知值 fail-close)+ResolveSecret(env/_FILE 二选一,冲突报错,去尾部空白);共享无状态包"
```

---

## 任务 2：控制面 config.go 接线

**文件：**
- 修改：`internal/controlplane/app/config.go`
- 测试：`internal/controlplane/app/config_test.go`

已存在助手：`writeConfig(t, body)`、`envFunc(m)`、`validEnv()`（返回含 `SYDOM_MASTER_KEY`(base64 32 字节)+`SYDOM_ROOT_SECRET` 的 map）、`fullYAML` const（含全部必填项，无 TLS、无 environment）。import 已含 `encoding/base64`、`os`、`path/filepath`、`testing`、`time`、`app`、`crypto`、`require`。

- [ ] **步骤 1：编写失败的测试** — 在 `internal/controlplane/app/config_test.go` 追加：

```go
func TestLoadConfig_EnvironmentDefaultsDevelopment(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.False(t, cfg.Environment.IsProduction())
}

func TestLoadConfig_EnvironmentUnknownFailsClose(t *testing.T) {
	_, err := app.LoadConfig(writeConfig(t, fullYAML+"environment: prod\n"), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_ProductionRequiresTLS(t *testing.T) {
	// 生产但无 TLS → 报错
	_, err := app.LoadConfig(writeConfig(t, fullYAML+"environment: production\n"), envFunc(validEnv()))
	require.Error(t, err)
	// 生产 + TLS → ok（LoadConfig 不读证书文件本身，仅校验路径非空）
	okYAML := fullYAML + "environment: production\ntls_cert_file: /x/cert.pem\ntls_key_file: /x/key.pem\n"
	cfg, err := app.LoadConfig(writeConfig(t, okYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.True(t, cfg.Environment.IsProduction())
	// dev 无 TLS → ok（向后兼容）
	_, err = app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
}

func TestLoadConfig_EnvironmentEnvOverride(t *testing.T) {
	env := validEnv()
	env["SYDOM_ENVIRONMENT"] = "production" // env 覆盖 yaml，且触发生产硬校验
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err, "env 覆盖为 production 且无 TLS 应报错（证明覆盖生效）")
}

func TestLoadConfig_MasterKeyFromFile(t *testing.T) {
	env := validEnv()
	b64 := env["SYDOM_MASTER_KEY"]
	delete(env, "SYDOM_MASTER_KEY")
	p := filepath.Join(t.TempDir(), "mk")
	require.NoError(t, os.WriteFile(p, []byte(b64+"\n"), 0o600)) // 尾换行应被 trim 后 base64 解码成功
	env["SYDOM_MASTER_KEY_FILE"] = p
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Len(t, cfg.MasterKey, crypto.KeySize)
}

func TestLoadConfig_RootSecretFromFileAndConflict(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_ROOT_SECRET")
	p := filepath.Join(t.TempDir(), "rs")
	require.NoError(t, os.WriteFile(p, []byte("file-root-secret\n"), 0o600))
	env["SYDOM_ROOT_SECRET_FILE"] = p
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, []byte("file-root-secret"), cfg.RootSecret)
	// env + file 同设 → 报错
	env["SYDOM_ROOT_SECRET"] = "env-root-secret"
	_, err = app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行验证失败** — `go test ./internal/controlplane/app/ -run 'TestLoadConfig_Environment|TestLoadConfig_ProductionRequiresTLS|TestLoadConfig_MasterKeyFromFile|TestLoadConfig_RootSecretFromFileAndConflict' -v`，预期编译失败（`cfg.Environment` 未定义）。

- [ ] **步骤 3：编写实现** — `internal/controlplane/app/config.go`：

(a) import 块补：
```go
	"github.com/nickZFZ/Sydom/internal/deploycfg"
```

(b) `Config` 结构在 `RelayPollInterval time.Duration` 后加：
```go
	Environment deploycfg.Environment // development（默认）/ production；production 下传输 TLS 缺失即拒启动
```

(c) `fileConfig` 结构在 `RelayPollInterval string ...` 后加：
```go
	Environment string `yaml:"environment"`
```

(d) `LoadConfig` 中，在 `cfg := Config{...}` 字面量与三个 `parseDurationDefault` 之后、`mk, err := base64...` 之前，插入环境解析：
```go
	if cfg.Environment, err = deploycfg.ParseEnvironment(firstNonEmpty(getenv("SYDOM_ENVIRONMENT"), fc.Environment)); err != nil {
		return Config{}, fmt.Errorf("environment: %w", err)
	}
```

(e) 把原来的：
```go
	mk, err := base64.StdEncoding.DecodeString(getenv("SYDOM_MASTER_KEY"))
	if err != nil {
		return Config{}, fmt.Errorf("decode SYDOM_MASTER_KEY: %w", err)
	}
	cfg.MasterKey = mk
	cfg.RootSecret = []byte(getenv("SYDOM_ROOT_SECRET"))
```
改为：
```go
	masterKeyB64, err := deploycfg.ResolveSecret(getenv, "SYDOM_MASTER_KEY")
	if err != nil {
		return Config{}, err
	}
	mk, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		return Config{}, fmt.Errorf("decode SYDOM_MASTER_KEY: %w", err)
	}
	cfg.MasterKey = mk
	rootSecret, err := deploycfg.ResolveSecret(getenv, "SYDOM_ROOT_SECRET")
	if err != nil {
		return Config{}, err
	}
	cfg.RootSecret = []byte(rootSecret)
```

(f) 在必填字段校验循环（`for _, f := range []struct{ name, val string }{...}`）之后、`return cfg, nil` 之前，加生产 TLS 硬校验：
```go
	if cfg.Environment.IsProduction() && (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") {
		return Config{}, errors.New("environment=production 要求设置 tls_cert_file 与 tls_key_file（生产不得走明文）")
	}
```

- [ ] **步骤 4：运行验证通过** — `go test ./internal/controlplane/app/ -run 'TestLoadConfig' -v`（新旧全 PASS）；`go build ./...`；`gofmt -l internal/controlplane/app/config.go`（应无输出）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/config.go internal/controlplane/app/config_test.go
git commit -m "feat(cp): M5.3a 控制面配置接 deploycfg(environment 装载+SYDOM_MASTER_KEY/ROOT_SECRET 支持 _FILE+生产模式缺 TLS 拒启动);默认 development 向后兼容"
```

---

## 任务 3：边车 config.go 接线

**文件：**
- 修改：`internal/sidecar/app/config.go`
- 测试：`internal/sidecar/app/config_test.go`

已存在助手：`writeConfig`、`envFunc`、`validEnv()`（返回 `{"SYDOM_APP_SECRET":"app-secret"}`）、`fullYAML` const（含 control_plane_addr/app_key/domain/auth_addr 等，无 environment、无 control_plane_tls）。import 已含 `os`、`path/filepath`、`testing`、`time`、`app`、`require`。

- [ ] **步骤 1：编写失败的测试** — 在 `internal/sidecar/app/config_test.go` 追加：

```go
func TestLoadConfig_EnvironmentAndProductionRequiresTLS(t *testing.T) {
	// dev 默认，无 control_plane_tls → ok
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.False(t, cfg.Environment.IsProduction())
	// 生产但无 control_plane_tls → 报错
	_, err = app.LoadConfig(writeConfig(t, fullYAML+"environment: production\n"), envFunc(validEnv()))
	require.Error(t, err)
	// 生产 + control_plane_tls: true → ok
	cfg, err = app.LoadConfig(writeConfig(t, fullYAML+"environment: production\ncontrol_plane_tls: true\n"), envFunc(validEnv()))
	require.NoError(t, err)
	require.True(t, cfg.Environment.IsProduction())
	// 未知环境值 → 报错
	_, err = app.LoadConfig(writeConfig(t, fullYAML+"environment: prod\n"), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_AppSecretFromFileAndConflict(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_APP_SECRET")
	p := filepath.Join(t.TempDir(), "as")
	require.NoError(t, os.WriteFile(p, []byte("file-app-secret\n"), 0o600))
	env["SYDOM_APP_SECRET_FILE"] = p
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, []byte("file-app-secret"), cfg.Secret)
	// env + file 同设 → 报错
	env["SYDOM_APP_SECRET"] = "env-app-secret"
	_, err = app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行验证失败** — `go test ./internal/sidecar/app/ -run 'TestLoadConfig_EnvironmentAndProductionRequiresTLS|TestLoadConfig_AppSecretFromFileAndConflict' -v`，预期编译失败（`cfg.Environment` 未定义）。

- [ ] **步骤 3：编写实现** — `internal/sidecar/app/config.go`：

(a) import 块补：
```go
	"github.com/nickZFZ/Sydom/internal/deploycfg"
```

(b) `Config` 结构在 `BackoffMax time.Duration` 后（或任一合适位置，与既有字段对齐）加：
```go
	Environment deploycfg.Environment // development（默认）/ production；production 下 ControlPlaneTLS 必为 true
```

(c) `fileConfig` 结构加：
```go
	Environment string `yaml:"environment"`
```

(d) `LoadConfig` 中，在 `cfg := Config{...}` 字面量与三个 `parseDurationDefault` 之后、`cfg.Secret = ...` 之前，插入环境解析：
```go
	if cfg.Environment, err = deploycfg.ParseEnvironment(firstNonEmpty(getenv("SYDOM_ENVIRONMENT"), fc.Environment)); err != nil {
		return Config{}, fmt.Errorf("environment: %w", err)
	}
```

(e) 把原来的：
```go
	cfg.Secret = []byte(getenv("SYDOM_APP_SECRET"))
```
改为：
```go
	appSecret, err := deploycfg.ResolveSecret(getenv, "SYDOM_APP_SECRET")
	if err != nil {
		return Config{}, err
	}
	cfg.Secret = []byte(appSecret)
```

(f) 在必填字段校验循环之后、`return cfg, nil` 之前，加生产 TLS 硬校验：
```go
	if cfg.Environment.IsProduction() && !cfg.ControlPlaneTLS {
		return Config{}, errors.New("environment=production 要求 control_plane_tls: true（生产不得明文 dial 控制面）")
	}
```

> 注：`internal/sidecar/app/config.go` 已 import `errors`（`SYDOM_APP_SECRET required` 用了 `errors.New`）与 `fmt`，无需新增这两个 import。

- [ ] **步骤 4：运行验证通过** — `go test ./internal/sidecar/app/ -run 'TestLoadConfig' -v`（新旧全 PASS）；`go build ./...`；`gofmt -l internal/sidecar/app/config.go`（应无输出）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/app/config.go internal/sidecar/app/config_test.go
git commit -m "feat(sidecar): M5.3a 边车配置接 deploycfg(environment 装载+SYDOM_APP_SECRET 支持 _FILE+生产模式需 control_plane_tls);默认 development 向后兼容"
```

---

## 任务 4：config.example.yaml 文档 + M53A-1..7 验收

**文件：**
- 修改：`cmd/sydom-controlplane/config.example.yaml`
- 修改：`cmd/sydom-sidecar/config.example.yaml`

- [ ] **步骤 1：控制面示例补说明** — 在 `cmd/sydom-controlplane/config.example.yaml` 顶部（`database_dsn` 之前或紧邻其它可选项处）加：

```yaml
# 运行环境（缺省 development）；production 下未设 tls_cert_file/tls_key_file 即拒绝启动（拒明文）：
# environment: production
```
并在敏感项说明处补 `_FILE` 约定：
```yaml
#   或用 <VAR>_FILE 指向挂载的密钥文件（与裸 env 二选一，同设报错；文件尾部空白/换行自动裁剪）：
#   SYDOM_MASTER_KEY_FILE / SYDOM_ROOT_SECRET_FILE
```

- [ ] **步骤 2：边车示例补说明** — 在 `cmd/sydom-sidecar/config.example.yaml` 加：

```yaml
# 运行环境（缺省 development）；production 下 control_plane_tls 必为 true 否则拒绝启动：
# environment: production
```
并在敏感项说明处补：
```yaml
#   或用 SYDOM_APP_SECRET_FILE 指向挂载的密钥文件（与裸 env 二选一，同设报错；尾部空白自动裁剪）。
```

- [ ] **步骤 3：Commit 文档**

```bash
git add cmd/sydom-controlplane/config.example.yaml cmd/sydom-sidecar/config.example.yaml
git commit -m "docs(m5.3a): config.example.yaml 补 environment 与 _FILE 密钥用法说明"
```

- [ ] **步骤 4：M53A-1 零触碰授权核心（机器验证）**

运行：
```bash
git diff --numstat b8e4a0f..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/sidecar/authz/ internal/auth/ internal/obs/ internal/tlsconfig/
```
预期：**空输出**。若有输出即违反铁律。

- [ ] **步骤 5：M53A-2/3/4/5 复核**

运行：
```bash
go test ./internal/deploycfg/ ./internal/controlplane/app/ ./internal/sidecar/app/ -v
```
预期：全 PASS。对照：M53A-3=environment 四类值（含未知→err）；M53A-4=生产需 TLS（控制面/边车各自，含 dev/有 TLS→ok 的向后兼容支）；M53A-5=三密钥 `_FILE` 装载 + trim + 冲突报错。

- [ ] **步骤 6：M53A-6 全量回归**

运行：`go test ./...`
预期：EXIT 0，全绿（既有 `TestRun_WiringEndToEnd`/`TestEndToEnd_*` 等无 environment/`_FILE` 配置 → 默认 development、走 env 密钥，行为不变）。

- [ ] **步骤 7：M53A-7 收尾确认** — 确认两 `config.example.yaml` 已补说明（步骤 1/2）。无 runbook 新建。

---

## 自检

**1. 规格覆盖度：**
- §3 deploycfg 接口 → 任务 1。
- §4.1 环境信号（默认 dev/env 覆盖/未知 err）→ 任务 1（ParseEnvironment）+ 任务 2/3（装载+覆盖+err 测试）。
- §4.2 生产 TLS fail-close → 任务 2（控制面）+ 任务 3（边车）。
- §4.3 `_FILE` 三密钥 + 冲突 → 任务 1（ResolveSecret）+ 任务 2（master/root）+ 任务 3（app）。
- §5 测试策略 → 任务 1/2/3 各测试步骤。
- §6 M53A-1..7 → 任务 4。
- §7 文档（config.example）→ 任务 4 步骤 1/2。
- 全覆盖，无遗漏。

**2. 占位符扫描：** 所有步骤均含完整可运行代码与精确命令，无 TODO/待定/伪代码。

**3. 类型一致性：** `deploycfg.Environment`、`deploycfg.Development`/`Production`、`ParseEnvironment(string)(Environment,error)`、`(Environment).IsProduction()bool`、`ResolveSecret(func(string)string,string)(string,error)` 在定义（任务 1）与所有调用点（任务 2 控制面、任务 3 边车、测试）签名一致；`Config.Environment` 字段名在两 config.go 与测试中一致。
