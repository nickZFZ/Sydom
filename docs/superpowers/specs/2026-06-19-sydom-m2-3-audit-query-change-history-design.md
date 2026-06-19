# 司域 (Sydom) M2.3 设计 · 审计查询 + 变更历史（谁在何时改了什么）

> 里程碑：**M2 · 授权产品功能纵深（operate / understand）** 的第三个子项目（M2.3）。
> M2 拆 4 子项目：M2.1 撤权对称 + 生命周期补全（已交付）/ M2.2 决策可解释性（已交付）/ **M2.3 审计查询 + 变更历史** / M2.4 List 分页·搜索·排序·过滤。本文仅覆盖 M2.3，经 brainstorm 收敛为「补齐 system 域审计覆盖 + 两域内容级 diff + 三面查询呈现」。

## 1. 背景与目标

写时审计基建**已存在**：`policy_audit_log` 表（migration `000010` + `000012` 加宽 action）、3 个索引（按时间 / 版本 / 实体）、`store.InsertAudit` 写助手。但有两处缺口：

- **覆盖面缺口**：仅 **app 域策略写**落审计（`policy.Manager` 的 versioned write 两处调用 `InsertAudit`）。**system 域管理动作全部无审计**——建应用 / 超管授权 / 绑定解绑 / 撤权 / 轮换重置密钥 / 注册租户 / 邀请成员 / 启停应用 等都不经过 `policy.Manager`，落不下任何审计记录。
- **保真度缺口**：`policy_audit_log.diff` 列（JSONB）**自建表起恒 NULL**（`InsertAudit` 不写 diff）。当前审计只能答「谁 / 何时 / 什么动作 / 哪个实体 / 哪个版本」，答不出「改了什么内容」。PROGRESS.md ③-2 早将「补 diff」挂起待决。

**目标**：补齐审计覆盖面到 system 域、两域都记内容级 diff（before/after），并提供**三面查询 + per-entity 变更历史**——形成「谁在何时对哪个实体做了什么、改前改后是什么」的完整可审计闭环。受众：超管（全系统审计）+ 租户管理员（本租户管理审计）+ app 运维（本应用策略审计）。

## 2. 范围与 YAGNI 边界

**纳入（M2.3 必须项）**：
- **补齐 system 域审计**：新建 `admin_audit_log` 表，所有 system 域管理 handler 落审计，**审计行与动作同事务原子提交**。
- **两域内容级 diff**：`policy_audit_log.diff`（app 域，从既有 `Delta` 序列化）+ `admin_audit_log.diff`（system 域，before/after），**diff 绝不含 secret/凭据**。
- **查询 RPC ×2**：`QueryAuditLog`（app 域，scopeApp）+ `QueryAdminAuditLog`（admin 域，scopeTenant），keyset 分页 + 过滤（实体 / 动作 / operator / 时间区间 / 版本区间），**per-entity 变更历史 = 同 RPC 带 entity 过滤**。
- **三面 parity**：gRPC + REST + Console。

**不纳入（移出 M2.3，留后续）**：
- **审计保留 / 归档 / 清理（retention）**——运维特性，YAGNI，留 M3+。
- **审计导出 / 报表 / 告警**——独立特性。
- **通用 List 分页·搜索·排序·过滤（M2.4）**——本切片只给审计做其**内在必需**的 keyset 分页 + 审计专属过滤；通用化 List 留 M2.4（可沿用同一 keyset 范式）。
- **变更回放 / 撤销（revert）**——diff 已落库使未来回放成为可能，但回放本身非本切片。
- **Console diff 的 M1.4 业务语言美化**——M2.3 审计走管理/技术 Console，diff 结构化展示；业务语言美化（复用 `permNameMap`/`roleNameMap`）留后续运营台「活动」视图。

**四处已定决策（brainstorm 收敛，记录于此供审查）**：
- **(a) 覆盖面 = 补齐到 system 域**（一致性优先）：否决「仅暴露既有 app 域审计」（system 域动作仍是审计盲区，与一致性原则相悖）。
- **(b) 保真度 = 两域内容级 diff（before/after）**：否决「实体级（不填 diff）」（答不出改了什么）与「仅 app 域填 diff」（system 域保真不足）。
- **(c) 数据模型 = 分表**：`policy_audit_log` 结构不动（保住 NOT NULL 不变量——它承重于数据面版本路径），新建 `admin_audit_log` 专装 system 域。否决「统一表松约束」（动了承重表 + 行异构）与「通用事件表重建」（YAGNI + 大迁移 + 丢弃既有聚焦 schema/索引）。两查询路径正好镜像代码库既有「app 域读 scopeApp / system 域读 scopeSystem」分裂。
- **(d) admin 审计可见性 = scopeTenant**：租户管理员看本租户 admin 审计（谁建 app / 谁邀成员 / 谁轮换密钥），超管（tenant_id=0→"*"）看全部含纯系统级（tenant_id NULL）。否决「仅超管（scopeSystem）」（多租户 SaaS 下租户对自身管理动作无可见性）。

