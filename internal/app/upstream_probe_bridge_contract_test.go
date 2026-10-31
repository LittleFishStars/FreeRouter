// upstream_probe_bridge_contract_test.go —— 凭据池页「供应商可用性检验」的
// 真库契约：停用上游整单跳过（2026-10-31 实测修）。
//
// 背景：noOpenAI 渠道（minimax/qoder 系）落库即停用——端点不吐 OpenAI 兼容
// 响应。修复前探针不看 enabled，逐模型打真实请求收获一堆注定 404 的噪音
// （真库实测 0 可达 / 4 不通）。修复后：整单跳过 + 理由逐模型给出。
package app

import (
	"context"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

func TestContractProbeSkipsDisabledUpstream(t *testing.T) {
	a := newCandidateInstanceTestApp(t)
	probe := newUpstreamProbeBridge(a)

	ctx := context.Background()
	// 停用的 vendor 上游 + 两条 active 模型（修复前会对它们各发一次真实请求）。
	uid, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "minimax-oauth-key_deadbeef",
		"https://agent.minimax.example", nil, false)
	if err != nil {
		t.Fatalf("建停用上游失败：%v", err)
	}
	for _, m := range []string{"MiniMax-M2.7", "MiniMax-M3"} {
		if _, err := a.store.Repos().Models.UpsertRealModel(ctx, store.RealModel{
			UpstreamID: uid, UpstreamModelID: m, DisplayName: m,
		}); err != nil {
			t.Fatalf("建模型 %s 失败：%v", m, err)
		}
	}

	res, err := probe.ProbeUpstreamModels(ctx, uid)
	if err != nil {
		t.Fatalf("检验应成功返回（跳过不是错误）：%v", err)
	}
	if res.Skipped != 2 || res.OK != 0 || res.Failed != 0 {
		t.Fatalf("应整单跳过 2 条且不计通/不通，得 ok=%d failed=%d skipped=%d",
			res.OK, res.Failed, res.Skipped)
	}
	for _, r := range res.Results {
		if !strings.Contains(r.Reason, "上游已停用") {
			t.Errorf("模型 %s 的理由应说明上游已停用，得 %q", r.ModelID, r.Reason)
		}
	}

	// 启用后恢复可检验：不再整单跳过（走到真实请求路径，此处会因假端点失败，
	// 但失败语义是「不通」而不是「跳过」——这正是启用的意义）。
	if _, err := a.store.Repos().Models.UpsertUpstream(ctx, "vendor", "minimax-oauth-key_deadbeef",
		"https://agent.minimax.example", nil, true); err != nil {
		t.Fatalf("启用上游失败：%v", err)
	}
	res2, err := probe.ProbeUpstreamModels(ctx, uid)
	if err != nil {
		t.Fatalf("启用后检验失败：%v", err)
	}
	if res2.Skipped != 0 || res2.Failed != 2 {
		t.Fatalf("启用后应走真实请求（2 条不通、0 跳过），得 failed=%d skipped=%d",
			res2.Failed, res2.Skipped)
	}
	for _, r := range res2.Results {
		if strings.Contains(r.Reason, "上游已停用") {
			t.Errorf("启用后不应再报停用跳过，得 %q", r.Reason)
		}
	}
}
