package main

import (
	"bytes"
	"encoding/json"
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

// 从改写后的 html 提取 __DSH_BOOT__ 的 JSON 部分
func extractBootJSON(t *testing.T, html []byte) map[string]json.RawMessage {
	t.Helper()
	i, marker := findBootMarker(html)
	if i == -1 {
		t.Fatalf("改写后 html 丢失 boot marker")
	}
	start := i + len(marker)
	end := bytes.Index(html[start:], []byte("</script>"))
	if end == -1 {
		t.Fatal("改写后 html 丢失 </script>")
	}
	var graph map[string]json.RawMessage
	if err := json.Unmarshal(html[start:start+end], &graph); err != nil {
		t.Fatalf("改写后 __DSH_BOOT__ 不是合法 JSON: %v", err)
	}
	return graph
}

func entryIDs(t *testing.T, graph map[string]json.RawMessage) []string {
	t.Helper()
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(graph["entries"], &entries); err != nil {
		t.Fatalf("entries 解析失败: %v", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestInjectBootManifestEntry(t *testing.T) {
	html := sampleShellHTML()
	patched, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("注入报错: %v", err)
	}
	if !changed {
		t.Fatal("带清单的页面应发生改写")
	}
	if len(patched) <= len(html) {
		t.Fatal("改写后体积应大于原页面")
	}

	graph := extractBootJSON(t, patched)
	if string(graph["rev"]) != `"rev-abc"` {
		t.Fatalf("rev 被破坏: %s", graph["rev"])
	}
	ids := entryIDs(t, graph)
	if len(ids) != 3 {
		t.Fatalf("应有 3 个条目（原 2 + 注入 1），实际 %d: %v", len(ids), ids)
	}
	if ids[0] != "@deepseek-ai/dsh-client-connection" || ids[1] != "@deepseek-ai/dsh-client-ui-settings" {
		t.Fatalf("原条目被破坏: %v", ids)
	}
	if ids[2] != trustPluginID {
		t.Fatalf("注入条目 id 应为 %s，实际 %s", trustPluginID, ids[2])
	}
	// 0.1.2 起每个 entry 必须归属一个 batch（parseBootManifest 校验），
	// 原 host batch 须原样保留，注入的信任插件条目须获得自己的 batch
	batches := batchDescriptors(t, graph)
	if len(batches) != 2 {
		t.Fatalf("应有 2 个 batch（原 1 + 注入 1），实际 %d: %v", len(batches), batches)
	}
	hostBatch := batches[0]
	if string(hostBatch["phase"]) != `"application"` ||
		!strings.Contains(string(hostBatch["url"]), "@deepseek-ai/dsh-client-connection") {
		t.Fatalf("原 host batch 被破坏: %v", hostBatch)
	}
	trustBatch := batches[1]
	if string(trustBatch["phase"]) != `"application"` {
		t.Fatalf("信任插件 batch phase 应为 application: %s", trustBatch["phase"])
	}
	if string(trustBatch["url"]) != `"/plugins/dsh-gateway-trust/client.js?rev=1"` {
		t.Fatalf("信任插件 batch url 与 entry url 不一致: %s", trustBatch["url"])
	}
	if string(trustBatch["entries"]) != `["dsh-gateway-trust"]` {
		t.Fatalf("信任插件 batch entries 应为自身 id: %s", trustBatch["entries"])
	}
	// 页面其余结构原样保留
	for _, marker := range []string{"<link rel=\"stylesheet\"", "<div id=\"root\">", "<!doctype html>", "<head>"} {
		if !bytes.Contains(patched, []byte(marker)) {
			t.Fatalf("页面结构被破坏，缺少 %q", marker)
		}
	}
}

// 从改写后的 html 提取 batches 数组
func batchDescriptors(t *testing.T, graph map[string]json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var batches []map[string]json.RawMessage
	if err := json.Unmarshal(graph["batches"], &batches); err != nil {
		t.Fatalf("batches 解析失败: %v", err)
	}
	return batches
}

func TestInjectNoMarkerPassesThrough(t *testing.T) {
	html := []byte("<html><body>普通页面，没有清单</body></html>")
	patched, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("无清单页面不应报错: %v", err)
	}
	if changed {
		t.Fatal("无清单页面不应改写")
	}
	if !bytes.Equal(patched, html) {
		t.Fatal("无清单页面应原样返回")
	}
}

func TestInjectIdempotent(t *testing.T) {
	html, _, err := injectBootManifestEntry(sampleShellHTML())
	if err != nil {
		t.Fatalf("首次注入失败: %v", err)
	}
	patched, changed, err := injectBootManifestEntry(html)
	if err != nil {
		t.Fatalf("二次注入报错: %v", err)
	}
	if changed {
		t.Fatal("已注入的页面不应重复注入")
	}
	graph := extractBootJSON(t, patched)
	if ids := entryIDs(t, graph); len(ids) != 3 {
		t.Fatalf("重复注入导致条目数异常: %d", len(ids))
	}
	if batches := batchDescriptors(t, graph); len(batches) != 2 {
		t.Fatalf("重复注入导致 batch 数异常: %d", len(batches))
	}
}

// 旧版 dsh（rc.8 及之前）的注入行形态 window.__DSH_BOOT__ 且图无 batches：
// 注入照常工作；我们的 batch 对旧 host 无害（其解析与加载都忽略该字段）
func TestInjectLegacyMarkerAndGraph(t *testing.T) {
	patched, changed, err := injectBootManifestEntry(legacyShellHTML())
	if err != nil {
		t.Fatalf("旧版图注入报错: %v", err)
	}
	if !changed {
		t.Fatal("旧版图应发生改写")
	}
	graph := extractBootJSON(t, patched)
	if string(graph["rev"]) != `"rev-legacy"` {
		t.Fatalf("旧版 rev 被破坏: %s", graph["rev"])
	}
	if ids := entryIDs(t, graph); len(ids) != 2 {
		t.Fatalf("旧版图注入后应有 2 个条目: %d", len(ids))
	}
	batches := batchDescriptors(t, graph)
	if len(batches) != 1 || string(batches[0]["url"]) != `"/plugins/dsh-gateway-trust/client.js?rev=1"` {
		t.Fatalf("旧版图应恰好注入信任插件自己的 batch: %v", batches)
	}
}

func TestInjectMalformedFailsLoud(t *testing.T) {
	html := []byte("<head><script>window.__DSH_BOOT__ = {broken-json}</script></head>")
	if _, _, err := injectBootManifestEntry(html); err == nil {
		t.Fatal("畸形清单必须报错（响亮失败，不静默透传）")
	}
}

func TestBootEntryWireShape(t *testing.T) {
	// wire 形态须过 host 的 parseBootManifest 校验
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(trustBootEntryJSON, &entry); err != nil {
		t.Fatalf("boot 条目不是合法 JSON: %v", err)
	}
	for _, field := range []string{"id", "url", "rev"} {
		if !strings.HasPrefix(string(entry[field]), `"`) {
			t.Fatalf("%s 应为 string: %s", field, entry[field])
		}
	}
	if !bytes.Equal(entry["url"], []byte(`"/plugins/dsh-gateway-trust/client.js?rev=1"`)) {
		t.Fatalf("url 与 id 推导不符: %s", entry["url"])
	}
	if string(entry["inject"]) != `["connection"]` {
		t.Fatalf("inject 应为 [connection]: %s", entry["inject"])
	}
	if string(entry["immediately"]) != "true" {
		t.Fatalf("immediately 应为 true: %s", entry["immediately"])
	}
}

func TestBootBatchWireShape(t *testing.T) {
	// wire 形态须过新 host 的 parseBootManifest batch 校验（phase 枚举、url/rev 字符串、entries 非空且都存在）
	var batch map[string]json.RawMessage
	if err := json.Unmarshal(trustBootBatchJSON, &batch); err != nil {
		t.Fatalf("boot batch 不是合法 JSON: %v", err)
	}
	if string(batch["phase"]) != `"application"` {
		t.Fatalf("phase 应为 application: %s", batch["phase"])
	}
	for _, field := range []string{"url", "rev"} {
		if !strings.HasPrefix(string(batch[field]), `"`) {
			t.Fatalf("%s 应为 string: %s", field, batch[field])
		}
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(trustBootEntryJSON, &entry); err != nil {
		t.Fatalf("boot 条目解析失败: %v", err)
	}
	if !bytes.Equal(batch["url"], entry["url"]) {
		t.Fatalf("batch url 应等于 entry url（同一 bundle 端点）: %s", batch["url"])
	}
	var entries []string
	if err := json.Unmarshal(batch["entries"], &entries); err != nil {
		t.Fatalf("entries 解析失败: %v", err)
	}
	if len(entries) != 1 || entries[0] != trustPluginID {
		t.Fatalf("entries 应恰好含自身 id: %v", entries)
	}
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
