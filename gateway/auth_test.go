package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// requireAuth 的 Sec-Fetch 判定：ES module 脚本（Sec-Fetch-Mode=navigate +
// Sec-Fetch-Dest=script）是资源，须公开放行；document 导航须会话。
// 2026-08 事故：无会话时壳资源被误拦，浏览器把登录页 HTML 当 JS 执行。
func TestRequireAuthSecFetchDest(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		dest    string
		wantOK  bool // false = 应 302 登录页
	}{
		{"页面导航（document）", "navigate", "document", false},
		{"module 脚本（壳/bundle）", "navigate", "script", true},
		{"样式表", "no-cors", "style", true},
		{"经典脚本", "no-cors", "script", true},
		{"fetch API", "cors", "empty", true},
		{"无 Sec-Fetch 头（curl）", "", "", true},
		{"iframe 导航", "navigate", "iframe", false},
		{"manifest", "no-cors", "manifest", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
			h := requireAuth(next)
			req := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
			if c.mode != "" {
				req.Header.Set("Sec-Fetch-Mode", c.mode)
			}
			if c.dest != "" {
				req.Header.Set("Sec-Fetch-Dest", c.dest)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if c.wantOK && !hit {
				t.Fatalf("资源请求被误拦: %s/%s", c.mode, c.dest)
			}
			if !c.wantOK && hit {
				t.Fatalf("导航请求未被拦: %s/%s", c.mode, c.dest)
			}
			if !c.wantOK && rec.Code != http.StatusFound {
				t.Fatalf("导航请求应 302，实际 %d", rec.Code)
			}
		})
	}
}
