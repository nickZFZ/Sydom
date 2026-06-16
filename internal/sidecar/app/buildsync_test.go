package app

import "testing"

func TestBuildSyncConfigTLSWiring(t *testing.T) {
	// TLS 关 → Secure=false，无附加 DialOptions
	off, err := buildSyncConfig(Config{ControlPlaneAddr: "cp:8082", AppKey: "k", Secret: []byte("s")})
	if err != nil {
		t.Fatal(err)
	}
	if off.Secure || len(off.DialOptions) != 0 {
		t.Fatalf("tls off want Secure=false & no dialopts, got %+v", off)
	}
	// TLS 开（系统根，无 CA 文件）→ Secure=true 且注入传输凭据
	on, err := buildSyncConfig(Config{ControlPlaneAddr: "cp:8082", AppKey: "k", Secret: []byte("s"), ControlPlaneTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if !on.Secure || len(on.DialOptions) == 0 {
		t.Fatalf("tls on want Secure=true & dialopts injected, got %+v", on)
	}
}
