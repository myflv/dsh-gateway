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
	}
	// 信任插件注入：只碰 200 + text/html + 带 __DSH_BOOT__ 标记的壳页面，
	// 其余响应原样透传（dsh 不压缩静态响应，无需解压处理）
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
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
		patched, changed, err := injectBootManifestEntry(body)
		if err != nil {
			// 清单在但解析失败 = dsh 版本漂移，必须响亮失败而不是静默空白
			log.Printf("信任插件注入失败: %v", err)
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(patched))
		resp.ContentLength = int64(len(patched))
		resp.Header.Set("Content-Length", strconv.Itoa(len(patched)))
		if changed {
			log.Printf("已注入信任插件 boot 条目（%s）", trustPluginID)
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "上游不可用（dsh web 可能没起来）", http.StatusBadGateway)
	}
	return proxy
}
