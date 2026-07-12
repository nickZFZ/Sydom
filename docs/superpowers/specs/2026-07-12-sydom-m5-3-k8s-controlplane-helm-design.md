# M5.3-k8s 控制面 Helm Chart + Sidecar 参考清单 — 设计规格

> M5.3「部署硬化」剩余切片之一（〔TODO-M5.3-k8s〕）。BASE=main `e3feed0`（M5.4a + leader 加固）。**纯 `deploy/*`，零 Go / 零授权核心触碰。**

## 1. 背景与目标

司域面向「全托管多租户 SaaS」。前序 M5 已把运行时打磨到位——可观测 `/metrics`（M5.1）、严格 CSP 安全头（M5.2a）、policysync mTLS（M5.2b）、配置硬化 + 生产模式 fail-close + `_FILE` 密钥（M5.3a）、distroless static:nonroot 镜像 + digest 固定（M5.3b）、控制面多副本安全 relay 选主（M5.4a）——但**这些投资只有能在生产集群里真正部署 N 副本时才兑现**。当前仅有 `deploy/docker-compose.yaml`（单机 demo 编排），无任何 Kubernetes 清单。

**目标**：交付一份生产可用的 Kubernetes 部署工件，把控制面按多副本 HA 跑起来，并把前序所有硬化开关（生产 TLS、`_FILE` 密钥、distroless nonroot、探针、指标抓取）以 k8s 惯用方式接线；同时给出客户侧 sidecar 的参考接入清单。

**非目标（本切片外，明确排除）**：
- 零停机迁移（expand/contract、迁移即 Job）→ 归 〔TODO-M5.3-migrate〕；本切片仅在 NOTES 里说明「rollout 前须先迁移」的时序约束，不含迁移 Job。
- 备份恢复 → 归 〔TODO-M5.3-backup〕。
- REST/Console 对外 Ingress + TLS 终止 → 本切片 values 预留开关但**默认关**（`rest_addr`/`console_addr` 不启用），Ingress 模板留后续切片。
- HPA / 自动扩缩 → 需 metrics-server，默认不含（固定 `replicaCount`）。
- 打包/托管 PG、Redis → 生产用托管数据存储；chart 只接受外部 endpoint（不内置 PG/Redis 子 chart）。

## 2. 现状拓扑（实查）

**控制面**（`cmd/sydom-controlplane`，config 见 `internal/controlplane/app/config.go`）：
- 监听：`admin_addr :8081`（gRPC AdminService）、`sync_addr :8082`（gRPC PolicySync，sidecar 拨入）、`health_addr :8083`（ops：`/healthz`+`/readyz`+`/metrics`，明文）；`rest_addr`/`console_addr` 可选（本切片不启）。
- 依赖：`database_dsn`（PG，含口令）、`redis_addr`。
- 密钥：env `SYDOM_MASTER_KEY`（base64→32 字节）/`SYDOM_ROOT_SECRET`（原始）；M5.3a 支持 `*_FILE` 变体读挂载文件；`SYDOM_ENVIRONMENT`。
- 生产 TLS fail-close（M5.3a）：`environment: production` 时缺 `tls_cert_file`/`tls_key_file` 拒启动。
- mTLS（M5.2b）：`sync_client_ca_file` 可选。
- M5.4a：多副本安全靠 PG 会话级 advisory-lock 选主，**无需任何副本级配置**，`replicas: N` 开箱安全。

**Sidecar**（`cmd/sydom-sidecar`，`internal/sidecar/app/config.go`）：`control_plane_addr`（→cp:8082）、`app_key`、`domain`、`auth_addr :8090`（数据面 Check）、`health_addr :8091`（ops）、`max_staleness`、backoff；密钥 env `SYDOM_APP_SECRET`(+`_FILE`)；生产模式缺 `control_plane_tls:true` 拒启动；mTLS 客户端证书 `control_plane_client_cert_file`/`_key_file`。

**健康端点**（`internal/health/health.go`）：`/healthz` 恒 200（liveness，不连依赖避免抖动误重启）；`/readyz` 跑就绪 checker、fail-close 503（readiness）。**均为纯 HTTP GET，无需 shell → 与 distroless 探针天然契合**（补上 M5.3b「distroless 无 wget」的运维缺口）。

**镜像**（M5.3b）：distroless static:nonroot，数字 uid 65532，无 shell，两层 digest 固定。

## 3. 方案选择

**A（选定）Helm chart（控制面）+ 原始参考清单（sidecar）。** 路线图明列「K8s/Helm 清单」；Helm 的 values 参数化天然映射 M5.3a 的 environment/`_FILE`/TLS 开关与副本数/资源/外部依赖 endpoint，是托管 SaaS 运维的预期工件。sidecar 是**客户侧**、通常作为容器注入客户自有应用 Pod，用一份带注释的原始清单（文档即清单）比再给一个 chart 更直观。

**B 纯原始清单 + Kustomize overlay。** 更透明、无模板逻辑，`kubeconform` 易校验；但本机无 kustomize/kubeconform，且路线图点名 Helm，dev/prod 差异用 values 比 overlay 更集中。否决。

