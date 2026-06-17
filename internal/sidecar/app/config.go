// Package app 装配 Sidecar 进程：加载配置、构造内核+数据权限+同步客户端+鉴权门面、
// 起对账协程、监听本地 AuthService、优雅关闭。
package app

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 Sidecar 进程运行参数。非敏感项来自 YAML，敏感项（Secret）只来自 env。
type Config struct {
	ControlPlaneAddr   string        // 控制面 PolicySync 地址
	AppKey             string        // app_key：HMAC 认证标识 + 流路由（→ syncclient.AppID）
	Domain             string        // casbin 域（= application.domain，→ kernel.New 域）
	AuthAddr           string        // 本地 AuthService 监听地址（如 "127.0.0.1:8090"）
	MaxStaleness       time.Duration // 陈旧守卫上限（零值=关闭）
	BackoffInitial     time.Duration // syncclient 退避初值（零值用 500ms）
	BackoffMax         time.Duration // syncclient 退避上限（零值用 30s）
	TLSCertFile        string        // serve auth 口（SDK→sidecar）证书；空=明文，与 TLSKeyFile 须同设
	TLSKeyFile         string
	ControlPlaneTLS    bool   // dial 控制面 sync 是否走 TLS
	ControlPlaneCAFile string // 信任 CA；空=系统根
	HealthAddr         string // 空=不起健康口

	Secret []byte // env SYDOM_APP_SECRET（HMAC 密钥，原始字节）
}

type fileConfig struct {
	ControlPlaneAddr   string `yaml:"control_plane_addr"`
	AppKey             string `yaml:"app_key"`
	Domain             string `yaml:"domain"`
	AuthAddr           string `yaml:"auth_addr"`
	MaxStaleness       string `yaml:"max_staleness"`
	BackoffInitial     string `yaml:"backoff_initial"`
	BackoffMax         string `yaml:"backoff_max"`
	TLSCertFile        string `yaml:"tls_cert_file"`
	TLSKeyFile         string `yaml:"tls_key_file"`
	ControlPlaneTLS    bool   `yaml:"control_plane_tls"`
	ControlPlaneCAFile string `yaml:"control_plane_ca_file"`
	HealthAddr         string `yaml:"health_addr"`
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
		ControlPlaneAddr:   firstNonEmpty(getenv("SYDOM_CONTROL_PLANE_ADDR"), fc.ControlPlaneAddr),
		AppKey:             fc.AppKey,
		Domain:             fc.Domain,
		AuthAddr:           fc.AuthAddr,
		TLSCertFile:        fc.TLSCertFile,
		TLSKeyFile:         fc.TLSKeyFile,
		ControlPlaneTLS:    fc.ControlPlaneTLS,
		ControlPlaneCAFile: fc.ControlPlaneCAFile,
		HealthAddr:         fc.HealthAddr,
	}
	if cfg.MaxStaleness, err = parseDurationDefault(fc.MaxStaleness, 0); err != nil {
		return Config{}, fmt.Errorf("max_staleness: %w", err)
	}
	if cfg.BackoffInitial, err = parseDurationDefault(fc.BackoffInitial, 500*time.Millisecond); err != nil {
		return Config{}, fmt.Errorf("backoff_initial: %w", err)
	}
	if cfg.BackoffMax, err = parseDurationDefault(fc.BackoffMax, 30*time.Second); err != nil {
		return Config{}, fmt.Errorf("backoff_max: %w", err)
	}

	cfg.Secret = []byte(getenv("SYDOM_APP_SECRET"))

	if len(cfg.Secret) == 0 {
		return Config{}, errors.New("SYDOM_APP_SECRET required")
	}
	for _, f := range []struct{ name, val string }{
		{"control_plane_addr", cfg.ControlPlaneAddr},
		{"app_key", cfg.AppKey},
		{"domain", cfg.Domain},
		{"auth_addr", cfg.AuthAddr},
	} {
		if f.val == "" {
			return Config{}, fmt.Errorf("%s required", f.name)
		}
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
