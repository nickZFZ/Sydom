# sydom-controlplane Helm Chart

司域控制面的 Kubernetes 部署 chart。多副本 HA（M5.4a relay 选主开箱安全）、distroless nonroot 硬化、httpGet 探针、`_FILE` 密钥、生产 TLS fail-close。

## 前置

- Kubernetes ≥ 1.23、Helm 3/4。
- 外部托管 PostgreSQL 与 Redis（生产）。
- **数据库迁移须先于安装完成**（本 chart 不含迁移，见 M5.3-migrate）：`make migrate-up` 或外部 Job 对目标库应用 `db/migrations`。

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

## 安全基线

- Pod：`runAsNonRoot`、uid/gid 65532、`fsGroup 65532`、seccomp RuntimeDefault。
- 容器：`readOnlyRootFilesystem`、`allowPrivilegeEscalation:false`、`drop:[ALL]`、`/tmp` emptyDir。
- 密钥：走 Secret + `SYDOM_*_FILE` 挂载路径，绝不作 env 明文值；DSN 在 Secret 内非 ConfigMap。
- SA：`automountServiceAccountToken:false`。

## sidecar 接入

见 `deploy/k8s/sidecar-reference.yaml`（同 Pod 边车模式）。
