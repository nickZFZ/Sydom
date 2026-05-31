# 司域 gRPC 同步协议契约 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 实现司域控制面 ↔ Sidecar 之间策略下发的 gRPC 协议契约层——proto 契约与生成代码、AppSecret 对称加解密、HMAC 签名/验签拦截器（强制 app_id 隔离），以及配套 migration 000011。

**架构：** 用 buf（自带 protocompile，无需系统 protoc）从 `api/proto/sync/v1/policy_sync.proto` 生成 Go 代码到 `gen/sync/v1`。`internal/crypto` 提供 AES-256-GCM 加解密（控制面存 `app_secret_enc` 用）。`internal/auth` 提供纯函数 HMAC 签名/验签、客户端 `PerRPCCredentials`、服务端 unary+stream 拦截器（校验后把已认证 app_id 注入 context，强制后续隔离）。拦截器经 `SecretResolver` 接口取 AppSecret 原文，DB 解密实现留给控制面 spec。migration 000011 把 `application.app_secret_hash` 改为 `app_secret_enc BYTEA`。

**技术栈：** Go 1.26.3、buf v1.34.0、`google.golang.org/protobuf` v1.33.0、`google.golang.org/grpc` v1.59.0（均已在 go.mod 间接依赖）、stdlib `crypto/aes`·`crypto/cipher`·`crypto/hmac`·`crypto/sha256`、`grpc/test/bufconn`（集成测试）、testify、golang-migrate（已有）。

**边界（spec §9，本计划不做）：** delta 生成逻辑、Redis Pub/Sub 扇出、Sidecar apply 细节、主密钥轮转/KMS 选型。本计划只交付"契约 + 认证 + 加解密原语"，PolicySync 服务端业务逻辑不实现（集成测试用 stub 验证拦截器与生成代码可用）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `db/migrations/000011_application_secret_enc.up.sql` | 把 `application.app_secret_hash` 改为 `app_secret_enc BYTEA` |
| `db/migrations/000011_application_secret_enc.down.sql` | 回滚为 `app_secret_hash VARCHAR(255)` |
| `internal/db/helpers_test.go`（改） | `seedApp` 改插 `app_secret_enc` |
| `internal/db/identity_schema_test.go`（改） | `TestApplication_Constraints` 改用 `app_secret_enc`；新增列类型断言测试 |
| `internal/crypto/aesgcm.go` | AES-256-GCM `Encrypt`/`Decrypt` |
| `internal/crypto/aesgcm_test.go` | 加解密往返、错误密钥、篡改检测、nonce 随机性 |
| `api/proto/sync/v1/policy_sync.proto` | PolicySync 服务的 proto 契约 |
| `buf.yaml` | buf 模块与 lint 配置 |
| `buf.gen.yaml` | buf 代码生成配置 |
| `gen/sync/v1/policy_sync.pb.go`（生成） | 消息类型 |
| `gen/sync/v1/policy_sync_grpc.pb.go`（生成） | 服务 client/server 桩 |
| `internal/wire/doc.go` + `internal/wire/roundtrip_test.go` | 线格式往返契约测试 |
| `internal/auth/signature.go` | HMAC 签名串、`Sign`/`Verify`、metadata key 常量 |
| `internal/auth/signature_test.go` | 签名确定性、验签真/假 |
| `internal/auth/resolver.go` | `SecretResolver` 接口 |
| `internal/auth/context.go` | app_id 注入/读取 context |
| `internal/auth/interceptor.go` | 服务端 unary+stream 验签拦截器 |
| `internal/auth/interceptor_test.go` | `authenticate` 单元测试（时间窗、缺字段、错签名） |
| `internal/auth/credentials.go` | 客户端 `PerRPCCredentials` |
| `internal/auth/integration_test.go` | bufconn 端到端：client 签名 → server 拦截器 → app_id 注入 |
| `Makefile`（改） | 新增 `proto-tools`/`proto-lint`/`proto-gen` 目标 |

任务顺序：先 000011（关闭跨子项目 schema 欠账，影响面最小）→ AES-GCM（与 enc 列配套）→ proto 工具链与生成 → HMAC 纯函数 → 拦截器与 bufconn 集成（依赖前两者）。

---

## 任务 1：migration 000011 —— app_secret_hash → app_secret_enc

**背景：** HMAC 验签要求控制面持有 AppSecret 原文，原 schema 存不可逆的 `app_secret_hash` 无法验签。改为 `app_secret_enc BYTEA`（存 AES-GCM 密文）。schema 尚未上线，无数据迁移负担。此改动会破坏现有插入 `app_secret_hash` 的测试，必须在本任务同步修复。

**文件：**
- 创建：`db/migrations/000011_application_secret_enc.up.sql`
- 创建：`db/migrations/000011_application_secret_enc.down.sql`
- 修改：`internal/db/helpers_test.go`（`seedApp`）
- 修改：`internal/db/identity_schema_test.go`（`TestApplication_Constraints` + 新增测试）

