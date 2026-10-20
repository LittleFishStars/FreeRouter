package ir

import (
	"encoding/json"
	"testing"
)

// TestContractErrorTypeMapping 锁定错误体 type/code 分层契约（docs/api/03-errors.md §2.4/§3.1）。
//
// 为什么表驱动白盒测：这张表是两个协议出站错误体共用的唯一映射源；表错一行，
// 所有走该码的 SDK 客户端分支就错一片，而编译器与 golden 都拦不住"语义错但形状对"。
func TestContractErrorTypeMapping(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"invalid_api_key", "authentication_error"},
		{"invalid_request", "invalid_request_error"},
		{"capability_unsupported", "invalid_request_error"},
		{"request_too_large", "invalid_request_error"},
		{"model_not_found", "not_found_error"},
		{"model_retired", "not_found_error"},
		{"quota_exceeded", "rate_limit_error"},
		{"pool_exhausted", "rate_limit_error"},
		{"rate_limited", "rate_limit_error"},
		{"upstream_unavailable", "service_unavailable_error"},
		{"upstream_timeout", "service_unavailable_error"},
		{"upstream_error", "service_unavailable_error"},
		{"no_candidate", "service_unavailable_error"},
		{"internal_error", "internal_error"},
	}
	for _, c := range cases {
		if got := CodeToErrorType(c.code); got != c.want {
			t.Errorf("CodeToErrorType(%q)=%q, want %q", c.code, got, c.want)
		}
		// 每个粗 type 必须能反查回某个细码（表闭合性：出站→入站→出站不丢信息类）。
		back := ErrorTypeToCode(c.want)
		if back == "" {
			t.Errorf("type %q 无代表细码", c.want)
		}
		if CodeToErrorType(back) != c.want {
			t.Errorf("往返断裂: %q → %q → %q", c.code, c.want, back)
		}
	}
	// 未登记的细码落 internal_error：宁可让客户端重试一次，不可伪装成参数错误。
	if got := CodeToErrorType("some_future_code"); got != "internal_error" {
		t.Errorf("未知码兜底应为 internal_error，得到 %q", got)
	}
	// 未知粗 type 原样返回：保留上游原词比编造新词诚实。
	if got := ErrorTypeToCode("provider_specific_error"); got != "provider_specific_error" {
		t.Errorf("未知 type 应原样返回，得到 %q", got)
	}
}

// TestContractErrorShapeInStream 锁定流式错误体的形状：type 粗、code 细。
// openai 包与 anthropic 包各测各的帧；此处测 IR 侧三件套（Code/Message/Capability）。
func TestContractErrorShapeInStream(t *testing.T) {
	e := &Error{Code: "capability_unsupported", Message: "m", Capability: CapImageInput, Index: 3}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["code"] != "capability_unsupported" || m["capability"] != "image_input" {
		t.Fatalf("错误事件 JSON 形状错误: %s", b)
	}
}
