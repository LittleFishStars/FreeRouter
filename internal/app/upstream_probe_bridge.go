package app

// upstream_probe_bridge.go —— 凭据池页的「供应商可用性检验」（2026-10-31）。
//
// 对一个上游的全部 active 真实模型各发一次 max_tokens=1 的真实推理：复用与
// 业务流量**完全相同**的管线（newUpstreamShim → Caller → targetOf → 传输），
// 逐模型报告 通/不通 + 延迟 + 失败原因。与抽象模型的 /connectivity 的分工：
// 那边以「抽象模型」为单位（走路由候选与策略排序），这边以「供应商」为单位
// （遍历该上游的模型行）——凭据池页没有抽象模型的概念。
//
// 探测不落用量结算、不写冷却账本：写冷却会让两次点击看到不同的候选集，
// 伤害诊断的可重复性（真实流量的冷却口径不受影响——探测失败不经
// CandidateFailureNotifier 回传）。

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// upstreamProbeBridge 实现 httpapi.AdminUpstreamModelProber。
type upstreamProbeBridge struct {
	b   *adminBridge
	app *App
}

func newUpstreamProbeBridge(a *App) *upstreamProbeBridge {
	return &upstreamProbeBridge{b: newAdminBridge(a.store), app: a}
}

// probeOverallBudget 是整次检验的总预算：模型数多时串行推理可能拖长，
// 到点后余下模型如实标「预算用尽」而不是永远转圈。
const probeOverallBudget = 120 * time.Second

// probeModelBudget 是单个模型的推理预算：给足慢模型（推理型首字延迟），
// 又不让单个模型吃光总预算。
const probeModelBudget = 30 * time.Second

