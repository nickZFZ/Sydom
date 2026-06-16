# 司域(Sydom) 部署 Runbook

> M1.5 最小可托管运维底座。涵盖：密钥准备、明文 Demo 起栈、TLS 开启、健康探针说明、非 root 证书权限、运维注记。

---

## 1. 前置与密钥

**依赖**：Docker（建议 24+）、Docker Compose v2（`docker compose` 命令）。

在 `deploy/` 目录下创建 `.env` 文件，填入三把密钥：

```bash
# 在 deploy/ 目录执行：先把 $() 展开成真实随机值，再写盘 .env
# （切勿用引号 heredoc 如 <<'EOF'——那会把 $(openssl ...) 原样写成字面量，
#  生成可预测的弱密钥且能通过非空校验，属安全隐患）
MASTER_KEY=$(openssl rand -base64 32)
ROOT_SECRET=$(openssl rand -base64 32)
APP_SECRET=$(openssl rand -base64 32)
printf "SYDOM_MASTER_KEY=%s\nSYDOM_ROOT_SECRET=%s\nSYDOM_APP_SECRET=%s\n" \
    "$MASTER_KEY" "$ROOT_SECRET" "$APP_SECRET" > .env
```

字段说明：

| 变量 | 用途 |
|---|---|
| `SYDOM_MASTER_KEY` | AES-256 主密钥，base64(32 字节)，由控制面加密存储 app secret |
| `SYDOM_ROOT_SECRET` | root operator 初始 HMAC 密钥 |
| `SYDOM_APP_SECRET` | 该 app（demo-shop）的 HMAC 密钥，须与控制面建该 app 时一致 |

> **重要**：`.env` 绝不入镜像、绝不提交 git（确认 `.gitignore` 已包含 `deploy/.env` 或 `.env`）。密钥一旦泄露须立即轮换。

---

## 2. 默认起栈（明文 Demo）

```bash
cd deploy/
docker compose -f docker-compose.yaml up -d
```

起栈顺序由 `depends_on` 保证：

1. **postgres** 就绪（`pg_isready`）→ **migrate** 自动执行全部迁移 → 迁移完成
2. **redis** 就绪 → **controlplane** 启动（依赖 migrate 完成 + redis 就绪）
3. **sidecar** 启动（依赖控制面就绪探针通过）
4. **orderservice** 启动（依赖 sidecar 就绪 + postgres 就绪）

**验证就绪**（控制面默认健康口为容器内 `:8083`，未发布到宿主）：

```bash
# 方式 A：docker exec 进容器查
docker compose -f docker-compose.yaml exec controlplane \
    wget -qO- http://127.0.0.1:8083/readyz

# 方式 B：临时映射端口后本机查
# 先在 docker-compose.yaml 的 controlplane 的 ports 段加 "8083:8083"，重启后：
wget -qO- http://127.0.0.1:8083/readyz

# sidecar 就绪（容器内 :8091）
docker compose -f docker-compose.yaml exec sidecar \
    wget -qO- http://127.0.0.1:8091/readyz
```

就绪返回 `200 OK`，未就绪返回 `503 Service Unavailable`（不含内部错误细节）。

**带 seeder 初始化示例数据**（profile=tools，不默认启动）：

```bash
docker compose -f docker-compose.yaml --profile tools up seeder
```

---

## 3. 生成 TLS 证书

### 3.1 自签测试证书（仅用于开发/测试）

以下生成单节点自签 CA + 服务端证书，适合测试环境：

```bash
mkdir -p tls && cd tls

# 生成 CA 私钥与自签 CA 证书
openssl req -x509 -newkey rsa:4096 -nodes \
    -keyout ca.key -out ca.pem \
    -days 365 \
    -subj "/CN=sydom-test-ca"

# 生成控制面/sidecar 服务端私钥与 CSR
openssl req -newkey rsa:4096 -nodes \
    -keyout key.pem -out server.csr \
    -subj "/CN=sydom-server"

# 用 CA 签发服务端证书（SAN 含内网主机名，按实际 compose 服务名填）
openssl x509 -req -in server.csr \
    -CA ca.pem -CAkey ca.key -CAcreateserial \
    -out cert.pem -days 365 \
    -extfile <(printf "subjectAltName=DNS:controlplane,DNS:sidecar,DNS:localhost,IP:127.0.0.1")

cd ..
```

产出文件：

| 文件 | 用途 |
|---|---|
| `tls/ca.pem` | CA 证书，sidecar dial 控制面时用作信任根（`control_plane_ca_file`）；SDK 客户端验证 sidecar 时亦可用 |
| `tls/cert.pem` | 服务端证书（控制面、sidecar 均可使用同一张，或分别签发） |
| `tls/key.pem` | 服务端私钥 |

> **生产环境**：使用真实 CA（内部 PKI 或公信 CA）签发，SAN 须与实际域名/IP 一致。M1.5 仅支持单向 server-TLS（服务端出示证书，客户端验证），不支持 mTLS 双向认证。

---

## 4. 开启 TLS

### 4.1 挂载证书目录（compose volumes）

在 `docker-compose.yaml` 的 `controlplane` 与 `sidecar` 服务下各加一条只读挂载：

```yaml
services:
  controlplane:
    volumes:
      - ./cp.config.yaml:/config.yaml:ro
      - ./tls:/etc/sydom/tls:ro      # 新增：证书目录

  sidecar:
    volumes:
      - ./sidecar.config.yaml:/config.yaml:ro
      - ./tls:/etc/sydom/tls:ro      # 新增：证书目录
```

