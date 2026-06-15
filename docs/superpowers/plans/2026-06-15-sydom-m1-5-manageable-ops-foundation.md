# 司域 M1.5 最小可托管运维底座 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给司域加上「最小可托管」运维底座——全链可选 server-TLS、专用明文健康探针、硬化为可托管的 docker-compose + 部署 runbook，一切默认关闭、向后兼容。

**架构：** 新增两个共享小包 `internal/tlsconfig`（证书加载 + fail-close 校验）与 `internal/health`（/healthz 恒活、/readyz 跑就绪 checker）。控制面与 sidecar 的 `Run` 在装配期构造 `*tls.Config` 并穿入各 gRPC（`grpc.Creds`）与 HTTP（`tls.NewListener`）监听器；sidecar→控制面 dial 经既有 `syncclient.Config{Secure,DialOptions}` 钩子走 TLS。健康探针就绪判定：控制面=DB+Redis Ping，sidecar=复用唯一 `checkFresh`（导出 `Authorizer.Ready()`）。部署侧四 Dockerfile 非 root、compose 健康门控（`service_healthy`）、`deploy/README.md` runbook。

**技术栈：** Go（`crypto/tls`、`crypto/x509`、`google.golang.org/grpc/credentials`）、docker-compose、alpine、`migrate/migrate`。

**不变量（验收逐条核验）：** MO-1 fail-close（部分 TLS 配置/证书不可读→启动失败，绝不静默明文；readiness 失败→503）；MO-2 HMAC×TLS 可组合；MO-3 授权真相零触碰（`internal/controlplane/adminauthz/` diff 0 行）；MO-4 探针忠实 fail-close（sidecar `/readyz`⟺`checkFresh`）；MO-5 探针/日志不泄露 secret；MO-6 向后兼容（缺省全关，`go test ./...` 全绿）；MO-7 可托管闭环。

---

## 文件结构

**新建：**
- `internal/tlsconfig/tlsconfig.go` — `Server(cert,key)`/`Client(ca)` 构造 `*tls.Config`，集中 fail-close 校验。职责：证书加载。
- `internal/tlsconfig/tlsconfig_test.go` — 单元 + TLS 往返 + 部分配置 fail-close。
- `internal/health/health.go` — `Handler(ready Checker) http.Handler`，/healthz 恒活、/readyz 跑 ready。职责：探针 HTTP 表面。
- `internal/health/health_test.go` — 200/503 + 不泄露。
- `deploy/README.md` — 部署 runbook。

**修改：**
- `internal/sidecar/authz/authorizer.go` — 导出 `Ready() error`（复用 `checkFresh`）。
- `internal/sidecar/authz/authorizer_test.go` — `Ready()` 三态测试。
- `internal/controlplane/app/config.go` — 加 `TLSCertFile`/`TLSKeyFile`/`HealthAddr` 字段 + yaml。
- `internal/controlplane/app/config_test.go` — 新字段解析测试。
- `internal/controlplane/app/run.go` — 装配 TLS（gRPC creds + HTTP `tls.NewListener`）+ 健康口。
- `internal/controlplane/mgmt/server.go` — `NewGRPCServer(..., opts ...grpc.ServerOption)`。
- `internal/controlplane/policysync/server.go` — `NewGRPCServer(..., opts ...grpc.ServerOption)`。
- `internal/sidecar/authz/server.go` — `NewGRPCServer(..., opts ...grpc.ServerOption)`。
- `internal/sidecar/app/config.go` — 加 `TLSCertFile`/`TLSKeyFile`/`ControlPlaneTLS`/`ControlPlaneCAFile`/`HealthAddr` + yaml。
- `internal/sidecar/app/config_test.go` — 新字段解析测试。
- `internal/sidecar/app/run.go` — 装配 TLS（serve creds + dial via syncclient）+ 健康口 + `buildSyncConfig` 助手。
- `cmd/sydom-controlplane/config.example.yaml`、`cmd/sydom-sidecar/config.example.yaml` — 注释新选项。
- `deploy/cp.config.yaml`、`deploy/sidecar.config.yaml` — 加 `health_addr`。
- `deploy/docker-compose.yaml` — controlplane/sidecar 加 healthcheck + 下游 `service_healthy`。
- `deploy/Dockerfile.controlplane`/`.sidecar`/`.seed`/`.orderservice` — 非 root user。

---

## 任务 1：`internal/tlsconfig` 共享包（证书加载 + fail-close）

