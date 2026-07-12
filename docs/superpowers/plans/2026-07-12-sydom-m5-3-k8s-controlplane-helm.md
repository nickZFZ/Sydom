# M5.3-k8s 控制面 Helm Chart + Sidecar 参考清单 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 交付 `deploy/helm/sydom-controlplane/` Helm chart（控制面多副本 HA + distroless nonroot 硬化 + httpGet 探针 + `_FILE` 密钥 + 生产 TLS）与 `deploy/k8s/sidecar-reference.yaml`（客户侧接入参考），把前序 M5 硬化以 k8s 惯用方式部署起来。

**架构：** 一个 Helm chart 渲染 Secret（config.yaml 含 DSN + 主/根密钥 + 可选 TLS）、Deployment（`replicas≥2`、runAsNonRoot uid 65532、readOnlyRootFilesystem、drop ALL、httpGet `/healthz`·`/readyz`、`SYDOM_*_FILE` env 指向挂载密钥）、Service（ClusterIP grpc-admin/grpc-sync）、PDB、SA；外部托管 PG/Redis 经 values endpoint；`existingSecret` 支持外部密钥注入。sidecar 参考为带注释的原始双容器清单。

**技术栈：** Helm v4.2.0（本机）、Kubernetes（apiVersion v2 chart / apps/v1 / policy/v1）、`helm lint`/`helm template` 校验（无 kubeconform/kustomize，回退 helm template + grep 断言 + 可选 `kubectl --dry-run=client`）。

**BASE：** `feat/m5-3-k8s-controlplane-helm` @ `b00ac9a`（含设计规格）；规格 `docs/superpowers/specs/2026-07-12-sydom-m5-3-k8s-controlplane-helm-design.md`。

**零触碰铁律：** `git diff e3feed0..HEAD -- '*.go' casbin/ adminauthz/ internal/` 必须为空（纯 `deploy/*`+`docs/*`）。

---

## 约定：渲染校验命令

多数步骤用 `helm template` 渲染后 grep 断言。密钥字段有 `required`，故渲染需带占位值（helm 不校验值内容，Go 运行时才校验）。约定复用一组占位 `--set`：

```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=Zm9vYmFyMzJieXRlc2Zvb2Jhcm1hc3RlcmtleTEy --set secrets.rootSecret=rootsecretplaceholder --set tls.cert=PEMCERT --set tls.key=PEMKEY'
```

---

## 任务 1：Chart 骨架（Chart.yaml + values + helpers + SA + Service）

**文件：**
- 创建：`deploy/helm/sydom-controlplane/Chart.yaml`
- 创建：`deploy/helm/sydom-controlplane/.helmignore`
- 创建：`deploy/helm/sydom-controlplane/values.yaml`
- 创建：`deploy/helm/sydom-controlplane/templates/_helpers.tpl`
- 创建：`deploy/helm/sydom-controlplane/templates/serviceaccount.yaml`
- 创建：`deploy/helm/sydom-controlplane/templates/service.yaml`

- [ ] **步骤 1：写 Chart.yaml**

```yaml
apiVersion: v2
name: sydom-controlplane
description: 司域(Sydom)控制面 —— 多副本安全的多租户授权控制平面
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.23.0-0"
sources:
  - https://github.com/nickZFZ/Sydom
```

- [ ] **步骤 2：写 .helmignore**

```
.git
*.md
ci/
*.tmp
.DS_Store
```

- [ ] **步骤 3：写 values.yaml**

