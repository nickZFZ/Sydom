# sydom-controlplane Helm Chart

司域控制面的 Kubernetes 部署 chart。多副本 HA（M5.4a relay 选主开箱安全）、distroless nonroot 硬化、httpGet 探针、`_FILE` 密钥、生产 TLS fail-close。

## 前置

- Kubernetes ≥ 1.23、Helm 3/4。
- 外部托管 PostgreSQL 与 Redis（生产）。
- **数据库迁移已自动化**（M5.3-migrate）：`migrations.enabled=true`（默认）时，pre-install/pre-upgrade hook Job 在滚动前跑嵌入迁移；失败则不滚动（fail-close）。零停机纪律见 `docs/runbooks/zero-downtime-migrations.md`。

## 快速起步（非生产，chart 自建 Secret）

```bash
helm install cp deploy/helm/sydom-controlplane \
  --set image.digest=sha256:<controlplane-digest> \
  --set database.dsn='postgres://user:pass@pg:5432/sydom?sslmode=require' \
  --set redis.addr='redis:6379' \
  --set secrets.masterKey=<base64-32B> \
  --set secrets.rootSecret=<root-secret> \
  --set tls.cert="$(cat tls.crt)" --set tls.key="$(cat tls.key)"
```

## 生产（existingSecret 外部注入，推荐）

先由外部密钥管理创建 Secret（键：`config.yaml`、`master-key`、`root-secret`、`tls.crt`、`tls.key`，可选 `sync-client-ca.crt`），再：

> 用 `existingSecret` 时 `config.yaml` 由外部提供：若开 `restGateway.enabled`/`console.enabled`，须在该 `config.yaml` 内自行加 `rest_addr`/`console_addr`/`console_base_url`（chart 自建 Secret 路径会据 values 自动写入，外部注入路径不经 chart 渲染）。

```bash
helm install cp deploy/helm/sydom-controlplane \
  --set image.digest=sha256:<digest> \
  --set existingSecret=sydom-cp-secret --set replicaCount=3
```

## 关键 values

| 键 | 默认 | 说明 |
|---|---|---|
| `replicaCount` | `2` | 控制面副本（M5.4a 选主使 ≥2 安全） |
| `image.digest` | `""` | 优先 digest（防漂移）；非空忽略 tag |
| `existingSecret` | `""` | 非空→复用外部 Secret，chart 不自建 |
| `environment` | `production` | 生产模式缺 TLS 拒启动（M5.3a） |
| `tls.enabled` | `true` | 传输 TLS；cert/key 进 Secret 挂 `/etc/sydom/tls` |
| `tls.syncClientCA` | `""` | 非空→policysync mTLS 校验 sidecar 证书（M5.2b） |
| `pdb.minAvailable` | `1` | replicas>1 时保 relay leader 连续 |
| `autoscaling.enabled` | `false` | 开 HPA（CPU 扩缩 `minReplicas`-`maxReplicas`，默认 2-5/80%）；开启后 Deployment 不再固定 `replicas`，交 HPA 管（M5.3-k8s-ext） |
| `serviceMonitor.enabled` | `false` | 开 Prometheus Operator ServiceMonitor 抓 `/metrics`（需集群已装其 CRD；或用既有 pod 注解抓取）（M5.3-k8s-ext） |
| `prometheusRule.enabled` | `false` | 开 SLO 告警 PrometheusRule（可用性/延迟/命中率/连接性/relay leader；需 Operator CRD）。阈值经 `prometheusRule.*`（`grpcFaultRatioWarning/Critical`、`checkLatencyP99Seconds`、`cacheHitRatioMin`）调优，详见 [SLO runbook](../../../docs/runbooks/service-level-objectives.md)（M6-SLO） |
| `restGateway.enabled` | `false` | 开 REST/JSON 网关监听器（SP2，端口 `ports.rest`=8084）；生产自持 TLS |
| `console.enabled` | `false` | 开 Web Console BFF 监听器（SP3，端口 `ports.console`=8085） |
| `console.baseURL` | `""` | Console 对外绝对 URL（OIDC redirect_uri 基址，M6-sso-2）；经 Ingress 暴露时须 = `https://<ingress.hosts.console>`；空→企业 SSO fail-close |
| `ingress.enabled` | `false` | 开 ingress-nginx 接入（M5.3-k8s Ingress，须集群已装 controller） |
| `ingress.className` | `nginx` | IngressClass 名 |
| `ingress.tls.secretName` | `sydom-controlplane-ingress-tls` | 入口对外 TLS 证书 Secret（cert-manager 或手动） |
| `ingress.hosts.{console,rest,admin}` | `""` | 各面 host；空则不路由。`console`/`rest` 还须对应 `.enabled=true` |

### Ingress（ingress-nginx）

默认关。开启后按后端协议渲染**两个** Ingress（`backend-protocol` 是每对象级注解）：

- `<fullname>-http`（`HTTPS`）：路由 Console / REST（须各自 `.enabled=true` 且设 host）
- `<fullname>-grpc`（`GRPCS`）：路由 gRPC AdminService（设 `ingress.hosts.admin`）

后端各监听器生产模式自持 TLS（M5.3a fail-close）→ nginx 入口用 `ingress.tls.secretName` 对外终止、对后端**重加密**（GRPCS/HTTPS）。**安全边界**：`policysync` mTLS（`grpc-sync`）与 `health/metrics` **绝不经此终止型 ingress**——nginx 代理会丢失 sidecar 客户端证书身份、破坏 mTLS；sidecar 直连 `grpc-sync` Service（见 `deploy/k8s/sidecar-reference.yaml`）。

```bash
helm upgrade cp deploy/helm/sydom-controlplane --reuse-values \
  --set console.enabled=true --set console.baseURL=https://console.example.com \
  --set restGateway.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts.console=console.example.com \
  --set ingress.hosts.rest=rest.example.com \
  --set ingress.hosts.admin=admin.example.com
```

## 安全基线

- Pod：`runAsNonRoot`、uid/gid 65532、`fsGroup 65532`、seccomp RuntimeDefault。
- 容器：`readOnlyRootFilesystem`、`allowPrivilegeEscalation:false`、`drop:[ALL]`、`/tmp` emptyDir。
- 密钥：走 Secret + `SYDOM_*_FILE` 挂载路径，绝不作 env 明文值；DSN 在 Secret 内非 ConfigMap。
- SA：`automountServiceAccountToken:false`。

## sidecar 接入

见 `deploy/k8s/sidecar-reference.yaml`（同 Pod 边车模式）。

## 相关文档

全部文档索引见 [`docs/README.md`](../../../docs/README.md)。运维直达：[零停机迁移](../../../docs/runbooks/zero-downtime-migrations.md) · [备份恢复](../../../docs/runbooks/backup-restore.md) · [性能基线](../../../docs/runbooks/performance-baselines.md)。
