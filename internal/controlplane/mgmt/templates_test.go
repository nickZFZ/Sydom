package mgmt_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListTemplates_ReturnsBuiltins(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)

	resp, err := srv.ListTemplates(context.Background(),
		&adminv1.ListTemplatesRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resp.Templates), 2, "should have at least 2 builtin templates")

	var names []string
	for _, tpl := range resp.Templates {
		names = append(names, tpl.Name)
		require.NotEmpty(t, tpl.Permissions, "template %q should have permissions", tpl.Id)
		require.NotEmpty(t, tpl.Roles, "template %q should have roles", tpl.Id)
	}
	// 验证含中文 name：「通用后台管理」或「电商运营」
	found := false
	for _, n := range names {
		if strings.Contains(n, "通用后台管理") || strings.Contains(n, "电商运营") {
			found = true
			break
		}
	}
	require.True(t, found, "expected builtin Chinese name in templates, got: %v", names)
}

func TestApplyTemplate_OwnTenant(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)

	resp, err := srv.ApplyTemplate(context.Background(),
		&adminv1.ApplyTemplateRequest{AppId: uint64(appID), TemplateId: "general-admin"})
	require.NoError(t, err)
	// general-admin: 5 权限点、3 角色
	require.EqualValues(t, 5, resp.PermissionsUpserted)
	require.EqualValues(t, 3, resp.RolesCreated)

	// 确认确定性 code 存在于 DB
	var code string
	err = db.QueryRowContext(context.Background(),
		`SELECT code FROM role WHERE app_id=$1 AND code='tpl:general-admin:admin'`, appID).Scan(&code)
	require.NoError(t, err)
	require.Equal(t, "tpl:general-admin:admin", code)
}

func TestApplyTemplate_UnknownTemplate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)

	_, err := srv.ApplyTemplate(context.Background(),
		&adminv1.ApplyTemplateRequest{AppId: uint64(appID), TemplateId: "nope"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestApplyTemplate_CrossTenant403(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/ApplyTemplate"
	req := &adminv1.ApplyTemplateRequest{AppId: uint64(appB), TemplateId: "general-admin"}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