## 3. 数据模型

### 3.1 新表 `admin_audit_log`（migration `000013`）

```sql
CREATE TABLE admin_audit_log (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT,                     -- 租户隔离/过滤；NULL=纯系统级(跨租户超管动作)
    operator      VARCHAR(128) NOT NULL,
    action        VARCHAR(32)  NOT NULL,
    entity_type   VARCHAR(32)  NOT NULL,      -- application/operator/admin_grant/admin_binding/tenant/membership
    entity_id     VARCHAR(128),
    diff          JSONB,                      -- before/after，绝不含 secret
    admin_version BIGINT,                     -- 有 bump 的动作记 admin_policy_version；无则 NULL
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_admin_audit_tenant_created ON admin_audit_log (tenant_id, created_at);
CREATE INDEX idx_admin_audit_tenant_entity  ON admin_audit_log (tenant_id, entity_type, entity_id);
```

镜像 `policy_audit_log` 的列与索引风格；`app_id`→`tenant_id`、`version`→`admin_version`（均可空，承载 system 域异构）。down migration `DROP TABLE admin_audit_log`。

### 3.2 `policy_audit_log` 结构不变，仅写路径补 diff

表结构、索引、NOT NULL 约束**一字未改**。仅 `store.InsertAudit` 签名加 `diff []byte` 参数；app 域写时在 `policy.Manager` 既有 `Delta`（`RuleAdds`/`RuleRemoves`/`DataChanges` 当场可得）序列化为 JSON 填入。`diff` 仍为 nullable（无策略影响的 no-op 不落审计的既有行为不变）。

## 4. 写路径补齐（system 域）

### 4.1 写助手（`internal/controlplane/adminauthz/store.go`）

新增 `InsertAdminAudit(ctx, ex cp.DBTX, tenantID *int64, operator, action, entityType, entityID string, diff []byte, adminVersion *int64) error`——与既有 `BumpPolicyVersion`/`ReadPolicyVersion` 同包同文件，镜像 `store.InsertAudit` 风格。`tenantID`/`adminVersion` 用指针承载 NULL。

### 4.2 各 handler 落审计（**审计行与动作同事务原子**）

一致性铁律：审计行必须与动作在**同一事务**提交，杜绝「动作成功审计丢失」或反之。`RotateApplicationSecret`/`ResetOperatorSecret`/`SetApplicationStatus`/`CreateApplication` 现为单语句无事务 → **包进事务**（动作 UPDATE/INSERT + 审计 INSERT 一并提交）。已有事务的 Grant/Bind/Revoke/Unbind 等在 commit 前插入审计。

| 动作 | entity_type | entity_id | tenant_id | admin_version | diff |
|---|---|---|---|---|---|
| CreateApplication | application | 新 app_id | r.TenantId | NULL | after={name,app_key,domain,tenant_id}（**无 app_secret**） |
| RotateApplicationSecret | application | app_id | app→tenant | NULL | {rotated:true}（**新旧 secret 都不入**） |
| ResetOperatorSecret | operator | operator_id | NULL | NULL | {reset:true}（**无 secret**） |
| SetApplicationStatus | application | app_id | app→tenant | NULL | {before,after}=status |
| SetOperatorStatus | operator | operator_id | NULL | NULL | {before,after}=status |
| CreateOperator | operator | 新 operator_id | NULL | NULL | after={非敏感字段}（**无 secret**） |
| GrantAdminRole | admin_grant | role_id | domain→tenant | new ver | after={role_id,domain,resource,action} |
| BindOperatorRole | admin_binding | operator_id | domain→tenant | new ver | after={operator_id,role_id,domain} |
| RevokeAdminGrant | admin_grant | role_id | domain→tenant | new ver | before={role_id,domain,resource,action} |
| UnbindOperatorRole | admin_binding | operator_id | domain→tenant | new ver | before={operator_id,role_id,domain} |
| RegisterTenant | tenant | 新 tenant_id | 新 tenant_id | new ver | after={tenant_name 等非敏感} |
| InviteMember | membership | member 标识 | 所属 tenant | new ver | after={member,role} |

`tenant_id` 推导：app 类动作经 `app→tenant`（建 app 直接用 r.TenantId）；admin_grant/binding 经 domain 解析（`*`→NULL 纯系统级，`t:<id>`→该租户）；operator/secret 类纯系统级→NULL。RegisterTenant/InviteMember 均经 `BumpPolicyVersion`（建 casbin 绑定，与 membership 锁步，已核 accounts.go:56/119）→ admin_version 记 new ver。

