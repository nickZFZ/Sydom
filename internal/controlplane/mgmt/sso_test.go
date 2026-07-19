package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigureTenantIdp_EncryptsAndGetOmitsSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('sso-t') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s3cr3t", Domains: []string{"acme.com"}, Enabled: true,
	})
	require.NoError(t, err)

	// DB 里 client_secret 为密文，绝非明文。
	var enc []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc))
	require.NotContains(t, string(enc), "s3cr3t", "secret 须密文存储")

	// GetTenantIdp 回元数据但绝不含 secret（GetTenantIdpResponse 无 secret 字段，proto 保证）。
	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.True(t, got.Configured)
	require.Equal(t, "https://idp", got.Issuer)
	require.Equal(t, []string{"acme.com"}, got.Domains)
	require.True(t, got.Enabled)
}

func TestConfigureTenantIdp_DomainConflict_AlreadyExists(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var t1, t2 int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('a') RETURNING id`).Scan(&t1))
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('b') RETURNING id`).Scan(&t2))
	ctx := cp.WithOperator(context.Background(), "root")
	base := func(tid int64) *adminv1.ConfigureTenantIdpRequest {
		return &adminv1.ConfigureTenantIdpRequest{TenantId: uint64(tid), Issuer: "https://i",
			ClientId: "c", ClientSecret: "s", Domains: []string{"shared.com"}, Enabled: false}
	}
	_, err := srv.ConfigureTenantIdp(ctx, base(t1))
	require.NoError(t, err)
	_, err = srv.ConfigureTenantIdp(ctx, base(t2))
	require.Equal(t, codes.AlreadyExists, status.Code(err), "跨租户抢同域须 AlreadyExists")
}

func TestConfigureTenantIdp_MissingFields_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('c') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	cases := []struct {
		name string
		req  *adminv1.ConfigureTenantIdpRequest
	}{
		{"empty issuer", &adminv1.ConfigureTenantIdpRequest{
			TenantId: uint64(tid), Issuer: "", ClientId: "c", ClientSecret: "s", Domains: []string{"x.com"}}},
		{"empty client_id", &adminv1.ConfigureTenantIdpRequest{
			TenantId: uint64(tid), Issuer: "https://i", ClientId: "", ClientSecret: "s", Domains: []string{"x.com"}}},
		{"empty client_secret", &adminv1.ConfigureTenantIdpRequest{
			TenantId: uint64(tid), Issuer: "https://i", ClientId: "c", ClientSecret: "", Domains: []string{"x.com"}}},
		{"empty domains", &adminv1.ConfigureTenantIdpRequest{
			TenantId: uint64(tid), Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.ConfigureTenantIdp(ctx, tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// 单个域条目 TrimSpace 后为空串须 InvalidArgument（不得写入占用全局唯一的 "" 槽位）。
func TestConfigureTenantIdp_EmptyDomain_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('empd') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")
	for _, doms := range [][]string{{"  "}, {"acme.com", ""}, {"acme.com", "   "}} {
		_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
			TenantId: uint64(tid), Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: doms,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "空/纯空白域须 InvalidArgument：%v", doms)
	}
}

// 未知 tenant_id 触发外键违例（tenant_idp_tenant_id_fkey），须映射为 NotFound，
// 不得把裸 pq 错误（含表名/约束名）以 Internal 透传。
func TestConfigureTenantIdp_UnknownTenant_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: 999999, Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: []string{"nx.com"},
	})
	require.Equal(t, codes.NotFound, status.Code(err), "未知租户须 NotFound 而非 Internal/泄露裸 SQL")
}

// 授权门：跨租户配置 IdP 须 PermissionDenied（scopeTenant）。
func TestConfigureTenantIdp_CrossTenant_Denied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	_ = appB
	var tB int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='tenant-b'`).Scan(&tB))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/ConfigureTenantIdp"
	req := &adminv1.ConfigureTenantIdpRequest{TenantId: uint64(tB), Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: []string{"z.com"}}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "tenant-a 管理员配 tenant-b IdP 须拒")
}

// 授权门：跨租户读 IdP 须 PermissionDenied（scopeTenant，读路径同受租户隔离）。
func TestGetTenantIdp_CrossTenant_Denied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "gt-a", "gdom-a", "GAK_a")
	dbtest.SeedAppInTenant(t, db, "gt-b", "gdom-b", "GAK_b")
	var tB int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='gt-b'`).Scan(&tB))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	_, err = mgmt.AuthorizeRule(ctx, enf,
		"/sydom.admin.v1.AdminService/GetTenantIdp", "alice",
		&adminv1.GetTenantIdpRequest{TenantId: uint64(tB)})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "跨租户读 IdP 须拒")
}

// 授权门：租户 admin 对本租户 ConfigureTenantIdp 须放行（证明门非对所有人 fail-closed）。
func TestConfigureTenantIdp_SameTenant_Allowed(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "own-a", "odom-a", "OAK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	_, err = mgmt.AuthorizeRule(ctx, enf,
		"/sydom.admin.v1.AdminService/ConfigureTenantIdp", "alice",
		&adminv1.ConfigureTenantIdpRequest{TenantId: uint64(tA), Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: []string{"own.com"}})
	require.NoError(t, err, "owner 配本租户 IdP 授权门须放行")
}

// M6-sso-3：jit_enabled 经 ConfigureTenantIdp 落库 + GetTenantIdp 回显。
func TestConfigureTenantIdp_JITRoundtrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('jit-t') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)

	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.True(t, got.JitEnabled, "GetTenantIdp 须回显 jit_enabled")

	var jit bool
	require.NoError(t, db.QueryRow(`SELECT jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&jit))
	require.True(t, jit)
}

// M6-sso-4：编辑时空 client_secret 保留旧密文；首次配置须提供 secret。
func TestConfigureTenantIdp_KeepSecretOnEmptyUpdate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-keep') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	// 首次配置（带 secret）。
	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s3cr3t", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: false,
	})
	require.NoError(t, err)
	var enc1 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc1))

	// 编辑：空 secret + 切 jit_enabled → 密文不变、jit 变。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)
	var enc2 []byte
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc2, &jit))
	require.Equal(t, enc1, enc2, "空 secret 编辑须保留旧密文")
	require.True(t, jit, "jit_enabled 应已切换")

	// 编辑：带新 secret → 密文变化（轮换）。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "rotated", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)
	var enc3 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc3))
	require.NotEqual(t, enc2, enc3, "带新 secret 须轮换密文")
}

func TestDeleteTenantIdp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-del') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	// 无配置删→NotFound。
	_, err := srv.DeleteTenantIdp(ctx, &adminv1.DeleteTenantIdpRequest{TenantId: uint64(tid)})
	require.Equal(t, codes.NotFound, status.Code(err))

	// 配置后删→成功、GetTenantIdp Configured=false、域清空。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s", Domains: []string{"acme.com"}, Enabled: true})
	require.NoError(t, err)
	_, err = srv.DeleteTenantIdp(ctx, &adminv1.DeleteTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.False(t, got.Configured)
	var domainCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&domainCount))
	require.Equal(t, 0, domainCount, "删除须清空域")
}

func TestConfigureTenantIdp_FirstConfigRequiresSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-first') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "", Domains: []string{"acme.com"}, Enabled: true,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "首次配置空 secret 须 InvalidArgument")
}
