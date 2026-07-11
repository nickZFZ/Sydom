# M5.3b 容器镜像 / 运行时硬化实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 `deploy/` 下 4 个同构 Dockerfile 的最终阶段从 `alpine` 换成 distroless static:nonroot，并给构建层加 digest 固定 + 可复现 flags，达成 ca-certs 内置 / 数字 nonroot(65532) / 无 shell / 供应链固定，全程零触碰 Go 代码。

**架构：** 纯 `deploy/*` 改动。4 文件同构（仅 `go build` 目标路径不同）：构建层 `golang:1.26@sha256` + `-trimpath -ldflags='-s -w'`；最终层 `gcr.io/distroless/static-debian12:nonroot@sha256`。验证靠 `docker build`/`docker inspect`/`docker cp`/`make demo`+`make smoke`，非 Go 单测。

**技术栈：** Docker 29.4.2（已确认可用）、docker compose、distroless、golang-migrate（既有 demo 栈）。

**BASE：** `feat/m5-3b-container-hardening` @ `ba220a3`（含规格 commit）；规格 `docs/superpowers/specs/2026-07-11-sydom-m5-3b-container-image-hardening-design.md`。

**零触碰铁律：** 仅动 `deploy/Dockerfile.{controlplane,sidecar,seed,orderservice}` 与 `deploy/README.md`。任何 `*.go`、`casbin/`、`adminauthz/`、`internal/` 内容 diff 必须为 0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `deploy/Dockerfile.controlplane`（修改） | 最终阶段 distroless + 两层 digest 固定 + build flags；本片模板文件。 |
| `deploy/Dockerfile.sidecar`（修改） | 同构（build 目标 `./cmd/sydom-sidecar`）。 |
| `deploy/Dockerfile.seed`（修改） | 同构（build 目标 `./examples/seed/cmd/seeder`）。 |
| `deploy/Dockerfile.orderservice`（修改） | 同构（build 目标 `./examples/orderservice`）。 |
| `deploy/README.md`（修改） | 记 uid 10001→65532 与 distroless 无 shell 的运维含义。 |

---

## 任务 1：前置探针 + 解析并记录两个基础镜像 digest（头号风险闸）

**文件：** 无（数据收集，digest 值在任务 2/3 写入 Dockerfile）。

- [ ] **步骤 1：探针 gcr.io 可拉取性**（头号风险，见规格 §5）

运行：
```bash
docker pull gcr.io/distroless/static-debian12:nonroot
```
预期：拉取成功。

**若失败（gcr.io 被墙）：** 立即以 **BLOCKED** 报告，附错误输出，交控制者/用户定 fallback（规格 §5：改镜像源钉同 digest，或退 alpine-hardened）。**不要**擅自改方案。

- [ ] **步骤 2：拉取构建层镜像并解析两个 RepoDigest**

运行：
```bash
docker pull golang:1.26
docker inspect --format '{{index .RepoDigests 0}}' golang:1.26
docker inspect --format '{{index .RepoDigests 0}}' gcr.io/distroless/static-debian12:nonroot
```
预期：两行形如 `golang@sha256:...` 与 `gcr.io/distroless/static-debian12@sha256:...`。

**把这两个 `@sha256:...` 值原样记下**（下称 `BUILD_DIGEST` 与 `DISTROLESS_DIGEST`），任务 2/3 的 Dockerfile 逐字使用。同时记下它们对应的 tag（`golang:1.26` / `distroless/static-debian12:nonroot`）用于 Dockerfile 注释。

- [ ] **步骤 3：报告 digest 值**

在任务汇报里明确列出解析到的 `BUILD_DIGEST`、`DISTROLESS_DIGEST` 两个确切字符串，供后续任务与审查核对。

---

## 任务 2：硬化 Dockerfile.controlplane（模板）+ 建镜像 + 运行时断言

**文件：**
- 修改：`deploy/Dockerfile.controlplane`

- [ ] **步骤 1：记录「当前 alpine 镜像有 shell」作为反向对照（teeth 前置）**

运行（构建当前版本并证其有 shell——即本片要消除的攻击面）：
```bash
docker build -f deploy/Dockerfile.controlplane -t sydom-cp:before .
docker run --rm --entrypoint=/bin/sh sydom-cp:before -c 'echo HAS_SHELL'
```
预期：输出 `HAS_SHELL`（当前 alpine 镜像有 `/bin/sh`）。这是硬化后必须消失的能力。

- [ ] **步骤 2：改写 `deploy/Dockerfile.controlplane`**

把整个文件替换为（`<BUILD_DIGEST>`/`<DISTROLESS_DIGEST>` 用任务 1 记下的确切值逐字替换）：

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26@<BUILD_DIGEST> AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/app ./cmd/sydom-controlplane

