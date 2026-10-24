package apidir

// apidir 的守卫测试：数据文件是手工维护的事实表，最容易坏在两处——
// 顺序被谁重排了（前端列表会跳）、被回加了本该排除的条目（一次有意的删除
// 被悄悄撤销）。这两点都不是编译期能发现的，只能靠断言钉住。

import (
	"strings"
	"testing"
)

// TestCatalogShape 钉住 catalog.json 的结构不变量。
func TestCatalogShape(t *testing.T) {
	rows := List()
	if len(rows) != 55 {
		t.Fatalf("目录应有 55 家（52 + 2026-10-25 回加的 modelscope-cn/opencode-ai/opencode-go），得 %d —— 增删条目要同步改本断言与契约文档", len(rows))
	}
	if len(rows) != len(load()) {
		t.Fatal("List 长度应与内部加载结果一致")
	}
	seen := make(map[string]bool, len(rows))
	for i, p := range rows {
		if p.ID == "" || p.Name == "" || p.BaseURL == "" {
			t.Errorf("第 %d 条缺 id/name/base_url：%+v", i, p)
		}
		if seen[p.ID] {
			t.Errorf("id 重复：%s", p.ID)
		}
		seen[p.ID] = true
		if p.Protocol != "openai" {
			t.Errorf("%s 的 protocol 应为 openai，得 %q", p.ID, p.Protocol)
		}
		if strings.Contains(p.BaseURL, "{") || strings.Contains(p.BaseURL, "localhost") ||
			strings.Contains(p.BaseURL, "127.") {
			t.Errorf("%s 的端点不该是模板/回环地址：%s", p.ID, p.BaseURL)
		}
	}
	// 顺序即数据文件顺序（已按 id 升序）：List 明确承诺不重排。
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ID > rows[i].ID {
			t.Fatalf("顺序应保持 id 升序（List 不重排）：%s 在 %s 之前", rows[i-1].ID, rows[i].ID)
		}
	}
}

// TestKeyOnlyProvidersStayListed 钉住 2026-10-25 澄清的口径：modelscope /
// opencode 移除的只是**反代（OAuth）形态**，它们贴 Key 即用的端点必须留在
// 目录里——这是把「形态不适用」与「供应商整体移除」区分开的可执行副本。
func TestKeyOnlyProvidersStayListed(t *testing.T) {
	for _, id := range []string{"modelscope-cn", "opencode-ai", "opencode-go"} {
		p, ok := Get(id)
		if !ok {
			t.Errorf("%s 只移除了反代形态，其 API 端点应留在内置目录", id)
			continue
		}
		if p.BaseURL == "" {
			t.Errorf("%s 在目录里但端点为空", id)
		}
	}
	// 反代形态的 id（无 -cn/-ai/-go 后缀的裸名）不在本目录——那是 sidecar
	// 反代表里的名字，两边同名不同物，别混。
	for _, id := range []string{"modelscope", "opencode"} {
		if _, ok := Get(id); ok {
			t.Errorf("%s 是反代形态的 id，不应出现在 API 目录", id)
		}
	}
}

// TestGetAndListCopy 覆盖两个导出函数的契约：命中、未命中、以及返回副本。
func TestGetAndListCopy(t *testing.T) {
	p, ok := Get("deepseek-com")
	if !ok {
		t.Fatal("目录里应有 deepseek-com")
	}
	if !strings.Contains(p.BaseURL, "deepseek") {
		t.Fatalf("deepseek-com 的端点可疑：%s", p.BaseURL)
	}
	if _, ok := Get("no-such-provider"); ok {
		t.Fatal("未知 id 不应命中")
	}
	if _, ok := Get(""); ok {
		t.Fatal("空 id 不应命中")
	}

	// List 返回副本：调用方改动不得影响下一次读取（否则一处赋值会污染全进程）。
	rows := List()
	rows[0].Name = "被改坏"
	rows[0].Models = append(rows[0].Models, Model{ID: "注入"})
	again, _ := Get(rows[0].ID)
	if again.Name == "被改坏" || len(again.Models) != len(load()[0].Models) {
		t.Fatal("List 返回的切片被共享了，改动泄漏到了内部状态")
	}
}