- [ ] **步骤 1：编写失败的测试**

在 `internal/db/identity_schema_test.go` 末尾新增：

```go
func TestApplication_SecretColumnIsEnc(t *testing.T) {
	db := setupSchema(t)

	// 新列 app_secret_enc 必须是 bytea
	var dataType string
	require.NoError(t, db.QueryRow(`
		SELECT data_type FROM information_schema.columns
		WHERE table_name = 'application' AND column_name = 'app_secret_enc'`).Scan(&dataType))
	require.Equal(t, "bytea", dataType)

	// 旧列 app_secret_hash 必须已被移除
	var n int
	require.NoError(t, db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'application' AND column_name = 'app_secret_hash'`).Scan(&n))
	require.Equal(t, 0, n)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestApplication_SecretColumnIsEnc -v`
预期：FAIL —— `app_secret_enc` 不存在（data_type 查询 Scan 到空，断言不等于 "bytea"）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000011_application_secret_enc.up.sql`：

```sql
ALTER TABLE application DROP COLUMN app_secret_hash;
ALTER TABLE application ADD COLUMN app_secret_enc BYTEA NOT NULL;
```

`db/migrations/000011_application_secret_enc.down.sql`：

```sql
ALTER TABLE application DROP COLUMN app_secret_enc;
ALTER TABLE application ADD COLUMN app_secret_hash VARCHAR(255) NOT NULL;
```

> 注：迁移在空表上执行（migration 跑在新建 DB），`ADD COLUMN ... NOT NULL` 无需默认值即可成功。

- [ ] **步骤 4：修复因列改名而破坏的测试**

`internal/db/helpers_test.go` 中 `seedApp` 的 INSERT 改为：

```go
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1, 'order-system', '订单系统', 'AK_order', '\xab'::bytea) RETURNING id`,
		tenantID).Scan(&appID))
```

`internal/db/identity_schema_test.go` 中 `TestApplication_Constraints` 的四处 INSERT 同步把列名 `app_secret_hash` → `app_secret_enc`、值 `'hashN'` → `'\xab'::bytea`。改完后该函数为：

```go
func TestApplication_Constraints(t *testing.T) {
	db := setupSchema(t)

	var tenantID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))

	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'order-system', '订单系统', 'AK_order', '\xab'::bytea)`, tenantID)
	require.NoError(t, err)

	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE app_key = 'AK_order'`).Scan(&ver))
	require.Equal(t, int64(0), ver)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'other', '其他', 'AK_order', '\xab'::bytea)`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'order-system', '重复域', 'AK_dup', '\xab'::bytea)`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES (999999, 'x', 'x', 'AK_x', '\xab'::bytea)`)
	require.Error(t, err)
}
```

- [ ] **步骤 5：运行全量 db 测试验证通过**

运行：`go test ./internal/db/ -v`
预期：PASS（含新 `TestApplication_SecretColumnIsEnc`、修复后的 `TestApplication_Constraints`、不受影响的 `TestMigrations_UpDownRoundTrip`）。

- [ ] **步骤 6：Commit**

```bash
git add db/migrations/000011_application_secret_enc.up.sql \
        db/migrations/000011_application_secret_enc.down.sql \
        internal/db/helpers_test.go internal/db/identity_schema_test.go
git commit -m "feat(db): migration 000011 将 app_secret_hash 改为 app_secret_enc(BYTEA)"
```

---

## 任务 2：AES-256-GCM 加解密 helper

**背景：** 控制面把 AppSecret 原文用主密钥加密后存入 `app_secret_enc`，验签时解密取回原文。本任务实现对称加解密原语。主密钥由进程外部（环境变量/KMS）注入，绝不入库——本任务只提供算法，密钥注入归控制面/运维 spec。

**文件：**
- 创建：`internal/crypto/aesgcm.go`
- 测试：`internal/crypto/aesgcm_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/crypto/aesgcm_test.go`：

```go
package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := mustKey(t)
	plain := []byte("super-secret-app-secret")

	blob, err := Encrypt(key, plain)
	require.NoError(t, err)
	require.NotEqual(t, plain, blob) // 密文不等于明文

	got, err := Decrypt(key, blob)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestEncrypt_NonceIsRandom(t *testing.T) {
	key := mustKey(t)
	plain := []byte("same-input")

	a, err := Encrypt(key, plain)
	require.NoError(t, err)
	b, err := Encrypt(key, plain)
	require.NoError(t, err)
	require.False(t, bytes.Equal(a, b)) // 相同明文两次加密因随机 nonce 而不同
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	blob, err := Encrypt(mustKey(t), []byte("x"))
	require.NoError(t, err)

	_, err = Decrypt(mustKey(t), blob) // 另一把钥匙
	require.Error(t, err)
}

func TestDecrypt_TamperedFails(t *testing.T) {
	key := mustKey(t)
	blob, err := Encrypt(key, []byte("payload"))
	require.NoError(t, err)

	blob[len(blob)-1] ^= 0xff // 翻转最后一字节（GCM 认证标签）
	_, err = Decrypt(key, blob)
	require.Error(t, err)
}

func TestBadKeySize(t *testing.T) {
	_, err := Encrypt([]byte("short"), []byte("x"))
	require.ErrorIs(t, err, ErrKeySize)
	_, err = Decrypt([]byte("short"), []byte("x"))
	require.ErrorIs(t, err, ErrKeySize)
}

func TestDecrypt_TooShort(t *testing.T) {
	_, err := Decrypt(mustKey(t), []byte{0x01})
	require.ErrorIs(t, err, ErrCiphertext)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/crypto/ -v`
