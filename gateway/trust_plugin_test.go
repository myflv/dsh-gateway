package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 模拟 host 的真实产物：rc.2 起注入行形态为 globalThis["__DSH_BOOT__"]（< 转义为 <），
// 0.1.2 起 graph 带 batches
func sampleShellHTML() []byte {
	return []byte(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<script>globalThis["__DSH_BOOT__"] = {"rev":"rev-abc","entries":[{"id":"@deepseek-ai/dsh-client-connection","url":"/plugins/@deepseek-ai/dsh-client-connection/client.js?rev=r1","rev":"r1","immediately":true},{"id":"@deepseek-ai/dsh-client-ui-settings","url":"/plugins/@deepseek-ai/dsh-client-ui-settings/client.js?rev=r2","rev":"r2","inject":[],"immediately":true}],"batches":[{"phase":"application","url":"/plugins/??@deepseek-ai/dsh-client-connection/client.js,@deepseek-ai/dsh-client-ui-settings/client.js&rev=r1","rev":"r1","entries":["@deepseek-ai/dsh-client-connection","@deepseek-ai/dsh-client-ui-settings"]}]}</script>
<link rel="stylesheet" href="/assets/index.css">
</head>
<body><div id="root"></div><script type="module" src="/assets/index.js"></script></body>
</html>`)
}

// 旧版（rc.8 及之前）的注入行形态与无 batches 的图
func legacyShellHTML() []byte {
	return []byte(`<script>window.__DSH_BOOT__ = {"rev":"rev-legacy","entries":[{"id":"@deepseek-ai/dsh-client-connection","url":"/plugins/@deepseek-ai/dsh-client-connection/client.js?rev=r1","rev":"r1","immediately":true}]}</script>`)
}

// 改写后 __DSH_BOOT__ 的类型化视图（与生产 wire 契约同构，避免 raw-JSON 字符串体操）
type bootGraphView struct {
	Rev     string      `json:"rev"`
	Entries []bootEntry `json:"entries"`
	Batches []bootBatch `json:"batches"`
}

func extractBootGraph(t *testing.T, html []byte) bootGraphView {
	t.Helper()
	start := findBootMarkerStart(html)
	if start == -1 {
		t.Fatalf("改写后 html 丢失 boot marker")
	}
	end := bytes.Index(html[start:], scriptClose)
	if end == -1 {
		t.Fatal("改写后 html 丢失 </script>")
	}
	var graph bootGraphView
	if err := json.Unmarshal(html[start:start+end], &graph); err != nil {
		t.Fatalf("改写后 __DSH_BOOT__ 不是合法 JSON: %v", err)
	}
	return graph
}

func TestInjectBootManifestEntry(t *testing.T) {
	html := sampleShellHTML()
	patched, bootFound, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("注入报错: %v", err)
	}
	if !bootFound || !changed {
		t.Fatal("带清单的页面应识别 marker 并改写")
	}
	if len(patched) <= len(html) {
		t.Fatal("改写后体积应大于原页面")
	}

	graph := extractBootGraph(t, patched)
	if graph.Rev != "rev-abc" {
		t.Fatalf("rev 被破坏: %s", graph.Rev)
	}
	if len(graph.Entries) != 3 {
		t.Fatalf("应有 3 个条目（原 2 + 注入 1），实际 %d", len(graph.Entries))
	}
	if graph.Entries[0].ID != "@deepseek-ai/dsh-client-connection" || graph.Entries[1].ID != "@deepseek-ai/dsh-client-ui-settings" {
		t.Fatalf("原条目被破坏: %+v", graph.Entries)
	}
	wantEntry := bootEntry{ID: trustPluginID, URL: trustPluginURL, Rev: trustPluginRev, Inject: []string{"connection"}, Immediately: true}
	if !reflect.DeepEqual(graph.Entries[2], wantEntry) {
		t.Fatalf("注入条目 wire 形态漂移: %+v", graph.Entries[2])
	}
	// 0.1.2 起每个 entry 必须归属一个 batch（parseBootManifest 校验），
	// 原 host batch 须原样保留，注入的信任插件条目须获得自己的 batch
	if len(graph.Batches) != 2 {
		t.Fatalf("应有 2 个 batch（原 1 + 注入 1），实际 %d", len(graph.Batches))
	}
	if graph.Batches[0].Phase != "application" || !strings.Contains(graph.Batches[0].URL, "@deepseek-ai/dsh-client-connection") {
		t.Fatalf("原 host batch 被破坏: %+v", graph.Batches[0])
	}
	wantBatch := bootBatch{Phase: "application", URL: trustPluginURL, Rev: trustPluginRev, Entries: []string{trustPluginID}}
	if !reflect.DeepEqual(graph.Batches[1], wantBatch) {
		t.Fatalf("注入 batch wire 形态漂移: %+v", graph.Batches[1])
	}
	// 页面其余结构原样保留
	for _, marker := range []string{"<link rel=\"stylesheet\"", "<div id=\"root\">", "<!doctype html>", "<head>"} {
		if !bytes.Contains(patched, []byte(marker)) {
			t.Fatalf("页面结构被破坏，缺少 %q", marker)
		}
	}
}

func TestInjectNoMarkerPassesThrough(t *testing.T) {
	html := []byte("<html><body>普通页面，没有清单</body></html>")
	patched, bootFound, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("无清单页面不应报错: %v", err)
	}
	if bootFound {
		t.Fatal("无 marker 页面不应声称找到 marker")
	}
	if changed {
		t.Fatal("无清单页面不应改写")
	}
	if !bytes.Equal(patched, html) {
		t.Fatal("无清单页面应原样返回")
	}
}

func TestInjectIdempotent(t *testing.T) {
	html, _, _, err := injectBootManifestEntry(sampleShellHTML())
	if err != nil {
		t.Fatalf("首次注入失败: %v", err)
	}
	patched, bootFound, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("二次注入报错: %v", err)
	}
	if !bootFound {
		t.Fatal("二次注入应识别 marker")
	}
	if changed {
		t.Fatal("已注入的页面不应重复注入")
	}
	graph := extractBootGraph(t, patched)
	if len(graph.Entries) != 3 {
		t.Fatalf("重复注入导致条目数异常: %d", len(graph.Entries))
	}
	if len(graph.Batches) != 2 {
		t.Fatalf("重复注入导致 batch 数异常: %d", len(graph.Batches))
	}
}

// 旧版 dsh（rc.8 及之前）的注入行形态 window.__DSH_BOOT__ 且图无 batches：
// 注入照常工作；我们的 batch 对旧 host 无害（其解析与加载都忽略该字段）
func TestInjectLegacyMarkerAndGraph(t *testing.T) {
	patched, bootFound, changed, err := injectBootManifestEntry(legacyShellHTML())
	if err != nil {
		t.Fatalf("旧版图注入报错: %v", err)
	}
	if !bootFound || !changed {
		t.Fatal("旧版图应识别 marker 并改写")
	}
	graph := extractBootGraph(t, patched)
	if graph.Rev != "rev-legacy" {
		t.Fatalf("旧版 rev 被破坏: %s", graph.Rev)
	}
	if len(graph.Entries) != 2 {
		t.Fatalf("旧版图注入后应有 2 个条目: %d", len(graph.Entries))
	}
	if len(graph.Batches) != 1 || graph.Batches[0].URL != trustPluginURL {
		t.Fatalf("旧版图应恰好注入信任插件自己的 batch: %+v", graph.Batches)
	}
}

func TestInjectMalformedFailsLoud(t *testing.T) {
	html := []byte("<head><script>window.__DSH_BOOT__ = {broken-json}</script></head>")
	if _, _, _, err := injectBootManifestEntry(html); err == nil {
		t.Fatal("畸形清单必须报错（响亮失败，不静默透传）")
	}
}

func TestBootWireShape(t *testing.T) {
	// wire 形态须过 host 的 parseBootManifest 校验；硬编码期望值防常量漂移
	t.Run("entry", func(t *testing.T) {
		var got bootEntry
		if err := json.Unmarshal(trustBootEntryJSON, &got); err != nil {
			t.Fatalf("boot 条目不是合法 JSON: %v", err)
		}
		want := bootEntry{ID: trustPluginID, URL: trustPluginURL, Rev: trustPluginRev, Inject: []string{"connection"}, Immediately: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("entry wire 形态漂移: %+v != %+v", got, want)
		}
	})
	t.Run("batch", func(t *testing.T) {
		var got bootBatch
		if err := json.Unmarshal(trustBootBatchJSON, &got); err != nil {
			t.Fatalf("boot batch 不是合法 JSON: %v", err)
		}
		want := bootBatch{Phase: "application", URL: trustPluginURL, Rev: trustPluginRev, Entries: []string{trustPluginID}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("batch wire 形态漂移: %+v != %+v", got, want)
		}
	})
}

func TestPluginBundleServesAsModule(t *testing.T) {
	// 插件 bundle 须自调用 __ModuleLoader__.load({id, factory}) 注册
	src := string(trustPluginBundle)
	for _, want := range []string{
		"window.__ModuleLoader__.load({",
		"const pluginID = 'dsh-gateway-trust'",
		"factory: (require) => {",
		"exports.inject = ['connection']",
		"exports.apply = (ctx) => {",
		"isLoopback = true",
		"return module.exports",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("插件 bundle 缺少 %q", want)
		}
	}
}
