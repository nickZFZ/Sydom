// Package tlsconfig 集中构造服务端/客户端 *tls.Config，统一证书加载与 fail-close 校验：
// 任一证书项配置不全或加载失败即返错，调用方据此拒绝启动，绝不静默明文降级。
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Server 由 cert/key 文件对构造服务端 TLS 配置。
// 两者皆空 → 返回 (nil, nil)（调用方按明文处理）；仅一项非空 → 返错（fail-close）；
// 都非空但加载失败 → 返错。
// 调用方收到 nil 时须显式决定是否允许明文，不得默认将 nil 传入需要 TLS 的监听器。
func Server(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: cert_file 与 key_file 须同时设置（禁止半配置静默明文）")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 加载证书对失败: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// Client 构造客户端 TLS 配置；caFile 非空时以其为信任根，空时用系统根证书。
func Client(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 读取 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("tls: CA 文件不含有效 PEM 证书块")
	}
	cfg.RootCAs = pool
	return cfg, nil
}
