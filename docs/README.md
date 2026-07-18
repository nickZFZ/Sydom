# 司域(Sydom) 文档索引

一张地图，指向所有面向使用者/运维者的文档。按**评估 → 部署 → 运维 → 安全 → API 契约**分组。

> 本页只做导航，不复述内容——细节以各文档为准。设计/实现过程记录另见 `docs/superpowers/`（specs 与 plans），非本索引范围。

## 评估 · 先跑起来看

| 文档 | 覆盖 | 何时看 |
|---|---|---|
| [项目 README](../README.md) | 项目释义 + **一键 Demo**（`make demo` 起全栈：PG+Redis+控制面+Sidecar+订单服务） | 第一次接触，想在浏览器里直观看权限差异 |
| [浏览器走查 WALKTHROUGH](../test/e2e/browser/WALKTHROUGH.md) | 真人逐屏走查（含截图），印证功能权限 / 数据权限 / fail-close 三件事 | 想看端到端行为如何被验证 |

## 部署 · 把它跑到环境里

| 文档 | 覆盖 | 何时用 |
|---|---|---|
| [Docker Compose 部署 Runbook](../deploy/README.md) | 单机/测试全栈：密钥准备、明文起栈、TLS 开启、健康探针、容器镜像硬化（distroless nonroot）、证书权限、运维注记 | 单机、测试环境，或本地贴近生产的验证 |
| [Kubernetes Helm Chart](../deploy/helm/sydom-controlplane/README.md) | 生产 k8s：多副本 HA（M5.4a relay 选主）、`existingSecret` 外部注入、生产 TLS fail-close、迁移 Job、PDB、HPA/ServiceMonitor 开关、安全基线 | 生产 Kubernetes 部署 |
| [Sidecar 边车参考清单](../deploy/k8s/sidecar-reference.yaml) | 同 Pod 边车模式的 sidecar 部署参考 | 在 k8s 里给业务 Pod 挂 sidecar |

## 运维 · 长期运行

| 文档 | 覆盖 | 何时用 |
|---|---|---|
| [零停机迁移](runbooks/zero-downtime-migrations.md) | expand/contract 纪律、`maxUnavailable:0`、pre-upgrade 迁移 Job fail-close | 发版含 DB schema 变更时 |
| [备份与恢复](runbooks/backup-restore.md) | 逻辑 `pg_dump` 备份/恢复脚本、CronJob、两层策略（逻辑备份 + 委托托管 PG PITR）、DR 步骤、RPO/RTO | 建立备份策略 / 灾难恢复演练 |
| [授权决策性能基线](runbooks/performance-baselines.md) | 决策热路径 benchmark 实测基线、容量估算、benchstat 回归对照 | 容量规划 / 改动内核前立基线 |
| [SLO/SLA 与告警](runbooks/service-level-objectives.md) | 服务水平目标（可用性/延迟/命中率/连接性/leader）、PrometheusRule 告警、告警→处置 runbook、阈值调优 | 上生产监控 / 定 SLA 承诺 / 告警响应 |

## 安全 · 信任边界

| 文档 | 覆盖 | 何时看 |
|---|---|---|
| [GA 安全评审](security-review-ga.md) | 信任边界源码级审计：静态加密/认证/多租户授权/会话/输出边界的已验证强项、加固建议、一处停用≠吊销的语义澄清（F-1） | GA 前安全评估、事件响应查凭据吊销路径、决定是否做纵深加固 |

## API 契约 · 集成与演进

| 文档 | 覆盖 | 何时用 |
|---|---|---|
| [API 版本化 + 向后兼容](api-versioning.md) | v1 wire/JSON/源码兼容契约、proto 演进可否表、弃用流程、何时 v2、GA tag 冻结基线 | 依赖 gRPC/REST 契约做集成，或演进 proto 前 |
