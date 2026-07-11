// Package app 装配控制面进程：加载配置、连 DB/Redis、起 AdminService/PolicySync、跑 relay/dispatch、优雅关闭。
package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/deploycfg"
	"gopkg.in/yaml.v3"
)

// Config 是控制面进程运行参数。非敏感项来自 YAML，敏感项（MasterKey/RootSecret）只来自 env。
type Config struct {
	DatabaseDSN       string
	RedisAddr         string
	AdminAddr         string
	SyncAddr          string
	RESTAddr          string // 空=不起 REST，向后兼容
	RootPrincipal     string
	HeartbeatInterval time.Duration
	RelayPollInterval time.Duration
	Environment       deploycfg.Environment // development（默认）/ production；production 下传输 TLS 缺失即拒启动

	ConsoleAddr           string        // 空=不起 Console，向后兼容
	ConsoleSessionTTL     time.Duration // 默认 30m
	ConsoleCookieInsecure bool          // true=允许非 HTTPS 下发 cookie（本地/明文测试）

	TLSCertFile      string // 空=明文；与 TLSKeyFile 须同设（tlsconfig.Server 校验）
	TLSKeyFile       string
	SyncClientCAFile string // 非空=policysync 通道要求客户端证书链到此 CA（mTLS）；空=不要求
	HealthAddr       string // 空=不起健康口（向后兼容）；明文、免鉴权

	MasterKey  []byte // env SYDOM_MASTER_KEY（base64，解码须 32 字节）
	RootSecret []byte // env SYDOM_ROOT_SECRET（原始字节）
}

type fileConfig struct {
	DatabaseDSN       string `yaml:"database_dsn"`
	RedisAddr         string `yaml:"redis_addr"`
	AdminAddr         string `yaml:"admin_addr"`
	SyncAddr          string `yaml:"sync_addr"`
	RESTAddr          string `yaml:"rest_addr"`
	RootPrincipal     string `yaml:"root_principal"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
	RelayPollInterval string `yaml:"relay_poll_interval"`
	Environment       string `yaml:"environment"`

	ConsoleAddr           string `yaml:"console_addr"`
	ConsoleSessionTTL     string `yaml:"console_session_ttl"`
	ConsoleCookieInsecure bool   `yaml:"console_cookie_insecure"`

	TLSCertFile      string `yaml:"tls_cert_file"`
	TLSKeyFile       string `yaml:"tls_key_file"`
	SyncClientCAFile string `yaml:"sync_client_ca_file"`
	HealthAddr       string `yaml:"health_addr"`
}

// LoadConfig 读 YAML + env 覆盖密钥/可选项 + 校验（任一不满足 fail-close 返错）。
// getenv 注入便于测试（生产传 os.Getenv）。
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg := Config{
		DatabaseDSN:   firstNonEmpty(getenv("SYDOM_DATABASE_DSN"), fc.DatabaseDSN),
		RedisAddr:     firstNonEmpty(getenv("SYDOM_REDIS_ADDR"), fc.RedisAddr),
		AdminAddr:     fc.AdminAddr,
		SyncAddr:      fc.SyncAddr,
		RESTAddr:      fc.RESTAddr,
		RootPrincipal: fc.RootPrincipal,

		ConsoleAddr:           fc.ConsoleAddr,
		ConsoleCookieInsecure: fc.ConsoleCookieInsecure,

		TLSCertFile:      fc.TLSCertFile,
		TLSKeyFile:       fc.TLSKeyFile,
		SyncClientCAFile: fc.SyncClientCAFile,
		HealthAddr:       fc.HealthAddr,
	}
	if cfg.HeartbeatInterval, err = parseDurationDefault(fc.HeartbeatInterval, 30*time.Second); err != nil {
		return Config{}, fmt.Errorf("heartbeat_interval: %w", err)
	}
	if cfg.RelayPollInterval, err = parseDurationDefault(fc.RelayPollInterval, time.Second); err != nil {
		return Config{}, fmt.Errorf("relay_poll_interval: %w", err)
	}
	if cfg.ConsoleSessionTTL, err = parseDurationDefault(fc.ConsoleSessionTTL, 30*time.Minute); err != nil {
		return Config{}, fmt.Errorf("console_session_ttl: %w", err)
	}

	if cfg.Environment, err = deploycfg.ParseEnvironment(firstNonEmpty(getenv("SYDOM_ENVIRONMENT"), fc.Environment)); err != nil {
		return Config{}, fmt.Errorf("environment: %w", err)
	}

	masterKeyB64, err := deploycfg.ResolveSecret(getenv, "SYDOM_MASTER_KEY")
	if err != nil {
		return Config{}, err
	}
	mk, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		return Config{}, fmt.Errorf("decode SYDOM_MASTER_KEY: %w", err)
	}
	cfg.MasterKey = mk
	rootSecret, err := deploycfg.ResolveSecret(getenv, "SYDOM_ROOT_SECRET")
	if err != nil {
		return Config{}, err
	}
	cfg.RootSecret = []byte(rootSecret)

	if len(cfg.MasterKey) != crypto.KeySize {
		return Config{}, fmt.Errorf("SYDOM_MASTER_KEY must decode to %d bytes, got %d", crypto.KeySize, len(cfg.MasterKey))
	}
	if len(cfg.RootSecret) == 0 {
		return Config{}, errors.New("SYDOM_ROOT_SECRET required")
	}
	for _, f := range []struct{ name, val string }{
		{"database_dsn", cfg.DatabaseDSN},
		{"redis_addr", cfg.RedisAddr},
		{"admin_addr", cfg.AdminAddr},
		{"sync_addr", cfg.SyncAddr},
		{"root_principal", cfg.RootPrincipal},
	} {
		if f.val == "" {
			return Config{}, fmt.Errorf("%s required", f.name)
		}
	}
	if cfg.Environment.IsProduction() && (cfg.TLSCertFile == "" || cfg.TLSKeyFile == "") {
		return Config{}, errors.New("environment=production 要求设置 tls_cert_file 与 tls_key_file（生产不得走明文）")
	}
	return cfg, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func parseDurationDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}
