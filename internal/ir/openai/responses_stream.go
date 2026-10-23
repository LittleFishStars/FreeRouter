package openai

// responses_stream.go —— Responses API 流式出站 writer。
//
// 实现 IR 事件流 → OpenAI Responses /v1/responses stream 模式的 SSE 帧。
// 02-ir.md §5.3 的映射（本文件只做出站方向，入站在 v1responses.go）：
//
//	text_delta      → response.output_text.delta
//	tool_call_*     → response.output_item.added / .done（含 function）
//	usage           → response.usage（在 response.completed 前发）
//	done            → response.completed（终帧）
//	error           → response.failed
//
// 与 Chat 流式的差异：Chat 用单帧 choices 异步增量，Responses 每个事件是一个
// 完整的具名对象（data.type 表示事件名）。这里不复用 streamWriter，独立实现，
// 但共享 ir.EncoderState 的 I4/I5 去重语义（Accept/MarkWritten）。

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// responsesStreamWriter 把 IR 事件流写成 Responses 流式 SSE 帧。
type responsesStreamWriter struct {
	w      *ir.SSEWriter
	st     *ir.EncoderState
	model  string
	closed bool
}

// NewResponsesStreamWriter 构造 Responses 流式 writer。model 是出站模型名
// （路由已归一的抽象名，§4.1）。reqID 预留但当前每个事件对象自带内联 id。
func NewResponsesStreamWriter(w io.Writer, model string) ir.StreamWriter {
	return &responsesStreamWriter{
		w:     ir.NewSSEWriter(w),
		st:    &ir.EncoderState{},
		model: model,
	}
}

// State 暴露 EncoderState（C7 判定可见内容）。
func (s *responsesStreamWriter) State() *ir.EncoderState { return s.st }

// Write 编码一个 IR 事件成 SSE 帧。
func (s *responsesStreamWriter) Write(ev ir.Event) error {
	if s.closed {
		return ir.ErrStreamClosed
	}
	if !s.st.Accept(ev) {
		return nil
	}
	switch ev.Type {
	case ir.EvStart:
		// Responses 无显式 start，但要把状态标为已写（C7 不会误判还没吐）。
		s.st.MarkWritten(ev)
		return nil

	case ir.EvThinkingDelta:
		// 透出有文本的思考增量（保持最小面，不展开摘要树）。
		if ev.Text == "" {
			return nil
		}
		return s.writeMap(map[string]any{
			"type":    "response.reasoning_summary_text.delta",
			"summary": []string{ev.Text},
		})

	case ir.EvTextDelta:
		s.st.MarkWritten(ev)
		return s.writeMap(map[string]any{
			"type":  "response.output_text.delta",
			"delta": ev.Text,
		})

	case ir.EvToolCallStart:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_start 缺少 call"}
		}
		// 先 add 一个空的 function_call 槽位，后续 delta 补 arguments。
		item := map[string]any{
			"type":      "function_call",
			"id":        ev.Call.ID,
			"name":      ev.Call.Name,
			"arguments": "",
		}
		return s.writeMap(map[string]any{
			"type":         "response.output_item.added",
			"output_index": ev.Index,
			"item":         item,
		})

	case ir.EvToolCallDelta:
		if ev.Call == nil {
			return &ir.InvalidRequestError{Param: "event.call", Detail: "tool_call_delta 缺少 call"}
		}
		// function 参数逐段增量。
		return s.writeMap(map[string]any{
			"type":    "response.function_call_arguments.delta",
			"index":   ev.Index,
			"call_id": ev.Call.ID,
			"delta":   ev.Call.Arguments,
		})

	case ir.EvToolCallEnd:
		return s.writeMap(map[string]any{
			"type":        "response.output_item.done",
			"index":       ev.Index,
			"output_type": "function_call",
		})

	case ir.EvUsage:
		if ev.Usage == nil {
			return nil
		}
		// 必须先 MarkWritten：I4 靠 EncoderState.usage 去重重复 usage，而
		// 该标志只在 MarkWritten(EvUsage) 时置位（与 openai streamWriter
		// 的 writeFrame 内部 MarkWritten 同一语义）。不标的话第二次 usage
		// 会穿过 Accept 造成重复帧。
		s.st.MarkWritten(ev)
		return s.writeMap(map[string]any{
			"type":          "response.usage",
			"input_tokens":  intOrNil(ev.Usage.InputTokens),
			"output_tokens": intOrNil(ev.Usage.OutputTokens),
		})

	case ir.EvError:
		if ev.Err == nil {
			return nil
		}
		s.st.MarkWritten(ev)
		return s.writeMap(map[string]any{
			"type":    "response.failed",
			"code":    ev.Err.Code,
			"message": ev.Err.Message,
		})

	case ir.EvDone:
		// 标记完成：Accept 据 I5 丢弃 done 之后的所有事件。
		s.closed = true
		s.st.MarkWritten(ev)
		return s.writeMap(map[string]any{"type": "response.completed"})
	}
	return nil
}

// Close 幂等结束（Responses 无终止帧）。
func (s *responsesStreamWriter) Close() error {
	s.closed = true
	return nil
}

// writeMap 序列化一个对象为 SSE data 行。
func (s *responsesStreamWriter) writeMap(obj map[string]any) error {
	out := map[string]any{"model": s.model}
	for k, v := range obj {
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("responses stream: marshal: %w", err)
	}
	return s.w.Write(ir.SSEEvent{Data: string(b)})
}

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// 编译期：实现接口。
var _ ir.StreamWriter = (*responsesStreamWriter)(nil)