// ProbeUpstreamModels 对上游的全部 active 真实模型逐个真实请求一次。
//
// targetOf 按**行**组装（不经全局 UpstreamModelID 匹配）：多上游同名模型时
// 全局匹配会打到错的行——供应商探测明确知道自己在测哪一行。
func (p *upstreamProbeBridge) ProbeUpstreamModels(ctx context.Context, id string) (*httpapi.UpstreamModelProbeResult, error) {
	u, err := p.b.repos.Models.GetUpstreamByID(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	adapter := adapterForKind(u.Kind)
	if adapter == nil {
		return nil, fmt.Errorf("app: 上游 %s 的 kind %q 无适配器", u.Name, u.Kind)
	}
	models, err := p.b.repos.Models.ListRealModelsByUpstream(ctx, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	httpT := upstream.NewHTTPTransport(nil, p.app.secretSource())
	relayT := upstream.NewRelayTransport(p.app.sidecar, p.app.credentialJSONSource(), p.app.credentialWriteBack())

	ctx, cancel := context.WithTimeout(ctx, probeOverallBudget)
	defer cancel()

	rows := make([]httpapi.UpstreamModelProbeRow, 0, len(models))
	okCount, failCount, skipCount := 0, 0, 0
	// cur 是当前正在探测的模型行：targetOf 闭包按它组装 Target（串行探测，
	// 一次一个；Caller 每模型重建一个——struct 级构造零成本，闭包不共享状态）。
	up := u
	var cur *store.RealModel
	targetOf := func(upstream.CallCandidate) (upstream.Target, error) {
		return upstream.TargetOf(upstream.CandidateInfo{
			UpstreamID:   up.ID,
			Kind:         upstream.Kind(up.Kind),
			BaseURL:      up.BaseURL,
			RealModel:    relayAwareRealModel(up, cur),
			SecretRef:    derefSecretID(up.SecretID),
			CredentialID: cur.ID,
		})
	}
	one := 1
	for i := range models {
		m := &models[i]
		row := httpapi.UpstreamModelProbeRow{RealModelID: m.ID, ModelID: m.UpstreamModelID}
		// 非 active / 被排除的模型不是「通的」也不是「坏的」——如实标跳过。
		switch {
		case m.Status != store.RealModelActive:
			row.Reason = "跳过：模型状态 " + string(m.Status)
			skipCount++
			rows = append(rows, row)
			continue
		case m.ExcludedFromCandidates:
			row.Reason = "跳过：已排除出候选"
			skipCount++
			rows = append(rows, row)
			continue
		case ctx.Err() != nil:
			row.Reason = "跳过：检验总预算用尽"
			skipCount++
			rows = append(rows, row)
			continue
		}
		cur = m
		caller := upstream.NewCaller(relayT, httpT, targetOf)
		cand := upstream.CallCandidate{
			UpstreamModelID: m.UpstreamModelID,
			// Display 与路由层的落点描述同构（渠道/模型#行id）——检验结果里
			// 看到的落点与真实流量 trace 一致。
			Display: fmt.Sprintf("%s/%s#%s", channelOf(m.Channel, *up), m.UpstreamModelID, m.ID),
			Adapter: adapter,
		}
		// ping 请求：max_tokens=1 压成本；模型用真实模型 id（业务流量同款）。
		probe := ir.Request{
			Model:    m.UpstreamModelID,
			Messages: []ir.Message{{Role: ir.RoleUser, Parts: []ir.Part{ir.TextPart{Text: "ping"}}}},
			Sampling: ir.Sampling{MaxOutput: &one},
			Stream:   false,
		}
		mctx, mcancel := context.WithTimeout(ctx, probeModelBudget)
		start := time.Now()
		call, callErr := caller.Call(mctx, cand, probe, false)
		switch {
		case callErr != nil:
			attemptProbeReason(&row, callErr.Error())
		case call.Err != nil:
			attemptProbeReason(&row, call.Err.Error())
		default:
			raw, rerr := io.ReadAll(io.LimitReader(call.Body, 1<<20))
			_ = call.Body.Close()
			switch {
			case rerr != nil:
				attemptProbeReason(&row, "读响应失败："+rerr.Error())
			default:
				resp, derr := cand.Adapter.DecodeResponse(raw)
				if derr != nil {
					// 原文摘要必须带：不少「OpenAI 兼容」网关把业务错误以
					// HTTP 200 + {code,desc} 信封返回（loomy 实测），只报
					// 「协议不符」等于没诊断。body 是网关侧响应，不含凭据。
					snippet := string(raw)
					if len(snippet) > 200 {
						snippet = snippet[:200]
					}
					attemptProbeReason(&row, "响应解析失败："+derr.Error()+"；body: "+snippet)
				} else {
					row.OK = true
					if resp.Usage.InputTokens != nil || resp.Usage.OutputTokens != nil {
						t := 0
						if resp.Usage.InputTokens != nil {
							t += *resp.Usage.InputTokens
						}
						if resp.Usage.OutputTokens != nil {
							t += *resp.Usage.OutputTokens
						}
						row.Tokens = &t
					}
				}
			}
		}
		row.LatencyMs = time.Since(start).Milliseconds()
		mcancel()
		rows = append(rows, row)
		if row.OK {
			okCount++
		} else {
			failCount++
		}
	}
	return &httpapi.UpstreamModelProbeResult{
		UpstreamID: id,
		Name:       u.Name,
		Kind:       u.Kind,
		OK:         okCount,
		Failed:     failCount,
		Skipped:    skipCount,
		Results:    rows,
	}, nil
}

// relayAwareRealModel 组装 relay 行的 RealModel（provider 前缀，业务路径的
// targetOf 同款）；vendor/selfhosted 原样。
func relayAwareRealModel(u *store.Upstream, m *store.RealModel) string {
	if u.Kind == "relay" && upstream.IsRelayBaseURL(u.BaseURL) {
		return strings.TrimPrefix(u.BaseURL, upstream.RelayBaseURLPrefix) + "/" + m.UpstreamModelID
	}
	return m.UpstreamModelID
}

// derefSecretID 解引用密文 id（nil = 无凭据，返回空串——无鉴权 selfhosted
// 的合法形态）。
func derefSecretID(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// attemptProbeReason 统一失败原因的落点（OK=false + reason）。
func attemptProbeReason(row *httpapi.UpstreamModelProbeRow, reason string) {
	row.OK = false
	row.Reason = reason
}
