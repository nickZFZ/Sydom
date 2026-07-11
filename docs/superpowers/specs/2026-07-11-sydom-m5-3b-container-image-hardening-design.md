# M5.3b 容器镜像 / 运行时硬化设计规格

**里程碑：** M5.3 部署硬化 · 第二片（M5.3b）

**BASE：** `main` @ `5c6e7a4`（M5.3a 之后 + roadmap cherry-pick + worktree 清理）。

---

## 1. 目标与范围

把 `deploy/` 下 4 个同构 Dockerfile（`Dockerfile.controlplane` / `Dockerfile.sidecar` / `Dockerfile.seed` / `Dockerfile.orderservice`）从「能跑」提升到「生产硬」，围绕三件事：

1. **最终阶段基础镜像换 distroless static:nonroot** —— 内置 ca-certificates（补齐 M5.3a 生产模式 TLS 硬校验所需的证书能力）、内置 nonroot uid 65532（K8s `runAsNonRoot` 开箱可用）、无 shell/包管理器（最小攻击面）。
2. **供应链固定** —— 构建层 `golang` 与最终层 distroless 基础镜像均按 `@sha256` digest 钉死，防 tag 漂移。
3. **可复现构建** —— `go build` 加 `-trimpath -ldflags='-s -w'`（去除本地路径 + 符号表，产出更可复现、更小）。

**唯一改动面 = `deploy/*`**（4 个 Dockerfile + 可能 `deploy/README.md` 记 uid 变更）。**零触碰**：所有 `*.go`、`casbin/`、`adminauthz/`、`internal/` 全不动（M5.3a 刚完成的配置装载亦不碰）。

**明确不在本片范围**（留 M5.3 其它切片 / 各自 TODO）：K8s/Helm 清单、`readOnlyRootFilesystem` 及 securityContext 强制、迁移自动化+零停机、备份恢复、二进制版本戳（`-ldflags -X` 注入 git commit——需碰 4 个 `main.go`，破坏本片纯 `deploy/*` 边界且当前无版本展示面，故排除）、HEALTHCHECK（见 §4）。

---

## 2. 现状与缺口

4 个 Dockerfile 当前完全同构（仅 `go build` 目标路径不同），最终阶段均为：

```dockerfile
FROM alpine:3.21
RUN adduser -D -u 10001 sydom
COPY --from=build /out/app /app
USER sydom
ENTRYPOINT ["/app"]
```

缺口：

| 缺口 | 影响 |
|---|---|
| 无 `ca-certificates` | 静态二进制无系统根证书，**无法校验到 PG/Redis 的 TLS**——与 M5.3a 刚设的「生产模式必须 TLS」硬校验直接矛盾。 |
| 基础镜像用 tag（`alpine:3.21`/`golang:1.26`）非 digest | 供应链漂移：同 tag 内容可变。 |
| `go build` 无 `-trimpath`/`-ldflags` | 二进制含本地构建路径、符号表；不可复现、体积偏大。 |
| `USER sydom`（名字型） | K8s `runAsNonRoot` 需数字 uid 才能强制校验。 |
| alpine 带 shell + apk | 攻击面大于必要（本系统运行期不需要 shell/包管理器）。 |

---

## 3. 目标 Dockerfile（4 个同构，仅 build 目标路径不同）

以 controlplane 为例：

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26@sha256:<BUILD_DIGEST> AS build   # = golang:1.26（digest 固定）
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o /out/app ./cmd/sydom-controlplane

FROM gcr.io/distroless/static-debian12:nonroot@sha256:<DISTROLESS_DIGEST>  # = distroless static-debian12:nonroot（digest 固定）
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

四文件 build 目标路径分别为：`./cmd/sydom-controlplane`、`./cmd/sydom-sidecar`、`./examples/seed/cmd/seeder`、`./examples/orderservice`。

`<BUILD_DIGEST>` / `<DISTROLESS_DIGEST>` 由实现时以 `docker manifest inspect <tag>` 在目标架构上解析出的当前 `@sha256` 填入（非规格占位符——是构建时可解析的确定值，机制在此写死：钉「解析当前 tag 得到的 digest」并保留 tag 注释便于将来有意升级）。

---

## 4. 关键决策与默认

