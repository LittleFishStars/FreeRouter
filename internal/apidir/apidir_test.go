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

// TestCatalogEndpointFacts 钉住 endpoints 的三条数据不变量（契约 §2.3）。
//
// 守卫的漂移各有具体后果：① endpoints.openai 与 base_url 分叉——两处都自称
// 「端点」，分叉后 API 目录显示一个地址、贡献入口又落库另一个地址；② anthropic
// 端点被写成模板/回环地址——它是事实标注（见包注释「为什么端点清单里会有
// anthropic」），写进假地址等于让徽标说谎，而徽标没有第二处可以校对；③ 带
// anthropic 端点的条目数变化——那是数据变更，必须有人同步契约 §1.2 与前端文案。
func TestCatalogEndpointFacts(t *testing.T) {
	rows := List()
	withAnthropic := 0
	for _, p := range rows {
		if p.Endpoints.OpenAI == "" {
			t.Errorf("%s 缺 endpoints.openai（契约 §2.3：每条都要有，不许缺键）", p.ID)
		} else if p.Endpoints.OpenAI != p.BaseURL {
			t.Errorf("%s 的 endpoints.openai=%q 与 base_url=%q 不一致（不变量：两者恒相等）",
				p.ID, p.Endpoints.OpenAI, p.BaseURL)
		}
		a := p.Endpoints.Anthropic
		if a == "" {
			continue
		}
		withAnthropic++
		if strings.Contains(a, "{") || strings.Contains(a, "localhost") ||
			strings.Contains(a, "127.") {
			t.Errorf("%s 的 anthropic 端点不该是模板/回环地址：%s", p.ID, a)
		}
	}
	// 32 是 2026-10-26 从本机 Tingly Box registry 实测的值（55 条里 45 条能对上
	// registry，其中 32 条带 base_url_anthropic）。改数据要同步改本断言。
	if withAnthropic != 32 {
		t.Fatalf("带 anthropic 端点的条目数应为 32，得 %d —— 改数据要同步改本断言与契约 §1.2",
			withAnthropic)
	}
}

// TestProviderProtocols 覆盖 Protocols() 的三条契约：顺序恒 openai 在前、
// 与 endpoints.anthropic 的存在性一一对应、返回的是副本。
func TestProviderProtocols(t *testing.T) {
	for _, p := range List() {
		got := p.Protocols()
		want := 1
		if p.Endpoints.Anthropic != "" {
			want = 2
		}
		if len(got) != want {
			t.Fatalf("%s 应有 %d 个协议，得 %v", p.ID, want, got)
		}
		if got[0] != "openai" {
			t.Fatalf("%s 的协议列表应以 openai 开头，得 %v（顺序即前端徽标的契约）", p.ID, got)
		}
		if want == 2 && got[1] != "anthropic" {
			t.Fatalf("%s 的第二项应为 anthropic，得 %q", p.ID, got[1])
		}
	}

	// 无 anthropic 端点的条目退化成 ["openai"]，而不是空列表或带空串的元素。
	b, ok := Get("baichuan-com")
	if !ok {
		t.Fatal("目录里应有 baichuan-com（本用例拿它当「无 anthropic 端点」的样本）")
	}
	if got := b.Protocols(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("无 anthropic 端点的条目应退化为 [\"openai\"]，得 %v", got)
	}

	// 返回副本：调用方改动不得泄漏进目录（否则一次赋值会污染全进程的下一次请求）。
	p, ok := Get("deepseek-com")
	if !ok {
		t.Fatal("目录里应有 deepseek-com")
	}
	got := p.Protocols()
	got[0] = "被改坏"
	if again, _ := Get("deepseek-com"); again.Protocols()[0] != "openai" {
		t.Fatal("Protocols 返回的切片被共享了，改动泄漏到了内部状态")
	}
}
