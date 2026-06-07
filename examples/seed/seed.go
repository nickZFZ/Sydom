package seed

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// 轮询与供应各自独立计时；60×500ms ≈ 30s。
const (
	readyPollInterval = 500 * time.Millisecond
	readyTimeout      = 30 * time.Second
	provisionTimeout  = 30 * time.Second
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Run 是 seeder 二进制的主流程：连 AdminService、等就绪、供应一个 app，
// 把明文 app_secret 打到 stdout（唯一 stdout 输出），所有日志走 stderr，供编排器捕获。
func Run() int {
	log.SetOutput(os.Stderr) // 仅 secret 走 stdout
	adminAddr := env("CP_ADMIN_ADDR", "127.0.0.1:8081")
	principal := env("ROOT_PRINCIPAL", "root@sydom")
	rootSecret := []byte(env("SYDOM_ROOT_SECRET", ""))
	tenant := env("TENANT", "demo")
	domain := env("DOMAIN", "shop")
	appKey := env("APP_KEY", "demo-shop")
	if len(rootSecret) == 0 {
		log.Print("SYDOM_ROOT_SECRET 必填")
		return 1
	}

	conn, err := grpc.NewClient(adminAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(principal, rootSecret, false)))
	if err != nil {
		log.Printf("dial admin: %v", err)
		return 1
	}
	defer conn.Close()
	cli := adminv1.NewAdminServiceClient(conn)

	// 等控制面就绪（root 能调通 AdminService）。
	waitCtx, waitCancel := context.WithTimeout(context.Background(), readyTimeout)
	defer waitCancel()

	var lastErr error
	for {
		if _, err := cli.ListApplications(waitCtx, &adminv1.ListApplicationsRequest{}); err == nil {
			break
		} else {
			lastErr = err
		}
		select {
		case <-time.After(readyPollInterval):
		case <-waitCtx.Done():
			log.Printf("控制面未就绪 (last: %v)", lastErr)
			return 1
		}
	}

	provCtx, provCancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer provCancel()
	secret, err := Provision(provCtx, cli, tenant, domain, appKey)
	if err != nil {
		log.Printf("provision: %v", err)
		return 1
	}
	fmt.Print(secret) // 唯一 stdout 输出：app_secret 明文（编排器捕获）
	return 0
}

// Provision 建 app + 角色/权限/授权/绑定/数据策略，返回该 app 的明文 secret。
func Provision(ctx context.Context, cli adminv1.AdminServiceClient, tenant, domain, appKey string) (string, error) {
	appResp, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: tenant, Domain: domain, Name: appKey, AppKey: appKey,
	})
	if err != nil {
		return "", fmt.Errorf("create app: %w", err)
	}
	appID := appResp.GetAppId()

	mgr, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "manager", Name: "经理"})
	if err != nil {
		return "", fmt.Errorf("role manager: %w", err)
	}
	clerk, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "clerk", Name: "店员"})
	if err != nil {
		return "", fmt.Errorf("role clerk: %w", err)
	}

	// 被授权的权限点经 UpsertPermission 预建（source=manual）。order:export 不在此——交 app auto 上报。
	perm := func(code, action, name string) (int64, error) {
		r, e := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
			AppId: appID, Code: code, Resource: "order", Action: action, Ptype: "api", Name: name,
		})
		if e != nil {
			return 0, e
		}
		return r.GetPermissionId(), nil
	}
	pRead, err := perm("order:read", "read", "查看订单")
	if err != nil {
		return "", fmt.Errorf("perm read: %w", err)
	}
	pWrite, err := perm("order:write", "write", "创建订单")
	if err != nil {
		return "", fmt.Errorf("perm write: %w", err)
	}
	pDelete, err := perm("order:delete", "delete", "删除订单")
	if err != nil {
		return "", fmt.Errorf("perm delete: %w", err)
	}

	grant := func(roleID, permID int64) error {
		_, e := cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
			AppId: appID, RoleId: roleID, PermissionId: permID, Eft: "allow",
		})
		return e
	}
	for _, p := range []int64{pRead, pWrite, pDelete} {
		if err := grant(mgr.GetRoleId(), p); err != nil {
			return "", fmt.Errorf("grant manager: %w", err)
		}
	}
	if err := grant(clerk.GetRoleId(), pRead); err != nil {
		return "", fmt.Errorf("grant clerk read: %w", err)
	}

	bind := func(user string, roleID int64) error {
		_, e := cli.BindUserRole(ctx, &adminv1.UserRoleRequest{AppId: appID, UserId: user, RoleId: roleID})
		return e
	}
	if err := bind("alice", mgr.GetRoleId()); err != nil {
		return "", fmt.Errorf("bind alice: %w", err)
	}
	if err := bind("bob", clerk.GetRoleId()); err != nil {
		return "", fmt.Errorf("bind bob: %w", err)
	}

	// 数据策略（resource=order）：clerk 仅本部门；manager 覆盖既有两部门（看全部）。
	dp := func(subjectID, condition string) error {
		_, e := cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
			AppId: appID, Id: 0, SubjectType: "role", SubjectId: subjectID,
			Resource: "order", Condition: condition, Effect: "allow",
		})
		return e
	}
	if err := dp("clerk", `{"field":"dept","op":"EQ","value":"$user.department"}`); err != nil {
		return "", fmt.Errorf("dp clerk: %w", err)
	}
	if err := dp("manager", `{"field":"dept","op":"IN","value":["shanghai","beijing"]}`); err != nil {
		return "", fmt.Errorf("dp manager: %w", err)
	}

	return appResp.GetAppSecret(), nil
}
