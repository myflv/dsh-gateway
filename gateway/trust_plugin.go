package main

// 信任插件注入：SPA 壳 HTML 里给 window.__DSH_BOOT__ 追加一条 boot 条目，并
// 由网关直接下发该插件的 client.js。dsh 进程零改动、零补丁，升级无感。
//
// 机制（对应 dsh 源码 packages/client/modules）：
//   host 把启动清单以 <script>window.__DSH_BOOT__ = <json></script> 注入
//   <head> 首位（injectBootManifest，json 内 < 转义为 <）；浏览器按
//   entries 逐条拉取 /plugins/<id>/client.js 并交给 cordis loader 创建条目。
//   条目顺序无语义（fiber inject waiting 决定激活顺序），因此新条目直接
//   追加到末尾即可；插件自身声明 inject: ['connection'] 保证在 connection
//   提供之后激活。

import (
	_ "embed"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

//go:embed trust-plugin/client.js
var trustPluginBundle []byte // 浏览器端插件本体，嵌入二进制随镜像分发

const (
	trustPluginID   = "dsh-gateway-trust"
	trustPluginRev  = "1"
	trustPluginPath = "/plugins/dsh-gateway-trust/client.js"
)

// trustBootEntryJSON 追加进 __DSH_BOOT__.entries 的条目，与 host 的
// graphRow 产物同构（id/url/rev + inject + immediately）。
var trustBootEntryJSON = []byte(
	`{"id":"dsh-gateway-trust","url":"/plugins/dsh-gateway-trust/client.js?rev=1","rev":"1","inject":["connection"],"immediately":true}`,
)

const bootMarker = "window.__DSH_BOOT__ = "

// injectBootManifestEntry 在壳 HTML 的 __DSH_BOOT__ 清单里追加信任插件条目。
// 返回 (改写后的 html, 是否发生改写, 错误)。无清单标记的页面原样返回。
// 清单存在但 JSON 不合法：返回错误让代理 502——dsh 版本漂移必须响亮失败，
// 而不是静默带着空白配置跑。
func injectBootManifestEntry(html []byte) ([]byte, bool, error) {
	i := bytes.Index(html, []byte(bootMarker))
	if i == -1 {
		return html, false, nil
	}
	start := i + len(bootMarker)
	relEnd := bytes.Index(html[start:], []byte("</script>"))
	if relEnd == -1 {
		return nil, false, fmt.Errorf("dsh-gateway: %q 后未找到 </script>，页面结构异常", bootMarker)
	}
	end := start + relEnd

	var graph struct {
		Rev     string            `json:"rev"`
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(html[start:end], &graph); err != nil {
		return nil, false, fmt.Errorf("dsh-gateway: 解析 __DSH_BOOT__ 失败（dsh 版本漂移？）: %w", err)
	}
	// 防重复注入（页面缓存/重放场景）：已含本条目则原样返回
	for _, raw := range graph.Entries {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &id) == nil && id.ID == trustPluginID {
			return html, false, nil
		}
	}

	graph.Entries = append(graph.Entries, trustBootEntryJSON)
	out, err := json.Marshal(graph) // SetEscapeHTML 默认开启，< 会转义为 <，与 host 注入一致
	if err != nil {
		return nil, false, fmt.Errorf("dsh-gateway: 重写 __DSH_BOOT__ 失败: %w", err)
	}

	patched := make([]byte, 0, len(html)+len(out)-relEnd)
	patched = append(patched, html[:start]...)
	patched = append(patched, out...)
	patched = append(patched, html[end:]...)
	return patched, true, nil
}

// serveTrustPlugin 下发信任插件 bundle（浏览器端 ESM）。Content-Type 必须是
// JS MIME——模块系统用动态 import 拉取，浏览器要求 JS MIME 才按模块执行。
func serveTrustPlugin(w http.ResponseWriter, r *http.Request) {
	log.Printf("serve trust plugin bundle: %s", r.URL.Path)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(trustPluginBundle)
}
