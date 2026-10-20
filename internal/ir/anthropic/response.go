package anthropic

import (
	"encoding/json"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
)

// DecodeResponse 解析非流式响应。
func (adapter) DecodeResponse(body []byte) (*ir.Response, error) {
	var raw response
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &ir.InvalidRequestError{Param: "body", Detail: "不是合法 JSON：" + err.Error()}
	}
	// Anthropic 用 200 + error 体外带内错的情况极少，但兼容层存在：先判 error 字段。
	if raw.Error != nil {
		return nil, &ir.Error{Code: raw.Error.Type, Message: raw.Error.Message}
	}
	resp := &ir.Response{ID: raw.ID, Model: raw.Model}
	parts := make([]ir.Part, 0, len(raw.Content))
	for i, b := range raw.Content {
		p, err := decodeBlock(b, -1, i)
		if err != nil {
			return nil, err
		}
		if p != nil {
			parts = append(parts, p)
		}
	}
	resp.Parts = parts
	stop, _ := stopReasonToStop(raw.StopReason)
	resp.Stop = stop
	if raw.Usage != nil {
		resp.Usage = decodeUsage(raw.Usage)
	} else {
		resp.Usage = ir.Usage{Source: ir.UsageUnknown}
	}
	return resp, nil
}

// decodeUsage 归一 Anthropic 的 usage。
//
// 三态保持（§9）：Anthropic 必报 input/output tokens，但 cache 两项是可缺的——
// 缺失就是 nil，绝不能当 0：缓存命中与否直接影响对账。
func decodeUsage(u *usage) ir.Usage {
	out := ir.Usage{Source: ir.UsageUpstream}
	out.InputTokens = u.InputTokens
	out.OutputTokens = u.OutputTokens
	out.CacheReadTokens = u.CacheReadInputTokens
	out.CacheWriteTokens = u.CacheCreationInputTokens
	return out
}

// EncodeResponse 把 IR 响应编码成 Messages 响应体。
func (adapter) EncodeResponse(resp *ir.Response) ([]byte, error) {
	if resp == nil {
		return nil, &ir.InvalidRequestError{Param: "response", Detail: "响应为空"}
	}
	blocks := make([]contentBlock, 0, len(resp.Parts))
	for _, p := range resp.Parts {
		b, err := encodePart(p, -1)
		if err != nil {
			return nil, err
		}
		if b != nil {
			blocks = append(blocks, *b)
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, contentBlock{Type: "text", Text: ""})
	}
	out := response{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		Content:    blocks,
		StopReason: stopToStopReason(resp.Stop),
		Usage:      encodeUsage(resp.Usage),
	}
	if out.ID == "" {
		out.ID = "msg_freerouter"
	}
	return json.Marshal(out)
}

// encodeUsage 反向编码。全 nil 时仍给出对象：Anthropic 客户端普遍假设 usage 存在，
// 且 input/output 至少要有 0 值以外的选择吗？——不：nil 就省略字段（三态），
// 整个 usage 都没有时省略整个对象，让客户端显示 "—" 而不是虚报 0 消耗。
func encodeUsage(u ir.Usage) *usage {
	if u.InputTokens == nil && u.OutputTokens == nil && u.CacheReadTokens == nil && u.CacheWriteTokens == nil {
		return nil
	}
	return &usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
	}
}
