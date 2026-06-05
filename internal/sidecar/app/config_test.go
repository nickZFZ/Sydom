package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/app"
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
	return map[string]string{"SYDOM_APP_SECRET": "app-secret"}
}

const fullYAML = `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "90s"
backoff_initial: "250ms"
backoff_max: "10s"
`

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "localhost:8082", cfg.ControlPlaneAddr)
	require.Equal(t, "app-1", cfg.AppKey)
	require.Equal(t, "shop", cfg.Domain)
	require.Equal(t, "127.0.0.1:8090", cfg.AuthAddr)
	require.Equal(t, 90*time.Second, cfg.MaxStaleness)
	require.Equal(t, 250*time.Millisecond, cfg.BackoffInitial)
	require.Equal(t, 10*time.Second, cfg.BackoffMax)
	require.Equal(t, []byte("app-secret"), cfg.Secret)
}

func TestLoadConfig_EnvOverridesControlPlaneAddr(t *testing.T) {
	env := validEnv()
	env["SYDOM_CONTROL_PLANE_ADDR"] = "cp.internal:9000"
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, "cp.internal:9000", cfg.ControlPlaneAddr)
}

func TestLoadConfig_MissingSecret(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_APP_SECRET")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRequiredField(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
auth_addr: "127.0.0.1:8090"
` // 缺 domain
	_, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_Defaults(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
` // 无 max_staleness / 退避
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.MaxStaleness)
	require.Equal(t, 500*time.Millisecond, cfg.BackoffInitial)
	require.Equal(t, 30*time.Second, cfg.BackoffMax)
}

func TestLoadConfig_MaxStalenessZeroExplicit(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "0s"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.MaxStaleness)
}
