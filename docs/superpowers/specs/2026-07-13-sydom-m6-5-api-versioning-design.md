# M6.5 API 版本化 + 向后兼容策略 — 设计规格

> M6「商业化 + 合规 + GA」的一个子项目。BASE=main `ccab3a7`（M5 收官）。**M6 需拆多子项目**（计费/配额/用量、企业 SSO、合规、渗透测试、API 版本化、GA 文档）；其中**计费/SSO/合规涉及产品/供应商选型（路线图 §6 明列留 brainstorm，属用户战略决策）**，本切片取**决策无关、纯工程、GA 前置**的一片：把 API 向后兼容从「buf.yaml 有配置但无人跑」升级为**可执行/可 CI 的强制门 + 成文契约**。

## 1. 背景与目标

司域对外/对边车的 API 是 3 个 `v1` proto（`api/proto/sydom/{admin,auth,sync}/v1/*.proto`）+ 生成的 Go stub + REST 网关 + Go SDK。M1–M5 已在这套 API 上постро大量表面。GA 前必须**锁定 v1 向后兼容契约**——否则后续改动可能悄悄破坏边车/SDK/REST 消费者。

**现状**：`buf.yaml` v2 已声明 `breaking: use: FILE`（最严格类别，含源码+wire 兼容），`buf` 1.34.0 可用，但 **Makefile 无 `proto-breaking` 目标、CI 不跑、无成文兼容策略**——配置在但未强制、未文档化。

**目标**：
1. **可执行强制门**：`make proto-breaking` 用 `buf breaking` 对基线检测破坏性变更；供 CI/本地跑。
2. **成文契约**：`docs/api-versioning.md`——版本方案、v1 向后兼容保证（wire + JSON + 生成码）、proto 演进「可/不可」规则、弃用流程、何时 v2、REST/SDK 兼容。
3. **有齿**：变异实验证 `buf breaking` 能捕获破坏（字段改名 → FAIL）。

**非目标（明确排除）**：
- **计费/SSO/合规**等需产品/供应商选型的 M6 子项目（留用户 brainstorm 定方向）。
- 引入 v2 或改任何 proto/生成码（本切片只立**兼容门 + 契约**，proto 字节不变）。
- REST/SDK 的独立版本化机制（REST 复用同一 gRPC 契约、SDK 随 proto；本切片以 proto 兼容为单一真相源，REST/SDK 兼容在文档里派生说明）。
- CI 平台接线（本仓无 CI 配置文件；交付可跑的 `make` 目标 + 文档「CI 应跑此目标」，实际接线待 CI 存在）。

## 2. 现状（实查）

- proto：`api/proto/sydom/{admin,auth,sync}/v1/*.proto`（均 `v1` 包）；`buf.yaml` v2 `modules: api/proto`、`lint: DEFAULT(+若干 except)`、**`breaking: use: FILE`**。
- Makefile：`proto-lint`(buf lint)、`proto-gen`(buf generate)、`proto-check`(gen 漂移)；**无 breaking 目标**。
- `buf breaking --against '.git#branch=main'` 实测：protos 未改 → PASS（rc 0）；变异字段改名 → FAIL（rc 100，报 `changed name` + `json_name`）。**机制已验证有齿。**

## 3. 方案

**A（选定）Makefile `proto-breaking` 目标（`buf breaking --against` 可配基线）+ 成文兼容策略 doc。** buf.yaml breaking 配置已在，复用；基线用 `.git#branch=main`（标准 CI 模式：每次改动对 main 检破坏）。纯 Makefile + docs，proto/Go 字节不变。

**B 冻结 v1 二进制基线镜像（`buf build -o api/v1.binpb` + `buf breaking --against` 它）。** 更强「v1 永久冻结」语义，但引入 git 内二进制、更新基线是显式动作。**折中**：本切片用 against-main（简单标准）；策略 doc 写明「GA 打 tag 时该 tag 即冻结基线，v1 wire/JSON 兼容永久」，冻结镜像法留 GA-tag 时采用。否决 B 作本切片主路径（避免 git 二进制）。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `Makefile`（改） | 加 `proto-breaking` 目标（`buf breaking --against $(BREAKING_AGAINST)`，默认 `.git#branch=main`）+ 入 `.PHONY` |
| `docs/api-versioning.md`（新） | 版本方案 + v1 向后兼容契约 + proto 演进规则 + 弃用流程 + v2 criteria + REST/SDK 兼容 + 强制门用法 |

