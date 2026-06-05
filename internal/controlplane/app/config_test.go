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
