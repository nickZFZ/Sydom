package deploycfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nickZFZ/Sydom/internal/deploycfg"
)

func TestParseEnvironment(t *testing.T) {
	cases := []struct {
		in      string
		want    deploycfg.Environment
		wantErr bool
	}{
		{"", deploycfg.Development, false},
		{"development", deploycfg.Development, false},
		{"production", deploycfg.Production, false},
		{"prod", deploycfg.Development, true},       // 拼写错误 fail-close
		{"PRODUCTION", deploycfg.Development, true}, // 大小写敏感
	}
	for _, c := range cases {
		got, err := deploycfg.ParseEnvironment(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseEnvironment(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseEnvironment(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseEnvironment(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEnvironmentIsProduction(t *testing.T) {
	if deploycfg.Development.IsProduction() {
		t.Fatal("Development.IsProduction() 应为 false")
	}
	if !deploycfg.Production.IsProduction() {
		t.Fatal("Production.IsProduction() 应为 true")
	}
}

func TestResolveSecret_EnvOnly(t *testing.T) {
	getenv := func(k string) string {
		if k == "SYDOM_X" {
			return "env-value"
		}
		return ""
	}
	got, err := deploycfg.ResolveSecret(getenv, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "env-value" {
		t.Fatalf("want env-value, got %q", got)
	}
}

func TestResolveSecret_FileOnlyTrimsTrailing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "SYDOM_X_FILE" {
			return p
		}
		return ""
	}
	got, err := deploycfg.ResolveSecret(getenv, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-value" {
		t.Fatalf("want file-value（尾换行应被 trim）, got %q", got)
	}
}

func TestResolveSecret_FileTrimsOnlyTrailingWhitespaceClass(t *testing.T) {
	// 前导空白必须保留（防止误用 TrimSpace）；尾部混合空白字符类须全裁（防止误用 TrimSuffix(s,"\n")）。
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("  file-value \t\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "SYDOM_X_FILE" {
			return p
		}
		return ""
	}
	got, err := deploycfg.ResolveSecret(getenv, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "  file-value" {
		t.Fatalf("want %q（仅裁尾部完整空白类、保前导）, got %q", "  file-value", got)
	}
}

func TestResolveSecret_BothSetFailsClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("file-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case "SYDOM_X":
			return "env-value"
		case "SYDOM_X_FILE":
			return p
		}
		return ""
	}
	if _, err := deploycfg.ResolveSecret(getenv, "SYDOM_X"); err == nil {
		t.Fatal("env 与 _FILE 同设应报错")
	}
}

func TestResolveSecret_NeitherSetReturnsEmpty(t *testing.T) {
	got, err := deploycfg.ResolveSecret(func(string) string { return "" }, "SYDOM_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("皆空应返回空串, got %q", got)
	}
}

func TestResolveSecret_UnreadableFileFailsClose(t *testing.T) {
	getenv := func(k string) string {
		if k == "SYDOM_X_FILE" {
			return "/no/such/secret/file"
		}
		return ""
	}
	if _, err := deploycfg.ResolveSecret(getenv, "SYDOM_X"); err == nil {
		t.Fatal("文件不可读应报错")
	}
}
