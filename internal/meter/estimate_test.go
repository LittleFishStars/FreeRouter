package meter

import (
	"strings"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 估算器的契约（2026-11-02「使全部模型都有 token 统计」）。
// 设计约束见 02-ir.md §9：estimestimated 必须与上游上报分离、必须有独立分词器、
// 估不出来时返回 nil 而不是编 0。

// TestEstimateTokensCountsCJKAndCode 分词器对中文与代码都要给出合理量级。
func TestEstimateTokensCountsCJKAndCode(t *testing.T) {
	cases := []struct{ name, text string }{
		{"英文", "hello world, this is a test sentence."},
		{"中文", "这是一段中文文本，用来验证分词器对 CJK 的处理。"},
		{"代码", "func main() { fmt.Println(\"hi\") }"},
	}
	for _, c := range cases {
		n, ok := CountTokens(c.text)
		if !ok {
			t.Fatalf("%s: 分词器不可用", c.name)
		}
		if n <= 0 {
			t.Errorf("%s: token 数 = %d，应为正", c.name, n)
		}
		// 量级校验：token 数不该超过字符数的 2 倍，也不该少到 1/10。
		// 这条不是精度要求，而是防"分词器返回了无意义的数"（例如整段当 1 token）。
		if n > len([]rune(c.text))*2 || n*10 < len([]rune(c.text)) {
			t.Errorf("%s: token=%d 与字符数=%d 量级不符", c.name, n, len([]rune(c.text)))
		}
	}
}

// TestEstimateUsageMarksEstimated 估算结果必须带 Source=estimated。
//
// 这是 02-ir.md §9 的硬约束：估算与上游上报混在一起后，
// 「对不对得上账」永远无法回答。
func TestEstimateUsageMarksEstimated(t *testing.T) {
	u := EstimateUsage("你好，请介绍一下你自己", "我是一个 AI 助手。")
	if u == nil {
		t.Fatalf("估算返回 nil（分词器不可用？）")
	}
	if u.Source != ir.UsageEstimated {
		t.Fatalf("Source = %q，必须为 estimated", u.Source)
	}
	if u.InputTokens == nil || *u.InputTokens <= 0 {
		t.Errorf("InputTokens = %v，应为正", u.InputTokens)
	}
	if u.OutputTokens == nil || *u.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %v，应为正", u.OutputTokens)
	}
}

// TestEstimateUsageEmptyReturnsNil 两侧都空时返回 nil（不编 0）。
//
// "0" 的语义是「上游明确说是零」，与「我们也不知道」相反——
// 用 0 冒充已知会污染配额护栏与贡献者对账（02-ir.md §9）。
func TestEstimateUsageEmptyReturnsNil(t *testing.T) {
	if u := EstimateUsage("", ""); u != nil {
		t.Fatalf("空输入应返回 nil，得到 %+v", u)
	}
}

// TestEstimateUsagePartialSides 只有一侧有内容时，另一侧留 nil 而不是 0。
func TestEstimateUsagePartialSides(t *testing.T) {
	u := EstimateUsage("请求文本", "")
	if u == nil {
		t.Fatalf("有一侧可用时应返回估算")
	}
	if u.InputTokens == nil || *u.InputTokens <= 0 {
		t.Errorf("InputTokens 应为正")
	}
	if u.OutputTokens != nil {
		t.Errorf("输出侧无内容时应为 nil（未知），得到 %v", *u.OutputTokens)
	}
}

// TestEstimateLoadsOfflineFast 分词器必须**离线**加载且够快。
//
// 2026-11-02 实测：默认加载器会从网络下载 BPE 表，首次 48 秒——足以把
// 一次普通请求拖成超时，且出不了外网的环境直接永久失败。
// 本用例是那条教训的回归闸：超过 5 秒即视为回归到联网加载。
func TestEstimateLoadsOfflineFast(t *testing.T) {
	start := time.Now()
	if _, ok := CountTokens("warm up"); !ok {
		t.Fatalf("分词器不可用")
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("分词器首次加载耗时 %v，超过 5s——很可能退回了联网加载", elapsed)
	}
}

// TestRequestTextHelper 不在本包（在 httpapi），但估算的正确性依赖它把
// 工具定义也算进去。这里只验证「长文本不会因超长而失败」。
func TestEstimateLargeInput(t *testing.T) {
	big := strings.Repeat("这是一段用于测试的长文本内容。", 2000)
	n, ok := CountTokens(big)
	if !ok || n <= 0 {
		t.Fatalf("长文本分词失败")
	}
	t.Logf("长文本 %d 字符 → %d token", len([]rune(big)), n)
}
