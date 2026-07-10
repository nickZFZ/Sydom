# M5.2b 策略同步通道双向 TLS（mTLS）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给 sidecar→控制面 policysync gRPC 通道加双向客户端证书校验，构成传输层纵深防御。

**架构：** `tlsconfig` 新增两个平行构造器 `MutualServer`（从共享 server 配置派生要求客户端证书的变体）/ `MutualClient`（在信任根基础上附加客户端证书）；控制面装配时**仅** policysync 监听器换用派生的 `syncTLS`，admin/REST/Console 继续用共享 `srvTLS`；sidecar 拨号改用 `MutualClient`。三个新配置项全部 opt-in 加法，皆空 ⟹ 行为与 BASE 逐字节一致。

**技术栈：** Go、`crypto/tls`、`crypto/x509`、gRPC（`google.golang.org/grpc/credentials`）、testify、`internal/dbtest`（testcontainers PG+Redis）。

**BASE：** `feat/m5-2b-mtls` @ `f9afd0b`（规格已提交）。

**零触碰铁律：** 仅动 `internal/tlsconfig`、`internal/certtest`(新)、`internal/controlplane/app`、`internal/sidecar/app`。`casbin/`、`adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`、`internal/auth/`、`internal/obs/` 内容 diff 必须为 0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/certtest/certtest.go`（创建） | 共享测试助手：自签 CA + 用 CA 签发 server/client leaf 证书写入临时文件，供 tlsconfig 与装配测试复用（仿 `internal/dbtest` 先例，非 _test 文件导入 testing）。 |
| `internal/certtest/certtest_test.go`（创建） | certtest 自检：签发的 leaf 能链到 CA。 |
| `internal/tlsconfig/tlsconfig.go`（修改，追加两函数） | `MutualServer(base, clientCAFile)` / `MutualClient(caFile, certFile, keyFile)`。现有 `Server`/`Client` 零改。 |
| `internal/tlsconfig/tlsconfig_test.go`（修改，追加测试） | 两函数各分支单测 + 双向握手集成测试（无证书被拒/有证书通过/反向验证有齿）。 |
| `internal/controlplane/app/config.go`（修改） | 新增 `SyncClientCAFile` 字段 + yaml `sync_client_ca_file` + LoadConfig 装配。 |
| `internal/controlplane/app/config_test.go`（修改或创建） | 断言 yaml `sync_client_ca_file` 装入 cfg。 |
| `internal/controlplane/app/run.go`（修改） | 派生 `syncTLS`，仅 policysync 监听器用 `syncGrpcOpts`；admin/REST/Console 不变。 |
| `internal/controlplane/app/run_test.go`（修改，追加测试） | `TestRun_SyncChannelRequiresClientCert`：sync 拒无证书、收 CA 签发证书；admin/REST/Console 不要求证书（分通道隔离）。 |
| `internal/sidecar/app/config.go`（修改） | 新增 `ControlPlaneClientCertFile`/`ControlPlaneClientKeyFile` + yaml + LoadConfig 装配。 |
| `internal/sidecar/app/config_test.go`（修改或创建） | 断言两 yaml 键装入 cfg。 |
| `internal/sidecar/app/run.go`（修改，`buildSyncConfig`） | `tlsconfig.Client(...)` → `tlsconfig.MutualClient(...)`。 |
| `internal/sidecar/app/run_test.go`（修改或创建 `mtls_test.go`） | `buildSyncConfig` 携客户端证书成功 + 半配置 fail-close。 |

---

## 任务 1：`internal/certtest` 共享证书测试助手

**文件：**
- 创建：`internal/certtest/certtest.go`
- 创建：`internal/certtest/certtest_test.go`

- [ ] **步骤 1：编写 certtest 包**

`internal/certtest/certtest.go`：

```go
// Package certtest 为测试生成自签 CA 及其签发的 leaf 证书，写入临时文件返回路径。
// 供 tlsconfig 与控制面/边车装配测试共享，全离线（crypto/x509 自签，无网络）。
// 仿 internal/dbtest 先例：非 _test 文件导入 testing，仅测试树消费。
package certtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA 持有自签 CA 的证书与私钥，可签发 leaf。
type CA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	caFile string
}

// NewCA 生成一个自签 CA，证书 PEM 写入 t.TempDir。
func NewCA(t *testing.T) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "sydom-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", der)
	return &CA{cert: cert, key: key, caFile: caFile}
}

