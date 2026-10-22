package upstream

import (
	"io"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// 本文件守的是 task-3 第 1 点：sidecar 流式帧 → ir.Event 的映射契约。
//
// 覆盖：start/delta(text|thinking|tool_call)/usage/done/error 六类帧、I1/I4/I5/I6
// 四条不变式、IPC 封装帧与数组/字符串载荷形态、空流与畸形帧的协议错。
// 不断言 sidecar 进程行为——那条链路由 dshprovider 的契约测试负责。

func collectRelayEvents(t *testing.T, s *RelayStream) []ir.Event {
	t.Helper()
	var out []ir.Event
	for {
		ev, err := s.Recv()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Recv 失败: %v", err)
		}
		out = append(out, ev)
	}
}

func newFrameStream(lines ...string) *RelayStream {
	return NewRelayStreamFromPayload([]byte(strings.Join(lines, "\n")))
}

func TestContractRelayFrameMapping(t *testing.T) {
	s := newFrameStream(
		`{"event":"start"}`,
		`{"event":"delta","kind":"thinking","index":0,"text":"想一想"}`,
		`{"event":"delta","kind":"thinking_delta","index":0,"signature":"sig-1"}`,
		`{"event":"delta","kind":"text","index":1,"text":"你好"}`,
		`{"event":"delta","kind":"tool_call_start","index":2,"call":{"id":"call_1","name":"read_file","arguments":""}}`,
		`{"event":"delta","kind":"tool_call_delta","index":2,"call":{"arguments":"{\"pa"}}`,
		`{"event":"delta","kind":"tool_call_end","index":2,"call":{"id":"call_1","name":"read_file","arguments":"{\"path\":\"/tmp\"}"}}`,
		`{"event":"usage","usage":{"inputTokens":10,"outputTokens":2}}`,
		`{"event":"usage","usage":{"input_tokens":11,"completion_tokens":3,"reasoning_tokens":1}}`,
		`{"event":"done","stop":"length"}`,
	)
	events := collectRelayEvents(t, s)
	if len(events) != 9 {
		t.Fatalf("事件数 %d，want 9：%+v", len(events), events)
	}

	// I1：start 是第一个且只有一个。
	if events[0].Type != ir.EvStart {
		t.Fatalf("首事件应为 start，got %s", events[0].Type)
	}
	if events[1].Type != ir.EvThinkingDelta || events[1].Text != "想一想" || events[1].Index != 0 {
		t.Fatalf("thinking 文本映射错误：%+v", events[1])
	}
	// 纯签名终帧：Text 空、Signature 非空，且按 02-ir.md §5.3 不算可见内容。
	if events[2].Type != ir.EvThinkingDelta || events[2].Text != "" || events[2].Signature != "sig-1" {
		t.Fatalf("thinking 签名终帧映射错误：%+v", events[2])
	}
	if events[2].Visible() {
		t.Fatal("纯签名终帧不得算可见内容（否则候选会被无谓锁死）")
	}
	if events[3].Type != ir.EvTextDelta || events[3].Text != "你好" || events[3].Index != 1 {
		t.Fatalf("text delta 映射错误：%+v", events[3])
	}
	if events[4].Type != ir.EvToolCallStart || events[4].Call == nil || events[4].Call.ID != "call_1" || events[4].Call.Name != "read_file" {
		t.Fatalf("tool_call_start 映射错误：%+v", events[4])
	}
	if events[5].Type != ir.EvToolCallDelta || events[5].Call == nil || events[5].Call.Arguments != `{"pa` {
		t.Fatalf("tool_call_delta 映射错误：%+v", events[5])
	}
	if events[6].Type != ir.EvToolCallEnd || events[6].Call == nil || events[6].Call.Arguments != `{"path":"/tmp"}` {
		t.Fatalf("tool_call_end 映射错误：%+v", events[6])
	}
	// I4：两次 usage 取最后一次，且 usage 在 done 之前。
	if events[7].Type != ir.EvUsage || events[7].Usage == nil {
		t.Fatalf("usage 应排在 done 之前：%+v", events[7])
	}
	if events[7].Usage.InputTokens == nil || *events[7].Usage.InputTokens != 11 ||
		events[7].Usage.OutputTokens == nil || *events[7].Usage.OutputTokens != 3 ||
		events[7].Usage.ReasoningTokens == nil || *events[7].Usage.ReasoningTokens != 1 {
		t.Fatalf("usage 应取最后一次且支持 snake_case：%+v", events[7].Usage)
	}
	if events[7].Usage.Source != ir.UsageUpstream {
		t.Fatalf("usage.Source 应为 upstream，got %q", events[7].Usage.Source)
	}
	// stop "length" 归一为 max_tokens（OpenAI finish_reason 语义）。
	if events[8].Type != ir.EvDone || events[8].Stop != ir.StopMaxTokens {
		t.Fatalf("done 映射错误：%+v", events[8])
	}

	st := s.Stats()
	if st.UpstreamStatus != 200 || st.EventsOut != 9 {
		t.Fatalf("统计口径漂移：%+v", st)
	}
	if st.BytesIn == 0 {
		t.Fatal("BytesIn 必须累计帧字节")
	}
	if !st.VisibleWritten {
		t.Fatal("出现 text/thinking/tool_call 后 VisibleWritten 必须为 true")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}
}

