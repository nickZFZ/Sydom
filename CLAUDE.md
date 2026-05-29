# CLAUDE.md

This file provides guidance to Claude (claude.ai/code) when working with code in this repository.

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
