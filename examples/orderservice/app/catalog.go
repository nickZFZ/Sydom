package app

import (
	"context"
	"log"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// CatalogPermissions 是订单服务自声明的功能权限点目录。
// read/write/delete 已接路由并由管理员授权（控制面侧为 source=manual）；
// export 是声明但尚未接路由/授权的能力点——auto 上报后落 source=auto（演示新增 auto 插入）。
func CatalogPermissions() []sydom.Permission {
	return []sydom.Permission{
		{Code: "order:read", Resource: "order", Action: "read", Type: "api", Name: "查看订单"},
		{Code: "order:write", Resource: "order", Action: "write", Type: "api", Name: "创建订单"},
		{Code: "order:delete", Resource: "order", Action: "delete", Type: "api", Name: "删除订单"},
		{Code: "order:export", Resource: "order", Action: "export", Type: "api", Name: "导出订单"},
	}
}

// ReportCatalog 启动时上报权限点目录。上报是目录元数据、非鉴权决策——fail-soft：
// 失败记日志后继续，不阻塞启动、不影响鉴权（与 SDK D 切片语义一致）。
func ReportCatalog(ctx context.Context, r sydom.PermissionReporter) {
	reg := sydom.NewRegistry()
	for _, p := range CatalogPermissions() {
		reg.Register(p)
	}
	res, err := reg.Report(ctx, r)
	if err != nil {
		log.Printf("权限点上报失败（fail-soft，继续启动）: %v", err)
		return
	}
	log.Printf("权限点上报完成：新增/刷新 %d，跳过(人工保留) %d", res.Upserted, res.Skipped)
}
