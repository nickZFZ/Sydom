# M5.3-k8s-ext Helm chart 补 HPA + ServiceMonitor 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面 Helm chart 补 HPA（自动扩缩）+ ServiceMonitor（Prometheus Operator 抓取），门控 off-by-default、模板化。

**架构：** 纯 `deploy/helm/sydom-controlplane/`。HPA 门控 `autoscaling.enabled`，Deployment `replicas` 条件化；ServiceMonitor 门控 `serviceMonitor.enabled`，Service 加 `metrics` 端口。验证经 `helm lint`/`helm template`（开关两态断言）+ `kubectl --dry-run=client`。

**技术栈：** Helm 3、Kubernetes（autoscaling/v2 HPA、monitoring.coreos.com/v1 ServiceMonitor）。

规格：`docs/superpowers/specs/2026-07-14-sydom-m53k-ext-hpa-servicemonitor-design.md`

**注**：无 Go/单测，验证即 helm/kubectl 渲染断言（本领域的「测试」）。

---

## 文件结构

- **修改** `deploy/helm/sydom-controlplane/values.yaml` — 加 `autoscaling` + `serviceMonitor` 两块（默认关）
- **修改** `deploy/helm/sydom-controlplane/templates/deployment.yaml:8` — `replicas` 条件化（`not autoscaling.enabled`）
- **创建** `deploy/helm/sydom-controlplane/templates/hpa.yaml` — HPA（门控）
- **修改** `deploy/helm/sydom-controlplane/templates/service.yaml` — 加 `metrics` 端口
- **创建** `deploy/helm/sydom-controlplane/templates/servicemonitor.yaml` — ServiceMonitor（门控）
- **修改** `deploy/helm/sydom-controlplane/templates/NOTES.txt` — 记 HPA/SM 开关 + Ingress 仍延后

---

### 任务 1：HPA（values + Deployment replicas 条件化 + hpa.yaml）

**文件：**
- 修改：`deploy/helm/sydom-controlplane/values.yaml`
- 修改：`deploy/helm/sydom-controlplane/templates/deployment.yaml:8`
- 创建：`deploy/helm/sydom-controlplane/templates/hpa.yaml`

- [ ] **步骤 1：values.yaml 加 autoscaling 块**

在 `values.yaml` 的 `pdb:` 块之后（或 `resources:` 附近）加：

```yaml
# 自动扩缩（HPA，M5.3-k8s-ext）。默认关；开启则 Deployment 省略 replicas 交 HPA 管。
# 多副本安全见 M5.4a relay 选主（仅 relay/drain leader-gated，N 副本都服务 gRPC/REST/health）。
autoscaling:
  enabled: false
  minReplicas: 2
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80
```

- [ ] **步骤 2：Deployment replicas 条件化**

`templates/deployment.yaml` 第 7-8 行 `spec:` / `  replicas: {{ .Values.replicaCount }}` 改为：

```yaml
spec:
{{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
{{- end }}
  strategy:
```

- [ ] **步骤 3：创建 hpa.yaml**

`templates/hpa.yaml`：

```yaml
{{- if .Values.autoscaling.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "sydom-controlplane.fullname" . }}
  minReplicas: {{ .Values.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.autoscaling.maxReplicas }}
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ .Values.autoscaling.targetCPUUtilizationPercentage }}
{{- end }}
```

- [ ] **步骤 4：验证 HPA 两态**

运行：
```bash
cd /home/tongyu/codes/Sydom
helm lint deploy/helm/sydom-controlplane
# 默认：无 HPA，Deployment 有 replicas
helm template rel deploy/helm/sydom-controlplane | grep -c "kind: HorizontalPodAutoscaler"   # 期望 0
helm template rel deploy/helm/sydom-controlplane | grep -E "^  replicas: 2"                    # 期望命中
# 开启：有 HPA（min2/max5/cpu80），Deployment 无 replicas
helm template rel deploy/helm/sydom-controlplane --set autoscaling.enabled=true | grep -c "kind: HorizontalPodAutoscaler"  # 期望 1
helm template rel deploy/helm/sydom-controlplane --set autoscaling.enabled=true | grep -E "minReplicas: 2|maxReplicas: 5|averageUtilization: 80"
helm template rel deploy/helm/sydom-controlplane --set autoscaling.enabled=true | grep -c "^  replicas:"  # 期望 0
```
预期：lint 0 failed；默认 HPA 计数 0 + replicas 命中；开启 HPA 计数 1 + 三参数命中 + replicas 计数 0。

- [ ] **步骤 5：Commit**

