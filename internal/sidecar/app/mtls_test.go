package app

import (
	"crypto/x509"
	"testing"

	"github.com/nickZFZ/Sydom/internal/certtest"
)

func TestBuildSyncConfig_WithClientCertSucceeds(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	cfg := Config{
		ControlPlaneAddr:           "127.0.0.1:8081",
		ControlPlaneTLS:            true,
		ControlPlaneCAFile:         ca.File(),
		ControlPlaneClientCertFile: cliCert,
		ControlPlaneClientKeyFile:  cliKey,
	}
	sc, err := buildSyncConfig(cfg)
	if err != nil {
		t.Fatalf("配齐客户端证书应成功: %v", err)
	}
	if !sc.Secure {
		t.Fatal("走 TLS 时 Secure 应为 true")
	}
	if len(sc.DialOptions) != 1 {
		t.Fatalf("应注入一个传输凭据 DialOption, got %d", len(sc.DialOptions))
	}
}

func TestBuildSyncConfig_PartialClientCertFailsClose(t *testing.T) {
	ca := certtest.NewCA(t)
	cliCert, _ := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)
	cfg := Config{
		ControlPlaneAddr:           "127.0.0.1:8081",
		ControlPlaneTLS:            true,
		ControlPlaneCAFile:         ca.File(),
		ControlPlaneClientCertFile: cliCert, // 缺 key
	}
	if _, err := buildSyncConfig(cfg); err == nil {
		t.Fatal("客户端证书半配置应 fail-close 返错")
	}
}