```yaml
# 司域控制面 Helm values。生产请覆盖 image.digest / database / secrets / tls / existingSecret。
replicaCount: 2

image:
  repository: sydom/controlplane
  # 优先 digest（对齐 M5.3b 内容寻址防漂移）；digest 非空则忽略 tag。
  digest: ""            # 例：sha256:...
  tag: "latest"
  pullPolicy: IfNotPresent
imagePullSecrets: []

# 外部托管 PG / Redis endpoint。database.dsn 含口令 → 渲染进 Secret（非 ConfigMap）。
database:
  dsn: "postgres://sydom:CHANGEME@postgres:5432/sydom?sslmode=require"
redis:
  addr: "redis:6379"

# 密钥。existingSecret 非空 → 复用既有 Secret（生产推荐，外部注入），chart 不渲染 secret.yaml；
# 该 Secret 须含键 config.yaml/master-key/root-secret[/tls.crt/tls.key/sync-client-ca.crt]。
# existingSecret 为空 → chart 用下方值自建 Secret（起步/非生产便利路径）。
existingSecret: ""
secrets:
  masterKey: ""         # base64，解码须 32 字节（→ SYDOM_MASTER_KEY）
  rootSecret: ""        # 原始字节（→ SYDOM_ROOT_SECRET）

environment: production

# 生产模式传输 TLS（M5.3a fail-close：production 缺 cert/key 拒启动）。
tls:
  enabled: true
  cert: ""              # PEM；existingSecret 为空时写入自建 Secret
  key: ""               # PEM
  syncClientCA: ""      # policysync mTLS（M5.2b）：非空则校验 sidecar 客户端证书

controlPlane:
  rootPrincipal: "root@sydom"
  heartbeatInterval: "30s"
  relayPollInterval: "1s"

ports:
  admin: 8081
  sync: 8082
  health: 8083

service:
  type: ClusterIP

resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits: { cpu: 500m, memory: 256Mi }

pdb:
  enabled: true
  minAvailable: 1

podAnnotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8083"
  prometheus.io/path: "/metrics"

serviceAccount:
  create: true
  name: ""

nameOverride: ""
fullnameOverride: ""
nodeSelector: {}
tolerations: []
affinity: {}
```

- [ ] **步骤 4：写 templates/_helpers.tpl**

```
{{- define "sydom-controlplane.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sydom-controlplane.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "sydom-controlplane.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "sydom-controlplane.labels" -}}
app.kubernetes.io/name: {{ include "sydom-controlplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sydom
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "sydom-controlplane.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sydom-controlplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "sydom-controlplane.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "sydom-controlplane.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "sydom-controlplane.secretName" -}}
{{- if .Values.existingSecret -}}
{{- .Values.existingSecret -}}
{{- else -}}
{{- include "sydom-controlplane.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "sydom-controlplane.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.tag -}}
{{- end -}}
{{- end -}}
```

- [ ] **步骤 5：写 templates/serviceaccount.yaml**

```
{{- if .Values.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "sydom-controlplane.serviceAccountName" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
automountServiceAccountToken: false
{{- end }}
```

- [ ] **步骤 6：写 templates/service.yaml**

```
apiVersion: v1
kind: Service
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  selector:
    {{- include "sydom-controlplane.selectorLabels" . | nindent 4 }}
  ports:
    - name: grpc-admin
      port: {{ .Values.ports.admin }}
      targetPort: grpc-admin
      protocol: TCP
      appProtocol: grpc
    - name: grpc-sync
      port: {{ .Values.ports.sync }}
      targetPort: grpc-sync
      protocol: TCP
      appProtocol: grpc
```

- [ ] **步骤 7：验证 lint + 渲染**

运行：
```bash
helm lint deploy/helm/sydom-controlplane
helm template t deploy/helm/sydom-controlplane --set serviceAccount.create=true 2>&1 | grep -E "kind: (ServiceAccount|Service)|grpc-admin|grpc-sync|automountServiceAccountToken"
```
预期：`helm lint` `1 chart(s) linted, 0 chart(s) failed`；渲染含 `kind: ServiceAccount`、`kind: Service`、两个 `grpc-*` 端口、`automountServiceAccountToken: false`。

- [ ] **步骤 8：Commit**

```bash
git add deploy/helm/sydom-controlplane/
git commit -m "feat(deploy): M5.3-k8s 控制面 Helm chart 骨架(Chart/values/helpers/SA〔不挂 token〕/Service〔grpc-admin·grpc-sync ClusterIP〕)"
```

---

## 任务 2：Secret 模板（config.yaml 含 DSN + 主/根密钥 + TLS，existingSecret 门控）