> proto 标量约定：`AdminAuditEntry.tenant_id=0` 表示 NULL（纯系统级），`admin_version=0`／`AuditEntry.version` 同理用 0 表示无版本/缺省（uint64 无法承载 NULL，与既有响应口径一致）。

## 5. 查询 RPC

### 5.1 契约（`api/proto/sydom/admin/v1/admin.proto`）

```proto
rpc QueryAuditLog(QueryAuditLogRequest) returns (QueryAuditLogResponse);
rpc QueryAdminAuditLog(QueryAdminAuditLogRequest) returns (QueryAdminAuditLogResponse);

message QueryAuditLogRequest {                  // app 域
  uint64 app_id      = 1;                        // path 权威
  string entity_type = 2;                        // 可选过滤
  string entity_id   = 3;                        // 可选(per-entity 变更历史)
  string action      = 4;
  string operator    = 5;
  string since       = 6;                         // RFC3339，可选
  string until       = 7;
  uint64 cursor      = 8;                          // keyset：上页最后 id；0=首页
  uint32 limit       = 9;                          // 默认/上限服务端钳制
}
message AuditEntry {
  uint64 id = 1; string operator = 2; string action = 3;
  string entity_type = 4; string entity_id = 5;
  string diff = 6;                                 // JSON 字符串(原样)
  uint64 version = 7; string created_at = 8;        // RFC3339
}
message QueryAuditLogResponse { repeated AuditEntry entries = 1; uint64 next_cursor = 2; }

message QueryAdminAuditLogRequest {              // admin 域
  uint64 tenant_id   = 1;                          // 权威：超管 0→全部；租户管理员=本租户
  string entity_type = 2; string entity_id = 3;
  string action = 4; string operator = 5;
  string since = 6; string until = 7;
  uint64 cursor = 8; uint32 limit = 9;
}
message AdminAuditEntry {
  uint64 id = 1; uint64 tenant_id = 2;             // 0 表示纯系统级(NULL)
  string operator = 3; string action = 4;
  string entity_type = 5; string entity_id = 6;
  string diff = 7; uint64 admin_version = 8; string created_at = 9;
}
message QueryAdminAuditLogResponse { repeated AdminAuditEntry entries = 1; uint64 next_cursor = 2; }
```

### 5.2 keyset 分页（新增 → 旧降序）

`WHERE id < $cursor`（cursor=0 时省略）`ORDER BY id DESC LIMIT $limit+1`；取回 limit+1 行判定是否有下一页，`next_cursor` = 本页最后 id（无下页则 0）。`limit` 服务端钳制（如默认 50、上限 200）。审计无界增长、并发插入下 keyset 稳定不跳不重（优于 offset）。M2.4 通用化 List 分页可沿用同一 keyset 范式。

### 5.3 store 查询（`internal/controlplane/store` / `adminauthz`）

`QueryAppAudit(ctx, q, appID, filters, cursor, limit)` 打 `policy_audit_log`（复用 `idx_audit_app_created` / `idx_audit_app_entity`）；`QueryAdminAudit(ctx, q, tenantScope, filters, cursor, limit)` 打 `admin_audit_log`。过滤条件动态拼装（参数化，防注入）；时间/版本区间、实体、动作、operator 皆可选。

## 6. 鉴权（`internal/controlplane/mgmt/authz.go` ruleTable）

增量加 2 行，新资源类 `audit`，isWrite=false（纯读）：

| RPC | rpcRule `{resource, action, isWrite, scope}` |
|---|---|
| `QueryAuditLog` | `{"audit", "read", false, scopeApp}` |
| `QueryAdminAuditLog` | `{"audit", "read", false, scopeTenant}` |

- `QueryAuditLog`：域取自请求 `app_id`（scopeApp，复用 `TenantDomainOf` 租户隔离 fail-close）。
- `QueryAdminAuditLog`：域取自请求 `tenant_id`（scopeTenant，0→"*"）。**查询 WHERE 子句必须与鉴权 scope 锁步**：scopeTenant 解析出的租户即查询过滤的租户——超管在 "*" 域有权 → 看全部（不加 tenant_id 过滤或显式全量）；租户管理员仅在 `t:<id>` 域有权 → WHERE tenant_id=<id>，物理上看不到他租户行。
- adminauthz matcher **一字未改**（M1.1 不变量），enforcer.go diff=0。

## 7. 三面 parity

- **gRPC**：2 RPC。
- **REST**（`internal/controlplane/restgw`）：
  - `GET /v1/apps/{app_id}/audit?entity_type=&entity_id=&action=&operator=&since=&until=&cursor=&limit=`（app_id path 权威，余取 query；REST-HMAC 签名覆盖完整请求）。
  - `GET /v1/admin/audit?tenant_id=&entity_type=&...`（admin 域）。