// File 返回 CA 证书 PEM 文件路径。
func (c *CA) File() string { return c.caFile }

// Leaf 用 CA 签发一张 leaf 证书（含 127.0.0.1/localhost SAN），eku 指定用途
// （server 传 x509.ExtKeyUsageServerAuth，client 传 x509.ExtKeyUsageClientAuth）。
// 返回 cert/key PEM 文件路径（写入各自的 t.TempDir）。
func (c *CA) Leaf(t *testing.T, cn string, eku ...x509.ExtKeyUsage) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	writePEM(t, certFile, "CERTIFICATE", der)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	return certFile, keyFile
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **步骤 2：编写 certtest 自检**

`internal/certtest/certtest_test.go`：

```go
package certtest_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	"github.com/nickZFZ/Sydom/internal/certtest"
)

func TestLeafChainsToCA(t *testing.T) {
	ca := certtest.NewCA(t)
	certFile, keyFile := ca.Leaf(t, "leaf", x509.ExtKeyUsageServerAuth)
	if keyFile == "" {
		t.Fatal("want key file path")
	}

	caPEM, err := os.ReadFile(ca.File())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM 无效")
	}

	leafPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatal("leaf PEM 无效")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf 应链到 CA: %v", err)
	}
}
```

- [ ] **步骤 3：运行验证**

运行：`go test ./internal/certtest/ -run TestLeafChainsToCA -v`
预期：PASS。

- [ ] **步骤 4：Commit**

```bash
git add internal/certtest/
git commit -m "test(certtest): M5.2b 共享自签 CA/leaf 证书测试助手(供 tlsconfig+装配测试复用,全离线)"
```

---

## 任务 2：`tlsconfig.MutualServer` + `MutualClient`

**文件：**
- 修改：`internal/tlsconfig/tlsconfig.go`（追加两函数，现有 `Server`/`Client` 不动）
- 测试：`internal/tlsconfig/tlsconfig_test.go`（追加单测）

- [ ] **步骤 1：编写失败的单测**

在 `internal/tlsconfig/tlsconfig_test.go` 追加（并在 import 补 `"github.com/nickZFZ/Sydom/internal/certtest"` 与 `"crypto/x509"`；`crypto/tls` 已在）：

```go
func TestMutualServerEmptyCAReturnsBaseUnchanged(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	base, err := tlsconfig.Server(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tlsconfig.MutualServer(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatal("空 clientCAFile 应原样返回 base（向后兼容）")
	}
	if got.ClientAuth != tls.NoClientCert {
		t.Fatal("空 clientCAFile 不应要求客户端证书")
	}
}

func TestMutualServerNilBaseFailsClose(t *testing.T) {
	ca := certtest.NewCA(t)
	if _, err := tlsconfig.MutualServer(nil, ca.File()); err == nil {
		t.Fatal("明文(base=nil)下要求客户端证书应 fail-close 返错")
	}
}

func TestMutualServerBadCAFailsClose(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	base, _ := tlsconfig.Server(certFile, keyFile)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsconfig.MutualServer(base, bad); err == nil {
		t.Fatal("无效 CA PEM 应返错")
	}
}

func TestMutualServerHappyDoesNotMutateBase(t *testing.T) {
	ca := certtest.NewCA(t)
	srvCert, srvKey := ca.Leaf(t, "localhost", x509.ExtKeyUsageServerAuth)
	base, err := tlsconfig.Server(srvCert, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tlsconfig.MutualServer(base, ca.File())
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("应要求并验证客户端证书")
	}
	if got.ClientCAs == nil {
		t.Fatal("应设置 ClientCAs")
	}
	if base.ClientAuth != tls.NoClientCert || base.ClientCAs != nil {
		t.Fatal("入参 base 不应被改写（别名安全）")
	}
}

func TestMutualClientEmptyCertEquivalentToClient(t *testing.T) {
	ca := certtest.NewCA(t)
	got, err := tlsconfig.MutualClient(ca.File(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificates) != 0 {
		t.Fatal("未配置客户端证书时不应出示证书（向后兼容）")
	}
	if got.RootCAs == nil {
		t.Fatal("应从 caFile 构造信任根")
	}
}

func TestMutualClientPartialFailsClose(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	if _, err := tlsconfig.MutualClient(ca.File(), cliCert, ""); err == nil {
		t.Fatal("仅 cert 无 key 应 fail-close")
	}
	if _, err := tlsconfig.MutualClient(ca.File(), "", cliKey); err == nil {
		t.Fatal("仅 key 无 cert 应 fail-close")
	}
}

func TestMutualClientHappyLoadsCert(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	got, err := tlsconfig.MutualClient(ca.File(), cliCert, cliKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificates) != 1 {
		t.Fatal("应加载一张客户端证书")
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/tlsconfig/ -run TestMutual -v`
预期：编译失败（`undefined: tlsconfig.MutualServer` / `MutualClient`）。

