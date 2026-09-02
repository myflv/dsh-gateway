package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// proxyHandler 转发到 dsh web（容器内 127.0.0.1:3080）；标准库 ReverseProxy 自带 WebSocket 支持
func proxyHandler(backend *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(backend)
	director := proxy.Director
	originHeader := backend.Scheme + "://" + backend.Host // 预计算，避免每请求拼接
	proxy.Director = func(req *http.Request) {
		director(req)
		// dsh web 的 /api browser-trust fence 只信任 loopback 主机（或
		// --trusted-host 声明，安装版 rc.6 该参数实测未生效）。此处改写
		// 不是绕过活防御：容器内 3080 不对外暴露，DNS-rebinding 的防御
		// 职能已由网关的会话认证（HttpOnly + SameSite=Strict）承担，
		// fence 无实际防御对象。改写范围仅此一处，其余保持真实 Host
		req.Host = backend.Host
		if origin := req.Header.Get("Origin"); origin != "" {
			req.Header.Set("Origin", originHeader)
		}
		// 0.1.2 起 webserver 对 HTML 开了 gzip。只 Del 不够：Go Transport 在
		// 头缺失时会自己补 gzip 并透明解压，上游仍压缩一轮。identity 让上游
		// 直接出明文，ModifyResponse 才能注入 boot 条目。
		req.Header.Set("Accept-Encoding", "identity")
	}
	// 壳 HTML 注入信任插件 boot 条目；index 401 改写成启动令牌交换
	proxy.ModifyResponse = func(resp *http.Response) error {
		if rewriteUnauthorizedIndex(resp) {
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		patched, bootFound, changed, err := injectBootManifestEntry(body)
		if err != nil {
			log.Printf("信任插件注入失败: %v", err) // 版本漂移响亮失败，不静默带空白配置跑
			return err
		}
		if changed {
			resp.Body = io.NopCloser(bytes.NewReader(patched))
			resp.Header.Set("Content-Length", strconv.Itoa(len(patched)))
			return nil
		}
		// 无 boot marker 的 HTML：静默透传会让信任插件"安静地失效"（2026-08 事故：
		// rc.2 注入行形态从 window.__DSH_BOOT__ 漂移到 globalThis[...]）。
		// 这里不打日志的话，版本漂移只有用户打开页面才知道。
		if !bootFound {
			log.Printf("警告：HTML 页面未找到 boot marker，信任插件未注入: %s", resp.Request.URL.Path)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body)) // 无标记页面：仅恢复被读的 Body，header 不动
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "上游不可用（dsh web 可能没起来）", http.StatusBadGateway)
	}
	return proxy
}