### 4.2 `proto-breaking` 目标

```make
BREAKING_AGAINST ?= .git#branch=main
# 向后兼容门：对基线检测 proto 破坏性变更（buf.yaml breaking: FILE）。CI 与合并前应跑。
proto-breaking:
	PATH="$(GOBIN):$$PATH" buf breaking --against '$(BREAKING_AGAINST)'
```
入 `.PHONY`。基线可覆盖（如对 tag：`make proto-breaking BREAKING_AGAINST='.git#tag=v1.0.0'`）。

### 4.3 兼容策略 doc（要点）

- **版本方案**：API 版本在 proto 包路径（`sydom.<svc>.v1`）。v1 = 当前稳定契约。REST 路径/网关与 SDK 随 proto，不另立版本号（单一真相源）。
- **v1 向后兼容保证**：v1 内只做**向后兼容（additive）**变更；wire 兼容 + JSON 兼容 + 生成码源码兼容（buf `FILE` 类别）。
- **可/不可**（proto 演进）：

  | 可以（additive） | 不可以（breaking，须 v2 或弃用流程） |
  |---|---|
  | 加新 message/enum/RPC | 删/改字段名或编号、改字段类型 |
  | 加新字段（新编号） | 删/改 RPC 或其请求/响应类型 |
  | 加新枚举值（不改旧值） | 删/改枚举值、改包名/服务名 |
  | 字段标 `deprecated=true` | 复用已删字段的编号（须 `reserved`） |

- **弃用流程**：标 `[deprecated = true]` → 文档公告 + 保留 ≥1 个 minor → 删除时 `reserved` 编号/名，且只在 v2 删（v1 永不删）。
- **何时 v2**：确需破坏性变更时新起 `sydom.<svc>.v2` 包并存运行，v1 保留至消费者迁移完（不原地破坏 v1）。
- **强制**：`make proto-breaking` 对 main 检每次改动；**GA 打 tag 后，该 tag 为 v1 冻结基线**（`make proto-breaking BREAKING_AGAINST='.git#tag=<ga>'`），v1 wire/JSON 兼容永久保证。
- **REST/SDK 派生兼容**：REST 网关与 gRPC 共用唯一 `ruleTable`/契约、Go SDK 随生成 stub → proto v1 兼容即三面兼容；无第二套版本策略。

## 5. 验证

- `make proto-breaking` 对 main（protos 未改）→ PASS（rc 0）。
- **有齿**：临时字段改名 → `make proto-breaking` FAIL（rc≠0，报 changed name）→ 还原 → PASS。
- **零触碰**：`git diff ccab3a7..HEAD -- '*.go' casbin/ adminauthz/ internal/ api/proto/ gen/` = 空（只改 Makefile + 加 doc；proto/生成码字节不变）。
- `buf lint` 仍 0（未破坏既有）。

## 6. 验收标准（M65-1..6）

- **M65-1** 零触碰代码/proto：上述 diff = 空（纯 Makefile + docs）。
- **M65-2** `make proto-breaking` 存在、对 main PASS。
- **M65-3** 有齿：字段改名变异 → `proto-breaking` FAIL；还原 → PASS（复现）。
- **M65-4** 策略 doc：版本方案 + v1 兼容保证 + 可/不可表 + 弃用流程 + v2 criteria + REST/SDK 派生 + 强制门用法。
- **M65-5** `buf lint` 0、`make proto-check`（gen 漂移）仍绿。
- **M65-6** 不改 proto/生成码（`git diff -- api/proto gen` 空）；M6 决策无关子项目落地，计费/SSO/合规等待用户产品方向。

## 7. 风险

- **against-main 基线随 main 演进**：破坏性变更若合入 main 会成新基线——故策略 doc 定「GA tag 为永久冻结基线」补齐永久性；per-change 门用 against-main 防意外破坏足够。
- **无 CI 文件**：本仓无 CI 配置，`make proto-breaking` 交付为可跑目标 + 文档要求 CI 跑；CI 接线待 CI 引入（非本切片）。
- **buf 版本漂移**：`proto-tools` 钉 `BUF_VERSION`；breaking 规则跨 buf 版本稳定（FILE 类别语义稳定）。