**C 单体大 chart（含 PG/Redis/迁移/sidecar 全套）。** 违背 YAGNI 与托管 SaaS 现实（数据存储用托管），且把独立关注点（迁移=M5.3-migrate）纠缠进来。否决。

## 4. 设计

### 4.1 交付物

```
deploy/helm/sydom-controlplane/
  Chart.yaml
  values.yaml
  README.md
  .helmignore
  templates/
    _helpers.tpl          # 名称/标签/selector 助手
    serviceaccount.yaml
    secret.yaml           # config.yaml(含 DSN)+主密钥/根密钥+可选 TLS → k8s Secret
    deployment.yaml       # 多副本、硬化 securityContext、httpGet 探针、指标注解
    service.yaml          # ClusterIP，暴露 grpc-admin(8081)/grpc-sync(8082)
    poddisruptionbudget.yaml
    NOTES.txt
deploy/k8s/
  sidecar-reference.yaml  # 客户侧：app+sidecar 双容器 Deployment 参考（带注释）
```

### 4.2 config.yaml 落 Secret（非 ConfigMap）

控制面 `database_dsn` 含 PG 口令，故整个 `config.yaml` 由 `secret.yaml` 渲染进 **k8s Secret**（`stringData`），挂载为 `/etc/sydom/config.yaml`。同一 Secret 承载 `master-key`/`root-secret`（及可选 TLS `tls.crt`/`tls.key`/`sync-client-ca.crt`）文件条目，供 `_FILE` env 指向。生产禁止把 DSN 落 ConfigMap。**支持 `existingSecret`**：values 指定既有 Secret 名时不渲染本模板（生产由外部密钥管理注入），仅当 `existingSecret: ""` 时 chart 自建（便于起步/非生产）。

### 4.3 Deployment（硬化 + HA + 探针）

- `replicas: {{ .Values.replicaCount }}`（默认 **2**；M5.4a 使其安全）。`RollingUpdate` maxUnavailable=0/maxSurge=1（滚动不减容量）。
- **Pod securityContext**：`runAsNonRoot: true`、`runAsUser/runAsGroup: 65532`（对齐 distroless）、`fsGroup: 65532`、`seccompProfile: RuntimeDefault`。
- **Container securityContext**：`allowPrivilegeEscalation: false`、`readOnlyRootFilesystem: true`、`capabilities.drop: [ALL]`。
- 镜像：`{{ .Values.image.repository }}@{{ .Values.image.digest }}`（digest 优先，回落 tag）、`imagePullPolicy: IfNotPresent`。
- `args: ["-config", "/etc/sydom/config.yaml"]`。
- **env**：`SYDOM_ENVIRONMENT=production`（values 可覆盖）、`SYDOM_MASTER_KEY_FILE=/etc/sydom/secrets/master-key`、`SYDOM_ROOT_SECRET_FILE=/etc/sydom/secrets/root-secret`（走 M5.3a `_FILE` 路径，密钥永不进 env 值/ConfigMap/进程列表）。
- **volumeMounts**：config Secret（`/etc/sydom/config.yaml`，subPath）、secrets（`/etc/sydom/secrets` 只读）、可选 TLS（`/etc/sydom/tls` 只读）、`emptyDir` 挂 `/tmp`（因 readOnlyRootFilesystem）。
- **ports**：`admin 8081`、`sync 8082`、`health 8083`。
- **探针**（httpGet，distroless 友好）：`livenessProbe: GET /healthz :8083`（initialDelay 5s/period 10s/failure 3）；`readinessProbe: GET /readyz :8083`（period 5s/failure 3，fail-close 使未就绪副本自动摘出 Service）。
- **resources**：values 提供 requests/limits 默认（如 cpu 100m/500m、mem 128Mi/256Mi）。
- **指标抓取**：`podAnnotations` 默认含 `prometheus.io/scrape: "true"`、`prometheus.io/port: "8083"`、`prometheus.io/path: "/metrics"`（对齐 M5.1 ops 端口）。ServiceMonitor 留后续（避免依赖 Prometheus Operator CRD）。

### 4.4 Service / PDB / SA

- `service.yaml`：`ClusterIP`，端口 `grpc-admin:8081`、`grpc-sync:8082`（`appProtocol: grpc`）。health 端口不进主 Service（ops 面，探针经 Pod IP 直达）。sidecar 经 `<svc>.<ns>.svc:8082` 拨 sync。
- `poddisruptionbudget.yaml`：`minAvailable: {{ .Values.pdb.minAvailable | default 1 }}`，仅当 `replicaCount > 1 && pdb.enabled` 渲染（保证 drain/升级期至少 1 副本在，relay leader 连续）。
- `serviceaccount.yaml`：专用 SA（最小权限，不挂 API token 除非需要：`automountServiceAccountToken: false`）。

### 4.5 Sidecar 参考清单（`deploy/k8s/sidecar-reference.yaml`）