func TestContractRelayErrorFrameAndInvariants(t *testing.T) {
	s := newFrameStream(
		`{"event":"error","error":{"code":"E_UPSTREAM_HTTP","message":"上游 502","status":502}}`,
		`{"event":"delta","kind":"text","text":"这段不该出现"}`,
		`{"event":"done"}`,
	)
	events := collectRelayEvents(t, s)
	if len(events) != 3 {
		t.Fatalf("事件数 %d，want 3：%+v", len(events), events)
	}
	// I1：error 先到也要先合成 start。
	if events[0].Type != ir.EvStart {
		t.Fatalf("首事件应为 start，got %s", events[0].Type)
	}
	if events[1].Type != ir.EvError || events[1].Err == nil || events[1].Err.Code != "E_UPSTREAM_HTTP" {
		t.Fatalf("error 帧映射错误：%+v", events[1])
	}
	// I6：error 之后只有 done；后面的 delta 被丢弃。
	if events[2].Type != ir.EvDone || events[2].Stop != ir.StopError {
		t.Fatalf("error 之后应补 Stop=error 的 done，got %+v", events[2])
	}
	if s.Stats().VisibleWritten {
		t.Fatal("只有 start/error/done 时 VisibleWritten 必须为 false（可切换候选）")
	}
}

func TestContractRelayFrameInvalids(t *testing.T) {
	// 畸形 JSON → 协议错（不是 panic，也不是伪装成正常空流）。
	bad := NewRelayStreamFromPayload([]byte("{not json"))
	if _, err := bad.Recv(); err == nil {
		t.Fatal("畸形帧应返回错误")
	} else if ue, ok := err.(*Error); !ok || ue.Class != ErrProtocol {
		t.Fatalf("畸形帧应归 ErrProtocol，got %T %v", err, err)
	}

	// 空载荷：一个事件都没有 → 协议错（sidecar 根本没回答）。
	empty := NewRelayStreamFromPayload(nil)
	if _, err := empty.Recv(); err == nil {
		t.Fatal("空载荷应返回错误")
	}

	// 重复 start：I1 违规。
	dup := newFrameStream(`{"event":"start"}`, `{"event":"start"}`)
	if _, err := dup.Recv(); err != nil {
		t.Fatalf("首个 start 应正常: %v", err)
	}
	if _, err := dup.Recv(); err == nil {
		t.Fatal("重复 start 应报协议错")
	}

	// 未知事件名：不能静默忽略（否则协议漂移没有任何信号）。
	unknown := newFrameStream(`{"event":"start"}`, `{"event":"mystery"}`)
	if _, err := unknown.Recv(); err != nil {
		t.Fatalf("start 应正常: %v", err)
	}
	if _, err := unknown.Recv(); err == nil {
		t.Fatal("未知事件应报协议错")
	}
}