FROM gcr.io/distroless/static-debian12:nonroot@<DISTROLESS_DIGEST>
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

- [ ] **步骤 3：构建硬化镜像**

运行：
```bash
docker build -f deploy/Dockerfile.controlplane -t sydom-cp:hardened .
```
预期：构建成功。

- [ ] **步骤 4：运行时断言（4 条）**

运行并逐条核对：
```bash
# (a) nonroot：Config.User 为 65532（distroless nonroot 固定 uid）
docker inspect --format '{{.Config.User}}' sydom-cp:hardened
# 预期：65532（若输出 "nonroot" 亦可，二者同映射 uid 65532——记下实际值）

# (b) 无 shell：distroless 无 /bin/sh，此命令应失败
docker run --rm --entrypoint=/bin/sh sydom-cp:hardened -c 'echo SHOULD_NOT_PRINT'; echo "exit=$?"
# 预期：报错（no such file / exec 失败）、exit 非 0，绝不打印 SHOULD_NOT_PRINT

# (c) ca-certs 在：不需 shell，直接从镜像层拷出证书 bundle 断言非空
cid=$(docker create sydom-cp:hardened); docker cp "$cid":/etc/ssl/certs/ca-certificates.crt /tmp/ca.crt; docker rm "$cid"; wc -c < /tmp/ca.crt
# 预期：字节数远大于 0（distroless static 内置 ca-certificates）

# (d) 二进制真跑：无配置时按 M5.3a fail-close 干净报错退出（非空壳）
docker run --rm sydom-cp:hardened; echo "exit=$?"
# 预期：打印配置相关错误（如 read config / required）、exit 非 0
```

- [ ] **步骤 5：Commit**

```bash
git add deploy/Dockerfile.controlplane
git commit -m "feat(deploy): M5.3b controlplane 镜像换 distroless static:nonroot(ca-certs+nonroot 65532+无 shell)+两层 digest 固定+可复现 flags -trimpath/-ldflags -s -w"
```

---

## 任务 3：同法硬化 sidecar / seed / orderservice + 各自建镜像+断言 + commit

**文件：**
- 修改：`deploy/Dockerfile.sidecar`
- 修改：`deploy/Dockerfile.seed`
- 修改：`deploy/Dockerfile.orderservice`

三文件与任务 2 的 controlplane **完全同构，仅 `go build` 最后一段目标路径不同**。逐个处理。

- [ ] **步骤 1：改写 `deploy/Dockerfile.sidecar`**（`go build ... -o /out/app` 目标为 `./cmd/sydom-sidecar`）

整文件替换为：
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26@<BUILD_DIGEST> AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/app ./cmd/sydom-sidecar

FROM gcr.io/distroless/static-debian12:nonroot@<DISTROLESS_DIGEST>
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

- [ ] **步骤 2：改写 `deploy/Dockerfile.seed`**（目标 `./examples/seed/cmd/seeder`）

整文件替换为：
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26@<BUILD_DIGEST> AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/app ./examples/seed/cmd/seeder

FROM gcr.io/distroless/static-debian12:nonroot@<DISTROLESS_DIGEST>
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

- [ ] **步骤 3：改写 `deploy/Dockerfile.orderservice`**（目标 `./examples/orderservice`）

整文件替换为：
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.26@<BUILD_DIGEST> AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/app ./examples/orderservice

FROM gcr.io/distroless/static-debian12:nonroot@<DISTROLESS_DIGEST>
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
```

- [ ] **步骤 4：构建三镜像 + 对每个断言 nonroot & 无 shell**

运行：
```bash
for svc in sidecar seed orderservice; do
  docker build -f deploy/Dockerfile.$svc -t sydom-$svc:hardened .
  echo "== $svc User =="; docker inspect --format '{{.Config.User}}' sydom-$svc:hardened
  echo "== $svc no-shell（应失败）=="; docker run --rm --entrypoint=/bin/sh sydom-$svc:hardened -c 'echo NO' ; echo "exit=$?"
done
```
预期：三镜像各构建成功；`User` 均为 `65532`（或 `nonroot`）；`/bin/sh` 均失败、exit 非 0、不打印 `NO`。

- [ ] **步骤 5：Commit**

```bash
git add deploy/Dockerfile.sidecar deploy/Dockerfile.seed deploy/Dockerfile.orderservice
git commit -m "feat(deploy): M5.3b sidecar/seed/orderservice 镜像同构换 distroless static:nonroot+digest 固定+可复现 flags(四 Dockerfile 硬化对齐)"
```

---

## 任务 4：README uid 说明 + 端到端 demo/smoke + M53B-1..6 验收

**文件：**
- 修改：`deploy/README.md`

- [ ] **步骤 1：更新 `deploy/README.md`**

在 `deploy/README.md` 中新增一段（选文件内合适位置，如镜像/运行说明附近），内容说明容器硬化变更：

```markdown
## 容器镜像硬化（M5.3b）

