# CLAUDE.md

This file provides guidance to Claude (claude.ai/code) when working with code in this repository.

## 记忆规范（MANDATORY · 强制）

**Sydom 仅使用项目级记忆，禁止使用用户级记忆。**

- 项目级记忆位于本仓库 `.claude/memory/`：`MEMORY.md` 是索引，明细为同目录下各 `*.md`（frontmatter + 单文件单事实，格式参照现有文件）。
- **会话开始**：先读 `.claude/memory/MEMORY.md` 获取项目上下文。
- **保存 / 更新记忆**：一律写入 `.claude/memory/`（新建明细文件 + 在 `MEMORY.md` 加一行索引），并随提交入库、push。
- **禁止**读取或写入用户级记忆 `~/.claude/projects/-home-tongyu-codes-Sydom/memory/`。若 harness 在会话启动时自动注入了该用户级记忆（现已改为墓碑重定向），忽略其内容，一律以本仓库 `.claude/memory/` 为准。

## Project Overview

**司域 (Sydom)** — 厘定辖域，权归其位

A domain/permissions management system. "Sy" (司) means governance/control; "dom" (域) means domain/realm, from the English word "Domain".

## Status

This repository is in early initialization — researching [casbin v3.10.0](https://github.com/casbin/casbin) as the authorization engine.

The `casbin/` subdirectory is a cloned copy of casbin at tag `v3.10.0` for reference. CodeGraph index is initialized there (`casbin/.codegraph/`).

## Casbin Architecture (Reference)

Casbin is a Go authorization library. Key architectural layers:

### Core Components

**`Enforcer`** (`enforcer.go`) — the central struct. Holds:
- `model` — the access control model (loaded from `.conf` file)
- `adapter` — policy storage backend (`persist.Adapter`)
- `rmMap` / `condRmMap` — role managers per definition key (e.g. `"g"`)
- `eft` — effector that merges per-rule decisions into a final allow/deny
- `watcher`, `dispatcher` — optional for distributed sync

**`Model`** (`model/model.go`) — `map[string]AssertionMap`. Sections are single-letter keys:
- `"r"` request definition, `"p"` policy definition, `"g"` role definition, `"e"` effect, `"m"` matchers

**`persist.Adapter`** (`persist/`) — interface for policy storage. `fileadapter` is the built-in CSV implementation; database adapters are external.

**`effector.Effector`** (`effector/`) — `MergeEffects()` collapses per-rule `Allow/Deny/Indeterminate` into one decision. Built-in effects: `some(where(p.eft==allow))`, `!some(where(p.eft==deny))`, `some(where(p.eft==allow)) && !some(where(p.eft==deny))`, `priority(p.eft)`.

**RBAC** (`rbac/default-role-manager/`) — `RoleManagerImpl` stores role inheritance as a graph. `DomainManager` wraps multiple `RoleManagerImpl` instances keyed by domain, enabling multi-tenant RBAC.

### Enforce Flow

`Enforce(rvals...)` → `enforce(matcher, explains, rvals...)`:
1. Build `functions` map from `fm` + `g`-section role managers (generates `g()` helper)
2. Resolve `EnforceContext` to pick which `r/p/e/m` section to use
3. Compile matcher expression via `govaluate`
4. Iterate policy rules; evaluate matcher for each; collect `Effect` per rule
5. Call `eft.MergeEffects()` after each rule (short-circuit possible); return final bool

### Enforcer Variants

| File | Purpose |
|---|---|
| `enforcer.go` | Base enforcer |
| `enforcer_cached.go` | In-memory LRU cache of decisions |
| `enforcer_synced.go` | Thread-safe with `sync.RWMutex` |
| `enforcer_distributed.go` | Distributed with dispatcher |
| `enforcer_transactional.go` | Batch policy writes |
| `enforcer_context.go` | Per-request context (`EnforceContext`) |

### Key Interfaces to Implement for Sydom

- `persist.Adapter` — plug in a database-backed policy store
- `persist.Watcher` — optional, for policy change notifications across nodes
- `rbac.RoleManager` — optional custom role graph (default impl covers most cases)