func TestContractRelaySynthesizedStartAndEOF(t *testing.T) {
	// 首帧不是 start：按 I1 合成 start，原事件排在其后。
	s := newFrameStream(`{"event":"delta","kind":"text","text":"hi"}`, `{"event":"done"}`)
	events := collectRelayEvents(t, s)
	if len(events) != 3 || events[0].Type != ir.EvStart || events[1].Type != ir.EvTextDelta || events[2].Type != ir.EvDone {
		t.Fatalf("合成 start 的事件序列不对：%+v", events)
	}

	// 没有 done 就断流：有内容时补 done（否则客户端永远等不到收尾），
	// 且缓冲区里的 usage 必须在 done 之前交付（I4）。
	trunc := newFrameStream(
		`{"event":"start"}`,
		`{"event":"delta","kind":"text","text":"半截"}`,
		`{"event":"usage","usage":{"inputTokens":1,"outputTokens":1}}`,
	)
	events = collectRelayEvents(t, trunc)
	if len(events) != 4 {
		t.Fatalf("断流事件数 %d，want 4：%+v", len(events), events)
	}
	if events[2].Type != ir.EvUsage || events[3].Type != ir.EvDone {
		t.Fatalf("断流时应先 usage 后 done：%+v", events)
	}
}

func TestContractRelayPayloadShapes(t *testing.T) {
	// JSON 数组。
	arr := NewRelayStreamFromPayload([]byte(`[{"event":"start"},{"event":"delta","kind":"text","text":"a"},{"event":"done"}]`))
	events := collectRelayEvents(t, arr)
	if len(events) != 3 || events[1].Text != "a" {
		t.Fatalf("数组载荷解析错误：%+v", events)
	}

	// {"frames":[…]}。
	wrapped := NewRelayStreamFromPayload([]byte(`{"frames":[{"event":"start"},{"event":"done"}]}`))
	if got := collectRelayEvents(t, wrapped); len(got) != 2 {
		t.Fatalf("frames 载荷解析错误：%+v", got)
	}

	// JSON 字符串（换行分隔）。
	str := NewRelayStreamFromPayload([]byte(`"{\"event\":\"start\"}\n{\"event\":\"done\"}"`))
	if got := collectRelayEvents(t, str); len(got) != 2 {
		t.Fatalf("字符串载荷解析错误：%+v", got)
	}

	// IPC 封装帧：ok:true 取 result；ok:false 按 error 帧。
	env := newFrameStream(
		`{"id":1,"ok":true,"result":{"event":"start"}}`,
		`{"id":2,"ok":true,"result":{"event":"delta","kind":"text","text":"x"}}`,
		`{"id":3,"ok":false,"error":{"code":"E_RATE_LIMITED","message":"限流"}}`,
	)
	events = collectRelayEvents(t, env)
	if len(events) != 4 || events[0].Type != ir.EvStart || events[1].Type != ir.EvTextDelta {
		t.Fatalf("封装帧解析错误：%+v", events)
	}
	if events[2].Type != ir.EvError || events[2].Err.Code != "E_RATE_LIMITED" {
		t.Fatalf("ok:false 封装帧应转 error：%+v", events[2])
	}
	if events[3].Type != ir.EvDone || events[3].Stop != ir.StopError {
		t.Fatalf("error 后应补 done：%+v", events[3])
	}
}

func TestContractRelayHeartbeatAndAliases(t *testing.T) {
	s := newFrameStream(
		`{"event":"start"}`,
		`{"event":"ping"}`,
		`{"event":"delta","kind":"text","text":""}`,
		`{"event":"delta","kind":"content","content":"别名文本"}`,
		`{"event":"done","reason":"stop"}`,
	)
	events := collectRelayEvents(t, s)
	if len(events) != 3 {
		t.Fatalf("心跳应被跳过，事件数 %d，want 3：%+v", len(events), events)
	}
	if events[1].Type != ir.EvTextDelta || events[1].Text != "别名文本" {
		t.Fatalf("content 别名映射错误：%+v", events[1])
	}
	if events[2].Stop != ir.StopEndTurn {
		t.Fatalf("reason=stop 应归一为 end_turn，got %q", events[2].Stop)
	}
}
