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

func TestLoadConfigParsesTLSAndHealth(t *testing.T) {
	body := "control_plane_addr: cp:8082\napp_key: k\ndomain: shop\nauth_addr: \":8090\"\n" +
		"tls_cert_file: /c/cert.pem\ntls_key_file: /c/key.pem\n" +
		"control_plane_tls: true\ncontrol_plane_ca_file: /c/ca.pem\nhealth_addr: \":8091\"\n"
	path := writeConfig(t, body)
	getenv := envFunc(map[string]string{"SYDOM_APP_SECRET": "appsecret"})
	cfg, err := app.LoadConfig(path, getenv)
	require.NoError(t, err)
	require.Equal(t, "/c/cert.pem", cfg.TLSCertFile)
	require.Equal(t, "/c/key.pem", cfg.TLSKeyFile)
	require.True(t, cfg.ControlPlaneTLS)
	require.Equal(t, "/c/ca.pem", cfg.ControlPlaneCAFile)
	require.Equal(t, ":8091", cfg.HealthAddr)
}

func TestLoadConfig_ControlPlaneClientCert(t *testing.T) {
	body := "control_plane_addr: cp:8082\napp_key: k\ndomain: shop\nauth_addr: \":8090\"\n" +
		"control_plane_tls: true\ncontrol_plane_ca_file: /c/ca.pem\n" +
		"control_plane_client_cert_file: /etc/sydom/sidecar.crt\n" +
		"control_plane_client_key_file: /etc/sydom/sidecar.key\n"
	path := writeConfig(t, body)
	cfg, err := app.LoadConfig(path, envFunc(validEnv()))
	require.NoError(t, err)
	if cfg.ControlPlaneClientCertFile != "/etc/sydom/sidecar.crt" {
		t.Fatalf("ControlPlaneClientCertFile = %q, want /etc/sydom/sidecar.crt", cfg.ControlPlaneClientCertFile)
	}
	if cfg.ControlPlaneClientKeyFile != "/etc/sydom/sidecar.key" {
		t.Fatalf("ControlPlaneClientKeyFile = %q, want /etc/sydom/sidecar.key", cfg.ControlPlaneClientKeyFile)
	}
}
