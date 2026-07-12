package app

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDSN(t *testing.T) {
	empty := func(string) string { return "" }

	// (a) 取 file database_dsn
	p := writeCfg(t, "database_dsn: \"postgres://f/db\"\n")
	if dsn, err := LoadDSN(p, empty); err != nil || dsn != "postgres://f/db" {
		t.Fatalf("file DSN: got %q err %v", dsn, err)
	}

	// (b) env SYDOM_DATABASE_DSN 覆盖 file
	getenv := func(k string) string {
		if k == "SYDOM_DATABASE_DSN" {
			return "postgres://e/db"
		}
		return ""
	}
	if dsn, err := LoadDSN(p, getenv); err != nil || dsn != "postgres://e/db" {
		t.Fatalf("env override: got %q err %v", dsn, err)
	}

	// (c) 空 DSN → 错（不静默放行）
	p2 := writeCfg(t, "redis_addr: \"r:6379\"\n")
	if _, err := LoadDSN(p2, empty); err == nil {
		t.Fatal("空 DSN 应返错")
	}
}