```bash
git add deploy/helm/sydom-controlplane/values.yaml deploy/helm/sydom-controlplane/templates/deployment.yaml deploy/helm/sydom-controlplane/templates/hpa.yaml
git commit -m "feat(deploy): M5.3-k8s-ext HPA(autoscaling/v2 CPU 扩缩,门控默认关;min2〔M5.4a 选主多副本安全〕/max5/80;Deployment replicas 条件化避与 HPA 打架;helm template 两态验证)"
```

---

### 任务 2：ServiceMonitor（values + Service metrics 端口 + servicemonitor.yaml）

**文件：**
- 修改：`deploy/helm/sydom-controlplane/values.yaml`
- 修改：`deploy/helm/sydom-controlplane/templates/service.yaml`
- 创建：`deploy/helm/sydom-controlplane/templates/servicemonitor.yaml`

- [ ] **步骤 1：values.yaml 加 serviceMonitor 块**

在 `values.yaml` 的 `autoscaling` 块之后加：

```yaml
# Prometheus Operator 抓取（ServiceMonitor，M5.3-k8s-ext）。默认关；需集群已装 Prometheus Operator CRD。
# 与 podAnnotations 抓取二选一（Operator-native 路径）。
serviceMonitor:
  enabled: false
  interval: "30s"
  path: "/metrics"
  labels: {}          # 供 Prometheus serviceMonitorSelector 匹配
```

- [ ] **步骤 2：Service 加 metrics 端口**

`templates/service.yaml` 的 `ports:` 列表末尾（`grpc-sync` 端口之后）加：

```yaml
    - name: metrics
      port: {{ .Values.ports.health }}
      targetPort: health
      protocol: TCP
```

- [ ] **步骤 3：创建 servicemonitor.yaml**

`templates/servicemonitor.yaml`：

```yaml
{{- if .Values.serviceMonitor.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
    {{- with .Values.serviceMonitor.labels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "sydom-controlplane.selectorLabels" . | nindent 6 }}
  endpoints:
    - port: metrics
      path: {{ .Values.serviceMonitor.path }}
      interval: {{ .Values.serviceMonitor.interval }}
{{- end }}
```

- [ ] **步骤 4：验证 ServiceMonitor 两态 + metrics 端口恒在**

运行：
```bash
cd /home/tongyu/codes/Sydom
helm lint deploy/helm/sydom-controlplane
# metrics 端口恒在 Service（不受门控）
helm template rel deploy/helm/sydom-controlplane | grep -E "name: metrics"                       # 期望命中
# 默认：无 ServiceMonitor
helm template rel deploy/helm/sydom-controlplane | grep -c "kind: ServiceMonitor"                 # 期望 0
# 开启：有 ServiceMonitor（port metrics/path /metrics/interval 30s）
helm template rel deploy/helm/sydom-controlplane --set serviceMonitor.enabled=true | grep -c "kind: ServiceMonitor"   # 期望 1
helm template rel deploy/helm/sydom-controlplane --set serviceMonitor.enabled=true | grep -E "port: metrics|path: /metrics|interval: 30s"
```
预期：lint 0 failed；metrics 端口命中；默认 SM 计数 0；开启 SM 计数 1 + 三字段命中。

- [ ] **步骤 5：Commit**

```bash
git add deploy/helm/sydom-controlplane/values.yaml deploy/helm/sydom-controlplane/templates/service.yaml deploy/helm/sydom-controlplane/templates/servicemonitor.yaml
git commit -m "feat(deploy): M5.3-k8s-ext ServiceMonitor(monitoring.coreos.com/v1 抓 /metrics,门控默认关;Service 加 metrics 端口〔恒在,对齐既有 prometheus.io 注解 8083〕;helm template 两态验证)"
```

---

### 任务 3：全量验证（kubectl dry-run + 零触碰 + NOTES）

**文件：**
- 修改：`deploy/helm/sydom-controlplane/templates/NOTES.txt`

- [ ] **步骤 1：kubectl --dry-run 内建类型**

运行（HPA 开启态渲染，Deployment/Service/HPA 内建类型逐一 client 校验）：
```bash
cd /home/tongyu/codes/Sydom
helm template rel deploy/helm/sydom-controlplane --set autoscaling.enabled=true \
  | kubectl apply --dry-run=client -f - 2>&1 | grep -vE "ServiceMonitor|no matches for kind" | tail -20
```
预期：Deployment/Service/HPA/其它内建资源均 `... (dry run)` 通过（ServiceMonitor 未开启不在输出；若开启则 CRD 未装会报 no matches，按设计不经 kubectl 校验）。

