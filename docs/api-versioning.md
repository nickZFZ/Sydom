# 司域 API 版本化与向后兼容策略

司域对外/对边车 API = 3 个 `v1` proto（`api/proto/sydom/{admin,auth,sync}/v1`）→ 生成 Go stub + gRPC + REST 网关 + Go SDK。本策略锁定 **v1 向后兼容契约**，GA 前后不破坏边车/SDK/REST 消费者。

## 版本方案

- API 版本在 **proto 包路径**：`sydom.<svc>.v1`。v1 = 当前稳定契约。
- REST 网关与 Go SDK **随 proto**，不另立版本号——proto v1 是单一真相源，三面（gRPC/REST/SDK）兼容由此派生。

## v1 向后兼容保证

v1 内只做**向后兼容（additive）**变更，保证 **wire 兼容 + JSON 兼容 + 生成码源码兼容**（buf `breaking: use: FILE`，最严格类别）。

## proto 演进规则

| 可以（additive，v1 内） | 不可以（breaking，须 v2 / 弃用流程） |
|---|---|
| 加新 message / enum / RPC | 删 / 改字段名或编号、改字段类型 |
| 加新字段（用新编号） | 删 / 改 RPC 或其请求 / 响应类型 |
| 加新枚举值（不改旧值） | 删 / 改既有枚举值、改包名 / 服务名 |
| 字段标 `[deprecated = true]` | 复用已删字段编号（须先 `reserved`） |

## 弃用流程

1. 字段/RPC 标 `[deprecated = true]` + 文档公告。
2. 保留 ≥ 1 个 minor 周期（消费者迁移窗口）。
3. 真正删除**只在 v2**（v1 内永不删）；删时 `reserved` 编号与名，防复用。

## 何时开 v2

确需破坏性变更时，新起 `sydom.<svc>.v2` 包**并存运行**；v1 保留至消费者迁移完，不原地破坏 v1。

## 强制门

- 每次改动 / 合并前：`make proto-breaking`（对 `main` 检破坏，`buf breaking`）。CI 应跑此目标。
- **GA 打 tag 后**：该 tag 为 v1 冻结基线——`make proto-breaking BREAKING_AGAINST='.git\#tag=<ga-tag>'`，保证 v1 wire/JSON 兼容**永久**。
- （Makefile 提示：`#` 在 Makefile 里是注释，覆盖 `BREAKING_AGAINST` 时字面量 `#` 须转义为 `\#`。）

## REST / SDK 派生兼容

REST 网关与 gRPC 共用唯一契约 / `ruleTable`，Go SDK 随生成 stub → **proto v1 兼容即三面兼容**，无第二套版本策略。REST 路径不含版本段（复用 v1 契约）；若将来需 REST 独立版本，随 proto v2 一并引入。
