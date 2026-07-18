# 服务水平目标（SLO/SLA）与告警

司域 GA 的服务水平目标、其度量口径，以及触发时的处置 runbook。告警规则由 Helm chart 的
`PrometheusRule`（`prometheusRule.enabled=true`）下发，全部基于 M5.1 可观测基座暴露的指标
（`internal/obs`，`/metrics`）。阈值为**推荐默认**，据实际流量与业务承诺调优。

## 度量前提

- **控制面指标**（`sydom_grpc_*`、`sydom_http_*`、`sydom_relay_leader`）由控制面 Pod 暴露，
  经 ServiceMonitor / podAnnotations 抓取（见 `values.yaml` 的 `serviceMonitor`）。
- **数据面指标**（`sydom_authz_*`、`sydom_cache_*`、`sydom_sidecar_connected`）由**边车**暴露。
  这些 SLO 生效的前提是**同一 Prometheus 也抓取边车 `/metrics`**（边车参考清单
  `deploy/k8s/sidecar-reference.yaml` 已带 Prometheus 注解）。
- `PrometheusRule` 是集群级对象，随控制面 chart 一并下发；它跨控制面 + 数据面聚合，反映**整体**服务水平。

## SLO 定义

| SLO | 目标（默认） | 度量（记录规则） | 依据 |
|---|---|---|---|
| 控制面可用性 | 服务端故障率 < 1%（≈99% 成功） | `sydom:grpc_server_fault_ratio:rate5m` | 只计**服务端**故障码 `Internal/Unknown/Unavailable/DataLoss`；客户端错误（`InvalidArgument/AlreadyExists/FailedPrecondition/NotFound/PermissionDenied`）不计——此区分由 [M6 error-semantics](../security-review-ga.md) 细分 `code` 后才成立 |
| REST/Console 可用性 | HTTP 5xx 率 < 1% | `sydom:http_server_fault_ratio:rate5m` | 用户面 5xx 计入，4xx（客户端）不计 |
| 数据面授权延迟 | Check p99 < 50ms | `sydom:authz_check_latency_p99:5m` | [M5.5 基线](performance-baselines.md)：缓存命中 137ns、未命中 rules=100 ~204µs（进程内）；50ms 为端到端含 gRPC 往返的宽裕上界 |
| 缓存命中率 | ≥ 80% | `sydom:cache_hit_ratio:rate5m` | M5.5：命中比未命中快 ~1487×，容量与延迟**强依赖**命中率 |
| 数据面连接性 | `sydom_sidecar_connected == 1` | 直接量规 | 断连即无法同步策略、服务陈旧快照 |
| relay 选主健康 | `sum(sydom_relay_leader) == 1` | 直接量规聚合 | 恰 1 个 leader drain outbox（[M5.4a](../../internal/controlplane/outbox) `pg_advisory_lock` 保证）；0=策略不广播，>1=疑似脑裂 |

## 告警 → 处置 runbook

| 告警 | 级别 | 含义 | 首要处置 |
|---|---|---|---|
| `SydomControlPlaneHighErrorRate` | warning | gRPC 服务端故障率 >1% 持续 5m | 查控制面日志 `code=Internal` 明细（脱敏后原始详情在日志）；多为 DB/依赖抖动 |
| `SydomControlPlaneHighErrorRateCritical` | critical | 服务端故障率 >5% 持续 5m | 大概率 DB 不可用或迁移中；查 DB 连接/`-migrate` Job 状态，必要时回滚（见[零停机迁移](zero-downtime-migrations.md)） |
| `SydomRestConsoleHighErrorRate` | warning | HTTP 5xx >1% 持续 5m | 同上，聚焦 REST 网关/Console BFF；`handler` label 定位面 |
| `SydomAuthzCheckLatencyHigh` | warning | Check p99 >50ms 持续 10m | 先看 `SydomCacheHitRatioLow` 是否同时触发（命中率跌是首因）；否则查策略规模是否激增（M5.5：未命中随 rules 近线性） |
| `SydomCacheHitRatioLow` | warning | 命中率 <80% 持续 15m | 大量唯一 subject/object 会压低命中率；评估扩容边车副本或增大 LRU（内核 `NewBoundedCache`）；持续低则延迟按未命中量级估算容量 |
| `SydomSidecarDisconnected` | critical | `sydom_sidecar_connected=0` 持续 2m | 边车正以最后快照服务——**撤权/新授权不生效**；查控制面 sync 监听器可达性、mTLS 证书、网络策略 |
| `SydomNoRelayLeader` | critical | `sum(sydom_relay_leader)<1` 持续 5m | 无副本持锁，outbox 未 drain、策略变更不广播；查控制面副本存活与 PG advisory lock 会话（见 M5.4a）；leader 重新参选带退避，短暂 0 属正常 failover |
| `SydomMultipleRelayLeaders` | critical | `sum(sydom_relay_leader)>1` 持续 5m | 应恰 1 个；持续 >1 说明选主/指标异常，可能重复投递（投递幂等兜底，但须排查）；查是否有陈旧 Pod 未清指标 |

## 阈值调优

在 `values.yaml` 的 `prometheusRule` 下调整（无需改模板）：

```yaml
prometheusRule:
  enabled: true
  grpcFaultRatioWarning: 0.01     # 可用性 warning 阈（据你的 SLA 承诺定）
  grpcFaultRatioCritical: 0.05    # critical 阈
  checkLatencyP99Seconds: 0.05    # 延迟 p99 上界（秒）
  cacheHitRatioMin: 0.8           # 命中率下界
  labels:                          # 供 Prometheus ruleSelector 匹配
    release: kube-prometheus-stack
```

**错误预算**：99% 可用性 ≈ 每月 ~7.2h 停机预算；99.9% ≈ ~43m。若采纳更严目标，须相应下调
`grpcFaultRatioWarning` 并复核容量（M5.5 基线 + HPA `autoscaling`）。

## 启用

1. 集群已装 Prometheus Operator（提供 `monitoring.coreos.com/v1` CRD）。
2. `helm upgrade --install ... --set prometheusRule.enabled=true --set serviceMonitor.enabled=true`。
3. 确认边车 `/metrics` 亦被抓取（否则数据面 SLO 无数据）。
4. 记录规则 `sydom:*` 可直接用于 Grafana 仪表盘。

## 相关

- [授权决策性能基线](performance-baselines.md) — 延迟/命中率阈值的实测依据
- [GA 安全评审](../security-review-ga.md) — error-semantics 对 `code` 的细分使可用性 SLO 成立
- [零停机迁移](zero-downtime-migrations.md) — 高故障率常与迁移相关
- Helm chart `values.yaml` 的 `serviceMonitor` / `prometheusRule` / `autoscaling` 块