- [ ] **步骤 3：编写实现**

在 `internal/tlsconfig/tlsconfig.go` 末尾追加（import 已含 `crypto/tls`、`crypto/x509`、`fmt`、`os`）：

```go
// MutualServer 由已构造的服务端配置派生「要求并验证客户端证书」的变体：
//   clientCAFile 空                                → 返回 base 原样（向后兼容，不要求客户端证书）；
//   base 为 nil（未启用服务端 TLS）但 clientCAFile 非空 → 返错（fail-close：明文上无法要求客户端证书）；
//   clientCAFile 不可读/无有效 PEM                  → 返错。
// 非空路径 base.Clone() 后设置 ClientAuth/ClientCAs，绝不改写入参 base（避免别名污染共享配置）。
func MutualServer(base *tls.Config, clientCAFile string) (*tls.Config, error) {
	if clientCAFile == "" {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("tls: 要求客户端证书须先启用服务端 TLS（sync_client_ca_file 已设但 cert/key 未设）")
	}
	pemBytes, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 读取客户端 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("tls: 客户端 CA 文件不含有效 PEM 证书块")
	}
	cfg := base.Clone()
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.ClientCAs = pool
	return cfg, nil
}

// MutualClient 在 Client(caFile) 基础上附加客户端证书对用于 mTLS：
//   certFile/keyFile 皆空 → 等价 Client（不出示客户端证书，向后兼容）；
//   仅一项非空          → 返错（fail-close：禁止半配置）；
//   都非空但加载失败    → 返错。
func MutualClient(caFile, certFile, keyFile string) (*tls.Config, error) {
	cfg, err := Client(caFile)
	if err != nil {
		return nil, err
	}
	if certFile == "" && keyFile == "" {
		return cfg, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: 客户端 cert_file 与 key_file 须同时设置（禁止半配置）")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 加载客户端证书对失败: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/tlsconfig/ -run TestMutual -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/tlsconfig/
git commit -m "feat(tlsconfig): M5.2b MutualServer(派生要求客户端证书,Clone 防别名)+MutualClient(附客户端证书对);现有 Server/Client 零改;fail-close 阶梯"
```

---

## 任务 3：`tlsconfig` 双向握手集成测试（核心可演示证明）

**文件：**
- 测试：`internal/tlsconfig/tlsconfig_test.go`（追加集成测试，纯测试代码，无新生产码）

- [ ] **步骤 1：编写集成测试**

在 `internal/tlsconfig/tlsconfig_test.go` 追加（`crypto/x509`、`certtest` 已在任务 2 引入）：

```go
// acceptHandshake 循环 Accept 并完成 TLS 握手后关闭（服务端侧）。
func acceptHandshake(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}(c)
	}
}

func TestMutualServerEnforcesClientCert(t *testing.T) {
	ca := certtest.NewCA(t)
	srvCert, srvKey := ca.Leaf(t, "localhost", x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)

	base, err := tlsconfig.Server(srvCert, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	srvCfg, err := tlsconfig.MutualServer(base, ca.File())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go acceptHandshake(ln)

	// 无客户端证书 → 握手被拒。
	noCert, err := tlsconfig.MutualClient(ca.File(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	noCert.ServerName = "localhost"
	if c, err := tls.Dial("tcp", ln.Addr().String(), noCert); err == nil {
		c.Close()
		t.Fatal("无客户端证书应被 mTLS 服务端拒绝")
	}

	// 持 CA 签发客户端证书 → 握手成功。
	withCert, err := tlsconfig.MutualClient(ca.File(), cliCert, cliKey)
	if err != nil {
		t.Fatal(err)
	}
	withCert.ServerName = "localhost"
	c, err := tls.Dial("tcp", ln.Addr().String(), withCert)
	if err != nil {
		t.Fatalf("持 CA 签发客户端证书应握手成功: %v", err)
	}
	c.Close()

	// 反向验证：退回单向 TLS（不要求客户端证书）后，无证书 client 握手成功，
	// 证明上面「拒绝」确由客户端证书要求所致（断言有齿）。
	plain, err := tlsconfig.Server(srvCert, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	ln2, err := tls.Listen("tcp", "127.0.0.1:0", plain)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	go acceptHandshake(ln2)
	c2, err := tls.Dial("tcp", ln2.Addr().String(), noCert)
	if err != nil {
		t.Fatalf("单向 TLS 下无证书 client 应握手成功（反向验证）: %v", err)
	}
	c2.Close()
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./internal/tlsconfig/ -run TestMutualServerEnforcesClientCert -v`
预期：PASS（任务 2 的实现已满足）。