**文件：**
- 创建：`internal/tlsconfig/tlsconfig.go`
- 测试：`internal/tlsconfig/tlsconfig_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/tlsconfig/tlsconfig_test.go`：
```go
package tlsconfig_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/tlsconfig"
)

// writeSelfSigned 生成自签证书写入 tmp，返回 certFile, keyFile（CA=该证书自身）。
func writeSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestServerNeitherSetReturnsNil(t *testing.T) {
	cfg, err := tlsconfig.Server("", "")
	if err != nil || cfg != nil {
		t.Fatalf("want (nil,nil), got (%v,%v)", cfg, err)
	}
}

func TestServerPartialConfigFailsClose(t *testing.T) {
	if _, err := tlsconfig.Server("only-cert.pem", ""); err == nil {
		t.Fatal("want error for partial config, got nil (silent plaintext is forbidden)")
	}
	if _, err := tlsconfig.Server("", "only-key.pem"); err == nil {
		t.Fatal("want error for partial config, got nil")
	}
}

func TestServerUnreadableCertFailsClose(t *testing.T) {
	if _, err := tlsconfig.Server("/no/such/cert.pem", "/no/such/key.pem"); err == nil {
		t.Fatal("want error for unreadable cert, got nil")
	}
}

func TestRoundTripTLSAndPlaintextRejected(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	srvCfg, err := tlsconfig.Server(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.(*tls.Conn).Handshake()
		c.Close()
	}()
	// 带 CA 的客户端握手成功。
	cliCfg, err := tlsconfig.Client(certFile)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatalf("TLS dial with CA should succeed: %v", err)
	}
	conn.Close()
	// 明文拨号 TLS 端口必失败（证明非静默降级）。
	raw, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetDeadline(time.Now().Add(time.Second))
	if _, err := raw.Write([]byte("PLAINTEXT\n")); err == nil {
		buf := make([]byte, 1)
		if _, rerr := raw.Read(buf); rerr == nil {
			t.Fatal("plaintext peer should not get a valid app response from TLS listener")
		}
	}
	raw.Close()
}

func TestClientBadCAFailsClose(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsconfig.Client(bad); err == nil {
		t.Fatal("want error for invalid CA pem, got nil")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/tlsconfig/`
预期：FAIL（包 `tlsconfig` 不存在 / 未定义 `Server`、`Client`）。

- [ ] **步骤 3：编写最少实现代码**

`internal/tlsconfig/tlsconfig.go`：
```go
// Package tlsconfig 集中构造服务端/客户端 *tls.Config，统一证书加载与 fail-close 校验：
// 任一证书项配置不全或加载失败即返错，调用方据此拒绝启动，绝不静默明文降级。
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Server 由 cert/key 文件对构造服务端 TLS 配置。
// 两者皆空 → 返回 (nil, nil)（调用方按明文处理）；仅一项非空 → 返错（fail-close）；
// 都非空但加载失败 → 返错。
func Server(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: cert_file 与 key_file 须同时设置（禁止半配置静默明文）")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 加载证书对失败: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// Client 构造客户端 TLS 配置；caFile 非空时以其为信任根，空时用系统根证书。
func Client(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 读取 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("tls: CA 文件 %q 不含有效证书", caFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/tlsconfig/ -v`
预期：PASS（5 个测试全绿）。

- [ ] **步骤 5：Commit**

```bash
git add internal/tlsconfig/
git commit -m "feat(ops): tlsconfig 共享包(证书加载+fail-close 校验, 半配置不静默明文)"
```

---

## 任务 2：`internal/health` 共享探针包

**文件：**
- 创建：`internal/health/health.go`
- 测试：`internal/health/health_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/health/health_test.go`：
```go
package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/health"
)

func do(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestHealthzAlwaysOK(t *testing.T) {
	h := health.Handler(func(context.Context) error { return errors.New("not ready") })
	code, body := do(t, h, "/healthz")
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("healthz want 200 ok, got %d %q", code, body)
	}
}

func TestReadyzOKWhenCheckerNil(t *testing.T) {
	code, _ := do(t, health.Handler(nil), "/readyz")
	if code != http.StatusOK {
		t.Fatalf("readyz nil checker want 200, got %d", code)
	}
}

func TestReadyzOKWhenCheckerPasses(t *testing.T) {
	code, body := do(t, health.Handler(func(context.Context) error { return nil }), "/readyz")
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("readyz pass want 200 ok, got %d %q", code, body)
	}
}

func TestReadyzServiceUnavailableWhenCheckerFails(t *testing.T) {
	secret := "super-secret-token"
	h := health.Handler(func(context.Context) error { return errors.New(secret) })
	code, body := do(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("readyz fail want 503, got %d", code)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("readyz body must not leak checker error detail, got %q", body)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/health/`
预期：FAIL（包 `health` 不存在）。

- [ ] **步骤 3：编写最少实现代码**