- **uid 10001 → 65532**：distroless `:nonroot` 变体以固定 uid 65532 运行。取消 `adduser`/`USER`（distroless 已内置 nonroot 用户与 `/etc/passwd` 条目）。本系统密钥走 env/`_FILE`、日志走 stdout、配置只读、**运行期不写本地盘**，故 uid 变更不影响挂卷属主；`deploy/README.md` 记此变更。
- **HEALTHCHECK：不加**。distroless 无 shell、二进制无 health 子命令，Dockerfile 级 HEALTHCHECK 不可行。健康探针留 K8s liveness/readiness（TODO-M5.3-k8s）；本地已有 `internal/health` 暴露的 `/healthz`+`/readyz` 明文 HTTP 口可直接被探针复用。
- **只读根文件系统友好**：镜像运行期零写盘，为 slice② 的 `securityContext.readOnlyRootFilesystem: true` 铺路；本片只保证「镜像不需要写」，不在 Dockerfile 层强制。
- **构建层也钉 digest**：可复现性要求构建工具链本身固定，故 `golang` 构建层与 distroless 最终层都钉 digest。
- **GOPROXY 保留** `https://goproxy.cn,direct`（既有，应对模块下载网络约束）。
- **ENTRYPOINT 保留 exec 形式** `["/app"]`。

---

## 5. 头号风险与 fallback

**`gcr.io` 可拉取性**：distroless 托管于 `gcr.io`。本仓库构建/开发环境网络可能受限（`GOPROXY=goproxy.cn` 即为佐证）。实现第一步必须先 `docker manifest inspect gcr.io/distroless/static-debian12:nonroot` 探针确认可拉取。

**若 gcr.io 被墙**（决策点，需回控制者/用户定）：
- 首选：改用可达的 distroless 镜像源（公共 mirror）钉同 digest；
- 退路：回退到 **alpine-hardened**——`apk add --no-cache ca-certificates` + 数字 `USER 10001` + digest 固定 + 可复现 flags（保住 ca-certs / 数字 nonroot / digest / flags 四项硬化，仅牺牲「无 shell/最小镜像」）。

---

## 6. 验证策略（docker 29.4.2 可用）

1. **前置探针**：`docker manifest inspect` 确认基础镜像可拉取（见 §5）。
2. **构建**：4 个镜像逐个 `docker build -f deploy/Dockerfile.<x> .` 通过。
3. **运行时断言**：
   - `docker inspect` 证 `Config.User == "65532"`；
   - `docker run --entrypoint=/bin/sh <img>` **应失败**（证无 shell = distroless 属性有齿）；
   - `docker run <controlplane/sidecar 镜像>` 见其执行（缺必填配置时按 M5.3a 的 fail-close 干净报错退出，证二进制真正在跑而非空壳）。
4. **端到端**：以硬化后镜像重跑既有 `make demo` 全栈 + `make smoke`（1×allow / 1×deny / 1×数据过滤 HTTP 冒烟），证 distroless 镜像跑通整个系统、含出站 TLS 能力（ca-certs 生效）。

---

## 7. 不变量 / 验收关卡 M53B-1..6

- **M53B-1 零触碰**：`git diff 5c6e7a4..HEAD -- '*.go' casbin/ adminauthz/ internal/` = 空（本片纯 `deploy/*`）。
- **M53B-2 ca-certs 在**：distroless static 自带；端到端 TLS 冒烟（§6.4）间接证。
- **M53B-3 nonroot**：`docker inspect` `Config.User == "65532"`（4 镜像）。
- **M53B-4 无 shell**：`docker run --entrypoint=/bin/sh <img>` 失败（有齿反向验证）。
- **M53B-5 digest 固定**：4 Dockerfile 每个两处 `FROM` 均含 `@sha256:`。
- **M53B-6 端到端**：`make demo` + `make smoke` 绿。

（若触发 §5 fallback 到 alpine-hardened，则 M53B-4「无 shell」降级为「数字 USER + ca-certs」，其余关卡不变，规格随实现结果回填。）

---

## 8. 文件清单

| 文件 | 改动 |
|---|---|
| `deploy/Dockerfile.controlplane` | 最终阶段换 distroless + 两 FROM 钉 digest + build flags |
| `deploy/Dockerfile.sidecar` | 同上 |
| `deploy/Dockerfile.seed` | 同上 |
| `deploy/Dockerfile.orderservice` | 同上 |
| `deploy/README.md` | 记 uid 10001→65532 变更与 distroless 无 shell 的运维含义（如需现场调试用 `kubectl debug`/临时 sidecar 而非 `exec sh`） |