- [ ] **步骤 3：整包回归**

运行：`go test ./internal/tlsconfig/`
预期：ok（含既有 `TestRoundTripTLSAndPlaintextRejected` 等）。

- [ ] **步骤 4：Commit**

```bash
git add internal/tlsconfig/tlsconfig_test.go
git commit -m "test(tlsconfig): M5.2b 双向握手集成测试(无证书被拒/CA 签发证书通过/撤 ClientAuth 反向验证有齿)"
```

---

## 任务 4：控制面配置 `SyncClientCAFile`

**文件：**
- 修改：`internal/controlplane/app/config.go`
- 测试：`internal/controlplane/app/config_test.go`

- [ ] **步骤 1：编写失败的配置测试**

在 `internal/controlplane/app/config_test.go` 追加（若文件不存在则创建，package `app_test`，import `os`/`path/filepath`/`testing`/`encoding/base64`/`crypto` 与 `github.com/nickZFZ/Sydom/internal/controlplane/app`、`github.com/nickZFZ/Sydom/internal/crypto`）：

```go
func TestLoadConfig_SyncClientCAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.yaml")
	yaml := "" +
		"database_dsn: postgres://x\n" +
		"redis_addr: 127.0.0.1:6379\n" +
		"admin_addr: 127.0.0.1:8080\n" +
		"sync_addr: 127.0.0.1:8081\n" +
		"root_principal: root@sydom\n" +
		"sync_client_ca_file: /etc/sydom/sync-ca.pem\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := make([]byte, crypto.KeySize)
	getenv := func(k string) string {
		switch k {
		case "SYDOM_MASTER_KEY":
			return base64.StdEncoding.EncodeToString(mk)
		case "SYDOM_ROOT_SECRET":
			return "root-secret"
		}
		return ""
	}
	cfg, err := app.LoadConfig(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SyncClientCAFile != "/etc/sydom/sync-ca.pem" {
		t.Fatalf("want sync_client_ca_file loaded, got %q", cfg.SyncClientCAFile)
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/app/ -run TestLoadConfig_SyncClientCAFile -v`
预期：编译失败（`cfg.SyncClientCAFile` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/app/config.go`：`Config` 结构在 `TLSKeyFile` 后加字段：

```go
	TLSCertFile      string // 空=明文；与 TLSKeyFile 须同设（tlsconfig.Server 校验）
	TLSKeyFile       string
	SyncClientCAFile string // 非空=policysync 通道要求客户端证书链到此 CA（mTLS）；空=不要求
	HealthAddr       string // 空=不起健康口（向后兼容）；明文、免鉴权
```

`fileConfig` 结构在 `TLSKeyFile` 后加：

```go
	TLSCertFile      string `yaml:"tls_cert_file"`
	TLSKeyFile       string `yaml:"tls_key_file"`
	SyncClientCAFile string `yaml:"sync_client_ca_file"`
	HealthAddr       string `yaml:"health_addr"`
