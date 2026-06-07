package app

const tmplText = `
{{define "head"}}<!doctype html><html lang="zh"><head><meta charset="utf-8"><title>司域 Demo · 订单</title>
<style>body{font-family:system-ui,sans-serif;max-width:760px;margin:40px auto;padding:0 16px;color:#222}
h1{font-size:20px}table{border-collapse:collapse;width:100%}td,th{border:1px solid #ddd;padding:8px;text-align:left}
.who{color:#666;margin:8px 0 16px}a.btn,button{padding:6px 12px;border:1px solid #888;border-radius:6px;background:#f6f6f6;cursor:pointer;text-decoration:none;color:#222}
.warn{padding:16px;border:1px solid #e0a800;background:#fff8e1;border-radius:8px}</style></head><body>{{end}}
{{define "foot"}}</body></html>{{end}}

{{define "landing"}}{{template "head" .}}
<h1>司域 Demo · 订单服务</h1>
<p>选择一个身份进入（demo 用选人代替登录；司域只做授权，不做认证）：</p>
<p><a class="btn" href="/login?user=alice">以 alice 进入（manager · 可删可见全部）</a>
&nbsp;<a class="btn" href="/login?user=bob">以 bob 进入（clerk · 只读 · 仅本部门）</a></p>
{{if .User}}<p class="who">当前：{{.User}} · <a href="/logout">退出</a></p>{{end}}
{{template "foot" .}}{{end}}

{{define "orders"}}{{template "head" .}}
<h1>订单列表</h1><p class="who">当前：{{.User}} · <a href="/logout">切换身份</a></p>
<table><tr><th>ID</th><th>客户</th><th>金额</th><th>部门</th><th>操作</th></tr>
{{range .Orders}}<tr><td>{{.ID}}</td><td>{{.Customer}}</td><td>{{.Amount}}</td><td>{{.Dept}}</td>
<td><form method="post" action="/orders/{{.ID}}/delete"><button type="submit">删除</button></form></td></tr>{{end}}
</table>{{template "foot" .}}{{end}}

{{define "forbidden"}}{{template "head" .}}
<h1>403 · 无权操作</h1>
<div class="warn">当前身份 <b>{{.User}}</b> 无权执行该操作（司域判定为拒绝）。<br>clerk 只读、不能删除；切到 alice 试试。</div>
<p><a class="btn" href="/orders">返回列表</a></p>{{template "foot" .}}{{end}}

{{define "unavailable"}}{{template "head" .}}
<h1>503 · 鉴权暂不可用</h1>
<div class="warn">Sidecar 尚未就绪或暂时不可达，按 fail-close 拒绝服务。请稍后重试。</div>{{template "foot" .}}{{end}}

{{define "error"}}{{template "head" .}}
<h1>500 · 出错了</h1><div class="warn">{{.Msg}}</div>{{template "foot" .}}{{end}}
`
