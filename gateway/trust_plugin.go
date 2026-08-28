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
	// bundle 端点：entry 与 batch 共用同一 URL（改 rev/path 只动这一处）
	trustPluginURL = trustPluginPath + "?rev=" + trustPluginRev
)

// 与 host 的 graphRow 产物同构（parseBootManifest 校验的 wire 契约）
type bootEntry struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Rev         string   `json:"rev"`
	Inject      []string `json:"inject"`
	Immediately bool     `json:"immediately"`
}

// 0.1.2 起 graph 增加 batches 数组：每个 entry 必须归属恰好一个 initial-load
// batch（combo 脚本调度描述），否则 parseBootManifest 抛
// "belongs to no initial-load batch"，整个应用无法 boot。
// 本插件是网关注入的伪 row，host 的 compose() 不会为它生成 batch，须一并注入。
type bootBatch struct {
	Phase   string   `json:"phase"`
	URL     string   `json:"url"`
	Rev     string   `json:"rev"`
	Entries []string `json:"entries"`
}

// 纯字符串/布尔 struct，Marshal 不会失败；panic 只兜底理论分支
func mustJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

var (
	trustBootEntryJSON = mustJSON(bootEntry{ID: trustPluginID, URL: trustPluginURL, Rev: trustPluginRev, Inject: []string{"connection"}, Immediately: true})
	// 与既有应用插件同走 application 调度（预加载，模块系统按需执行），
	// 不用 bootstrap（那会在壳启动前以阻塞脚本执行，行为路径与旧版不同）
	trustBootBatchJSON = mustJSON(bootBatch{Phase: "application", URL: trustPluginURL, Rev: trustPluginRev, Entries: []string{trustPluginID}})
)

// host 注入行的实际形态随版本变化：rc.8 是 window.__DSH_BOOT__ = ，
// rc.2 起改为 globalThis["__DSH_BOOT__"] =（带引号下标）。只守一个形态会
// 匹配失败并静默透传（无 marker 页面本就不改），插件"安静地失效"——见 2026-08 事故。
// 第三个 window["..."] 是推测形态（未观察到的保险），防 host 再换访问器写法。
// 列表按出现顺序取最先匹配者；JSON 起点 = marker 之后，结束仍为 </script>
var bootMarkers = [][]byte{
	[]byte(`window.__DSH_BOOT__ = `),
	[]byte(`globalThis["__DSH_BOOT__"] = `),
	[]byte(`window["__DSH_BOOT__"] = `),
}

var scriptClose = []byte("</script>")

// 返回 boot 注入行中 JSON 的起点偏移；找不到任何已知形态返回 -1
func findBootMarkerStart(html []byte) int {
	for _, marker := range bootMarkers {
		if i := bytes.Index(html, marker); i != -1 {
			return i + len(marker)
		}
	}
	return -1
}

// 追加信任插件条目到 __DSH_BOOT__；JSON 不合法时返回错误，让代理 502（版本漂移响亮失败）。
// bootFound 区分"页面没有 boot marker"（形态漂移，调用点应警告）与幂等跳过（良性）
func injectBootManifestEntry(html []byte) (patched []byte, bootFound, changed bool, err error) {
	start := findBootMarkerStart(html)
	if start == -1 {
		return html, false, false, nil
	}
	relEnd := bytes.Index(html[start:], scriptClose)
	if relEnd == -1 {
		return nil, true, false, fmt.Errorf("dsh-gateway: boot marker 后未找到 </script>，页面结构异常")
	}
	end := start + relEnd

	var graph struct {
		Rev     string            `json:"rev"`
		Entries []json.RawMessage `json:"entries"`
		Batches []json.RawMessage `json:"batches,omitempty"` // 新版必填；旧版 host 无此键，omitempty 保持图形态不漂移
	}
	if err := json.Unmarshal(html[start:end], &graph); err != nil {
		return nil, true, false, fmt.Errorf("dsh-gateway: 解析 __DSH_BOOT__ 失败（dsh 版本漂移？）: %w", err)
	}
	// 防重复注入（页面缓存/重放场景）
	for _, raw := range graph.Entries {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &id) == nil && id.ID == trustPluginID {
			return html, true, false, nil
		}
	}

	graph.Entries = append(graph.Entries, trustBootEntryJSON)
	graph.Batches = append(graph.Batches, trustBootBatchJSON) // 与 entry 同进同出，保持新旧 host 一致可解析
	out, err := json.Marshal(graph) // SetEscapeHTML 默认开启，转义与 host 注入一致
	if err != nil {
		return nil, true, false, fmt.Errorf("dsh-gateway: 重写 __DSH_BOOT__ 失败: %w", err)
	}

	patched = make([]byte, 0, len(html)+len(out)-relEnd)
	patched = append(patched, html[:start]...)
	patched = append(patched, out...)
	patched = append(patched, html[end:]...)
	return patched, true, true, nil
}

// 下发插件 bundle（immutable 缓存依赖 rev 寻址，改 bundle 须同步 bump rev）
func serveTrustPlugin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(trustPluginBundle)
}