`internal/health/health.go`：
```go
// Package health 提供两二进制共享的明文健康探针 handler。
// /healthz 恒活（进程在即 200，不连依赖，避免抖动误重启）；
// /readyz 跑就绪 checker，fail-close：checker 返错即 503。
// 响应体仅 "ok"/"not ready"——零业务、零 secret、零内部错误细节。
package health

import (
	"context"
	"net/http"
)

// Checker 返回 nil 表示就绪；返回非 nil 表示未就绪（fail-close）。
type Checker func(ctx context.Context) error

// Handler 构造健康 mux。ready 为 nil 时 /readyz 恒就绪。
func Handler(ready Checker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready == nil || ready(r.Context()) == nil {
			writePlain(w, http.StatusOK, "ok")
			return
		}
		writePlain(w, http.StatusServiceUnavailable, "not ready")
	})
	return mux
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/health/ -v`
预期：PASS（4 个测试全绿）。

- [ ] **步骤 5：Commit**

```bash
git add internal/health/
git commit -m "feat(ops): health 共享包(/healthz 恒活 + /readyz fail-close, 不泄露)"
```

---

## 任务 3：导出 sidecar `Authorizer.Ready()`（复用唯一 fail-close 条件）

**文件：**
- 修改：`internal/sidecar/authz/authorizer.go`
- 测试：`internal/sidecar/authz/authorizer_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `internal/sidecar/authz/authorizer_test.go`（复用既有 `fakeFresh{ready,last}` 与 `newAuthorizer`）：
```go
func TestReadyReflectsCheckFresh(t *testing.T) {
	// 未就绪 → ErrNotReady
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	if err := a.Ready(); !errors.Is(err, kernel.ErrNotReady) {
		t.Fatalf("not-ready want ErrNotReady, got %v", err)
	}
	// 就绪且新鲜 → nil
	a = newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	if err := a.Ready(); err != nil {
		t.Fatalf("fresh want nil, got %v", err)
	}
	// 就绪但超陈旧阈 → ErrTooStale
	a = newAuthorizer(t, Config{MaxStaleness: time.Minute}, fakeFresh{ready: true, last: time.Now().Add(-time.Hour)})
	if err := a.Ready(); !errors.Is(err, ErrTooStale) {
		t.Fatalf("stale want ErrTooStale, got %v", err)
	}
}
```
注：若该测试文件尚未导入 `errors`/`kernel`，补 import（`"errors"`、`"github.com/nickZFZ/Sydom/internal/sidecar/kernel"`）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run TestReadyReflectsCheckFresh`
预期：FAIL（`a.Ready` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

在 `internal/sidecar/authz/authorizer.go` 的 `checkFresh` 之后追加：
```go
// Ready 返回 nil 当且仅当鉴权门面当前可服务（快照就绪且未超陈旧阈）。
// 直接复用 checkFresh——与执行路径（Check/BatchCheck/FilterSQL/FilterRaw）同一 fail-close 条件，
// 供健康 /readyz 与拒绝判定同源，绝不另写第二份新鲜度逻辑。
func (a *Authorizer) Ready() error { return a.checkFresh() }
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/`
预期：PASS（含新测试 + 原有测试不回归）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/authorizer.go internal/sidecar/authz/authorizer_test.go
git commit -m "feat(ops): 导出 Authorizer.Ready()(复用 checkFresh, /readyz 与执行同源)"
```

---

## 任务 4：三个 gRPC 构造加变参 `ServerOption`（向后兼容）

**文件：**
- 修改：`internal/controlplane/mgmt/server.go:121`
- 修改：`internal/controlplane/policysync/server.go:160`
- 修改：`internal/sidecar/authz/server.go:33`

> 变参为 additive 改动，现有零参调用方（`app.Run` 与测试）无需改动即编译通过。本任务先改签名，TLS 实际穿入在任务 6/7。

- [ ] **步骤 1：改 `mgmt.NewGRPCServer`**

`internal/controlplane/mgmt/server.go`：
```go
// NewGRPCServer 装配认证→鉴权→status 三拦截器（按序）并注册 AdminService。
// opts 供调用方注入额外 ServerOption（如 grpc.Creds 启用 TLS）。
func NewGRPCServer(srv *AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB, opts ...grpc.ServerOption) *grpc.Server {
	chain := grpc.ChainUnaryInterceptor(
		auth.UnaryServerInterceptorExempt(resolver, UnauthenticatedMethods),
		AuthzUnaryInterceptor(enf),
		StatusWriteUnaryInterceptor(db),
	)
	base := []grpc.ServerOption{grpc.MaxRecvMsgSize(maxMsgSize), grpc.MaxSendMsgSize(maxMsgSize), chain}
	g := grpc.NewServer(append(base, opts...)...)
	adminv1.RegisterAdminServiceServer(g, srv)
	return g
}
```

- [ ] **步骤 2：改 `policysync.NewGRPCServer`**

`internal/controlplane/policysync/server.go`：
```go
// NewGRPCServer 组装带认证拦截器与 64MB 消息上限的 grpc.Server 并注册 PolicySync。
// opts 供注入额外 ServerOption（如 grpc.Creds）。
func NewGRPCServer(srv *Server, res auth.SecretResolver, opts ...grpc.ServerOption) *grpc.Server {
	base := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	}
	g := grpc.NewServer(append(base, opts...)...)
	syncv1.RegisterPolicySyncServer(g, srv)
	return g
}
```

- [ ] **步骤 3：改 `authz.NewGRPCServer`**

`internal/sidecar/authz/server.go`：
```go
// NewGRPCServer 装配带 AuthService 的 grpc.Server（供 cmd 监听本地端点）。
// opts 供注入额外 ServerOption（如 grpc.Creds 启用 TLS）。
func NewGRPCServer(a *Authorizer, relay PermissionRelay, opts ...grpc.ServerOption) *grpc.Server {
	g := grpc.NewServer(opts...)
	authv1.RegisterAuthServiceServer(g, NewServer(a, relay))
	return g
}
```

- [ ] **步骤 4：编译 + 全量测试不回归**

运行：`go build ./... && go test ./internal/controlplane/mgmt/ ./internal/controlplane/policysync/ ./internal/sidecar/authz/`
预期：PASS（变参 additive，零参调用方不受影响）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/server.go internal/controlplane/policysync/server.go internal/sidecar/authz/server.go
git commit -m "feat(ops): 三 gRPC 构造加变参 ServerOption(为 TLS 穿入铺路, 向后兼容)"
```

