package console

import (
	"io/fs"
	"strings"
	"testing"
)

// TestTemplates_NoInlineStyle 守住严格 CSP（SH-6）：Console 模板不得含内联 style= 属性。
// 内联 style= 会被 style-src 'self'（无 unsafe-inline）拒绝——有人再加即在此 FAIL，
// 逼其外提为 CSS 类。同理不得含内联 <script> 块（script-src 'self' 同样拒之）。
// 用 embed 的 templatesFS 作单一真相源（与运行时同一份文件）。
func TestTemplates_NoInlineStyle(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		checked++
		b, err := fs.ReadFile(templatesFS, "templates/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		body := string(b)
		if strings.Contains(body, "style=\"") {
			t.Errorf("%s 含内联 style= 属性——违反严格 CSP（style-src 'self'），请外提为 CSS 工具类", e.Name())
		}
		// 内联 <script>…</script>（有内容体）同样违反 script-src 'self'；外链 <script src> 允许。
		if strings.Contains(body, "<script>") {
			t.Errorf("%s 含内联 <script> 块——违反严格 CSP（script-src 'self'），请外提为 /static/*.js", e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("未扫描到任何模板——测试无效")
	}
}
