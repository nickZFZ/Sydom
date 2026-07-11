package app_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"SYDOM_MASTER_KEY":  base64.StdEncoding.EncodeToString(make([]byte, crypto.KeySize)),
		"SYDOM_ROOT_SECRET": "root-secret",
	}
}

const fullYAML = `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
heartbeat_interval: "10s"
relay_poll_interval: "2s"
`

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "postgres://localhost/sydom", cfg.DatabaseDSN)
	require.Equal(t, ":8081", cfg.AdminAddr)
	require.Equal(t, ":8082", cfg.SyncAddr)
	require.Equal(t, "root@sydom", cfg.RootPrincipal)
	require.Equal(t, 10*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, 2*time.Second, cfg.RelayPollInterval)
	require.Len(t, cfg.MasterKey, crypto.KeySize)
	require.Equal(t, []byte("root-secret"), cfg.RootSecret)
}

func TestLoadConfig_EnvOverridesDSN(t *testing.T) {
	env := validEnv()
	env["SYDOM_DATABASE_DSN"] = "postgres://override/db"
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, "postgres://override/db", cfg.DatabaseDSN)
}

func TestLoadConfig_MasterKeyWrongSize(t *testing.T) {
	env := validEnv()
	env["SYDOM_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingMasterKey(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_MASTER_KEY")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRootSecret(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_ROOT_SECRET")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRequiredAddr(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
sync_addr: ":8082"
root_principal: "root@sydom"
` // 缺 admin_addr
	_, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_IntervalDefaults(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
` // 无间隔
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, time.Second, cfg.RelayPollInterval)
}

func TestLoadConfig_RESTAddr(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
rest_addr: ":8083"
root_principal: "root@sydom"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, ":8083", cfg.RESTAddr)
}

func TestLoadConfig_RESTAddrOptional(t *testing.T) {
	// 省略 rest_addr 时 LoadConfig 应成功，RESTAddr 为空（不起 REST 向后兼容）。
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "", cfg.RESTAddr)
}

func TestLoadConfig_ConsoleOptional(t *testing.T) {
	// console_addr omitted → LoadConfig succeeds, ConsoleAddr=="", ConsoleSessionTTL defaults to 30m.
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "", cfg.ConsoleAddr)
	require.Equal(t, 30*time.Minute, cfg.ConsoleSessionTTL)
	require.False(t, cfg.ConsoleCookieInsecure)
}

func TestLoadConfig_ConsoleConfigured(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
console_addr: ":8084"
console_session_ttl: "15m"
console_cookie_insecure: true
root_principal: "root@sydom"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, ":8084", cfg.ConsoleAddr)
	require.Equal(t, 15*time.Minute, cfg.ConsoleSessionTTL)
	require.True(t, cfg.ConsoleCookieInsecure)
}

func TestLoadConfig_SyncClientCAFile(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
sync_client_ca_file: /etc/sydom/sync-ca.pem
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "/etc/sydom/sync-ca.pem", cfg.SyncClientCAFile)
}

func TestLoadConfigParsesTLSAndHealth(t *testing.T) {
	body := "database_dsn: postgres://x\nredis_addr: r:6379\nadmin_addr: \":1\"\nsync_addr: \":2\"\n" +
		"root_principal: root@sydom\ntls_cert_file: /c/cert.pem\ntls_key_file: /c/key.pem\nhealth_addr: \":8083\"\n"
	path := writeConfig(t, body)
	getenv := envFunc(map[string]string{
		"SYDOM_MASTER_KEY":  base64.StdEncoding.EncodeToString(make([]byte, crypto.KeySize)),
		"SYDOM_ROOT_SECRET": "rootsecret",
	})
	cfg, err := app.LoadConfig(path, getenv)
	require.NoError(t, err)
	require.Equal(t, "/c/cert.pem", cfg.TLSCertFile)
	require.Equal(t, "/c/key.pem", cfg.TLSKeyFile)
	require.Equal(t, ":8083", cfg.HealthAddr)
}

func TestLoadConfig_EnvironmentDefaultsDevelopment(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.False(t, cfg.Environment.IsProduction())
}

func TestLoadConfig_EnvironmentUnknownFailsClose(t *testing.T) {
	_, err := app.LoadConfig(writeConfig(t, fullYAML+"environment: prod\n"), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_ProductionRequiresTLS(t *testing.T) {
	// 生产但无 TLS → 报错
	_, err := app.LoadConfig(writeConfig(t, fullYAML+"environment: production\n"), envFunc(validEnv()))
	require.Error(t, err)
	// 生产 + TLS → ok（LoadConfig 不读证书文件本身，仅校验路径非空）
	okYAML := fullYAML + "environment: production\ntls_cert_file: /x/cert.pem\ntls_key_file: /x/key.pem\n"
	cfg, err := app.LoadConfig(writeConfig(t, okYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.True(t, cfg.Environment.IsProduction())
	// dev 无 TLS → ok（向后兼容）
	_, err = app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
}

func TestLoadConfig_EnvironmentEnvOverride(t *testing.T) {
	// yaml 显式设 development，env 设 production：env 必须覆盖 yaml。
	// 若 firstNonEmpty 顺序被颠倒（yaml 优先），则 development 生效、无 TLS 也不报错 → 本测试 FAIL（有齿）。
	env := validEnv()
	env["SYDOM_ENVIRONMENT"] = "production"
	_, err := app.LoadConfig(writeConfig(t, fullYAML+"environment: development\n"), envFunc(env))
	require.Error(t, err, "env=production 覆盖 yaml=development 且无 TLS 应报错（证明 env 优先）")
}

func TestLoadConfig_MasterKeyFromFile(t *testing.T) {
	env := validEnv()
	b64 := env["SYDOM_MASTER_KEY"]
	delete(env, "SYDOM_MASTER_KEY")
	p := filepath.Join(t.TempDir(), "mk")
	require.NoError(t, os.WriteFile(p, []byte(b64+"\n"), 0o600)) // 尾换行应被 trim 后 base64 解码成功
	env["SYDOM_MASTER_KEY_FILE"] = p
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Len(t, cfg.MasterKey, crypto.KeySize)
}

func TestLoadConfig_RootSecretFromFileAndConflict(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_ROOT_SECRET")
	p := filepath.Join(t.TempDir(), "rs")
	require.NoError(t, os.WriteFile(p, []byte("file-root-secret\n"), 0o600))
	env["SYDOM_ROOT_SECRET_FILE"] = p
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, []byte("file-root-secret"), cfg.RootSecret)
	// env + file 同设 → 报错
	env["SYDOM_ROOT_SECRET"] = "env-root-secret"
	_, err = app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}
