package main

// 信任插件注入：__DSH_BOOT__ 追加 boot 条目 + 网关下发 bundle，dsh 进程零改动。

import (
	_ "embed"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

//go:embed trust-plugin/client.js
var trustPluginBundle []byte

const (
	trustPluginID   = "dsh-gateway-trust"
	trustPluginRev  = "1"
	trustPluginPath = pluginsPrefix + "dsh-gateway-trust/client.js" // 网关自留命名空间，同 auth.go
)

// 与 host 的 graphRow 产物同构（parseBootManifest 校验的 wire 契约）
type bootEntry struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Rev         string   `json:"rev"`
	Inject      []string `json:"inject"`
	Immediately bool     `json:"immediately"`
}

var trustBootEntryJSON = func() []byte {
	out, err := json.Marshal(bootEntry{ID: trustPluginID, URL: trustPluginPath + "?rev=" + trustPluginRev, Rev: trustPluginRev, Inject: []string{"connection"}, Immediately: true})
	if err != nil {
		panic(err) // 纯字符串/布尔 struct，Marshal 不会失败
	}
	return out
}()

const bootMarker = "window.__DSH_BOOT__ = "

var (
	bootMarkerBytes = []byte(bootMarker)
	scriptClose     = []byte("</script>")
)

// 追加信任插件条目到 __DSH_BOOT__；JSON 不合法时返回错误，让代理 502（版本漂移响亮失败）
func injectBootManifestEntry(html []byte) ([]byte, bool, error) {
	i := bytes.Index(html, bootMarkerBytes)
	if i == -1 {
		return html, false, nil
	}
	start := i + len(bootMarkerBytes)
	relEnd := bytes.Index(html[start:], scriptClose)
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
	// 防重复注入（页面缓存/重放场景）
	for _, raw := range graph.Entries {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &id) == nil && id.ID == trustPluginID {
			return html, false, nil
		}
	}

	graph.Entries = append(graph.Entries, trustBootEntryJSON)
	out, err := json.Marshal(graph) // SetEscapeHTML 默认开启，转义与 host 注入一致
	if err != nil {
		return nil, false, fmt.Errorf("dsh-gateway: 重写 __DSH_BOOT__ 失败: %w", err)
	}

	patched := make([]byte, 0, len(html)+len(out)-relEnd)
	patched = append(patched, html[:start]...)
	patched = append(patched, out...)
	patched = append(patched, html[end:]...)
	return patched, true, nil
}

// 下发插件 bundle（immutable 缓存依赖 rev 寻址，改 bundle 须同步 bump rev）
func serveTrustPlugin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(trustPluginBundle)
}