带注释的原始 Deployment，示范客户把 sidecar 作为**同 Pod 边车容器**接入自有应用：app 容器（占位镜像）+ sidecar 容器（distroless 镜像、同款硬化 securityContext、`SYDOM_APP_SECRET_FILE` 走挂载 Secret、`-config` 挂 ConfigMap 的 sidecar.config.yaml、httpGet 探针 `/healthz`/`/readyz :8091`、`control_plane_addr` 指向控制面 Service）；app 经 `localhost:8090` 拨数据面 Check（同 Pod 共享 localhost）。含 ConfigMap（非密钥配置）+ Secret（app secret）示例。此文件是**文档即清单**，非 chart，客户按需改。

### 4.6 数据流

`helm install` → 渲染 Secret(config+密钥)/SA/Deployment/Service/PDB → kube-scheduler 起 N 个 nonroot 副本 → 各副本读 `/etc/sydom/config.yaml`（environment=production 触发 TLS 硬校验）+ `_FILE` 密钥 → 连外部 PG/Redis → readiness `/readyz` 通过后进 Service endpoints → N 副本中**恰一个**经 advisory-lock 成为 relay leader drain outbox（M5.4a）、其余 fail-close 待命 → Prometheus 按 Pod 注解抓 `:8083/metrics` → sidecar 经 Service `:8082` 拨 policysync（可 mTLS）引导全量快照。

### 4.7 错误处理 / fail-close 一致性

- 生产模式缺 TLS → 容器**启动即拒**（M5.3a），Pod CrashLoopBackOff 而非明文起服务——values 生产默认 `tls.enabled: true` 且校验 cert/key 存在。
- `/readyz` fail-close：依赖（PG/Redis/快照）未就绪 → 503 → 未进 Service endpoints → 不接流量。
- 数字 uid 65532 + readOnlyRootFilesystem + drop ALL：与 distroless 镜像自洽，任一被抹除即偏离硬化基线。
- Secret 优先 `existingSecret`（外部注入）；chart 自建仅便利路径。

## 5. 验证

- `helm lint deploy/helm/sydom-controlplane`（本机 helm v4.2.0）→ 0 error。
- `helm template`（默认 values + 生产样例 values）渲染成功、无未解析占位；断言关键不变量：Secret 含 config.yaml+master-key+root-secret、Deployment `runAsNonRoot`/uid 65532/`readOnlyRootFilesystem`/drop ALL、httpGet 探针指 `/healthz`&`/readyz :8083`、env 用 `SYDOM_MASTER_KEY_FILE`（非明文值）、`SYDOM_ENVIRONMENT=production`、指标注解、replicaCount≥2、PDB 存在。
- `kubectl apply --dry-run=client -f <(helm template ...)`（若 kubectl 可用，客户端离线校验 schema）。
- 用 shell/grep 断言：渲染产物**无明文密钥**（master/root 只出现为 `_FILE` 路径 env + Secret 条目，DSN 只在 Secret 内）；`deploy/README.md` 更新 k8s 部署段。
- 零触碰核验：`git diff <BASE>..HEAD -- '*.go' casbin/ adminauthz/ internal/` = 空。

## 6. 验收标准（M53K-1..7）

- **M53K-1** 零触碰：`git diff <BASE>..HEAD -- '*.go' casbin/ adminauthz/ internal/` = 空（纯 `deploy/*`+docs）。
- **M53K-2** `helm lint` 0 error；`helm template`（默认 + 生产 values）渲染成功无占位。
- **M53K-3** Deployment 硬化：渲染断言 `runAsNonRoot:true`、uid/gid 65532、`readOnlyRootFilesystem:true`、`allowPrivilegeEscalation:false`、`capabilities.drop:[ALL]`、镜像用 digest。
- **M53K-4** 探针 distroless 友好：liveness `httpGet /healthz`、readiness `httpGet /readyz`，均 `:8083`，无 exec/wget。
- **M53K-5** 密钥卫生：渲染产物无明文 master/root/DSN 暴露于 ConfigMap 或 env 值；密钥走 Secret + `_FILE` env；支持 `existingSecret`。
- **M53K-6** HA：`replicaCount` 默认 ≥2、PDB 保 `minAvailable≥1`、RollingUpdate maxUnavailable=0；NOTES 说明 M5.4a 选主使多副本安全、迁移时序归 M5.3-migrate。
- **M53K-7** sidecar 参考清单可 `helm template` 无关地 `kubectl apply --dry-run=client` 通过（或 yaml 合法）、示范同 Pod 边车 + `_FILE` 密钥 + httpGet 探针。

## 7. 风险

- **helm v4**（本机 v4.2.0）与常见 Helm 3 chart 细节差异：用标准 `apiVersion: v2` Chart + 基础模板函数，`helm lint`/`template` 兜底；不用 v4 专属特性。
- 迁移未纳入本切片 → 首次安装前须手动/外部迁移（NOTES 显著提示）；避免 chart 里塞半成品迁移 Job。
- config.yaml 落 Secret 而非 ConfigMap 是刻意选择（DSN 含口令）；若将来 DSN 拆出可改回 ConfigMap，本切片以安全优先。
