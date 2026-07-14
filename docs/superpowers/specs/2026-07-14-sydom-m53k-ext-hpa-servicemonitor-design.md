# M5.3-k8s-ext Helm chart 补 HPA + ServiceMonitor — 设计规格

**日期**：2026-07-14
**里程碑**：M5.3 部署硬化 · k8s chart 扩展（补 M5.3-k8s NOTES 显式延后件）
**前序**：M5.3-k8s 控制面 Helm chart（`deploy/helm/sydom-controlplane/`）、M5.4a 控制面多副本安全（relay 选主）

## 目标

给控制面 Helm chart 补两件标准生产组件——自动扩缩（HPA）+ Prometheus Operator 抓取（ServiceMonitor），门控 off-by-default、模板化合理默认。**纯 `deploy/helm/` 零 Go、零触碰授权核心、helm/kubectl 本地强验证。**

## 范围决策

**本片只做 HPA + ServiceMonitor。** 二者是标准、决策无关、可强验证的生产组件。

**Ingress 延后**：控制面 `console_addr` 可选且当前不在 chart 的 `ports` 里（chart 只暴露 admin/sync/health）；admin 口是 gRPC(+REST)，gRPC Ingress 依赖具体 ingress controller 选型（annotations/appProtocol/host），决策性强。Ingress 需先把 console listener 纳入 chart + 定 ingress controller，是独立后续增量。

## 非目标（YAGNI）

- Ingress（见范围决策）
- VPA / 自定义指标 HPA（CPU 利用率足够起步）
- 改既有 podAnnotations 抓取路径（ServiceMonitor 是并存的 Operator-native 备选，用户二选一）

## 架构与组件

### 1. HPA（`templates/hpa.yaml`，门控 `autoscaling.enabled` 默认 false）

`autoscaling/v2` HorizontalPodAutoscaler，`scaleTargetRef` 指向本 chart 的 Deployment，按 CPU 利用率扩缩。

- `minReplicas`（默认 2）、`maxReplicas`（默认 5）、`averageUtilization`（默认 80）。
- **多副本安全依据**：M5.4a relay 选主使多副本安全——N 副本都服务 admin gRPC/REST/health，仅 relay/drain 是 leader-gated（`pg_try_advisory_lock`），故 HPA 扩副本不会重复投递。`minReplicas: 2` 与 chart `replicaCount: 2` + PDB `minAvailable: 1` 一致。
- `resources.requests.cpu`（现 `100m`）已存在——HPA CPU 目标利用率前提满足。

**deployment.yaml 配套**：`replicas` 行改为仅在 `not .Values.autoscaling.enabled` 时渲染：

```yaml
{{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
{{- end }}
```

HPA 开启时省略 `replicas`（避免 `helm upgrade` 把副本重置回 replicaCount 与 HPA 打架——标准 Helm 范式）。

### 2. ServiceMonitor（`templates/servicemonitor.yaml`，门控 `serviceMonitor.enabled` 默认 false）

`monitoring.coreos.com/v1` ServiceMonitor（Prometheus Operator CRD），经 Service 端点抓 `/metrics`。

- `selector.matchLabels` 用 chart selectorLabels；`endpoints`：`port: metrics`、`path`（默认 `/metrics`）、`interval`（默认 `30s`）。
- 可选 `serviceMonitor.labels`（供 Prometheus `serviceMonitorSelector` 匹配）与 `namespace`（默认随 release）。

**service.yaml 配套**：加一个 `metrics` 命名端口，使 ServiceMonitor 经 Service 端点可达（现 Service 只暴露 grpc-admin/grpc-sync）：

```yaml
    - name: metrics
      port: {{ .Values.ports.health }}
      targetPort: health
      protocol: TCP
```

端口对齐既有 podAnnotations（`prometheus.io/port: 8083`、`prometheus.io/path: /metrics`）；health 容器口（8083）服务 `/healthz`+`/readyz`+`/metrics`（明文 HTTP）。

### 3. values.yaml（加两块，默认关）

```yaml
# 自动扩缩（HPA）。默认关；开启则 Deployment 省略 replicas 交 HPA 管。
# 多副本安全见 M5.4a relay 选主（仅 relay/drain leader-gated）。
autoscaling:
  enabled: false
  minReplicas: 2
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80

# Prometheus Operator 抓取（ServiceMonitor）。默认关；需集群已装 Prometheus Operator CRD。
# 与 podAnnotations 抓取二选一（Operator-native 路径）。
serviceMonitor:
  enabled: false
  interval: "30s"
  path: "/metrics"
  labels: {}          # 供 Prometheus serviceMonitorSelector 匹配
```

## 测试 / 验证（helm + kubectl 本地强验证）

- `helm lint deploy/helm/sydom-controlplane` → 0 failed。
- `helm template`（默认，两者关）→ 输出**无** `kind: HorizontalPodAutoscaler`、**无** `kind: ServiceMonitor`；Deployment 含 `replicas: 2`。
- `helm template --set autoscaling.enabled=true` → 输出含 HPA（min2/max5/cpu80，scaleTargetRef 指向 fullname Deployment）；Deployment **不含** `replicas:`。
- `helm template --set serviceMonitor.enabled=true` → 输出含 ServiceMonitor（port metrics/path /metrics/interval 30s）；Service 含 `name: metrics` 端口（metrics 端口应恒在 Service，不受门控——ServiceMonitor 门控仅控 ServiceMonitor 资源本身）。
- `kubectl apply --dry-run=client`：对 `helm template --set autoscaling.enabled=true` 的 Deployment/Service/HPA（内建类型）逐一 OK；ServiceMonitor（Operator CRD 本地未装）经 `helm template` 渲染 + YAML 合法性核验（`python -c yaml.safe_load` 或 helm 渲染本身即校验），不走 kubectl server 校验。
- 零 Go / 零授权核心触碰：机器 diff 仅 `deploy/helm/sydom-controlplane/`。

## 不变量

- 零触碰授权核心与所有 Go
- 既有 chart 模板除 `service.yaml`（加 metrics 口）、`deployment.yaml`（replicas 条件化）外不动
- metrics 端口恒加到 Service（内部 ClusterIP，无害）；HPA/ServiceMonitor 资源本身门控默认关（默认渲染无新资源类型）
- Ingress 延后

## 验收（M53KE-1..7）

1. 零触碰授权核心 + 零 Go（机器 diff 仅 `deploy/helm/`）
2. `helm lint` 0 failed
3. 默认渲染无 HPA/ServiceMonitor + Deployment 有 replicas:2
4. `autoscaling.enabled=true` → HPA 渲染（min2/max5/cpu80）+ Deployment 无 replicas
5. `serviceMonitor.enabled=true` → ServiceMonitor 渲染 + Service 含 metrics 端口
6. `kubectl --dry-run=client` 对 Deployment/Service/HPA OK；ServiceMonitor YAML 合法
7. values.yaml 两块默认关；Ingress 延后（NOTES/spec 记明）