### 4.2 控制面配置（`cp.config.yaml`）

取消注释并填入证书路径：

```yaml
tls_cert_file: "/etc/sydom/tls/cert.pem"
tls_key_file:  "/etc/sydom/tls/key.pem"
```

> `tls_cert_file` 与 `tls_key_file` **须同时设置**。只设其中一项=半配置，进程启动时直接失败（fail-close），不会以明文降级运行。证书文件不可读（路径不存在、权限不足）同样拒绝启动。

### 4.3 Sidecar 配置（`sidecar.config.yaml`）

```yaml
# serve 口（SDK → sidecar）TLS
tls_cert_file: "/etc/sydom/tls/cert.pem"
tls_key_file:  "/etc/sydom/tls/key.pem"

# dial 控制面走 TLS
control_plane_tls: true
control_plane_ca_file: "/etc/sydom/tls/ca.pem"   # 空字符串或省略 = 使用系统根证书
```

> Sidecar 的 serve TLS（`tls_cert_file`/`tls_key_file`）与控制面规则相同：须同设，半配置 fail-close。

### 4.4 SDK 侧注入 TLS（公开契约零改）

使用既有的 `sydom.WithDialOptions`，无需修改任何公开 API 签名：

```go
import (
    "crypto/tls"
    "crypto/x509"
    "os"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
    "github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// 构造信任 CA 的 TLS 配置
caPem, _ := os.ReadFile("/etc/sydom/tls/ca.pem")
pool := x509.NewCertPool()
pool.AppendCertsFromPEM(caPem)
tlsCfg := &tls.Config{RootCAs: pool}

// 注入 DialOption，公开契约签名不变
client, err := sydom.New(ctx, "sidecar:8090",
    sydom.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))))
```

若信任系统根（sidecar 使用公信 CA 签发的证书），可省略 `RootCAs`：

```go
client, err := sydom.New(ctx, "sidecar:8090",
    sydom.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{}))))
```

---

## 5. 健康探针语义

两个路由，均**明文、免鉴权**：

| 路由 | 语义 | 返回 |
|---|---|---|
| `/healthz` | 存活探针（liveness） | 恒返回 `200 OK`，不依赖任何后端状态 |
| `/readyz` | 就绪探针（readiness） | 就绪：`200 OK`；未就绪：`503 Service Unavailable` |

**控制面 `/readyz`**：DB Ping + Redis Ping 均通则就绪，任一失败返回 503（fail-close）。

**Sidecar `/readyz`**：`authzr.Ready()` 通则就绪。未就绪条件：策略快照尚未从控制面拉取完成，或当前快照已超过 `max_staleness` 配置（`max_staleness: "0s"` 表示关闭陈旧守卫，但 `!Ready` 仍 fail-close）。与执行路径的 fail-close 同源——sidecar 未就绪时执行 `Enforce` 同样拒绝。

**响应体**：不泄露内部错误细节（DB 连接串、Redis 地址、具体原因等）。

**网络建议**：健康口（`:8083`/`:8091`）建议绑内网或仅 compose 内网访问，不对公网发布。compose 中健康 check 在容器内自查，无需发布到宿主即可工作。

---

## 6. 非 root

四个镜像（controlplane、sidecar、seed、orderservice）在构建阶段均执行：

```dockerfile
RUN adduser -D -u 10001 sydom
USER sydom
```

容器以 `uid=10001(sydom)` 运行，不以 root 启动进程。

**证书文件权限**：挂载进容器的证书文件须对 `uid 10001` 可读，否则进程读取证书失败 → fail-close 启动失败。

宿主机上生成证书后建议设置权限：

```bash
# 证书（公钥）对所有人可读即可
chmod 644 tls/cert.pem tls/ca.pem
# 私钥须严格保护（仅属主可读）
chmod 600 tls/key.pem tls/ca.key
```

> Docker 挂载时宿主权限直接映射进容器。若私钥为 `600` 且宿主 uid 与容器 `10001` 不同，容器内会读取失败。可将宿主私钥属主改为 `10001`，或在 compose 中用 `user: "10001"` 显式对齐，或开放 `640`（仅当宿主组可控时）。实际部署依照团队安全规范选择。

---

## 7. 运维注记

**证书轮换**：M1.5 不支持热 reload（进程不监听证书文件变化）。轮换证书须执行滚动重启：

```bash
# 替换证书文件后，滚动重启对应服务
docker compose -f docker-compose.yaml restart controlplane
docker compose -f docker-compose.yaml restart sidecar
```

**缺省行为**（向后兼容）：不设 `tls_cert_file`/`tls_key_file` = 明文运行；不设 `health_addr` = 不起健康口。两者均可按需开启，不影响现有无 TLS/无健康口部署。

**健康口网络隔离**：健康口设计为内网/运维平面使用，不含业务鉴权信息，不应发布到公网。

**半配置 fail-close**：若只设 `tls_cert_file` 不设 `tls_key_file`（或反之），进程启动即报错退出，绝不以明文降级运行。

**日志**：容器进程日志通过 `docker compose logs` 查看，启动失败原因（如证书路径错误）会在日志中给出描述性错误信息，但不会将证书内容或密钥内容写入日志。

**端口速查**：

| 服务 | 业务口 | 健康口（容器内） |
|---|---|---|
| 控制面 admin gRPC | `:8081` | `:8083` |
| 控制面 sync gRPC | `:8082` | `:8083` |
| Sidecar auth gRPC | `:8090` | `:8091` |