**文件：**
- 创建：`deploy/helm/sydom-controlplane/templates/secret.yaml`

- [ ] **步骤 1：写 templates/secret.yaml**

```
{{- if not .Values.existingSecret }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
type: Opaque
stringData:
  config.yaml: |
    database_dsn: {{ .Values.database.dsn | quote }}
    redis_addr: {{ .Values.redis.addr | quote }}
    admin_addr: ":{{ .Values.ports.admin }}"
    sync_addr: ":{{ .Values.ports.sync }}"
    health_addr: ":{{ .Values.ports.health }}"
    root_principal: {{ .Values.controlPlane.rootPrincipal | quote }}
    heartbeat_interval: {{ .Values.controlPlane.heartbeatInterval | quote }}
    relay_poll_interval: {{ .Values.controlPlane.relayPollInterval | quote }}
    environment: {{ .Values.environment | quote }}
    {{- if .Values.tls.enabled }}
    tls_cert_file: "/etc/sydom/tls/tls.crt"
    tls_key_file: "/etc/sydom/tls/tls.key"
    {{- end }}
    {{- if .Values.tls.syncClientCA }}
    sync_client_ca_file: "/etc/sydom/tls/sync-client-ca.crt"
    {{- end }}
  master-key: {{ required "secrets.masterKey required when existingSecret is empty" .Values.secrets.masterKey | quote }}
  root-secret: {{ required "secrets.rootSecret required when existingSecret is empty" .Values.secrets.rootSecret | quote }}
  {{- if .Values.tls.enabled }}
  tls.crt: {{ required "tls.cert required when tls.enabled and existingSecret empty" .Values.tls.cert | quote }}
  tls.key: {{ required "tls.key required when tls.enabled and existingSecret empty" .Values.tls.key | quote }}
  {{- end }}
  {{- if .Values.tls.syncClientCA }}
  sync-client-ca.crt: {{ .Values.tls.syncClientCA | quote }}
  {{- end }}
{{- end }}
```

- [ ] **步骤 2：验证自建 Secret 渲染**

