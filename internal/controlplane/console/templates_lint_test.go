package console

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// inlineJS 匹配 javascript: URI（href/action 等）；inlineHandler 匹配内联事件处理器 on*=。
// 二者皆受 CSP script-src 'self' 管辖，无 'unsafe-inline' 即被浏览器拒。
var (
	inlineJS      = regexp.MustCompile(`(?i)javascript:`)
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
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
		// javascript: URI（如 href="javascript:...") 受 script-src 管辖，无 'unsafe-inline' 即被拒——
		// 点击静默失效。改为真实链接或外链脚本挂监听。
		if inlineJS.MatchString(body) {
			t.Errorf("%s 含 javascript: URI——违反严格 CSP（script-src 'self'），请改真实链接或 /static/*.js 挂监听", e.Name())
		}
		// 内联事件处理器 on*=（onclick 等）同样受 script-src 管辖被拒。
		if inlineHandler.MatchString(body) {
			t.Errorf("%s 含内联事件处理器 on*=——违反严格 CSP（script-src 'self'），请改 /static/*.js 挂监听", e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("未扫描到任何模板——测试无效")
	}
}
