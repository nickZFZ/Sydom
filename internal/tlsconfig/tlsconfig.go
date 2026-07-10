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

// MutualServer 由已构造的服务端配置派生「要求并验证客户端证书」的变体：
//   clientCAFile 空                                → 返回 base 原样（向后兼容，不要求客户端证书）；
//   base 为 nil（未启用服务端 TLS）但 clientCAFile 非空 → 返错（fail-close：明文上无法要求客户端证书）；
//   clientCAFile 不可读/无有效 PEM                  → 返错。
// 非空路径 base.Clone() 后设置 ClientAuth/ClientCAs，绝不改写入参 base（避免别名污染共享配置）。
func MutualServer(base *tls.Config, clientCAFile string) (*tls.Config, error) {
	if clientCAFile == "" {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("tls: 要求客户端证书须先启用服务端 TLS（sync_client_ca_file 已设但 cert/key 未设）")
	}
	pemBytes, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 读取客户端 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("tls: 客户端 CA 文件不含有效 PEM 证书块")
	}
	cfg := base.Clone()
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.ClientCAs = pool
	return cfg, nil
}

// MutualClient 在 Client(caFile) 基础上附加客户端证书对用于 mTLS：
//   certFile/keyFile 皆空 → 等价 Client（不出示客户端证书，向后兼容）；
//   仅一项非空          → 返错（fail-close：禁止半配置）；
//   都非空但加载失败    → 返错。
func MutualClient(caFile, certFile, keyFile string) (*tls.Config, error) {
	cfg, err := Client(caFile)
	if err != nil {
		return nil, err
	}
	if certFile == "" && keyFile == "" {
		return cfg, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: 客户端 cert_file 与 key_file 须同时设置（禁止半配置）")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: 加载客户端证书对失败: %w", err)
	}
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, nil
}
