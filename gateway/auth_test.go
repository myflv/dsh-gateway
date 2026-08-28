package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// requireAuth 的 Sec-Fetch 判定：默认需会话，只有明确是资源请求才公开
// （判定语义与事故背景见 auth.go requireAuth 的 doc 注释）：
// 1) ES module 脚本（navigate+script）是资源须公开——否则浏览器把登录页 HTML
//    当 JS 执行（2026-08 事故一）；
// 2) 无 Sec-Fetch 头（非浏览器环境）须走登录页——否则未登录用户拿到壳却拿不到
//    bundle，困在 "Failed to load plugins" 死局（2026-08 事故二）；
// 3) 数据面（/api、/plugins）不受资源判定豁免——带资源头的 fetch 也必须会话，
//    否则未认证请求直接打穿认证层。
func TestRequireAuthSecFetchDest(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		mode    string
		dest    string
		wantOK  bool // false = 应 302 登录页
	}{
		{"页面导航（document）", "/", "navigate", "document", false},
		{"module 脚本（壳/bundle）", "/assets/index.js", "navigate", "script", true},
		{"样式表", "/assets/index.css", "no-cors", "style", true},
		{"经典脚本", "/assets/vendor.js", "no-cors", "script", true},
		{"fetch API（页面数据）", "/data.json", "cors", "empty", true},
		{"无 Sec-Fetch 头（curl/降级）", "/", "", "", false},
		{"iframe 导航", "/", "navigate", "iframe", false},
		{"manifest", "/manifest.webmanifest", "no-cors", "manifest", true},
		{"favicon", "/favicon.svg", "no-cors", "image", true},
		{"navigate 无 dest（旧形态导航）", "/", "navigate", "", false},
		// 数据面：资源判定不豁免，带资源头的请求也必须会话
		{"数据面 fetch API（/api）", "/api/settings", "cors", "empty", false},
		{"数据面 module 脚本（/plugins）", "/plugins/x/client.js", "navigate", "script", false},
		{"数据面无头（/plugins）", "/plugins/x/client.js", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true })
			h := requireAuth(next)
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
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