---

## 任务 5：控制面 TLS + 健康口配置与装配

**文件：**
- 修改：`internal/controlplane/app/config.go`
- 测试：`internal/controlplane/app/config_test.go`
- 修改：`internal/controlplane/app/run.go`
- 修改：`cmd/sydom-controlplane/config.example.yaml`

- [ ] **步骤 1：编写失败的配置测试**

追加到 `internal/controlplane/app/config_test.go`（参照既有用例的写临时 YAML + 注入 env 的模式）：
```go
func TestLoadConfigParsesTLSAndHealth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yaml := "database_dsn: postgres://x\nredis_addr: r:6379\nadmin_addr: \":1\"\nsync_addr: \":2\"\n" +
		"root_principal: root@sydom\ntls_cert_file: /c/cert.pem\ntls_key_file: /c/key.pem\nhealth_addr: \":8083\"\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "SYDOM_MASTER_KEY":
			return base64.StdEncoding.EncodeToString(make([]byte, crypto.KeySize))
		case "SYDOM_ROOT_SECRET":
			return "rootsecret"
		}
		return ""
	}
	cfg, err := LoadConfig(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != "/c/cert.pem" || cfg.TLSKeyFile != "/c/key.pem" {
		t.Fatalf("tls fields not parsed: %+v", cfg)
	}
	if cfg.HealthAddr != ":8083" {
		t.Fatalf("health_addr not parsed: %q", cfg.HealthAddr)
	}
}
```
注：若文件未导入 `path/filepath`/`encoding/base64`/`crypto` 包，按既有 import 风格补齐（`crypto` 指 `github.com/nickZFZ/Sydom/internal/crypto`）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/app/ -run TestLoadConfigParsesTLSAndHealth`
预期：FAIL（`cfg.TLSCertFile` 等字段不存在）。

- [ ] **步骤 3：加配置字段**

`internal/controlplane/app/config.go`——`Config` 结构体加：
```go
	TLSCertFile string // 空=明文；与 TLSKeyFile 须同设（tlsconfig.Server 校验）
	TLSKeyFile  string
	HealthAddr  string // 空=不起健康口（向后兼容）；明文、免鉴权
```
`fileConfig` 加：
```go
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
	HealthAddr  string `yaml:"health_addr"`
```
`LoadConfig` 的 `cfg := Config{...}` 字面量内补：
```go
		TLSCertFile: fc.TLSCertFile,
		TLSKeyFile:  fc.TLSKeyFile,
		HealthAddr:  fc.HealthAddr,
```

- [ ] **步骤 4：运行配置测试验证通过**

运行：`go test ./internal/controlplane/app/ -run TestLoadConfigParsesTLSAndHealth`
预期：PASS。

- [ ] **步骤 5：装配 TLS + 健康口到 `Run`**

`internal/controlplane/app/run.go`：
1）import 补 `"crypto/tls"`、`"github.com/nickZFZ/Sydom/internal/health"`、`"github.com/nickZFZ/Sydom/internal/tlsconfig"`、`"google.golang.org/grpc"`、`"google.golang.org/grpc/credentials"`。
2）在构造 `grpcSrv`/`syncSrv` 之前插入 TLS 构造与穿入：
```go
	srvTLS, err := tlsconfig.Server(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("server tls: %w", err) // fail-close：半配置/证书不可读即拒绝启动
	}
	var grpcOpts []grpc.ServerOption
	if srvTLS != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(srvTLS)))
		logger.Info("control plane TLS enabled")
	}
