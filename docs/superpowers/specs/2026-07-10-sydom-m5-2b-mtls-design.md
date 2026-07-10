# M5.2b 策略同步通道双向 TLS（mTLS）设计

- **里程碑**：M5.2 安全硬化第二片（承 M5.2a 安全响应头）
- **BASE**：main `1ffb0a3`
- **日期**：2026-07-10
- **一句话**：给 sidecar→控制面 policysync gRPC 这一机对机策略同步通道加双向客户端证书校验，构成传输层纵深防御；人面（REST/Console）与授权核心零触碰，全离线可验证。

## 1. 背景与动机

控制面今天用一份共享服务端 TLS 配置（`tlsconfig.Server` 产出的 `srvTLS`）盖住四个监听器：admin gRPC、policysync gRPC、REST、Console。sidecar 数据面经 `syncclient` 拨号 policysync（relay）通道拉取策略快照，在 TLS 通道之上叠加 HMAC(app_id/secret) 应用鉴权（`Secure=true` ⟹ gRPC `RequireTransportSecurity()`）。

现状的传输层是**单向**的：只有客户端验证服务端证书。策略同步通道是纯机对机通道（唯一客户端是 sidecar），却只靠 HMAC 密钥这一道应用层凭据把关。**威胁**：一旦 app secret 泄露，攻击者即可从任意主机拉取该 app 的全量策略快照。

**mTLS 在此通道的价值**：TLS 握手阶段服务端即验证客户端证书链到受信 CA，发生在任何 HMAC 求值**之前**。⟹ 盗得 app secret 也拉不到策略快照，必须**同时**持有 CA 签发的客户端证书。这是真正的传输层纵深防御，且与既有 HMAC 身份正交、不耦合。

## 2. 范围决策（已定）

**仅** sidecar→控制面 policysync gRPC 通道要求双向客户端证书校验。

明确排除：
- **REST / Console**：浏览器/人面，强制客户端证书会直接打断人的访问 → 继续用共享 `srvTLS`（不要求客户端证书）。
- **admin operator gRPC**：operator 客户端（grpcurl/管理 SDK）用 HMAC 鉴权，强制证书会加重运维负担 → 继续用共享 `srvTLS`。
- **app→sidecar 数据面 Check gRPC**：通常同 pod（localhost），mTLS 价值低 → 本片不动。
- **ops/health 端口**：明文、免鉴权，不涉。

**分通道隔离铁律**：mTLS 关闭时，四个监听器行为与今日逐字节一致。

## 3. 构造方案

采用**派生式 + 平行构造器**（与既有 `Server`/`Client` 风格一致、纯函数易单测、fail-close 集中在 tlsconfig）。

### 3.1 `internal/tlsconfig` 新增两函数

```go
// MutualServer 由已构造的服务端配置派生「要求并验证客户端证书」的变体：
//   clientCAFile 空       → 返回 base 原样（向后兼容，不要求客户端证书）；
//   base 为 nil（未启用服务端 TLS）但 clientCAFile 非空 → 返错（fail-close：明文上无法要求客户端证书）；
//   clientCAFile 不可读/无有效 PEM → 返错。
// 非空路径 base.Clone() 后设 ClientAuth=RequireAndVerifyClientCert + ClientCAs，绝不改写入参 base（避免别名污染共享配置）。
func MutualServer(base *tls.Config, clientCAFile string) (*tls.Config, error)

// MutualClient 在 Client(caFile) 基础上附加客户端证书对用于 mTLS：
//   certFile/keyFile 皆空 → 等价 Client（不出示客户端证书，向后兼容）；
//   仅一项非空          → 返错（fail-close：禁止半配置）；
//   都非空但加载失败    → 返错。
func MutualClient(caFile, certFile, keyFile string) (*tls.Config, error)
```

现有 `Server(certFile, keyFile)` 与 `Client(caFile)` **签名与实现零改**（`MutualClient` 内部复用 `Client` 逻辑构造信任根，再附加证书对）。

### 3.2 配置（全部 opt-in，纯加法）

控制面 `internal/controlplane/app/config.go`：
- 新增 `SyncClientCAFile string`（yaml `sync_client_ca_file`）。空=策略同步通道无 mTLS（默认，向后兼容）。

sidecar `internal/sidecar/app/config.go`：
- 新增 `ControlPlaneClientCertFile string` / `ControlPlaneClientKeyFile string`（yaml `control_plane_client_cert_file` / `control_plane_client_key_file`）。皆空=不出示客户端证书（默认，向后兼容）。

### 3.3 装配接线

**控制面 `run.go`**：现在 `grpcOpts`（由 `srvTLS` 构造）同时传给 `mgmt.NewGRPCServer` 与 `policysync.NewGRPCServer`。改为拆分：
```go
srvTLS, err := tlsconfig.Server(cfg.TLSCertFile, cfg.TLSKeyFile)   // 共享：admin/REST/Console
// ...既有 grpcOpts 由 srvTLS 构造，供 admin/REST/Console...
syncTLS, err := tlsconfig.MutualServer(srvTLS, cfg.SyncClientCAFile) // 派生：仅 policysync
// syncGrpcOpts 由 syncTLS 构造；mTLS 关闭时 syncTLS==srvTLS，syncGrpcOpts 与 grpcOpts 等价
```
`policysync.NewGRPCServer(..., syncGrpcOpts...)`；`mgmt.NewGRPCServer(..., grpcOpts...)` 不变。REST/Console 继续用 `srvTLS`（经 `tls.NewListener`）不变。