预期：FAIL —— `package crypto` 编译失败（`Encrypt`/`Decrypt`/`KeySize`/`ErrKeySize`/`ErrCiphertext` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/crypto/aesgcm.go`：

```go
// Package crypto 提供司域控制面对敏感字段（如 AppSecret）的对称加解密。
// 主密钥由进程外部（环境变量 / KMS）注入，绝不入库。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// KeySize 是 AES-256 要求的主密钥字节数。
const KeySize = 32

var (
	// ErrKeySize 表示主密钥长度不是 32 字节。
	ErrKeySize = errors.New("crypto: master key must be 32 bytes")
	// ErrCiphertext 表示密文长度不足以容纳 nonce。
	ErrCiphertext = errors.New("crypto: ciphertext too short")
)

// Encrypt 用 AES-256-GCM 加密 plaintext，返回 nonce||ciphertext||tag。
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// Seal 把密文追加到 dst(=nonce) 之后，得到 nonce||ciphertext||tag。
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt 解密 Encrypt 产出的 nonce||ciphertext||tag。
func Decrypt(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, ErrCiphertext
	}
	nonce, ct := blob[:ns], blob[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/crypto/ -v`
预期：PASS（6 个测试全过）。

- [ ] **步骤 5：Commit**

```bash
git add internal/crypto/aesgcm.go internal/crypto/aesgcm_test.go
git commit -m "feat(crypto): AES-256-GCM 加解密 helper（AppSecret 存储用）"
```

---

## 任务 3：buf 工具链 + proto 契约 + 代码生成

**背景：** 用 buf 生成 gRPC Go 代码。buf 自带 protocompile，不需要系统装 protoc，只需 `protoc-gen-go`/`protoc-gen-go-grpc` 两个 Go 插件（go install）。生成代码提交入库，使 `go build` 无需重新生成。

**文件：**
- 创建：`api/proto/sync/v1/policy_sync.proto`
- 创建：`buf.yaml`、`buf.gen.yaml`
- 生成：`gen/sync/v1/policy_sync.pb.go`、`gen/sync/v1/policy_sync_grpc.pb.go`
- 创建：`internal/wire/doc.go`、`internal/wire/roundtrip_test.go`
- 修改：`Makefile`

- [ ] **步骤 1：安装 buf 与插件**

运行：

```bash
go install github.com/bufbuild/buf/cmd/buf@v1.34.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.33.0
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0
```

确认安装位置（buf 与插件需同在 PATH 上）：

```bash
ls "$(go env GOPATH)/bin"
```
预期：列出 `buf`、`protoc-gen-go`、`protoc-gen-go-grpc`。

> 若 `@v1.34.0` 不可用，可换成最近的 buf v1.x 版本；插件版本须与 go.mod 一致（protobuf v1.33.0 / go-grpc v1.3.0）。

- [ ] **步骤 2：编写 proto 契约**

`api/proto/sync/v1/policy_sync.proto`：

```protobuf
syntax = "proto3";

package sydom.sync.v1;

option go_package = "github.com/nickZFZ/Sydom/gen/sync/v1;syncv1";

// PolicySync 是控制面向 Sidecar 下发策略的同步服务。
// 控制面实现，Sidecar 调用。所有 RPC 均需 HMAC 认证。
service PolicySync {
  // Subscribe 订阅策略变更流。Sidecar 启动后调一次并长连保持，
  // 控制面持续推送 delta / heartbeat / snapshot_required。
  rpc Subscribe(SubscribeRequest) returns (stream SyncEvent);

  // PullSnapshot 拉取该 app 全量策略快照。
  // 冷启动、版本断档、收到 SnapshotRequired 时调用。
  rpc PullSnapshot(PullSnapshotRequest) returns (Snapshot);
}

// SubscribeRequest 携带 Sidecar 已应用到的版本号；冷启动为 0。
// app_id 由认证凭据强制，不在请求体。
message SubscribeRequest {
  uint64 last_applied_version = 1;
}

// PullSnapshotRequest 为空——app_id 由认证凭据强制，快照总返回该 app 当前全量态。
message PullSnapshotRequest {}

// SyncEvent 是 Subscribe 流推送的事件包络。
message SyncEvent {
  oneof event {
    Delta delta = 1;                        // 实时增量变更
    Heartbeat heartbeat = 2;                // ~30s 版本心跳（反熵）
    SnapshotRequired snapshot_required = 3; // 控制面要求 Sidecar 全量对齐
  }
}

// Delta 对应控制面一次策略变更事务的产物（一个新版本号，原子整体 apply）。
message Delta {
  uint64 version = 1;                         // 本次变更后的 app 版本号（单调递增）
  repeated PolicyChange policy_changes = 2;   // casbin_rule 行的增删改
  repeated DataPolicyChange data_changes = 3; // data_policy 的增删改
}

// ChangeOp 是变更操作类型。
enum ChangeOp {
  CHANGE_OP_UNSPECIFIED = 0; // proto3 要求首值为 0
  CHANGE_OP_ADD = 1;
  CHANGE_OP_REMOVE = 2;
  CHANGE_OP_UPDATE = 3;
}

// PolicyChange 是 casbin 策略行变更（对应 casbin_rule 表）。
message PolicyChange {
  ChangeOp op = 1;
  PolicyRule rule = 2;     // ADD/UPDATE 的新行
  PolicyRule old_rule = 3; // UPDATE 的旧行；REMOVE 时为待删行
}

// PolicyRule 是一条 casbin 策略行。
message PolicyRule {
  string ptype = 1;           // "p" / "g"
  repeated string values = 2; // v0..v5，变长（casbin []string 风格，尾部空串可省）
}

// DataPolicyChange 是数据权限变更（对应 data_policy 表）。
message DataPolicyChange {
  ChangeOp op = 1;
  DataPolicy policy = 2;
}

// DataPolicy 是一条数据权限规则。
message DataPolicy {
  uint64 id = 1;           // data_policy.id；REMOVE/UPDATE 靠它定位（无自然唯一键）
  string subject_type = 2; // "role" / "user"
  string subject_id = 3;
  string resource = 4;
  string condition = 5;    // 条件树 JSON 字符串（协议层不透明传输）
}

// Heartbeat 是反熵心跳，携带该 app 当前 max 版本号。
message Heartbeat {
  uint64 current_version = 1;
}

// SnapshotRequired 要求 Sidecar 调 PullSnapshot 全量对齐。
message SnapshotRequired {
  uint64 current_version = 1; // 当前版本，供 Sidecar 日志/决策
  string reason = 2;          // "behind" / "reconnect" 等，便于排查
}

// Snapshot 是 PullSnapshot 返回的全量策略快照。
message Snapshot {
  uint64 version = 1;                    // 快照对应的 app 当前版本号
  repeated PolicyRule rules = 2;         // 全量 casbin 策略行（p + g）
  repeated DataPolicy data_policies = 3; // 全量 data_policy
}
```

> 对 spec §3 的两处刻意细化（满足 buf STANDARD lint，不改变语义）：枚举值加 `CHANGE_OP_` 前缀（ENUM_VALUE_PREFIX 规则）；服务名 `PolicySync` 保留为公开契约名，在 buf.yaml 中 except `SERVICE_SUFFIX`。

- [ ] **步骤 3：编写 buf 配置**

`buf.yaml`：

```yaml
version: v2
modules:
  - path: api/proto
lint:
  use:
    - STANDARD
  except:
    # 服务名 PolicySync 是 spec 定义的公开契约名，不加 Service 后缀
    - SERVICE_SUFFIX
breaking:
  use:
    - FILE
```

`buf.gen.yaml`：

```yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
  - local: protoc-gen-go-grpc
    out: gen
    opt: paths=source_relative
```

> 模块根设为 `api/proto`，故 proto 在模块内路径为 `sync/v1/policy_sync.proto`；配合 `paths=source_relative` 与 `out: gen`，生成到 `gen/sync/v1/`，与 `go_package` 的 `gen/sync/v1;syncv1` 一致。

- [ ] **步骤 4：lint 并生成**

运行：

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
buf lint
buf generate
```
预期：`buf lint` 无输出（通过）；`buf generate` 产出 `gen/sync/v1/policy_sync.pb.go` 与 `gen/sync/v1/policy_sync_grpc.pb.go`。

- [ ] **步骤 5：将 grpc/protobuf 提升为直接依赖**

运行：

```bash
go mod tidy
```
预期：`go.mod` 中 `google.golang.org/grpc` 与 `google.golang.org/protobuf` 从 `// indirect` 变为直接依赖（被生成代码 import）。

- [ ] **步骤 6：编写线格式往返契约测试**

`internal/wire/doc.go`：

```go
// Package wire 校验 gRPC 线格式契约的往返一致性（序列化 → 反序列化保真）。
package wire
```

`internal/wire/roundtrip_test.go`：

```go
package wire

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sync/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSyncEvent_DeltaRoundTrip(t *testing.T) {
	ev := &syncv1.SyncEvent{
		Event: &syncv1.SyncEvent_Delta{
			Delta: &syncv1.Delta{
				Version: 42,
				PolicyChanges: []*syncv1.PolicyChange{{
					Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
					Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"alice", "data1", "read"}},
				}},
				DataChanges: []*syncv1.DataPolicyChange{{
					Op: syncv1.ChangeOp_CHANGE_OP_REMOVE,
					Policy: &syncv1.DataPolicy{
						Id: 7, SubjectType: "role", SubjectId: "admin",
						Resource: "order", Condition: `{"op":"eq"}`,
					},
				}},
			},
		},
	}

	raw, err := proto.Marshal(ev)
	require.NoError(t, err)

	got := &syncv1.SyncEvent{}
	require.NoError(t, proto.Unmarshal(raw, got))

	require.True(t, proto.Equal(ev, got))
	// 变长 values 保真（casbin []string 语义）
	require.Equal(t, []string{"alice", "data1", "read"},
		got.GetDelta().GetPolicyChanges()[0].GetRule().GetValues())
}

func TestSyncEvent_HeartbeatOneof(t *testing.T) {
	ev := &syncv1.SyncEvent{
		Event: &syncv1.SyncEvent_Heartbeat{Heartbeat: &syncv1.Heartbeat{CurrentVersion: 99}},
	}
	raw, err := proto.Marshal(ev)
	require.NoError(t, err)

	got := &syncv1.SyncEvent{}
	require.NoError(t, proto.Unmarshal(raw, got))
	require.Equal(t, uint64(99), got.GetHeartbeat().GetCurrentVersion())
	require.Nil(t, got.GetDelta()) // oneof 互斥
}
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go build ./... && go test ./internal/wire/ -v`
预期：编译通过（生成代码可用）；2 个往返测试 PASS。

- [ ] **步骤 8：更新 Makefile**

在 `Makefile` 中追加（注意 Makefile 用 Tab 缩进）：

```makefile
GOBIN := $(shell go env GOPATH)/bin
BUF_VERSION := v1.34.0

# 安装 proto 工具链（buf 自带 protocompile，无需系统 protoc）
proto-tools:
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.33.0
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

proto-lint:
	PATH="$(GOBIN):$$PATH" buf lint

proto-gen:
	PATH="$(GOBIN):$$PATH" buf generate
```

并把 `.PHONY` 行更新为：

```makefile
.PHONY: migrate-up migrate-down test proto-tools proto-lint proto-gen
```

- [ ] **步骤 9：Commit**

```bash
git add api/proto buf.yaml buf.gen.yaml gen internal/wire Makefile go.mod go.sum
git commit -m "feat(proto): PolicySync gRPC 契约 + buf 工具链 + 生成代码"
```

---

## 任务 4：HMAC 签名/验签纯函数

**背景：** 认证用 `HMAC-SHA256(AppSecret, "<app_id>\n<timestamp>\n<full_method>")` 的 hex。本任务实现纯函数签名与常量时间验签，以及 metadata key 常量。拦截器（任务 5）与客户端凭据（任务 5）都复用这些函数。

**文件：**
- 创建：`internal/auth/signature.go`
- 测试：`internal/auth/signature_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/auth/signature_test.go`：

```go
package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSign_Deterministic(t *testing.T) {
	secret := []byte("s3cr3t")
	a := Sign(secret, "AK_order", 1700000000, "/sydom.sync.v1.PolicySync/PullSnapshot")
	b := Sign(secret, "AK_order", 1700000000, "/sydom.sync.v1.PolicySync/PullSnapshot")
	require.Equal(t, a, b)         // 同输入同输出
	require.Len(t, a, 64)          // SHA-256 hex = 64 字符
}

func TestVerify_Match(t *testing.T) {
	secret := []byte("s3cr3t")
	const appID, ts, method = "AK_order", int64(1700000000), "/svc/M"
	sig := Sign(secret, appID, ts, method)
	require.True(t, Verify(secret, appID, ts, method, sig))
}

func TestVerify_RejectsTampering(t *testing.T) {
	secret := []byte("s3cr3t")
	const appID, ts, method = "AK_order", int64(1700000000), "/svc/M"
	sig := Sign(secret, appID, ts, method)

	require.False(t, Verify([]byte("wrong"), appID, ts, method, sig)) // 错密钥
	require.False(t, Verify(secret, "AK_other", ts, method, sig))     // 错 app_id
	require.False(t, Verify(secret, appID, ts+1, method, sig))        // 错时间戳
	require.False(t, Verify(secret, appID, ts, "/svc/Other", sig))    // 错方法
	require.False(t, Verify(secret, appID, ts, method, "deadbeef"))   // 错签名
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/auth/ -v`
预期：FAIL —— `package auth` 编译失败（`Sign`/`Verify` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/auth/signature.go`：

```go
// Package auth 实现司域控制面与 Sidecar 之间的 HMAC 认证：
// 客户端签名、服务端验签拦截器，以及对 app_id 的强制隔离。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// gRPC metadata key（均为小写）。
const (
	MDAppID     = "x-sydom-app-id"
	MDTimestamp = "x-sydom-timestamp"
	MDSignature = "x-sydom-signature"
)

// signingString 拼装待签名串：<app_id>\n<unix_ts>\n<full_method>。
func signingString(appID string, unixTS int64, method string) string {
	var b strings.Builder
	b.WriteString(appID)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(unixTS, 10))
	b.WriteByte('\n')
	b.WriteString(method)
	return b.String()
}

