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

> Ingress 暂未内置：admin 口为 gRPC(+REST)、console 为可选独立监听，接入按所选 ingress controller（须支持 gRPC）自行配置。

## 安全基线

- Pod：`runAsNonRoot`、uid/gid 65532、`fsGroup 65532`、seccomp RuntimeDefault。
- 容器：`readOnlyRootFilesystem`、`allowPrivilegeEscalation:false`、`drop:[ALL]`、`/tmp` emptyDir。
- 密钥：走 Secret + `SYDOM_*_FILE` 挂载路径，绝不作 env 明文值；DSN 在 Secret 内非 ConfigMap。
- SA：`automountServiceAccountToken:false`。

## sidecar 接入

见 `deploy/k8s/sidecar-reference.yaml`（同 Pod 边车模式）。

## 相关文档

全部文档索引见 [`docs/README.md`](../../../docs/README.md)。运维直达：[零停机迁移](../../../docs/runbooks/zero-downtime-migrations.md) · [备份恢复](../../../docs/runbooks/backup-restore.md) · [性能基线](../../../docs/runbooks/performance-baselines.md)。
