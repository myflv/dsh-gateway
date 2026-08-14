package main

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName  = "dsh_session"    // 登录会话 cookie
	sessionTTL  = 12 * time.Hour   // 会话有效期
	csrfTTL     = 10 * time.Minute // 登录表单 token 有效期
	maxFailures = 5                // 连续失败次数
	lockTime    = 5 * time.Minute  // 锁定时长

	// 认证入口固定在网关自留命名空间 /plugins/dsh-gateway-auth/ 下：dsh web
	// 只注册 /plugins、/api 前缀路由，SPA catch-all 不可能吞掉精确路径；真实
	// 插件 id 是 npm 包名，dsh-gateway-* 前缀不会撞名（trust 插件 bundle 同在
	// 此命名空间，见 trust_plugin.go）。旧 /login、/logout 保留 302 跳转兜底
	// 旧标签页（见 main.go）
	loginPath  = "/plugins/dsh-gateway-auth/login"
	logoutPath = "/plugins/dsh-gateway-auth/logout"
	homePath   = "/" // 登录成功跳转目标（也是反代的 catch-all 根）

	// dsh 的数据面前缀：认证只保护这两个（见 main 的路由注册），页面壳与静态资源公开
	apiPrefix     = "/api/"
	pluginsPrefix = "/plugins/"
)

var (
	authUser string
	authHash string
)

// ---------- session（内存存储，重启即失效，个人工具够用） ----------

var (
	sessMu   sync.RWMutex // 读多写少：validSession 走 RLock
	sessions = map[string]time.Time{}
)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newSession() string {
	token := randomHex(32)

	sessMu.Lock()
	defer sessMu.Unlock()
	now := time.Now()
	for k, expires := range sessions { // 顺手清一波过期 session
		if now.After(expires) {
			delete(sessions, k)
		}
	}
	sessions[token] = now.Add(sessionTTL)
	return token
}

func validSession(token string) bool {
	if token == "" {
		return false
	}
	sessMu.RLock()
	defer sessMu.RUnlock()
	expires, ok := sessions[token]
	if !ok {
		return false
	}
	// 过期条目不在此删除（RLock 下不可写），由 newSession 的写路径清理
	return time.Now().Before(expires)
}

func deleteSession(token string) {
	sessMu.Lock()
	defer sessMu.Unlock()
	delete(sessions, token)
}

// ---------- CSRF（登录表单一次性 token） ----------

var (
	csrfMu     sync.Mutex
	csrfTokens = map[string]time.Time{}
)

func newCSRF() string {
	token := randomHex(16)

	csrfMu.Lock()
	defer csrfMu.Unlock()
	now := time.Now()
	for k, t := range csrfTokens {
		if now.After(t) {
			delete(csrfTokens, k)
		}
	}
	csrfTokens[token] = now.Add(csrfTTL)
	return token
}

// 校验（不消费）：失败尝试不消耗 token，输错可直接重试，多标签页互不干扰
func checkCSRF(token string) bool {
	csrfMu.Lock()
	defer csrfMu.Unlock()
	t, ok := csrfTokens[token]
	if !ok {
		return false
	}
	return time.Now().Before(t)
}

// 登录成功后消费：一次性 token 只允许一次成功
func consumeCSRF(token string) {
	csrfMu.Lock()
	defer csrfMu.Unlock()
	delete(csrfTokens, token)
}

// ---------- 登录失败限速 ----------

type attempt struct {
	count int
	until time.Time
}

var failures sync.Map // ip -> *attempt

// nginx 反代时从 X-Forwarded-For 取第一个，否则用 RemoteAddr
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limited 只读检查：锁定期内返回 true；不能删计数，否则永远触发不了锁定
func limited(ip string) bool {
	if v, ok := failures.Load(ip); ok {
		a := v.(*attempt)
		if time.Now().Before(a.until) {
			return true
		}
	}
	return false
}

func recordFailure(ip string) {
	v, _ := failures.LoadOrStore(ip, &attempt{})
	a := v.(*attempt)
	if !a.until.IsZero() && time.Now().After(a.until) {
		// 上次锁定已过期，重新计数
		a.until = time.Time{}
		a.count = 0
	}
	a.count++
	if a.count >= maxFailures {
		a.until = time.Now().Add(lockTime)
		a.count = 0
	}
}

// ---------- handlers ----------

// 渲染登录页：替换模板占位符（token / 错误信息 / 用户名回填）
func renderLogin(w http.ResponseWriter, errMsg, username string) {
	token := newCSRF()
	page := strings.NewReplacer(
		"{{CSRF}}", token,
		"{{ERR}}", html.EscapeString(errMsg),
		"{{USER}}", html.EscapeString(username),
	).Replace(loginTemplate)
	// 禁缓存：防止浏览器缓存带旧 token 的页面
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, page)
}

// 统一的重定向入口：失败带 err/u 回登录页，普通跳转传空串
func redirectLogin(w http.ResponseWriter, r *http.Request, errMsg, username string) {
	loc := loginPath
	if errMsg != "" {
		loc += "?err=" + url.QueryEscape(errMsg) + "&u=" + url.QueryEscape(username)
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 错误信息由 POST 失败后 302 带回（PRG 模式）
		renderLogin(w, r.URL.Query().Get("err"), r.URL.Query().Get("u"))

	case http.MethodPost:
		ip := clientIP(r)
		loginErr := func(msg string) {
			redirectLogin(w, r, msg, r.FormValue("username"))
		}
		if limited(ip) {
			loginErr("尝试次数过多，请 5 分钟后再试")
			return
		}
		if err := r.ParseForm(); err != nil {
			loginErr("参数错误")
			return
		}
		if !checkCSRF(r.FormValue("csrf")) {
			loginErr("页面停留过久已失效，请刷新后重试")
			return
		}
		if r.FormValue("username") != authUser ||
			bcrypt.CompareHashAndPassword([]byte(authHash), []byte(r.FormValue("password"))) != nil {
			recordFailure(ip)
			loginErr("用户名或密码错误")
			return
		}

		consumeCSRF(r.FormValue("csrf")) // 成功后消费一次性 token
		failures.Delete(ip)
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    newSession(),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			// 直连 http（如容器裸端口）时自动关闭 Secure，否则浏览器不存 cookie；
			// 经过 nginx HTTPS（X-Forwarded-Proto: https）时保持 Secure
			Secure: !*insecureCookie && (r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")),
			MaxAge: int(sessionTTL.Seconds()),
		})
		http.Redirect(w, r, homePath, http.StatusFound)
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		deleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	redirectLogin(w, r, "", "")
}

// 认证中间件：数据面（/api、/plugins）与页面导航请求需会话，否则 302 登录页。
// 浏览器资源请求（manifest/favicon/script 等，Sec-Fetch-Mode 非 navigate）公开——它们不带 cookie 也能加载，无需维护资源名单
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dataPlane := strings.HasPrefix(r.URL.Path, apiPrefix) || strings.HasPrefix(r.URL.Path, pluginsPrefix)
		if dataPlane || r.Header.Get("Sec-Fetch-Mode") == "navigate" {
			if c, err := r.Cookie(cookieName); err == nil && validSession(c.Value) {
				next.ServeHTTP(w, r)
				return
			}
			redirectLogin(w, r, "", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