四个镜像最终阶段均为 distroless（`gcr.io/distroless/static-debian12:nonroot`，按 digest 固定）：
- 以固定非 root **uid 65532** 运行（此前 alpine 下为 uid 10001）；K8s 可直接 `runAsNonRoot`。
- 内置 ca-certificates，支持到 PG/Redis 的出站 TLS（生产模式硬要求，见 M5.3a）。
- **无 shell / 无包管理器**：不能 `docker exec sh` 进容器调试；现场排障请用 `kubectl debug`（临时调试容器）或旁挂一次性 sidecar，而非进入业务容器。
- 运行期不写本地盘，兼容 K8s `readOnlyRootFilesystem`（强制留后续 K8s 清单切片）。
```

- [ ] **步骤 2：Commit README**

```bash
git add deploy/README.md
git commit -m "docs(deploy): M5.3b README 记容器硬化(uid 10001→65532/内置 ca-certs/无 shell 调试改用 kubectl debug/只读根文件系统友好)"
```

- [ ] **步骤 3：M53B-6 端到端——硬化镜像重建 demo 全栈 + 冒烟**

运行：
```bash
make demo
make smoke
```
预期：`make demo` 用硬化 Dockerfile 重建并起全栈成功；`make smoke` 三项（1×allow / 1×deny / 1×数据过滤 HTTP 冒烟）全绿。证 distroless 镜像跑通整个系统。

拆栈：
```bash
make demo-down
```

> 注：若 `make demo`/`make smoke` 因本机 compose/网络约束无法完整跑通，如实报告实际输出与卡点，不要谎报绿。至少任务 2/3 的逐镜像 `docker run` 断言须真跑。

- [ ] **步骤 4：M53B-1 零触碰（机器验证）**

运行：
```bash
git diff --numstat 5c6e7a4..HEAD -- '*.go' casbin/ adminauthz/ internal/
```
预期：**空输出**。若有输出即违反铁律。

- [ ] **步骤 5：M53B-5 digest 固定核验**

运行：
```bash
grep -c 'FROM .*@sha256:' deploy/Dockerfile.controlplane deploy/Dockerfile.sidecar deploy/Dockerfile.seed deploy/Dockerfile.orderservice
```
预期：每个文件 `2`（两处 `FROM` 均带 `@sha256:`）。

- [ ] **步骤 6：收尾确认 M53B-2/3/4**

对照汇总任务 2/3 的断言证据：M53B-2 ca-certs（任务 2 步骤 4c，`docker cp` bundle 非空）；M53B-3 nonroot（4 镜像 `Config.User`）；M53B-4 无 shell（4 镜像 `/bin/sh` 均失败、有前置 alpine 有 shell 的反向对照）。全部满足即 M5.3b 关卡闭合。

---

## 自检

**1. 规格覆盖度：**
- §1 三件事（distroless / digest 固定 / 可复现 flags）→ 任务 2/3 的 Dockerfile 改写。
- §3 目标 Dockerfile → 任务 2（模板）+ 任务 3（其余三）。
- §4 决策（uid 65532 / HEALTHCHECK 不加 / 只读根友好）→ 任务 4 README + Dockerfile 无 HEALTHCHECK。
- §5 头号风险 + fallback → 任务 1 步骤 1 探针 + BLOCKED 上报。
- §6 验证策略 → 任务 1（探针）+ 任务 2/3（build+inspect+cp+run）+ 任务 4（demo/smoke）。
- §7 M53B-1..6 → 任务 4 步骤 3/4/5/6 + 任务 2/3 断言。
- §8 文件清单 → 5 文件全覆盖。
- 全覆盖，无遗漏。

**2. 占位符扫描：** `<BUILD_DIGEST>`/`<DISTROLESS_DIGEST>` 是任务 1 以明确 `docker inspect` 命令解析出的确定值（非模糊待定），机制与替换点已写死；其余步骤均含可运行命令与预期输出，无 TODO/伪代码。

**3. 类型一致性：** 4 个 Dockerfile 结构逐字一致，仅 `go build` 目标路径不同（controlplane=`./cmd/sydom-controlplane`、sidecar=`./cmd/sydom-sidecar`、seed=`./examples/seed/cmd/seeder`、orderservice=`./examples/orderservice`）；镜像 tag（`sydom-<svc>:hardened`）、digest 变量名在任务 1 定义与任务 2/3/4 引用一致。