// Sign 用 AppSecret 对 (appID, ts, method) 计算 HMAC-SHA256，返回 hex。
func Sign(secret []byte, appID string, unixTS int64, method string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingString(appID, unixTS, method)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 以常量时间比对签名是否匹配（防时序侧信道）。
func Verify(secret []byte, appID string, unixTS int64, method, gotHex string) bool {
	want := Sign(secret, appID, unixTS, method)
	return hmac.Equal([]byte(want), []byte(gotHex))
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/auth/ -run 'TestSign|TestVerify' -v`
预期：PASS（3 个测试全过）。

- [ ] **步骤 5：Commit**

```bash
git add internal/auth/signature.go internal/auth/signature_test.go
git commit -m "feat(auth): HMAC-SHA256 签名/验签纯函数"
```

---

## 任务 5：服务端拦截器 + 客户端凭据 + bufconn 集成

**背景：** 服务端 unary+stream 拦截器从 metadata 取 HMAC 三件套，经 `SecretResolver` 拿 AppSecret 原文验签，校验时间窗（±5 分钟防重放），通过后把 app_id 注入 context（强制后续隔离，架构 I2）。客户端 `PerRPCCredentials` 为每个 RPC 自动签名注入 metadata。bufconn 集成测试端到端验证：客户端签名 → 服务端拦截器 → app_id 注入，且生成的服务桩可用。

**文件：**
- 创建：`internal/auth/resolver.go`、`internal/auth/context.go`、`internal/auth/interceptor.go`、`internal/auth/credentials.go`
- 测试：`internal/auth/interceptor_test.go`、`internal/auth/integration_test.go`

- [ ] **步骤 1：编写 SecretResolver 接口与 context helper（非 TDD，纯接口/胶水）**

`internal/auth/resolver.go`：

```go
package auth

import "context"

// SecretResolver 按 app_id 返回其 AppSecret 原文
// （控制面从 application.app_secret_enc 解密得到）。
// 本子项目只定义接口；DB 解密实现归控制面 spec。
type SecretResolver interface {
	ResolveSecret(ctx context.Context, appID string) (secret []byte, err error)
}
```

`internal/auth/context.go`：

```go
package auth

import "context"

type ctxKey struct{}

// WithAppID 把已认证的 app_id 注入 context。
func WithAppID(ctx context.Context, appID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, appID)
}

// AppIDFromContext 取出已认证 app_id；未认证返回 ("", false)。
func AppIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok
}
```

- [ ] **步骤 2：编写拦截器单元测试（失败）**

`internal/auth/interceptor_test.go`：

```go
package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, appID string) ([]byte, error) {
	s, ok := f[appID]
	if !ok {
		return nil, errors.New("app not found")
	}
	return s, nil
}

