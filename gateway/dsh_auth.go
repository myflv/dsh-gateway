package main

// dsh 0.1.2 起 Host API 不再信任 loopback Host，改为进程启动令牌换 cookie
// （BrowserAuth：GET /?token=<launch> → Set-Cookie dsh-auth-* → 303 /）。
// 网关登录只证明到了反代；必须把 stdout 里打印的启动 URL 接进浏览器，
// 否则已登录用户看到 "dsh web authentication required; reopen the URL printed by dsh web."

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	tokenQuery      = "token"     // 与 dsh BrowserAuth TOKEN_QUERY 对齐
	dshWebLogPrefix = "dsh web: " // web-app 打印的启动行前缀
)

// launchTokenSink 从 dsh web stdout 捕获当前进程的启动令牌。
// Write 可与 log 共用 MultiWriter；token 本身是凭据，日志只报捕获成功。
type launchTokenSink struct {
	mu    sync.Mutex
	token string
	buf   []byte
}

var launchTokens = &launchTokenSink{}

func (s *launchTokenSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := string(bytes.TrimRight(s.buf[:i], "\r"))
		s.buf = s.buf[i+1:]
		if tok := parseLaunchTokenLine(line); tok != "" {
			s.token = tok
			log.Printf("已捕获 dsh web 启动令牌")
		}
	}
	return len(p), nil
}

func (s *launchTokenSink) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// 新 dsh 进程启动前清空：旧令牌对新进程无效，带着它 302 会在 /?token=stale 上 401。
func (s *launchTokenSink) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.buf = s.buf[:0]
}

// parseLaunchTokenLine 从 `dsh web: <url>` 行取出 token；LAN 附注与非 URL 行返回空。
func parseLaunchTokenLine(line string) string {
	if !strings.HasPrefix(line, dshWebLogPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(line, dshWebLogPrefix)
	if i := strings.IndexByte(rest, ' '); i != -1 {
		rest = rest[:i]
	}
	u, err := url.Parse(rest)
	if err != nil {
		return ""
	}
	return u.Query().Get(tokenQuery)
}

// 登录成功后的跳转：有令牌则带去让 dsh 完成 cookie 交换，否则退回干净首页
// （dsh 尚未打印 URL 时，后续 index 401 会再改写成令牌 URL）。
func homeWithLaunchToken() string {
	if tok := launchTokens.current(); tok != "" {
		return homePath + "?" + tokenQuery + "=" + url.QueryEscape(tok)
	}
	return homePath
}

// rewriteUnauthorizedIndex 把 dsh 对未交换 cookie 的 index 401 改成 302 令牌 URL。
// 已带 token 的请求不改写，避免错误/过期令牌造成重定向环。
func rewriteUnauthorizedIndex(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	req := resp.Request
	if req == nil {
		return false
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	path := req.URL.Path
	if path != homePath && path != "/index.html" {
		return false
	}
	if req.URL.Query().Get(tokenQuery) != "" {
		return false
	}
	token := launchTokens.current()
	if token == "" {
		return false
	}
	resp.StatusCode = http.StatusFound
	resp.Status = "302 Found"
	resp.Header.Set("Location", homePath+"?"+tokenQuery+"="+url.QueryEscape(token))
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Del("Content-Type")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Transfer-Encoding")
	resp.TransferEncoding = nil
	resp.ContentLength = 0
	resp.Header.Set("Content-Length", "0")
	if resp.Body != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	resp.Body = http.NoBody
	return true
}