```

`LoadConfig` 的 `cfg := Config{...}` 字面量在 `TLSKeyFile: fc.TLSKeyFile,` 后加：

```go
		SyncClientCAFile: fc.SyncClientCAFile,
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/app/ -run TestLoadConfig_SyncClientCAFile -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/config.go internal/controlplane/app/config_test.go
git commit -m "feat(cp): M5.2b 控制面配置 sync_client_ca_file(非空=policysync 通道要求客户端证书,opt-in 加法)"
```

---

## 任务 5：控制面装配——仅 policysync 监听器用 mTLS

**文件：**
- 修改：`internal/controlplane/app/run.go:73-93`（派生 `syncTLS`，policysync 用 `syncGrpcOpts`）
- 测试：`internal/controlplane/app/run_test.go`（追加 `TestRun_SyncChannelRequiresClientCert`）

- [ ] **步骤 1：编写失败的装配测试**

在 `internal/controlplane/app/run_test.go` 追加（import 补 `"crypto/tls"`、`"crypto/x509"`、`"os"`、`"github.com/nickZFZ/Sydom/internal/certtest"`；其余 `net`/`grpc`/`require`/`dbtest`/`crypto` 等已在文件顶部）：

```go
// caPool 从 CA 文件构造仅信任该 CA 的根池。
func caPool(t *testing.T, caFile string) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pem))
	return pool
}

func TestRun_SyncChannelRequiresClientCert(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)

	ca := certtest.NewCA(t)
	srvCert, srvKey := ca.Leaf(t, "localhost", x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)

	mk := make([]byte, crypto.KeySize)
	for i := range mk {
		mk[i] = 0x2a
	}
	cfg := app.Config{
		DatabaseDSN:       dsn,
		RedisAddr:         redisAddr,
		RootPrincipal:     "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond,
		RelayPollInterval: 20 * time.Millisecond,
		MasterKey:         mk,
		RootSecret:        []byte("root-secret"),
		TLSCertFile:       srvCert,
		TLSKeyFile:        srvKey,
		SyncClientCAFile:  ca.File(),
	}

	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	restLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	consoleLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, restLis, consoleLis, logger) }()

	roots := caPool(t, ca.File())

	// 先等服务端起来：admin 监听器用共享 srvTLS（不要求客户端证书），无证书 TLS 握手应成功。
	adminNoCert := &tls.Config{RootCAs: roots, ServerName: "localhost"}
	require.Eventually(t, func() bool {
		c, err := tls.Dial("tcp", adminLis.Addr().String(), adminNoCert)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, 10*time.Second, 100*time.Millisecond, "admin 监听器不应要求客户端证书（分通道隔离）")

	// sync 监听器：无客户端证书 → 握手被拒。
	syncNoCert := &tls.Config{RootCAs: roots, ServerName: "localhost"}
	if c, err := tls.Dial("tcp", syncLis.Addr().String(), syncNoCert); err == nil {
		c.Close()
		t.Fatal("policysync 监听器应要求客户端证书，无证书握手不该成功")
	}

	// sync 监听器：持 CA 签发客户端证书 → 握手成功（gRPC/HMAC 另说，此处只验传输层）。
	pair, err := tls.LoadX509KeyPair(cliCert, cliKey)
	require.NoError(t, err)
	syncWithCert := &tls.Config{RootCAs: roots, ServerName: "localhost", Certificates: []tls.Certificate{pair}}
	c, err := tls.Dial("tcp", syncLis.Addr().String(), syncWithCert)
	require.NoError(t, err, "持 CA 签发客户端证书应通过 policysync 传输层校验")
	c.Close()

	// Console 监听器（HTTPS，共享 srvTLS，不要求客户端证书）：无证书 GET /login → 200。
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}}
	consoleURL := "https://localhost:" + portOf(t, consoleLis) + "/login"
	resp, err := client.Get(consoleURL)
	require.NoError(t, err, "Console 监听器不应要求客户端证书（分通道隔离）")
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}

// portOf 从监听器地址提取端口字符串（用于以 ServerName=localhost 构造 https URL）。
func portOf(t *testing.T, ln net.Listener) string {
	t.Helper()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return port
}
```

> 说明：以 `ServerName: "localhost"` + `https://localhost:<port>` 拨号，令服务端证书 SAN（含 localhost/127.0.0.1）校验通过。REST 面不单独断言（Console 已代表共享 srvTLS 的人面通道；admin 已断言 gRPC 面）。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/app/ -run TestRun_SyncChannelRequiresClientCert -v`
预期：FAIL——当前 run.go 让 policysync 与 admin 共用 `srvTLS`（不要求客户端证书），故 `syncNoCert` 握手会成功，触发 `t.Fatal("...无证书握手不该成功")`。