- [ ] **步骤 2：ServiceMonitor YAML 合法性**

运行（helm 渲染即校验 YAML 结构；再确认可被 YAML 解析）：
```bash
cd /home/tongyu/codes/Sydom
helm template rel deploy/helm/sydom-controlplane --set serviceMonitor.enabled=true \
  | python3 -c "import sys,yaml; list(yaml.safe_load_all(sys.stdin)); print('YAML-OK')"
```
预期：`YAML-OK`（多文档全部合法解析，含 ServiceMonitor）。若无 python3，改用 `helm template ... >/dev/null && echo render-ok`（helm 渲染成功即结构合法）。

- [ ] **步骤 3：更新 NOTES.txt**

在 `NOTES.txt` 末尾加一段（记新开关 + Ingress 仍延后）：

```
自动扩缩（HPA）：--set autoscaling.enabled=true（CPU 目标 {{ .Values.autoscaling.targetCPUUtilizationPercentage }}%，{{ .Values.autoscaling.minReplicas }}-{{ .Values.autoscaling.maxReplicas }} 副本；开启后交 HPA 管副本）。
Prometheus 抓取：--set serviceMonitor.enabled=true（需集群已装 Prometheus Operator CRD；或用既有 pod 注解抓取）。
Ingress 暂未内置：控制面 admin 口为 gRPC(+REST)、console 为可选独立监听，接入需按所选 ingress controller（gRPC 支持）自行配置 Ingress/Gateway。
```

- [ ] **步骤 4：零触碰核验 + 改动面**

运行：
```bash
cd /home/tongyu/codes/Sydom
# 仅 deploy/helm 改动（零 Go / 零授权核心）
git diff --name-only bebac1e..HEAD 2>/dev/null | grep -vE '^deploy/helm/|^docs/' && echo "!!! 非 deploy/helm 改动" || echo "EMPTY ✓ 仅 deploy/helm（零 Go 零授权核心）"
git status --porcelain
```
（基线用本片起点=spec commit 前的 `b5732b6`；`git diff --name-only b5732b6..HEAD -- ':(exclude)docs'` 应仅列 `deploy/helm/sydom-controlplane/` 下文件。）
预期：改动仅 `deploy/helm/sydom-controlplane/`（values.yaml、deployment.yaml、service.yaml、hpa.yaml、servicemonitor.yaml、NOTES.txt）。

- [ ] **步骤 5：Commit NOTES**

```bash
git add deploy/helm/sydom-controlplane/templates/NOTES.txt
git commit -m "docs(deploy): M5.3-k8s-ext NOTES 记 HPA/ServiceMonitor 开关 + Ingress 仍延后（gRPC/console 监听接入按 controller 自配）"
```

---

## 验收对照（M53KE-1..7）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | 零 Go/零授权核心（机器 diff 仅 deploy/helm） | 任务 3 步骤 4 |
| 2 | `helm lint` 0 failed | 任务 1 步骤 4 / 任务 2 步骤 4 |
| 3 | 默认无 HPA/SM + Deployment replicas:2 | 任务 1 步骤 4 + 任务 2 步骤 4 |
| 4 | autoscaling.enabled → HPA(min2/max5/80) + Deployment 无 replicas | 任务 1 步骤 4 |
| 5 | serviceMonitor.enabled → SM 渲染 + Service metrics 端口 | 任务 2 步骤 4 |
| 6 | kubectl --dry-run Deployment/Service/HPA OK + SM YAML 合法 | 任务 3 步骤 1-2 |
| 7 | values 两块默认关 + Ingress 延后记明 | 任务 1/2 步骤 1 + 任务 3 步骤 3 |

## 自检

**1. 规格覆盖度：** HPA(任务1)、ServiceMonitor(任务2)、Service metrics 口(任务2)、Deployment replicas 条件化(任务1)、values 两块(任务1/2)、验证(任务3)、Ingress 延后记明(任务3 NOTES)。无遗漏。

**2. 占位符扫描：** 无 TODO；每步含完整 YAML/命令。

**3. 类型一致性：** `autoscaling.{enabled,minReplicas,maxReplicas,targetCPUUtilizationPercentage}`（任务1 values）与 hpa.yaml + deployment 条件一致；`serviceMonitor.{enabled,interval,path,labels}`（任务2 values）与 servicemonitor.yaml 一致；Service `metrics` 端口名与 servicemonitor.yaml `port: metrics` 一致；`ports.health`（8083）既有；helm helper `sydom-controlplane.fullname/labels/selectorLabels` 均已存在（_helpers.tpl 核实）。
