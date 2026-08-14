package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "上游不可用（dsh web 可能没起来）", http.StatusBadGateway)
	}
	return proxy
}