- [ ] **步骤 3：编写实现**

`internal/controlplane/app/run.go`：将 `srvTLS` 构造块（当前 73-81 行）之后、`m := obs.New()` 之前，插入 `syncTLS` 派生与 `syncGrpcOpts`：

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

	// M5.2b：仅 policysync（机对机策略同步）通道派生要求客户端证书的 mTLS 变体；
	// admin/REST/Console 继续用共享 srvTLS（人面不破）。CA 空 → syncTLS==srvTLS，行为不变。
	syncTLS, err := tlsconfig.MutualServer(srvTLS, cfg.SyncClientCAFile)
	if err != nil {
		return fmt.Errorf("policysync mtls: %w", err) // fail-close：CA 设但无服务端 TLS / 无效 PEM 即拒绝启动
	}
	syncGrpcOpts := grpcOpts
	if syncTLS != srvTLS {
		syncGrpcOpts = []grpc.ServerOption{grpc.Creds(credentials.NewTLS(syncTLS))}
		logger.Info("policysync mTLS enabled (client cert required)")
	}
```

然后把 policysync 服务端构造（当前 93 行）由 `grpcOpts` 改为 `syncGrpcOpts`：

```go
	syncSrv := policysync.NewGRPCServer(syncCore, appResolver, m, syncGrpcOpts...)
