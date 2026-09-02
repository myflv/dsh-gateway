package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestParseLaunchTokenLine(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"dsh web: http://127.0.0.1:3080/?token=abc123", "abc123"},
		{"dsh web: http://127.0.0.1:3080/?token=abc123 (LAN: http://10.0.0.2:3080/?token=abc123)", "abc123"},
		{"dsh web: opening the default browser; pass --no-open to disable", ""},
		{"[dsh-gateway] listening on 0.0.0.0:8080", ""},
		{"dsh web: not-a-url", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseLaunchTokenLine(c.line); got != c.want {
			t.Errorf("parseLaunchTokenLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestLaunchTokenSink(t *testing.T) {
	s := &launchTokenSink{}
	if s.current() != "" {
		t.Fatal("空 sink 应无令牌")
	}
	// 跨 Write 分片：启动行可能被 node 拆成多次 write
	if _, err := s.Write([]byte("noise\ndsh web: http://127.0.0.1:3080/?token=ab")); err != nil {
		t.Fatal(err)
	}
	if s.current() != "" {
		t.Fatal("不完整行不应产出令牌")
	}
	if _, err := s.Write([]byte("c-token\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := s.current(); got != "abc-token" {
		t.Fatalf("分片捕获失败: %q", got)
	}
	s.reset()
	if s.current() != "" {
		t.Fatal("reset 后令牌应清空")
	}
}

func TestHomeWithLaunchToken(t *testing.T) {
	launchTokens.reset()
	t.Cleanup(launchTokens.reset)
	if got := homeWithLaunchToken(); got != homePath {
		t.Fatalf("无令牌应回首页: %q", got)
	}
	// 启动令牌是 base64url（-/_），不含 +；QueryEscape 仍须 round-trip
	const tok = "launch-tok_ABC"
	if _, err := launchTokens.Write([]byte("dsh web: http://127.0.0.1:3080/?token=" + tok + "\n")); err != nil {
		t.Fatal(err)
	}
	got := homeWithLaunchToken()
	if !strings.HasPrefix(got, homePath+"?"+tokenQuery+"=") {
		t.Fatalf("有令牌应带 query: %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get(tokenQuery) != tok {
		t.Fatalf("token 转义/还原漂移: %q", u.Query().Get(tokenQuery))
	}
}

func TestRewriteUnauthorizedIndex(t *testing.T) {
	launchTokens.reset()
	t.Cleanup(launchTokens.reset)
	if _, err := launchTokens.Write([]byte("dsh web: http://127.0.0.1:3080/?token=tok\n")); err != nil {
		t.Fatal(err)
	}

	mk := func(method, rawURL string, status int) *http.Response {
		req := httptest.NewRequest(method, rawURL, nil)
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("dsh web authentication required\n")),
			Request:    req,
		}
	}

	t.Run("index 401 改写", func(t *testing.T) {
		resp := mk(http.MethodGet, "/", http.StatusUnauthorized)
		if !rewriteUnauthorizedIndex(resp) {
			t.Fatal("应改写")
		}
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/?token=tok" {
			t.Fatalf("Location = %q", loc)
		}
		body, _ := io.ReadAll(resp.Body)
		if len(body) != 0 {
			t.Fatalf("改写后 body 应清空: %q", body)
		}
	})
	t.Run("已带 token 不改写（防环）", func(t *testing.T) {
		resp := mk(http.MethodGet, "/?token=stale", http.StatusUnauthorized)
		if rewriteUnauthorizedIndex(resp) {
			t.Fatal("带 token 的 401 再改写会成环")
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("非 index 401 不改写", func(t *testing.T) {
		resp := mk(http.MethodGet, "/api/remote.mux", http.StatusUnauthorized)
		if rewriteUnauthorizedIndex(resp) {
			t.Fatal("/api 401 不应改写")
		}
	})
	t.Run("200 不改写", func(t *testing.T) {
		resp := mk(http.MethodGet, "/", http.StatusOK)
		if rewriteUnauthorizedIndex(resp) {
			t.Fatal("200 不应改写")
		}
	})
}

func TestRewriteUnauthorizedIndexWithoutToken(t *testing.T) {
	launchTokens.reset()
	t.Cleanup(launchTokens.reset)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    req,
	}
	if rewriteUnauthorizedIndex(resp) {
		t.Fatal("尚未捕获令牌时不应改写，避免跳到空 token")
	}
}

func TestProxyStripsAcceptEncoding(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	proxyHandler(u).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got != "identity" {
		t.Fatalf("Accept-Encoding 应为 identity（禁止上游 gzip），实际 %q", got)
	}
}

func TestProxyRewritesIndex401(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "dsh web authentication required; reopen the URL printed by dsh web.\n")
	}))
	t.Cleanup(up.Close)
	launchTokens.reset()
	t.Cleanup(launchTokens.reset)
	if _, err := launchTokens.Write([]byte("dsh web: http://127.0.0.1:3080/?token=from-stdout\n")); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	proxyHandler(u).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=from-stdout" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestLoginRedirectsWithLaunchToken(t *testing.T) {
	launchTokens.reset()
	t.Cleanup(func() {
		launchTokens.reset()
		authUser, authHash = "", ""
	})
	if _, err := launchTokens.Write([]byte("dsh web: http://127.0.0.1:3080/?token=after-login\n")); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	authUser, authHash = "admin", string(hash)

	csrf := newCSRF()
	form := url.Values{
		"username": {"admin"},
		"password": {"secret"},
		"csrf":     {csrf},
	}
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleLogin(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=after-login" {
		t.Fatalf("登录成功应带启动令牌跳转，实际 Location=%q", loc)
	}
}