运行（用约定的 `$SET`）：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
helm template t $CP $SET 2>&1 | grep -E "kind: Secret|config.yaml:|master-key:|root-secret:|tls.crt:|environment: \"production\"|database_dsn:"
```
预期：含 `kind: Secret`、`config.yaml:`、`master-key:`、`root-secret:`、`tls.crt:`、`environment: "production"`、`database_dsn:`（DSN 在 Secret 内）。

- [ ] **步骤 3：验证 existingSecret 时不渲染 Secret**

运行：
```bash
helm template t $CP --set existingSecret=my-cp-secret 2>&1 | grep -c "kind: Secret" || true
```
预期：输出 `0`（existingSecret 设定后 chart 不自建 Secret，也不触发 masterKey/tls required）。

- [ ] **步骤 4：验证 masterKey 缺失时 required 报错**

运行：
```bash
helm template t $CP 2>&1 | grep -E "secrets.masterKey required" && echo "REQUIRED-OK"
```
预期：报错含 `secrets.masterKey required`（证 fail-close：自建路径强制提供密钥）。

- [ ] **步骤 5：Commit**

```bash
git add deploy/helm/sydom-controlplane/templates/secret.yaml
git commit -m "feat(deploy): M5.3-k8s Secret 模板(config.yaml 含 DSN 落 Secret 非 ConfigMap+主/根密钥+TLS;existingSecret 门控外部注入;required 强制自建路径给密钥)"
```

---

## 任务 3：Deployment 模板（硬化 + HA + httpGet 探针 + subPath 密钥挂载）

**文件：**
- 创建：`deploy/helm/sydom-controlplane/templates/deployment.yaml`

- [ ] **步骤 1：写 templates/deployment.yaml**

```
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      {{- include "sydom-controlplane.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      annotations:
        {{- toYaml .Values.podAnnotations | nindent 8 }}
        checksum/secret: {{ include (print $.Template.BasePath "/secret.yaml") . | sha256sum }}
      labels:
        {{- include "sydom-controlplane.selectorLabels" . | nindent 8 }}
    spec:
      serviceAccountName: {{ include "sydom-controlplane.serviceAccountName" . }}
      automountServiceAccountToken: false
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: controlplane
          image: {{ include "sydom-controlplane.image" . | quote }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          args: ["-config", "/etc/sydom/config.yaml"]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          env:
            - name: SYDOM_ENVIRONMENT
              value: {{ .Values.environment | quote }}
            - name: SYDOM_MASTER_KEY_FILE
              value: "/etc/sydom/secrets/master-key"
            - name: SYDOM_ROOT_SECRET_FILE
              value: "/etc/sydom/secrets/root-secret"
          ports:
            - name: grpc-admin
              containerPort: {{ .Values.ports.admin }}
            - name: grpc-sync
              containerPort: {{ .Values.ports.sync }}
            - name: health
              containerPort: {{ .Values.ports.health }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: {{ .Values.ports.health }}
            initialDelaySeconds: 5
            periodSeconds: 10
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: {{ .Values.ports.health }}
            initialDelaySeconds: 3
            periodSeconds: 5
            failureThreshold: 3
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          volumeMounts:
            - name: config
              mountPath: /etc/sydom/config.yaml
              subPath: config.yaml
              readOnly: true
            - name: config
              mountPath: /etc/sydom/secrets/master-key
              subPath: master-key
              readOnly: true
            - name: config
              mountPath: /etc/sydom/secrets/root-secret
              subPath: root-secret
              readOnly: true
            {{- if .Values.tls.enabled }}
            - name: config
              mountPath: /etc/sydom/tls/tls.crt
              subPath: tls.crt
              readOnly: true
            - name: config
              mountPath: /etc/sydom/tls/tls.key
              subPath: tls.key
              readOnly: true
            {{- end }}
            {{- if .Values.tls.syncClientCA }}
            - name: config
              mountPath: /etc/sydom/tls/sync-client-ca.crt
              subPath: sync-client-ca.crt
              readOnly: true
            {{- end }}
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: config
          secret:
            secretName: {{ include "sydom-controlplane.secretName" . }}
        - name: tmp
          emptyDir: {}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
```

- [ ] **步骤 2：验证硬化 + 探针 + 密钥卫生渲染**

运行：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
helm template t $CP $SET --set image.digest=sha256:abc123 2>&1 | grep -E "runAsNonRoot: true|runAsUser: 65532|readOnlyRootFilesystem: true|drop:|- ALL|replicas: 2|path: /healthz|path: /readyz|SYDOM_MASTER_KEY_FILE|@sha256:abc123|subPath: master-key"
```
预期：命中 `runAsNonRoot: true`、`runAsUser: 65532`、`readOnlyRootFilesystem: true`、`drop:`+`- ALL`、`replicas: 2`、`path: /healthz`、`path: /readyz`、`SYDOM_MASTER_KEY_FILE`、`@sha256:abc123`（digest 优先）、`subPath: master-key`。

- [ ] **步骤 3：验证无明文密钥泄漏到 env 值**

运行：
```bash
helm template t $CP $SET 2>&1 | grep -A1 "SYDOM_MASTER_KEY_FILE" | grep -E "value: \"/etc/sydom/secrets/master-key\"" && echo "FILE-PATH-OK"
# 断言 env 里没有明文 BASE64KEY（只在 Secret stringData 出现）
test $(helm template t $CP $SET 2>&1 | grep -c "value: \"BASE64KEY\"") -eq 0 && echo "NO-PLAINTEXT-ENV-OK"
```
预期：`FILE-PATH-OK` + `NO-PLAINTEXT-ENV-OK`（密钥走 `_FILE` 挂载路径，绝不作 env 明文值）。

- [ ] **步骤 4：Commit**

```bash
git add deploy/helm/sydom-controlplane/templates/deployment.yaml
git commit -m "feat(deploy): M5.3-k8s Deployment(replicas 2 HA〔M5.4a 选主开箱安全〕+distroless nonroot uid 65532/readOnlyRoot/drop ALL+httpGet /healthz·/readyz〔distroless 无 shell 友好〕+SYDOM_*_FILE 走 subPath 挂载密钥不进 env 明文+digest 优先)"
```

---

## 任务 4：PodDisruptionBudget + NOTES.txt

**文件：**
- 创建：`deploy/helm/sydom-controlplane/templates/poddisruptionbudget.yaml`
- 创建：`deploy/helm/sydom-controlplane/templates/NOTES.txt`

- [ ] **步骤 1：写 templates/poddisruptionbudget.yaml**

```
{{- if and .Values.pdb.enabled (gt (int .Values.replicaCount) 1) }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "sydom-controlplane.fullname" . }}
  labels:
    {{- include "sydom-controlplane.labels" . | nindent 4 }}
spec:
  minAvailable: {{ .Values.pdb.minAvailable }}
  selector:
    matchLabels:
      {{- include "sydom-controlplane.selectorLabels" . | nindent 6 }}
{{- end }}
```

- [ ] **步骤 2：写 templates/NOTES.txt**

```
司域控制面已部署：{{ include "sydom-controlplane.fullname" . }}（副本 {{ .Values.replicaCount }}）。

⚠️ 迁移时序：本 chart 不含数据库迁移（归 M5.3-migrate 零停机切片）。
   首次安装/升级前，请先对目标库应用 db/migrations（make migrate-up 或外部 Job）。

多副本 HA：M5.4a relay 选主使 {{ .Values.replicaCount }} 副本开箱安全——
   仅一个副本经 PG advisory-lock 成为 outbox relay leader，leader 崩溃自动 failover。

sidecar 拨入：控制面 policysync 在 Service {{ include "sydom-controlplane.fullname" . }}:{{ .Values.ports.sync }}。
   sidecar 参考清单见 deploy/k8s/sidecar-reference.yaml。

指标：Prometheus 按 Pod 注解抓 :{{ .Values.ports.health }}/metrics。
健康：liveness :{{ .Values.ports.health }}/healthz、readiness :{{ .Values.ports.health }}/readyz。
{{- if not .Values.existingSecret }}

注意：当前用 chart 自建 Secret。生产请改用 existingSecret 由外部密钥管理注入
   （该 Secret 须含键 config.yaml/master-key/root-secret{{ if .Values.tls.enabled }}/tls.crt/tls.key{{ end }}）。
{{- end }}
```

- [ ] **步骤 3：验证 PDB 条件渲染 + NOTES**

运行：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
# replicas 2（默认）→ 有 PDB
helm template t $CP $SET 2>&1 | grep -E "kind: PodDisruptionBudget|minAvailable: 1" && echo "PDB-ON-OK"
# replicas 1 → 无 PDB
test $(helm template t $CP $SET --set replicaCount=1 2>&1 | grep -c "kind: PodDisruptionBudget") -eq 0 && echo "PDB-OFF-OK"
# NOTES 渲染
helm template t $CP $SET 2>&1 | grep -E "relay 选主|advisory-lock" >/dev/null && echo "NOTES-RENDER-OK"
```
预期：`PDB-ON-OK` + `PDB-OFF-OK` + `NOTES-RENDER-OK`。

- [ ] **步骤 4：Commit**

```bash
git add deploy/helm/sydom-controlplane/templates/poddisruptionbudget.yaml deploy/helm/sydom-controlplane/templates/NOTES.txt
git commit -m "feat(deploy): M5.3-k8s PDB(replicas>1 时 minAvailable≥1 保 relay leader 连续)+NOTES(迁移时序/HA 选主/sidecar/指标指引)"
```

---

## 任务 5：Sidecar 参考清单 + README + 最终验收

**文件：**
- 创建：`deploy/k8s/sidecar-reference.yaml`
- 创建：`deploy/helm/sydom-controlplane/README.md`

- [ ] **步骤 1：写 deploy/k8s/sidecar-reference.yaml**

```yaml
# 司域 Sidecar 客户侧接入参考（文档即清单，按需改）。
# 模式：sidecar 作为同 Pod 边车容器注入你的应用 Pod；应用经 localhost:8090 拨数据面 Check。
# 密钥走挂载 Secret + SYDOM_APP_SECRET_FILE（对齐 M5.3a）；探针用 httpGet（distroless 无 shell 友好）。
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: myapp-sidecar-config
data:
  config.yaml: |
    control_plane_addr: "sydom-controlplane.sydom.svc:8082"
    app_key: "demo-shop"
    domain: "shop"
    auth_addr: ":8090"
    health_addr: ":8091"
    max_staleness: "0s"
    backoff_initial: "500ms"
    backoff_max: "30s"
    environment: "production"
    control_plane_tls: true
    control_plane_ca_file: "/etc/sydom/tls/ca.crt"
    # policysync mTLS（M5.2b，可选）：出示客户端证书
    control_plane_client_cert_file: "/etc/sydom/tls/client.crt"
    control_plane_client_key_file: "/etc/sydom/tls/client.key"
---
apiVersion: v1
kind: Secret
metadata:
  name: myapp-sidecar-secret
type: Opaque
stringData:
  app-secret: "REPLACE_WITH_APP_HMAC_SECRET"   # → SYDOM_APP_SECRET_FILE
  ca.crt: "REPLACE_WITH_CONTROL_PLANE_CA_PEM"
  client.crt: "REPLACE_WITH_SIDECAR_CLIENT_CERT_PEM"
  client.key: "REPLACE_WITH_SIDECAR_CLIENT_KEY_PEM"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
  labels: { app: myapp }
spec:
  replicas: 2
  selector:
    matchLabels: { app: myapp }
  template:
    metadata:
      labels: { app: myapp }
    spec:
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
        # —— 你的应用容器（占位）——
        - name: app
          image: your-registry/myapp:latest
          env:
            - name: SIDECAR_ADDR
              value: "localhost:8090"   # 同 Pod 共享 localhost 拨 sidecar 数据面
          ports:
            - { name: http, containerPort: 8080 }
        # —— 司域 sidecar 边车容器 ——
        - name: sydom-sidecar
          image: sydom/sidecar@sha256:REPLACE_WITH_DIGEST   # M5.3b distroless nonroot
          args: ["-config", "/etc/sydom/config.yaml"]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsUser: 65532
            capabilities:
              drop: ["ALL"]
          env:
            - name: SYDOM_ENVIRONMENT
              value: "production"
            - name: SYDOM_APP_SECRET_FILE
              value: "/etc/sydom/secrets/app-secret"
          ports:
            - { name: check, containerPort: 8090 }
            - { name: health, containerPort: 8091 }
          livenessProbe:
            httpGet: { path: /healthz, port: 8091 }
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet: { path: /readyz, port: 8091 }
            initialDelaySeconds: 3
            periodSeconds: 5
          volumeMounts:
            - { name: sidecar-config, mountPath: /etc/sydom/config.yaml, subPath: config.yaml, readOnly: true }
            - { name: sidecar-secret, mountPath: /etc/sydom/secrets/app-secret, subPath: app-secret, readOnly: true }
            - { name: sidecar-secret, mountPath: /etc/sydom/tls/ca.crt, subPath: ca.crt, readOnly: true }
            - { name: sidecar-secret, mountPath: /etc/sydom/tls/client.crt, subPath: client.crt, readOnly: true }
            - { name: sidecar-secret, mountPath: /etc/sydom/tls/client.key, subPath: client.key, readOnly: true }
      volumes:
        - name: sidecar-config
          configMap: { name: myapp-sidecar-config }
        - name: sidecar-secret
          secret: { secretName: myapp-sidecar-secret }
```

- [ ] **步骤 2：写 deploy/helm/sydom-controlplane/README.md**

````markdown
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
````

- [ ] **步骤 3：最终验收 —— helm lint + 生产 values 全渲染断言**

运行：
```bash
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
helm lint $CP
helm template t $CP $SET --set image.digest=sha256:abc --set tls.syncClientCA=CAPEM 2>&1 | \
  grep -Ec "runAsNonRoot: true|runAsUser: 65532|readOnlyRootFilesystem: true|- ALL|replicas: 2|/healthz|/readyz|kind: (Secret|Deployment|Service|PodDisruptionBudget|ServiceAccount)|sync_client_ca_file|@sha256:abc"
```
预期：`helm lint` 0 failed；grep 计数 ≥ 13（各不变量齐全）。

- [ ] **步骤 4：最终验收 —— sidecar 清单合法 + 密钥卫生 + 零触碰**

运行：
```bash
# sidecar 参考 YAML 合法（helm 的内置 YAML 解析；或 kubectl client dry-run 若可用）
python3 -c "import yaml,sys; list(yaml.safe_load_all(open('deploy/k8s/sidecar-reference.yaml'))); print('SIDECAR-YAML-OK')"
kubectl apply --dry-run=client -f deploy/k8s/sidecar-reference.yaml >/dev/null 2>&1 && echo "SIDECAR-DRYRUN-OK" || echo "SIDECAR-DRYRUN-SKIP(无 kubectl/集群)"
# 密钥卫生：渲染产物里明文占位密钥只出现在 Secret，不出现在 env value
CP=deploy/helm/sydom-controlplane
SET='--set secrets.masterKey=BASE64KEY --set secrets.rootSecret=ROOTSECRET --set tls.cert=PEMCERT --set tls.key=PEMKEY'
test $(helm template t $CP $SET 2>&1 | grep -c 'value: "BASE64KEY"') -eq 0 && echo "NO-PLAINTEXT-ENV-OK"
# 零触碰
git diff e3feed0..HEAD -- '*.go' casbin/ adminauthz/ internal/ | head && echo "ZERO-TOUCH-CHECK-DONE(应为空)"
```
预期：`SIDECAR-YAML-OK`、`NO-PLAINTEXT-ENV-OK`、零触碰 diff 为空。

- [ ] **步骤 5：Commit**

```bash
git add deploy/k8s/sidecar-reference.yaml deploy/helm/sydom-controlplane/README.md
git commit -m "feat(deploy): M5.3-k8s sidecar 参考清单(同 Pod 边车+SYDOM_APP_SECRET_FILE+httpGet 探针+mTLS 客户端证书)+chart README(existingSecret 契约/安全基线)"
```

---

## 自检

**1. 规格覆盖度：**
- §4.1 交付物 → 任务 1-5 全部文件。
- §4.2 config 落 Secret → 任务 2。
- §4.3 Deployment 硬化/HA/探针 → 任务 3。
- §4.4 Service/PDB/SA → 任务 1（Service/SA）+任务 4（PDB）。
- §4.5 sidecar 参考 → 任务 5。
- §5 验证（helm lint/template/无明文/零触碰）→ 各任务验证步 + 任务 5 最终验收。
- §6 M53K-1..7 → M53K-1 任务5步4、M53K-2 任务1步7+任务5步3、M53K-3 任务3步2、M53K-4 任务3步2、M53K-5 任务2步2+任务3步3、M53K-6 任务3步2（replicas）+任务4步3（PDB）、M53K-7 任务5步4。全覆盖。

**2. 占位符扫描：** 各步含实际文件内容与确切命令+预期输出；sidecar 清单里的 `REPLACE_WITH_*` 是**给客户填的占位**（文档即清单本意），非计划缺陷。

**3. 类型一致性：** helper 名 `sydom-controlplane.{name,fullname,labels,selectorLabels,serviceAccountName,secretName,image}` 在任务 1 定义、任务 2/3/4 一致 include；端口 `admin 8081`/`sync 8082`/`health 8083` 与 config 字段 `admin_addr`/`sync_addr`/`health_addr` 一致；env `SYDOM_MASTER_KEY_FILE`/`SYDOM_ROOT_SECRET_FILE`/`SYDOM_ENVIRONMENT` 与 M5.3a `deploycfg.ResolveSecret`/`ParseEnvironment` 一致；Secret 键 `config.yaml`/`master-key`/`root-secret`/`tls.crt`/`tls.key`/`sync-client-ca.crt` 与 deployment subPath 挂载一致。