const testMethod = "/sydom.sync.v1.PolicySync/PullSnapshot"

func mdCtx(appID string, ts int64, sig string) context.Context {
	md := metadata.New(map[string]string{
		MDAppID:     appID,
		MDTimestamp: strconv.FormatInt(ts, 10),
		MDSignature: sig,
	})
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestAuthenticate_Success(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"AK_order": secret}
	now := time.Unix(1700000000, 0)
	sig := Sign(secret, "AK_order", now.Unix(), testMethod)

	ctx, err := authenticate(mdCtx("AK_order", now.Unix(), sig), res, testMethod, now)
	require.NoError(t, err)
	id, ok := AppIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "AK_order", id)
}

func TestAuthenticate_MissingMetadata(t *testing.T) {
	res := fakeResolver{}
	_, err := authenticate(context.Background(), res, testMethod, time.Now())
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_StaleTimestamp(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"AK_order": secret}
	signedAt := time.Unix(1700000000, 0)
	sig := Sign(secret, "AK_order", signedAt.Unix(), testMethod)

	// 服务端时钟比签名时间晚 10 分钟，超出 ±5 分钟窗口
	now := signedAt.Add(10 * time.Minute)
	_, err := authenticate(mdCtx("AK_order", signedAt.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_BadSignature(t *testing.T) {
	res := fakeResolver{"AK_order": []byte("s3cr3t")}
	now := time.Unix(1700000000, 0)
	_, err := authenticate(mdCtx("AK_order", now.Unix(), "deadbeef"), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_UnknownApp(t *testing.T) {
	res := fakeResolver{} // 空，解析必失败
	now := time.Unix(1700000000, 0)
	sig := Sign([]byte("x"), "AK_ghost", now.Unix(), testMethod)
	_, err := authenticate(mdCtx("AK_ghost", now.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/auth/ -run TestAuthenticate -v`
预期：FAIL —— `authenticate` 未定义。

- [ ] **步骤 4：编写服务端拦截器**

`internal/auth/interceptor.go`：

```go
package auth

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MaxClockSkew 是签名时间戳允许的前后偏移窗口（防重放）。
const MaxClockSkew = 5 * time.Minute

// authenticate 校验 metadata 中的 HMAC 凭据，成功返回带 app_id 的新 context。
func authenticate(ctx context.Context, resolver SecretResolver, method string, now time.Time) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	appID, tsStr, sig := first(md, MDAppID), first(md, MDTimestamp), first(md, MDSignature)
	if appID == "" || tsStr == "" || sig == "" {
		return nil, status.Error(codes.Unauthenticated, "missing auth fields")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "bad timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return nil, status.Error(codes.Unauthenticated, "timestamp out of window")
	}
	secret, err := resolver.ResolveSecret(ctx, appID)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "unknown app")
	}
	if !Verify(secret, appID, ts, method, sig) {
		return nil, status.Error(codes.Unauthenticated, "signature mismatch")
	}
	// 校验通过：强制后续一切操作使用此 app_id（架构 I2）。
	return WithAppID(ctx, appID), nil
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// UnaryServerInterceptor 校验一元 RPC 的 HMAC 凭据并注入 app_id。
func UnaryServerInterceptor(resolver SecretResolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		newCtx, err := authenticate(ctx, resolver, info.FullMethod, time.Now())
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamServerInterceptor 校验流式 RPC 的 HMAC 凭据并注入 app_id。
func StreamServerInterceptor(resolver SecretResolver) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := authenticate(ss.Context(), resolver, info.FullMethod, time.Now())
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
}

// wrappedStream 用已认证 context 覆盖原 ServerStream.Context()。
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
```

- [ ] **步骤 5：运行单元测试验证通过**

运行：`go test ./internal/auth/ -run 'TestAuthenticate|TestSign|TestVerify' -v`
预期：PASS。

- [ ] **步骤 6：编写客户端凭据**

`internal/auth/credentials.go`：

```go
package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
)

// perRPC 实现 grpc.PerRPCCredentials，为每个 RPC 注入 HMAC 三件套 metadata。
type perRPC struct {
	appID  string
	secret []byte
	secure bool
	now    func() time.Time
}

// NewPerRPCCredentials 构造 Sidecar 侧的 HMAC 凭据。
// secure 表示底层是否 TLS：非 TLS（本地/测试）须传 false 以允许明文凭据。
func NewPerRPCCredentials(appID string, secret []byte, secure bool) credentials.PerRPCCredentials {
	return &perRPC{appID: appID, secret: secret, secure: secure, now: time.Now}
}

func (c *perRPC) GetRequestMetadata(_ context.Context, uri ...string) (map[string]string, error) {
	method := ""
	if len(uri) > 0 {
		method = methodFromURI(uri[0])
	}
	ts := c.now().Unix()
	return map[string]string{
		MDAppID:     c.appID,
		MDTimestamp: strconv.FormatInt(ts, 10),
		MDSignature: Sign(c.secret, c.appID, ts, method),
	}, nil
}

func (c *perRPC) RequireTransportSecurity() bool { return c.secure }

// methodFromURI 从 grpc 传入的请求 URI 取 FullMethod 路径，
// 使其与服务端 info.FullMethod（形如 "/pkg.Service/Method"）一致。
func methodFromURI(uri string) string {
	if i := strings.Index(uri, "://"); i >= 0 {
		rest := uri[i+3:] // 去掉 scheme://
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:] // 去掉 authority，保留 /pkg.Service/Method
		}
	}
	return uri
}
```

- [ ] **步骤 7：编写 bufconn 集成测试（端到端）**

`internal/auth/integration_test.go`：

```go
package auth_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, appID string) ([]byte, error) {
	s, ok := f[appID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

// stubServer 是最小 PolicySync 实现，只把已认证 app_id 记录下来供断言。
type stubServer struct {
	syncv1.UnimplementedPolicySyncServer
	unaryAppID  string
	streamAppID string
}

func (s *stubServer) PullSnapshot(ctx context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	s.unaryAppID, _ = auth.AppIDFromContext(ctx)
	return &syncv1.Snapshot{Version: 1}, nil
}

func (s *stubServer) Subscribe(_ *syncv1.SubscribeRequest, ss syncv1.PolicySync_SubscribeServer) error {
	s.streamAppID, _ = auth.AppIDFromContext(ss.Context())
	return nil // 立即结束流，本测试只验证认证与 app_id 注入
}

func startServer(t *testing.T, res auth.SecretResolver) (*stubServer, *bufconn.Listener) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	)
	stub := &stubServer{}
	syncv1.RegisterPolicySyncServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return stub, lis
}

func dial(t *testing.T, lis *bufconn.Listener, creds grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		creds,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestIntegration_UnarySuccess(t *testing.T) {
	secret := []byte("s3cr3t")
	stub, lis := startServer(t, fakeResolver{"AK_order": secret})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", secret, false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.GetVersion())
	require.Equal(t, "AK_order", stub.unaryAppID) // app_id 被强制注入
}

func TestIntegration_StreamSuccess(t *testing.T) {
	secret := []byte("s3cr3t")
	stub, lis := startServer(t, fakeResolver{"AK_order": secret})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", secret, false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF) // stub 立即结束流
	require.Equal(t, "AK_order", stub.streamAppID)
}

func TestIntegration_WrongSecretRejected(t *testing.T) {
	stub, lis := startServer(t, fakeResolver{"AK_order": []byte("real")})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", []byte("wrong"), false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, stub.unaryAppID) // handler 未被调用
}

func TestIntegration_NoCredentialsRejected(t *testing.T) {
	_, lis := startServer(t, fakeResolver{"AK_order": []byte("real")})
	conn := dial(t, lis, grpc.EmptyDialOption{}) // 无 PerRPCCredentials

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

> `grpc.EmptyDialOption{}` 用作"无凭据"占位 DialOption（client 不附带 PerRPCCredentials），从而触发服务端 metadata 缺失 → Unauthenticated。

- [ ] **步骤 8：运行全部 auth 测试验证通过**

运行：`go test ./internal/auth/ -v`
预期：PASS（单元测试 + 5 个集成测试）。集成测试会真实跑通 client 签名 → server 验签，证明 `methodFromURI` 产出与 `info.FullMethod` 一致。

- [ ] **步骤 9：Commit**

```bash
git add internal/auth/resolver.go internal/auth/context.go \
        internal/auth/interceptor.go internal/auth/interceptor_test.go \
        internal/auth/credentials.go internal/auth/integration_test.go
git commit -m "feat(auth): HMAC 验签拦截器 + 客户端凭据 + bufconn 集成测试"
```

---

## 收尾：全量验证

- [ ] 运行 `go build ./...` —— 预期无错误。
- [ ] 运行 `go test ./... -v` —— 预期全绿（db / crypto / wire / auth；db 与 auth 集成需 Docker 起 PG / bufconn）。
- [ ] 运行 `go vet ./...` —— 预期无告警。
- [ ] 更新数据库 spec `docs/superpowers/specs/2026-05-31-sydom-database-schema-design.md` §4.1 `application` 表说明：`app_secret_hash` → `app_secret_enc BYTEA`（AES-GCM 密文），并 commit。
- [ ] 调用 `superpowers:finishing-a-development-branch` 收尾合入。

---

## 自检结果

**1. 规格覆盖度（对照 spec 各节）：**
- §3 Proto 契约 → 任务 3（含全部消息/枚举/服务，CHANGE_OP_ 前缀与 SERVICE_SUFFIX except 为 lint 合规细化，语义不变）。
- §4 HMAC 认证（metadata 三件套、重算比对、±5min 窗、强制 app_id、UNAUTHENTICATED）→ 任务 4（签名/验签）+ 任务 5（拦截器时间窗、app_id 注入、错误码）。
- §5 交互时序 / §6 容灾语义 → 属于 Sidecar/控制面运行时行为（spec §9 划归后续 spec）；本契约层提供其依赖的 proto 消息（SnapshotRequired/Heartbeat/版本号字段）与认证，已覆盖契约面。
- §7 Schema 回改（app_secret_enc）→ 任务 1（migration 000011 + 修复测试）+ 收尾（更新 DB spec §4.1）。AES-GCM 存储原语 → 任务 2。
- §8 关键参数：±5min → 任务 5 `MaxClockSkew`；maxRecvMsgSize 64MB / 心跳间隔 / 重连退避 → 属 Sidecar/控制面运行时配置（spec §9 后续 spec），非契约层代码。
- §9 边界：delta 生成 / Redis 扇出 / Sidecar apply / 密钥轮转 → 明确不在本计划，集成测试用 stub 替代服务端业务逻辑。

**2. 占位符扫描：** 全计划无 TODO/待定/"类似任务 N"。每个代码步骤均含完整可运行代码（任务 5 测试的时间戳 helper 直接用 `strconv.FormatInt`，无占位绕路）。

**3. 类型一致性：** `SecretResolver.ResolveSecret`、`Sign`/`Verify` 签名、metadata 常量（`MDAppID`/`MDTimestamp`/`MDSignature`）、`AppIDFromContext`/`WithAppID`、生成类型（`syncv1.*`、`RegisterPolicySyncServer`、`NewPolicySyncClient`、`PolicySync_SubscribeServer`、`SyncEvent_Delta`、`ChangeOp_CHANGE_OP_ADD`）跨任务引用一致。客户端 `methodFromURI` 与服务端 `info.FullMethod` 的一致性由任务 5 集成测试实测保证。
