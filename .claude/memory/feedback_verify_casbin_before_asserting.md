---
name: feedback-verify-casbin-before-asserting
description: "For Sydom, always verify claims against casbin v3.10.0 source before asserting them in design docs — never state casbin behavior from inference"
metadata: 
  node_type: memory
  type: feedback
  originSessionId: db698ca2-37fc-4821-8130-b63a6c379204
---

在司域（Sydom）设计中，**任何关于 casbin 行为的论断都必须先回源核实，不能凭推测/印象写进设计文档**。

**Why:** 用户反复强调"先核实再断言"。本会话中我两次因未核实而出错：(1) C2 写"司域走 casbin Dispatcher 路线的精神"是误导性类比——回源后发现 Dispatcher 是对等读写 enforcer 间的共识复制，恰是司域不具备的拓扑；(2) 写"启用 CachedEnforcer"未核实其失效逻辑，回源 `enforcer_cached.go` 才发现它不重写 AddPolicy/UpdatePolicy、按 key 删在 RBAC 下失效。用户原话："你提了先去核实，但实际上直接写进文档就提交了，违反了原则。"

**How to apply:**
- casbin 源码在 `/home/tongyu/codes/Sydom/casbin/`，CodeGraph 索引已建（查询时传 `projectPath: "/home/tongyu/codes/Sydom/casbin"`）。
- 写任何 casbin 能力/边界/行为的论断前，先 Read 对应源码或用 codegraph 核实。
- 在文档里对已核实的论断标注"（已回源核实 `文件名`）"，让读者知道哪些是事实、哪些是推测。
- 这是司域"casbin 是内核、绝不改源码、能复用绝不重造"原则的延伸——要复用就必须真的吃透它的行为。

相关：[[feedback-consistency-over-simplicity]]