**sidecar `buildSyncConfig`**：
```go
cliTLS, err := tlsconfig.MutualClient(cfg.ControlPlaneCAFile, cfg.ControlPlaneClientCertFile, cfg.ControlPlaneClientKeyFile)
```
其余（`Secure=true`、`DialOptions`）不变。

## 4. 错误处理（fail-close，承袭现有 tlsconfig 阶梯）

| 情形 | 行为 |
|---|---|
| `SyncClientCAFile` 设，但服务端 TLS 未启用（cert/key 皆空） | 启动返错（`MutualServer` base==nil 分支）——明文上无法要求客户端证书 |
| `SyncClientCAFile` 指向不可读文件 / 无有效 PEM | 启动返错 |
| sidecar 客户端证书半配置（仅 cert 或仅 key） | 启动返错 |
| 控制面已开 mTLS，sidecar 未出示证书 | TLS 握手失败 → sidecar 同步失败 → fail-close（宁拒不跑陈旧/越权快照） |
| 客户端证书未链到受信 CA / 过期 | 握手被 `RequireAndVerifyClientCert` 拒 |

## 5. 不变量

- **零触碰授权核心**：仅动 `internal/tlsconfig`、`internal/controlplane/app`、`internal/sidecar/app`。`casbin/`、`adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`、`internal/auth/`、`internal/obs/` 内容 diff=0。
- **无身份耦合**：mTLS 仅验证证书链到受信 CA，**不**绑定证书 CN/SAN↔app_id。HMAC(app_id/secret) 仍是唯一应用身份，authz 决策零改。
- **向后兼容**：三个新配置项皆空时，控制面四监听器 + sidecar 拨号行为与 BASE 逐字节一致。
- **分通道隔离**：客户端证书要求仅施于 policysync 监听器；admin/REST/Console 绝不要求。

## 6. 测试策略（全离线可验证）

测试内用 `crypto/x509` + `crypto/ecdsa` 自签一个测试 CA，再签发服务端 leaf 与客户端 leaf 证书（无需任何外部文件或网络）。

1. **`tlsconfig` 单测**：
   - `MutualServer`：CA 文件空→返回 base 原样；base==nil+CA 非空→返错；CA 无效 PEM→返错；正常路径→`ClientAuth==RequireAndVerifyClientCert` 且 `ClientCAs != nil` 且**入参 base 未被改写**（别名安全）。
   - `MutualClient`：证书对皆空→等价 `Client`；半配置→返错；加载失败→返错；正常→`Certificates` 非空且信任根正确。
2. **集成测试（核心可演示证明）**：起真实 gRPC 监听器套 `syncTLS`——
   - **无客户端证书**的 client 拨号 → 握手/RPC 失败（拒绝）；
   - **持 CA 签发客户端证书**的 client 拨号 → 成功；
   - **反向验证**：撤 `ClientAuth`（退回单向）后，无证书 client 也能连 → 证明上面「拒绝」断言有齿、确由客户端证书要求所致。
3. **`run_test.go` 扩展**：`TestRun_WiringEndToEnd` 断言配置 `SyncClientCAFile` 后 sync 监听器要求客户端证书（无证书 client 被拒），而 admin/REST/Console 不要求（分通道隔离有齿）。
4. **`go test ./...` EXIT 0**。

## 7. 验收关卡 MT-1..7

- **MT-1 零触碰授权核心**：`git diff --numstat 1ffb0a3..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/sidecar/authz/ internal/auth/ internal/obs/` = 空（用 `git diff --numstat`/`git show --numstat` 权威核验，不用可能短路的 grep）。
- **MT-2 向后兼容逐字节**：三个新配置项皆空 → 四监听器 + sidecar 拨号行为与 BASE 一致；既有部署零改（测试断言）。
- **MT-3 fail-close 三分支**：CA 设+无服务端 TLS→返错；CA 无效 PEM→返错；sidecar 证书半配置→返错。
- **MT-4 双向强制有齿 + 反向验证**：集成测试证明无证书被拒、有证书通过，且撤 `ClientAuth` 后断言翻转（有齿）。
- **MT-5 分通道隔离**：admin/REST/Console 不要求客户端证书；仅 sync 要求（wiring 测试断言）。
- **MT-6 无 CN↔app_id 耦合**：代码审查 + diff 确认证书验证不触 authz 身份。
- **MT-7 全绿**：`go test ./...` EXIT 0。

## 8. 非目标（YAGNI / 留后续）

- 内建 PKI / SPIFFE / 证书自动轮换。
- 证书 CN/SAN ↔ app_id 绑定与 DB 校验。
- app→sidecar 数据面 Check 通道 mTLS。
- admin/REST/Console 客户端证书。
- 无真浏览器走查：本片零 UI/JS/CSP 面（纯传输层机对机 gRPC 通道），"渐进增强须真浏览器走查"教训不适用；可演示证明由第 6 节离线集成测试承担。额外仅冒烟确认 REST/Console 未被客户端证书要求波及（集成/curl 层）。
