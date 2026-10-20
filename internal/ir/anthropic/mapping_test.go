package anthropic

import (
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// TestContractStopReasonRoundtrip 锁定 stop_reason ↔ StopReason 往返映射表。
// 理由同 openai/mapping_test.go：审计/计费归因依赖这张表，golden 只覆盖出现过的值。
func TestContractStopReasonRoundtrip(t *testing.T) {
	cases := []struct {
		reason string
		stop   ir.StopReason
		known  bool
	}{
		{"end_turn", ir.StopEndTurn, true},
		{"max_tokens", ir.StopMaxTokens, true},
		{"stop_sequence", ir.StopStopSequence, true},
		{"tool_use", ir.StopToolUse, true},
		{"refusal", ir.StopRefusal, true},
		{"", ir.StopUnspecified, true},
		{"pause_turn", ir.StopEndTurn, false}, // 长对话暂停：保守 end_turn，known=false 交调用方记告警
		{"future_reason", ir.StopEndTurn, false},
	}
	for _, c := range cases {
		got, known := stopReasonToStop(c.reason)
		if got != c.stop || known != c.known {
			t.Errorf("stopReasonToStop(%q)=(%q,%v), want (%q,%v)", c.reason, got, known, c.stop, c.known)
		}
	}
	for _, s := range []ir.StopReason{
		ir.StopUnspecified, ir.StopEndTurn, ir.StopMaxTokens, ir.StopStopSequence,
		ir.StopToolUse, ir.StopContentFilter, ir.StopRefusal, ir.StopError,
	} {
		r := stopToStopReason(s)
		if r == "" {
			t.Errorf("stopToStopReason(%q) 为空", s)
		}
		switch s {
		case ir.StopMaxTokens, ir.StopStopSequence, ir.StopToolUse:
			// 这三个决定客户端的重试/截断行为，必须往返闭合。
			// content_filter/refusal 归一到 refusal 是**有意单向**（协议无 content_filter 值），
			// 不要求闭合；end_turn 同理（StopUnspecified/StopError 都映到它）。
			if back, _ := stopReasonToStop(stopToStopReason(s)); back != s {
				t.Errorf("往返断裂: %q → %q → %q", s, stopToStopReason(s), back)
			}
		}
	}
}
