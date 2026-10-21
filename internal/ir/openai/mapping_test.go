package openai

import (
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// TestContractFinishReasonRoundtrip 锁定 finish_reason ↔ StopReason 往返映射表。
//
// 为什么白盒表驱动：这张表进审计与计费归因，"模型说完了"与"被长度截断"混淆一次
// 就是一次错误计费；golden 样本只覆盖出现过的值，表驱动覆盖全部枚举组合。
func TestContractFinishReasonRoundtrip(t *testing.T) {
	cases := []struct {
		reason string
		stop   ir.StopReason
		known  bool
	}{
		{"stop", ir.StopEndTurn, true},
		{"length", ir.StopMaxTokens, true},
		{"tool_calls", ir.StopToolUse, true},
		{"function_call", ir.StopToolUse, true}, // 旧方言同义
		{"content_filter", ir.StopContentFilter, true},
		{"", ir.StopUnspecified, true},
		{"brand_new_reason", ir.StopEndTurn, false}, // 未知值：保守 end_turn + known=false 交调用方记告警
	}
	for _, c := range cases {
		got, known := finishReasonToStop(c.reason)
		if got != c.stop || known != c.known {
			t.Errorf("finishReasonToStop(%q)=(%q,%v), want (%q,%v)", c.reason, got, known, c.stop, c.known)
		}
	}
	// 反向表：每个 IR 停止原因必须编出合法 finish_reason，且关键项双向往返闭合。
	for _, s := range []ir.StopReason{
		ir.StopUnspecified, ir.StopEndTurn, ir.StopMaxTokens,
		ir.StopToolUse, ir.StopContentFilter, ir.StopRefusal, ir.StopError,
	} {
		r := stopToFinishReason(s)
		if r == "" {
			t.Errorf("stopToFinishReason(%q) 为空", s)
		}
		switch s {
		case ir.StopMaxTokens, ir.StopToolUse, ir.StopContentFilter:
			// 这三个必须往返闭合——它们决定客户端的重试/截断行为。
			back, _ := finishReasonToStop(stopToFinishReason(s))
			if back != s {
				t.Errorf("往返断裂: %q → %q → %q", s, stopToFinishReason(s), back)
			}
		}
	}
}