```

`mgmt.NewGRPCServer(... grpcOpts...)`（admin 面）与 REST/Console 的 `srvTLS` 用法保持不变。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/app/ -run 'TestRun_SyncChannelRequiresClientCert|TestRun_WiringEndToEnd' -v`
预期：两者均 PASS（新测证明分通道隔离；既有 `TestRun_WiringEndToEnd` 证明 mTLS 关闭时明文装配未受影响，向后兼容）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/run.go internal/controlplane/app/run_test.go
git commit -m "feat(cp): M5.2b 接线 policysync 专用 mTLS(MutualServer 派生 syncTLS,仅 sync 监听器要求客户端证书,admin/REST/Console 用共享 srvTLS 不变)+分通道隔离装配测试"
```

---

## 任务 6：边车配置 `ControlPlaneClientCertFile`/`ControlPlaneClientKeyFile`

**文件：**
- 修改：`internal/sidecar/app/config.go`
- 测试：`internal/sidecar/app/config_test.go`

- [ ] **步骤 1：编写失败的配置测试**

在 `internal/sidecar/app/config_test.go` 追加（若不存在则创建，package `app_test`，import `os`/`path/filepath`/`testing` 与 `github.com/nickZFZ/Sydom/internal/sidecar/app`）：

```go
func TestLoadConfig_ControlPlaneClientCert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sc.yaml")
	yaml := "" +
		"control_plane_addr: 127.0.0.1:8081\n" +
		"app_key: app-1\n" +
		"domain: tenant-a\n" +
		"auth_addr: 127.0.0.1:8090\n" +
		"control_plane_tls: true\n" +
		"control_plane_ca_file: /etc/sydom/cp-ca.pem\n" +
		"control_plane_client_cert_file: /etc/sydom/sidecar.crt\n" +
		"control_plane_client_key_file: /etc/sydom/sidecar.key\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "SYDOM_APP_SECRET" {
			return "app-secret"
		}
		return ""
	}
	cfg, err := app.LoadConfig(path, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlPlaneClientCertFile != "/etc/sydom/sidecar.crt" {
		t.Fatalf("want client cert file, got %q", cfg.ControlPlaneClientCertFile)
	}
	if cfg.ControlPlaneClientKeyFile != "/etc/sydom/sidecar.key" {
		t.Fatalf("want client key file, got %q", cfg.ControlPlaneClientKeyFile)
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/app/ -run TestLoadConfig_ControlPlaneClientCert -v`
预期：编译失败（字段未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/app/config.go`：`Config` 结构在 `ControlPlaneCAFile` 后加：

```go
	ControlPlaneTLS            bool   // dial 控制面 sync 是否走 TLS
	ControlPlaneCAFile         string // 信任 CA；空=系统根
	ControlPlaneClientCertFile string // mTLS 客户端证书；与 KeyFile 须同设（tlsconfig.MutualClient 校验）；空=不出示
	ControlPlaneClientKeyFile  string
	HealthAddr                 string // 空=不起健康口
```

`fileConfig` 结构在 `ControlPlaneCAFile` 后加：

```go
	ControlPlaneTLS            bool   `yaml:"control_plane_tls"`
	ControlPlaneCAFile         string `yaml:"control_plane_ca_file"`
	ControlPlaneClientCertFile string `yaml:"control_plane_client_cert_file"`
	ControlPlaneClientKeyFile  string `yaml:"control_plane_client_key_file"`
	HealthAddr                 string `yaml:"health_addr"`
```

`LoadConfig` 的 `cfg := Config{...}` 字面量在 `ControlPlaneCAFile: fc.ControlPlaneCAFile,` 后加：

```go
		ControlPlaneClientCertFile: fc.ControlPlaneClientCertFile,
		ControlPlaneClientKeyFile:  fc.ControlPlaneClientKeyFile,
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/app/ -run TestLoadConfig_ControlPlaneClientCert -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/app/config.go internal/sidecar/app/config_test.go
git commit -m "feat(sidecar): M5.2b 边车配置 control_plane_client_cert_file/key_file(mTLS 客户端证书,opt-in 加法)"
```

---

## 任务 7：边车 `buildSyncConfig` 改用 `MutualClient`

**文件：**
- 修改：`internal/sidecar/app/run.go:175-182`
- 测试：`internal/sidecar/app/run_test.go`（或新建 `mtls_test.go`，package `app` 白盒测试以调用未导出的 `buildSyncConfig`）

- [ ] **步骤 1：编写失败的测试**

新建 `internal/sidecar/app/mtls_test.go`（package `app`，白盒；import `crypto/x509`/`testing` 与 `github.com/nickZFZ/Sydom/internal/certtest`）：

```go
package app

import (
	"crypto/x509"
	"testing"

	"github.com/nickZFZ/Sydom/internal/certtest"
)

func TestBuildSyncConfig_WithClientCertSucceeds(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	cfg := Config{
		ControlPlaneAddr:           "127.0.0.1:8081",
		ControlPlaneTLS:            true,
		ControlPlaneCAFile:         ca.File(),
		ControlPlaneClientCertFile: cliCert,
		ControlPlaneClientKeyFile:  cliKey,
	}
	sc, err := buildSyncConfig(cfg)
	if err != nil {
		t.Fatalf("配齐客户端证书应成功: %v", err)
	}
	if !sc.Secure {
		t.Fatal("走 TLS 时 Secure 应为 true")
	}
	if len(sc.DialOptions) != 1 {
		t.Fatalf("应注入一个传输凭据 DialOption, got %d", len(sc.DialOptions))
	}
}

func TestBuildSyncConfig_PartialClientCertFailsClose(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, _ := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	cfg := Config{
		ControlPlaneAddr:           "127.0.0.1:8081",
		ControlPlaneTLS:            true,
		ControlPlaneCAFile:         ca.File(),
		ControlPlaneClientCertFile: cliCert, // 缺 key
	}
	if _, err := buildSyncConfig(cfg); err == nil {
		t.Fatal("客户端证书半配置应 fail-close 返错")
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/app/ -run TestBuildSyncConfig -v`
预期：`TestBuildSyncConfig_PartialClientCertFailsClose` FAIL——当前 `buildSyncConfig` 用 `tlsconfig.Client(...)` 忽略客户端证书字段，半配置不会返错。

- [ ] **步骤 3：编写实现**

`internal/sidecar/app/run.go` 的 `buildSyncConfig` 中，将：

```go
		cliTLS, err := tlsconfig.Client(cfg.ControlPlaneCAFile)
```

改为：

```go
		cliTLS, err := tlsconfig.MutualClient(cfg.ControlPlaneCAFile, cfg.ControlPlaneClientCertFile, cfg.ControlPlaneClientKeyFile)
```

其余（`sc.Secure = true`、`sc.DialOptions = ...`）不变。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/app/ -run TestBuildSyncConfig -v`
预期：两测均 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/app/run.go internal/sidecar/app/mtls_test.go
git commit -m "feat(sidecar): M5.2b buildSyncConfig 改用 MutualClient 出示客户端证书(半配置 fail-close);未配证书时等价既有 Client 向后兼容"
```

---

## 任务 8：MT-1..7 验收核验 + 全量回归

**文件：** 无生产码改动；本任务为验证关卡。

- [ ] **步骤 1：MT-1 零触碰授权核心（机器验证）**

运行：
```bash
git diff --numstat f9afd0b..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/sidecar/authz/ internal/auth/ internal/obs/
```
预期：**空输出**（无任何行）。若有输出即违反铁律，须回退相应改动。

- [ ] **步骤 2：MT-2 向后兼容**

确认 `TestRun_WiringEndToEnd`（明文、无 mTLS 配置）仍 PASS：
```bash
go test ./internal/controlplane/app/ -run TestRun_WiringEndToEnd -v
```
预期：PASS——证明三个新配置项皆空时装配行为与 BASE 一致。

- [ ] **步骤 3：MT-3/MT-4/MT-5 复核**

运行：
```bash
go test ./internal/tlsconfig/ ./internal/certtest/ -v
go test ./internal/controlplane/app/ -run TestRun_SyncChannelRequiresClientCert -v
go test ./internal/sidecar/app/ -run 'TestBuildSyncConfig|TestLoadConfig_ControlPlaneClientCert' -v
```
预期：全 PASS。对照：MT-3=fail-close 三分支（`TestMutualServerNilBaseFailsClose`/`TestMutualServerBadCAFailsClose`/`TestMutualClientPartialFailsClose`/`TestBuildSyncConfig_PartialClientCertFailsClose`）；MT-4=`TestMutualServerEnforcesClientCert`（含反向验证）；MT-5=`TestRun_SyncChannelRequiresClientCert`（admin/Console 不要求）。

- [ ] **步骤 4：MT-6 无 CN↔app_id 耦合（人工复核）**

复核 `MutualServer` 仅设 `ClientAuth=RequireAndVerifyClientCert`+`ClientCAs`，不读取/比对证书 CN/SAN 与 app_id；HMAC 身份链未改。确认无对 `adminauthz`/`internal/auth` 的引用新增。

- [ ] **步骤 5：MT-7 全量回归**

运行：`go test ./...`
预期：EXIT 0，全绿。

- [ ] **步骤 6：更新 runbook/文档（若存在）**

若 `docs/` 下有部署 runbook，追加 mTLS 启用说明：控制面设 `sync_client_ca_file`（须已设 `tls_cert_file`/`tls_key_file`），边车设 `control_plane_client_cert_file`/`control_plane_client_key_file`；证书由同一 CA 签发。无 runbook 则跳过（不新建）。

- [ ] **步骤 7：Commit（若步骤 6 有改动）**

```bash
git add -A
git commit -m "docs(m5.2b): mTLS 启用说明(控制面 sync_client_ca_file + 边车客户端证书,同 CA 签发)"
```

---

## 自检

**1. 规格覆盖度：**
- §3.1 `MutualServer`/`MutualClient` → 任务 2。
- §3.2 三配置项 → 任务 4（控制面）+ 任务 6（边车）。
- §3.3 装配（拆 srvTLS/syncTLS + syncGrpcOpts）→ 任务 5；边车 buildSyncConfig → 任务 7。
- §4 fail-close 四情形 → 任务 2 单测（前三）+ 任务 5 装配（CA 无服务端 TLS 经 run.go 返错路径，由 `TestMutualServerNilBaseFailsClose` 覆盖 tlsconfig 层）+ 任务 7（边车半配置）。
- §6 测试策略 → 任务 1（certtest）+ 任务 2/3（tlsconfig 单测+集成）+ 任务 5（wiring）+ 任务 7（buildSyncConfig）。
- §7 MT-1..7 → 任务 8。
- 全覆盖，无遗漏。

**2. 占位符扫描：** 所有步骤均含完整可运行代码（certtest 自检已用标准库 `pem.Decode`），无 TODO/待定/伪代码。

**3. 类型一致性：** `certtest.NewCA(t) *CA`、`(*CA).File() string`、`(*CA).Leaf(t, cn, ...eku) (certFile, keyFile string)` 在任务 2/3/5/6/7 用法一致；`tlsconfig.MutualServer(base *tls.Config, clientCAFile string)`、`tlsconfig.MutualClient(caFile, certFile, keyFile string)` 签名在定义（任务 2）与调用（任务 5 run.go、任务 7 buildSyncConfig）处一致；配置字段名 `SyncClientCAFile`/`ControlPlaneClientCertFile`/`ControlPlaneClientKeyFile` 在定义与测试中一致。
