# M6.5 API 版本化 + 向后兼容策略 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 API 向后兼容从「buf.yaml 有配置但无人跑」升级为可执行 `make proto-breaking` 强制门 + 成文兼容契约。

**架构：** 复用 `buf.yaml` 既有 `breaking: use: FILE`；加 `Makefile` `proto-breaking` 目标（`buf breaking --against` 可配基线，默认 `.git#branch=main`）+ `docs/api-versioning.md` 兼容契约。proto/生成码字节不变。

**技术栈：** buf 1.34.0 `buf breaking`、Makefile、Markdown。

**BASE：** `feat/m6-5-api-versioning` @ 含设计规格提交；规格 `docs/superpowers/specs/2026-07-13-sydom-m6-5-api-versioning-design.md`。

**零触碰铁律：** `git diff ccab3a7..HEAD -- '*.go' casbin/ adminauthz/ internal/ api/proto/ gen/` 必须为空（纯 Makefile + docs）。

---

## 任务 1：`make proto-breaking` 门（含有齿变异实验）

**文件：**
- 修改：`Makefile`

- [ ] **步骤 1：加 proto-breaking 目标**

在 `Makefile` 的 `proto-check` 目标后加：
```make
BREAKING_AGAINST ?= .git#branch=main
# 向后兼容门：对基线检测 proto 破坏性变更（buf.yaml breaking: FILE）。CI 与合并前应跑。
# 基线可覆盖：make proto-breaking BREAKING_AGAINST='.git#tag=v1.0.0'
proto-breaking:
	PATH="$(GOBIN):$$PATH" buf breaking --against '$(BREAKING_AGAINST)'
```
并把 `.PHONY` 那行的 `proto-check` 后追加 `proto-breaking`（即 `... proto-gen proto-check proto-breaking`）。

- [ ] **步骤 2：对 main 跑（PASS）**

运行：`make proto-breaking`
预期：无输出、`rc 0`（protos 未改，对 main 无破坏）。

- [ ] **步骤 3：变异实验证有齿**

临时改 `api/proto/sydom/admin/v1/admin.proto` 里 `bool changed = 2;` 为 `bool changed_x = 2;`（改字段名）。
运行：`make proto-breaking`
预期：**FAIL（rc≠0）**，报 `Field "2" ... changed name from "changed" to "changed_x"`（+ json_name 变更）。
观察 FAIL 后**还原**（`git checkout -- api/proto/sydom/admin/v1/admin.proto`），再跑 `make proto-breaking` 确认 PASS。证门对破坏性变更有齿。

- [ ] **步骤 4：Commit**

```bash
git add Makefile
git commit -m "build(proto): M6.5 加 make proto-breaking 向后兼容门(buf breaking --against 可配基线默认 main;复用 buf.yaml breaking:FILE;变异字段改名证 FAIL 有齿)"
```

---

## 任务 2：兼容策略 doc + 最终验收

**文件：**
- 创建：`docs/api-versioning.md`

- [ ] **步骤 1：写 doc**

`docs/api-versioning.md`：
````markdown
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
- **GA 打 tag 后**：该 tag 为 v1 冻结基线——`make proto-breaking BREAKING_AGAINST='.git#tag=<ga-tag>'`，保证 v1 wire/JSON 兼容**永久**。

## REST / SDK 派生兼容

REST 网关与 gRPC 共用唯一契约 / `ruleTable`，Go SDK 随生成 stub → **proto v1 兼容即三面兼容**，无第二套版本策略。REST 路径不含版本段（复用 v1 契约）；若将来需 REST 独立版本，随 proto v2 一并引入。
````

- [ ] **步骤 2：最终验收**

运行：
```bash
make proto-breaking && echo "BREAKING-GATE-PASS"
PATH="$(go env GOPATH)/bin:$PATH" buf lint && echo "LINT-CLEAN"
make proto-check && echo "GEN-DRIFT-CLEAN"
git diff ccab3a7..HEAD -- '*.go' casbin/ adminauthz/ internal/ api/proto/ gen/ | head; echo "ZERO-TOUCH-DONE(空)"
```
预期：`BREAKING-GATE-PASS`、`LINT-CLEAN`、`GEN-DRIFT-CLEAN`、零触碰 diff 空。

- [ ] **步骤 3：Commit**

```bash
git add docs/api-versioning.md
git commit -m "docs(api): M6.5 API 版本化+向后兼容策略(v1 契约=wire+JSON+源码兼容;proto 演进可/不可表;弃用流程;何时 v2;GA tag 冻结基线;REST/SDK 随 proto 派生;M6 决策无关首片)"
```

---

## 自检

**1. 规格覆盖度：** §4.1 文件→任务1(Makefile)+任务2(doc)；§4.2 目标→任务1步1；§4.3 doc 要点→任务2步1；§5 验证→任务1步2/3+任务2步2；§6 M65-1..6→M65-1 任务2步2、M65-2 任务1步2、M65-3 任务1步3、M65-4 任务2步1、M65-5 任务2步2、M65-6 任务2步2。全覆盖。

**2. 占位符扫描：** Makefile/doc 为实内容；命令带预期输出；变异实验给确切改法+还原。doc 里 `<svc>`/`<ga-tag>` 是模板占位（说明性），非缺陷。

**3. 类型一致性：** `proto-breaking` 目标用 `$(GOBIN)`（Makefile line 14 已定义）+ `$(BREAKING_AGAINST)`（本目标定义）；`buf breaking --against` 语法与实测一致（`.git#branch=main` PASS、字段改名 FAIL rc 100 已验证）；`make proto-check`（既有 gen 漂移目标）不受影响。