```
3）把 `mgmt.NewGRPCServer(adminSrv, operatorResolver, enforcer, db)` 改为 `mgmt.NewGRPCServer(adminSrv, operatorResolver, enforcer, db, grpcOpts...)`；把 `policysync.NewGRPCServer(syncCore, appResolver)` 改为 `policysync.NewGRPCServer(syncCore, appResolver, grpcOpts...)`。
4）REST/Console 监听器在 `launch` 之前包 TLS（紧接各自 `if restLis != nil` / `if consoleLis != nil` 块首）：
```go
		if srvTLS != nil {
			restLis = tls.NewListener(restLis, srvTLS)
		}
```
```go
		if srvTLS != nil {
			consoleLis = tls.NewListener(consoleLis, srvTLS)
		}
```
5）`errCh` 缓冲从 `make(chan error, 6)` 提到 `make(chan error, 8)`（新增 health 协程）。
6）在 console 块之后、`<-runCtx.Done()` 之前加健康口：
```go
	var healthSrv *http.Server
	if cfg.HealthAddr != "" {
		healthLis, lerr := net.Listen("tcp", cfg.HealthAddr)
		if lerr != nil {
			return fmt.Errorf("listen health: %w", lerr)
		}
		healthSrv = &http.Server{Handler: health.Handler(cpReadiness(db, rdb))}
		logger.Info("control plane health enabled", "health_addr", cfg.HealthAddr)
		launch("health-serve", func() error {
			if e := healthSrv.Serve(healthLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}
```
7）在 shutdown 段（`if consoleSrv != nil {...}` 之后）加：
```go
	if healthSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}
```
8）文件末尾加就绪 checker（控制面就绪=DB+Redis 皆通）：
```go
// cpReadiness 构造控制面就绪 checker：DB Ping + Redis Ping 皆通即就绪（fail-close）。
func cpReadiness(db *sql.DB, rdb *redis.Client) health.Checker {
	return func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			return err
		}
		return rdb.Ping(pingCtx).Err()
	}
}
```

- [ ] **步骤 6：就绪 checker 单测**

追加到 `internal/controlplane/app/run_test.go`：
```go
func TestCPReadinessFailsWhenDBClosed(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://invalid:5432/x?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // 关闭 → Ping 必失败
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer rdb.Close()
	if err := cpReadiness(db, rdb)(context.Background()); err == nil {
		t.Fatal("readiness want error when DB closed, got nil")
	}
}
```
注：补 import `"context"`、`"database/sql"`、`_ "github.com/lib/pq"`、`"github.com/redis/go-redis/v9"`（若未导入）。

- [ ] **步骤 7：编译 + 测试**

运行：`go build ./... && go test ./internal/controlplane/app/`
预期：PASS（注意：依赖真实 DB/Redis 的既有集成测试若需环境，遵循其既有跳过约定；新就绪 checker 测试用关闭的 db，无需环境）。

- [ ] **步骤 8：更新 config.example + Commit**

`cmd/sydom-controlplane/config.example.yaml` 在敏感项注释前加：
```yaml
# 可选 TLS（cert/key 须同设，半配置启动失败；缺省=明文，仅本地/测试）：
# tls_cert_file: "/etc/sydom/tls/cert.pem"
# tls_key_file:  "/etc/sydom/tls/key.pem"
# 可选健康口（明文、免鉴权，仅 /healthz + /readyz；缺省不起）：
# health_addr: ":8083"
```
```bash
git add internal/controlplane/app/ cmd/sydom-controlplane/config.example.yaml
git commit -m "feat(ops): 控制面 TLS 穿入(gRPC creds + HTTP tls.NewListener) + 健康口(DB/Redis 就绪)"
```

---

## 任务 6：sidecar TLS（serve + dial）+ 健康口配置与装配

**文件：**
- 修改：`internal/sidecar/app/config.go`
- 测试：`internal/sidecar/app/config_test.go`
- 修改：`internal/sidecar/app/run.go`
- 测试：`internal/sidecar/app/run_test.go`
- 修改：`cmd/sydom-sidecar/config.example.yaml`

- [ ] **步骤 1：编写失败的配置 + dial-config 测试**

追加到 `internal/sidecar/app/config_test.go`：
```go
func TestLoadConfigParsesTLSAndHealth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yaml := "control_plane_addr: cp:8082\napp_key: k\ndomain: shop\nauth_addr: \":8090\"\n" +
		"tls_cert_file: /c/cert.pem\ntls_key_file: /c/key.pem\n" +
		"control_plane_tls: true\ncontrol_plane_ca_file: /c/ca.pem\nhealth_addr: \":8091\"\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "SYDOM_APP_SECRET" {
			return "appsecret"
		}
		return ""
	}
	cfg, err := LoadConfig(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != "/c/cert.pem" || cfg.TLSKeyFile != "/c/key.pem" {
		t.Fatalf("serve tls not parsed: %+v", cfg)
	}
	if !cfg.ControlPlaneTLS || cfg.ControlPlaneCAFile != "/c/ca.pem" {
		t.Fatalf("dial tls not parsed: %+v", cfg)
	}
	if cfg.HealthAddr != ":8091" {
		t.Fatalf("health_addr not parsed: %q", cfg.HealthAddr)
	}
}

