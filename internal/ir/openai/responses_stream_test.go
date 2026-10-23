package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// runResponsesWriter 把一组 IR 事件送入 writer，返回所有 SSE data 行。
func runResponsesWriter(events []ir.Event) (string, error) {
	var sb strings.Builder
	w := NewResponsesStreamWriter(&sb, "abstract-model")
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			return "", err
		}
	}
	_ = w.Close()
	return sb.String(), nil
}

func TestResponsesStreamTextDelta(t *testing.T) {
	out, err := runResponsesWriter([]ir.Event{
		{Type: ir.EvStart},
		{Type: ir.EvTextDelta, Text: "hello"},
		{Type: ir.EvTextDelta, Text: " world"},
		{Type: ir.EvDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "response.output_text.delta") != 2 {
		t.Fatalf("应有 2 个 output_text.delta，got:\n%s", out)
	}
	if !strings.Contains(out, "response.completed") {
		t.Fatal("应以 response.completed 结束")
	}
	// SSE 帧间用空行分隔，遍历跳过空行。
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("data 行应以 data: 开头，got: %q", line)
		}
		if !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
			t.Fatalf("非法 SSE data JSON: %q", line)
		}
	}
}

func TestResponsesToolCall(t *testing.T) {
	out, err := runResponsesWriter([]ir.Event{
		{Type: ir.EvToolCallStart, Index: 0, Call: &ir.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"beijing`}},
		{Type: ir.EvToolCallDelta, Index: 0, Call: &ir.ToolCallPart{ID: "call_1", Arguments: `"}}`}},
		{Type: ir.EvToolCallEnd, Index: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "response.output_item.added") {
		t.Fatalf("工具 start 应发 output_item.added，got:\n%s", out)
	}
	if !strings.Contains(out, "response.function_call_arguments.delta") {
		t.Fatalf("工具 delta 应发 function_call_arguments.delta，got:\n%s", out)
	}
	if !strings.Contains(out, "response.output_item.done") {
		t.Fatalf("工具 end 应发 output_item.done，got:\n%s", out)
	}
}

func TestResponsesUsageOnce(t *testing.T) {
	out, err := runResponsesWriter([]ir.Event{
		{Type: ir.EvTextDelta, Text: "x"},
		{Type: ir.EvUsage, Usage: &ir.Usage{InputTokens: ptrInt(10), OutputTokens: ptrInt(20)}},
		{Type: ir.EvUsage, Usage: &ir.Usage{}}, // 重复的应被 I4 丢弃
		{Type: ir.EvDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "response.usage") != 1 {
		t.Fatalf("usage 应只有一次，got:\n%s", out)
	}
}

func TestResponsesErrorAndDone(t *testing.T) {
	out, err := runResponsesWriter([]ir.Event{
		{Type: ir.EvTextDelta, Text: "partial"},
		{Type: ir.EvError, Err: &ir.Error{Code: "upstream_error", Message: "boom"}},
		{Type: ir.EvDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "response.failed") {
		t.Fatalf("应发 response.failed，got:\n%s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("failed 应带消息，got:\n%s", out)
	}
}

func ptrInt(v int) *int { return &v }
