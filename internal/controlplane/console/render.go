package console

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// pageSet：页文件名 → 该页独立模板集（layout + 所有 partial + 该页）。
type pageSet map[string]*template.Template

// mustTemplates 自动发现 templates/ 下的页文件，为每页解析独立模板集：
//
//	layout.html 为基；_*.html 为共享 partial（每页都带）；其余 X.html 为页，键 "X.html"。
//
// 每页独立解析 → 各页 define "content"/"title" 互不覆盖。解析失败 panic（启动期硬错误）。
func mustTemplates() pageSet {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		panic(err)
	}
	var partials, pages []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "layout.html":
			continue
		case strings.HasPrefix(name, "_"):
			partials = append(partials, "templates/"+name)
		default:
			pages = append(pages, name)
		}
	}
	funcs := template.FuncMap{"sortHref": sortHref}
	set := pageSet{}
	for _, p := range pages {
		files := append([]string{"templates/layout.html"}, partials...)
		files = append(files, "templates/"+p)
		set[p] = template.Must(template.New("layout.html").Funcs(funcs).ParseFS(templatesFS, files...))
	}
	return set
}

// renderPage 用指定页的独立模板集，以 layout.html 为入口渲染。
// 先渲到 buffer，成功才写 header+body（避免半截页）。
func (h *Handler) renderPage(w http.ResponseWriter, r *http.Request, page string, statusCode int, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	tmpl, ok := h.templates[page]
	if !ok {
		h.logger.Error("console template missing", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		h.logger.Error("console render", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = buf.WriteTo(w)
}