func TestBuildSyncConfigTLSWiring(t *testing.T) {
	// TLS 关 → Secure=false，无附加 DialOptions
	off, err := buildSyncConfig(Config{ControlPlaneAddr: "cp:8082", AppKey: "k", Secret: []byte("s")})
	if err != nil {
		t.Fatal(err)
	}
	if off.Secure || len(off.DialOptions) != 0 {
		t.Fatalf("tls off want Secure=false & no dialopts, got %+v", off)
	}
	// TLS 开（系统根，无 CA 文件）→ Secure=true 且注入传输凭据
	on, err := buildSyncConfig(Config{ControlPlaneAddr: "cp:8082", AppKey: "k", Secret: []byte("s"), ControlPlaneTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if !on.Secure || len(on.DialOptions) == 0 {
		t.Fatalf("tls on want Secure=true & dialopts injected, got %+v", on)
	}
}
```
注：补 import `"path/filepath"`、`"os"`（若未导入）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/app/ -run 'TestLoadConfigParsesTLSAndHealth|TestBuildSyncConfigTLSWiring'`
预期：FAIL（字段与 `buildSyncConfig` 未定义）。

- [ ] **步骤 3：加配置字段**

`internal/sidecar/app/config.go`——`Config` 加：
```go
	TLSCertFile        string // serve auth 口（SDK→sidecar）证书；空=明文，与 TLSKeyFile 须同设
	TLSKeyFile         string
	ControlPlaneTLS    bool   // dial 控制面 sync 是否走 TLS
	ControlPlaneCAFile string // 信任 CA；空=系统根
	HealthAddr         string // 空=不起健康口
```
`fileConfig` 加：
```go
	TLSCertFile        string `yaml:"tls_cert_file"`
	TLSKeyFile         string `yaml:"tls_key_file"`
	ControlPlaneTLS    bool   `yaml:"control_plane_tls"`
	ControlPlaneCAFile string `yaml:"control_plane_ca_file"`
	HealthAddr         string `yaml:"health_addr"`
```
`LoadConfig` 的 `cfg := Config{...}` 字面量补：
```go
		TLSCertFile:        fc.TLSCertFile,
		TLSKeyFile:         fc.TLSKeyFile,
		ControlPlaneTLS:    fc.ControlPlaneTLS,
		ControlPlaneCAFile: fc.ControlPlaneCAFile,
		HealthAddr:         fc.HealthAddr,
```

- [ ] **步骤 4：加 `buildSyncConfig` 助手 + 装配到 `Run`**

`internal/sidecar/app/run.go`：
1）import 补 `"context"`（若未导入）、`"net/http"`、`"github.com/nickZFZ/Sydom/internal/health"`、`"github.com/nickZFZ/Sydom/internal/tlsconfig"`、`"google.golang.org/grpc"`、`"google.golang.org/grpc/credentials"`。
2）文件末尾加助手（dial 配置单一构造点，可独测 MO-2 钩子）：
```go
// buildSyncConfig 由 app Config 组装 syncclient.Config；ControlPlaneTLS 开时
// 置 Secure=true 并注入 TLS 传输凭据——使 HMAC RequireTransportSecurity()=true，明文不可承载凭据。
func buildSyncConfig(cfg Config) (syncclient.Config, error) {
	sc := syncclient.Config{
		Endpoint:       cfg.ControlPlaneAddr,
		AppID:          cfg.AppKey,
		Secret:         cfg.Secret,
		Secure:         false,
		BackoffInitial: cfg.BackoffInitial,
		BackoffMax:     cfg.BackoffMax,
	}
	if cfg.ControlPlaneTLS {
		cliTLS, err := tlsconfig.Client(cfg.ControlPlaneCAFile)
		if err != nil {
			return syncclient.Config{}, fmt.Errorf("control plane client tls: %w", err)
		}
		sc.Secure = true
		sc.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(cliTLS))}
	}
	return sc, nil
}
```
3）把 `Run` 内原 `syncCli, err := syncclient.New(syncclient.Config{...}, engine)` 改为：
```go
	scCfg, err := buildSyncConfig(cfg)
	if err != nil {
		return err
	}
	syncCli, err := syncclient.New(scCfg, engine)
	if err != nil {
		return fmt.Errorf("new sync client: %w", err)
	}
```
4）serve TLS：在构造 `authSrv` 前加：
```go
	srvTLS, err := tlsconfig.Server(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("server tls: %w", err)
	}
	var grpcOpts []grpc.ServerOption
	if srvTLS != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(srvTLS)))
		logger.Info("sidecar TLS enabled")
	}
```
把 `authSrv := authz.NewGRPCServer(authzr, syncCli)` 改为 `authSrv := authz.NewGRPCServer(authzr, syncCli, grpcOpts...)`。
5）`errCh` 缓冲从 `make(chan error, 2)` 提到 `make(chan error, 4)`。
6）在两个 `launch(...)` 之后、`<-runCtx.Done()` 之前加健康口：
```go
	var healthSrv *http.Server
	if cfg.HealthAddr != "" {
		healthLis, lerr := net.Listen("tcp", cfg.HealthAddr)
		if lerr != nil {
			return fmt.Errorf("listen health: %w", lerr)
		}
		healthSrv = &http.Server{Handler: health.Handler(func(ctx context.Context) error { return authzr.Ready() })}
		logger.Info("sidecar health enabled", "health_addr", cfg.HealthAddr)
		launch("health-serve", func() error {
			if e := healthSrv.Serve(healthLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}
```
7）在 `authSrv.GracefulStop()` 之前加健康口优雅关闭：
```go
	if healthSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}
```
注：需补 import `"time"`（若未导入）。

- [ ] **步骤 5：运行测试验证通过**

运行：`go build ./... && go test ./internal/sidecar/app/`
预期：PASS（config + buildSyncConfig 测试绿；既有 run_test 因 HealthAddr/TLS 缺省全空而零回归）。

- [ ] **步骤 6：更新 config.example + Commit**

`cmd/sydom-sidecar/config.example.yaml` 敏感项注释前加：
```yaml
# 可选 serve TLS（SDK→sidecar；cert/key 须同设，缺省=明文）：
# tls_cert_file: "/etc/sydom/tls/cert.pem"
# tls_key_file:  "/etc/sydom/tls/key.pem"
# 可选 dial 控制面走 TLS（control_plane_ca_file 空=系统根）：
# control_plane_tls: true
# control_plane_ca_file: "/etc/sydom/tls/ca.pem"
# 可选健康口（明文、免鉴权；缺省不起）：
# health_addr: ":8091"
```
```bash
git add internal/sidecar/app/ cmd/sydom-sidecar/config.example.yaml
git commit -m "feat(ops): sidecar TLS(serve creds + dial via syncclient) + 健康口(复用 Ready)"
```

---

## 任务 7：部署硬化（四 Dockerfile 非 root + compose 健康门控 + TLS 可选挂载）

**文件：**
- 修改：`deploy/Dockerfile.controlplane`、`deploy/Dockerfile.sidecar`、`deploy/Dockerfile.seed`、`deploy/Dockerfile.orderservice`
- 修改：`deploy/cp.config.yaml`、`deploy/sidecar.config.yaml`
- 修改：`deploy/docker-compose.yaml`

- [ ] **步骤 1：四 Dockerfile 改非 root**

对四个 Dockerfile，将运行阶段（`FROM alpine:3.21` 起）统一改为（以 controlplane 为例，其余三个同改，仅 `COPY` 来源不变）：
```dockerfile
FROM alpine:3.21
RUN adduser -D -u 10001 sydom
COPY --from=build /out/app /app
USER sydom
ENTRYPOINT ["/app"]
```
四个文件的 build 阶段与 `COPY --from=build /out/app /app` 保持原样，仅在 `COPY` 前插 `RUN adduser -D -u 10001 sydom`、在 `ENTRYPOINT` 前插 `USER sydom`。

- [ ] **步骤 2：部署 config 加 health_addr**

`deploy/cp.config.yaml` 末尾追加：
```yaml
health_addr: ":8083"
```
`deploy/sidecar.config.yaml` 末尾追加：
```yaml
health_addr: ":8091"
```

- [ ] **步骤 3：compose 加健康门控**

`deploy/docker-compose.yaml`：
1）`controlplane` 服务加 healthcheck（与 ports 同级），并暴露健康口（可选）：
```yaml
    healthcheck:
      test: ["CMD", "wget", "-q", "-O-", "http://127.0.0.1:8083/readyz"]
      interval: 2s
      timeout: 3s
      retries: 30
```
2）`sidecar` 服务加 healthcheck：
```yaml
    healthcheck:
      test: ["CMD", "wget", "-q", "-O-", "http://127.0.0.1:8091/readyz"]
      interval: 2s
      timeout: 3s
      retries: 30
```
3）把下游 `depends_on` 从 `service_started` 升级为 `service_healthy`：
- `seeder.depends_on.controlplane`、`sidecar.depends_on.controlplane`：`{ condition: service_healthy }`
- `orderservice.depends_on.sidecar`：`{ condition: service_healthy }`

> 说明：alpine 运行镜像自带 busybox `wget`，healthcheck 零额外依赖；sidecar `/readyz` 在控制面可达且首个快照拉取成功后才转 healthy，故下游真正等就绪而非仅「进程起」。

- [ ] **步骤 4：构建验证（非 root + 健康门控）**

运行：
```bash
docker compose -f deploy/docker-compose.yaml build controlplane sidecar
docker run --rm --entrypoint id sydom-demo-controlplane 2>/dev/null || docker compose -f deploy/docker-compose.yaml run --rm --entrypoint id controlplane
```
预期：`uid=10001(sydom)` —— 容器以非 root 运行。
（若本机无 docker，则以 Dockerfile 文本断言 `USER sydom` 存在代替，并在 runbook 注明需 CI 验证。）

- [ ] **步骤 5：Commit**

```bash
git add deploy/Dockerfile.controlplane deploy/Dockerfile.sidecar deploy/Dockerfile.seed deploy/Dockerfile.orderservice deploy/cp.config.yaml deploy/sidecar.config.yaml deploy/docker-compose.yaml
git commit -m "feat(ops): 部署硬化(四镜像非 root + compose 健康门控 service_healthy + health_addr)"
```

---

## 任务 8：部署 runbook + 全仓兜底验证 + 不变量核验

**文件：**
- 创建：`deploy/README.md`

- [ ] **步骤 1：编写 `deploy/README.md`**

内容覆盖（每节给可复制命令）：
1）**前置**：docker compose；密钥经 `.env`（`SYDOM_MASTER_KEY`=base64(32B)、`SYDOM_ROOT_SECRET`、`SYDOM_APP_SECRET`），示例 `openssl rand -base64 32`。强调密钥绝不入镜像/git。
2）**默认起栈（明文 demo）**：`docker compose -f deploy/docker-compose.yaml up -d`；迁移自动（`migrate` 服务 `service_healthy` 门控 PG）；验就绪 `wget -qO- http://127.0.0.1:8083/readyz`（若映射）。
3）**生成 TLS 证书**：自签测试步骤（`openssl req -x509 -newkey ...` 或 `mkcert`），生产用真 CA 的说明；产出 `cert.pem`/`key.pem`/`ca.pem`。
4）**开启 TLS**：挂载证书卷 + 在 `cp.config.yaml`/`sidecar.config.yaml` 设 `tls_cert_file`/`tls_key_file`、sidecar `control_plane_tls: true` + `control_plane_ca_file`；SDK 侧经 `sydom.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(...)))` 注入（公开契约零改）。强调半配置启动失败=fail-close。
5）**健康探针语义**：`/healthz` 恒活；`/readyz` 503=控制面 DB/Redis 不可达，或 sidecar 快照未就绪/超 `max_staleness`（与执行 fail-close 同源）。
6）**非 root**：容器以 `uid 10001` 运行；挂载证书须该 uid 可读。
7）**运维注记**：证书轮换=滚动重启（M1.5 不热 reload）；健康口建议绑内网。

- [ ] **步骤 2：全仓兜底验证**

运行：`go vet ./... && go test ./...`
预期：`go vet` 干净；`go test` 全绿（依赖外部 DB/Redis 的集成测试遵循其既有跳过约定）。

- [ ] **步骤 3：核验 MO-3 授权真相零触碰**

运行：`git diff <本子项目起点>..HEAD -- internal/controlplane/adminauthz/ | wc -l`
预期：`0` —— M1.1 matcher 与 ruleTable 一字未改。
（`<本子项目起点>` = 进入 worktree 时的 main HEAD。）

- [ ] **步骤 4：Commit**

```bash
git add deploy/README.md
git commit -m "docs(ops): 部署 runbook(生证/密钥/TLS 开关/健康语义/非 root) + M1.5 收尾"
```

---

## 自检结论

- **规格覆盖度**：支柱 1 TLS → 任务 1/4/5/6；支柱 2 健康 → 任务 2/3/5/6；支柱 3 部署 → 任务 7/8。MO-1（任务 1 部分配置 fail-close + 任务 5/6 Run 返错）；MO-2（任务 6 buildSyncConfig + 任务 1 往返）；MO-3（任务 8 步骤 3 diff 0）；MO-4（任务 3 Ready 三态 + 任务 5 cpReadiness）；MO-5（任务 2 不泄露测试）；MO-6（缺省全关 + 任务 4 变参 additive + 任务 8 全量绿）；MO-7（任务 7/8）。全覆盖。
- **占位符扫描**：无 TODO/待定；每步含真实代码与命令。
- **类型一致性**：`tlsconfig.Server`/`Client`、`health.Handler`/`Checker`、`Authorizer.Ready`、`buildSyncConfig`、`cpReadiness`、三 `NewGRPCServer(..., opts ...grpc.ServerOption)` 全程命名一致；`syncclient.Config{Secure,DialOptions}` 与既有字段一致。
- **顺序依赖**：任务 4（变参签名）须在任务 5/6（穿入 creds）之前——已按此排序。
