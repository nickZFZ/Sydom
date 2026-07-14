# 司域 · Sydom
**中文名称**：司域
**英文名称**：Sydom

## 名称释义
- Sy：取自汉字「司」，寓意管控
- dom：取自汉字「域」，源自英文 Domain，代表权限

## 品牌标语
**Slogan**：厘定辖域，权归其位

## 快速开始 · 一键 Demo

`examples/orderservice` 是一个**只用公开 SDK** 接入司域的订单服务，一条命令即可起全栈
（PostgreSQL + Redis + 控制面 + Sidecar + 订单服务），在浏览器里直观对比权限差异：

```bash
make demo          # 起全栈并自动供应（建角色/权限/授权/数据策略）
# 浏览器打开 http://localhost:8080
#   alice（manager）：看到全部订单、可删除
#   bob  （clerk）  ：仅见本部门订单、删除被拒
make smoke         # 可选：HTTP 冒烟（allow / deny / 数据过滤 各一）
make demo-down     # 拆栈（清容器与卷）
```

> 端口被占用时，在 `deploy/.env.demo` 取消注释对应 `*_HOST_PORT` 改用空闲端口。
> `.env.demo` 内为 **DEMO 占位密钥**，生产务必另行注入，绝不沿用。
> 供应不可重入（seeder 非 upsert），**重跑 `make demo` 前先 `make demo-down`** 清栈清卷。

一次真人浏览器走查（含截图）见 [`test/e2e/browser/WALKTHROUGH.md`](test/e2e/browser/WALKTHROUGH.md)，
逐屏印证**功能权限**、**数据权限**与 **fail-close** 三件事。

## 文档导航

完整文档索引见 **[`docs/README.md`](docs/README.md)**（评估 / 部署 / 运维 / API 契约分组）。生产部署直达：

- **Docker Compose**（单机/测试）：[`deploy/README.md`](deploy/README.md)
- **Kubernetes Helm**（生产）：[`deploy/helm/sydom-controlplane/README.md`](deploy/helm/sydom-controlplane/README.md)
- **运维 Runbook**：[零停机迁移](docs/runbooks/zero-downtime-migrations.md) · [备份恢复](docs/runbooks/backup-restore.md) · [性能基线](docs/runbooks/performance-baselines.md)
- **API 契约**：[API 版本化 + 向后兼容](docs/api-versioning.md)