- **Console**（`internal/controlplane/console`）：
  - **app 审计页**：appnav 加「审计」tab，时序 feed + 过滤 + per-entity 钻取（entity_id query 参）+ keyset「下一页」；diff 结构化展示。
  - **admin 审计页**：ops/system 区，租户隔离 feed，超管可切租户。
  - 纯读、不 bump、降级无枚举、跨租户/跨域 403 不泄露存在性、页面不含任何 secret。

## 8. 一致性与安全不变量（AUD-1..AUD-7，验收逐条核验）

- **AUD-1 原子一致**：审计行与动作同事务提交（无单边）；测试构造失败回滚断言「动作回滚则审计行不存在」。
- **AUD-2 secret 绝不入 diff**：测试断言 Rotate/Reset/Create 的 diff JSON 不含任何 secret 值（新旧均不）；diff 序列化白名单非敏感字段。
- **AUD-3 租户隔离 fail-close**：`QueryAuditLog` scopeApp 仅本 app；`QueryAdminAuditLog` scopeTenant 仅本租户（WHERE 与 scope 锁步）；未知/外租户/外 app → 拒绝不泄露存在性；跨租户安全矩阵守门。
- **AUD-4 一份授权真相**：三面共用 `AuthorizeRule`+`ruleTable`；adminauthz matcher 一字未改；enforcer.go diff=0；ruleTable 仅新增 2 条。
- **AUD-5 读纯净**：query RPC 不 bump、无副作用、不改 status；读不受 status 写拦截。
- **AUD-6 diff 保真**：app 域 diff 忠实 `Delta`（adds/removes/dataChanges）；system 域 diff 忠实 before/after。
- **AUD-7 数据面零影响**：审计不入 sync/translate/sidecar；`policy_audit_log` 结构不变不影响数据面版本路径；`admin_audit_log` 控制面专属。

## 9. 错误处理

- 未知 app / 跨租户跨域无权 → `PermissionDenied`（经 AuthorizeRule scopeApp/scopeTenant fail-close，不泄露存在性）。
- 写 handler 内审计 INSERT 失败 → 整个事务回滚（动作一并失败），返 `Internal`；保证 AUD-1 原子性，不留单边动作。
- 查询内部失败（读库、游标解析）→ `Internal`（沿用既有 `%v` 透传债；统一脱敏留 M3）。
- 空结果（无审计 / 过滤无命中）**不是错误**：正常返回空 entries + next_cursor=0。

## 10. 测试策略（TDD）

- **AUD-1 原子性**：注入审计 INSERT 失败 → 断言动作（密钥未轮换 / 状态未变 / 授权未建）一并回滚。
- **AUD-2 secret 不入 diff**：Rotate/Reset/CreateApplication 后查审计行，断言 diff 不含 secret 明文。
- **覆盖面**：每个 system 域 handler 成功后落 1 条 `admin_audit_log`（正确 tenant_id/entity/action/diff/admin_version）。
- **app 域 diff**：CreateRole/GrantPermission/UpsertDataPolicy 等后 `policy_audit_log.diff` 含对应 adds/removes/dataChanges。
- **keyset 分页**：插 N 条，分页拉取断言无跳无重、并发插入新行不影响已翻页游标；next_cursor 语义。
- **过滤**：entity_type/entity_id（per-entity 历史）/action/operator/时间区间/版本区间各命中正确子集。
- **AUD-3 租户隔离矩阵**：租户 A 管理员查 admin 审计只见 A 行、查 B 的 app/tenant → 403；超管见全部含 tenant_id NULL。
- **三面 parity**：REST 状态码（403/200/path 权威/HMAC）；Console（无会话 302、有会话渲染、降级无枚举、diff 结构化、翻页）。
- 兜底：`gofmt -l` / `go vet ./...` / `make proto-check`（无漂移）/ store·adminauthz·mgmt·restgw·console 包 `go test` + 全仓 `go test ./...`（含 dbtest testcontainers）。

## 11. 范围边界 / 移交

- M2.3 仅审计「捕获 + 查询 + 变更历史」；保留/归档/清理、导出/报表/告警、变更回放各走独立 spec→plan→实现周期。
- 范式延续：子代理驱动 + 逐任务控制者独立验证 + 整体安全评审；跨包改签名（`InsertAudit` 加 diff 参）后 `go vet ./...` 全仓兜底。
- 非阻塞观察项（沿 M1.4/M2.1/M2.2 记录，留 M3）：Internal 错误 `%v` 透传统一脱敏。

## 12. 下一步

本 spec 经用户审查批准后，调用 writing-plans 创建 M2.3 实现计划（TDD 任务分解）。
