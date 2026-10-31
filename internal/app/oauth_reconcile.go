// oauth_reconcile.go —— OAuth 贡献上游行的形态对齐（2026-10-31 批量移植）。
//
// 背景：noOpenAI 渠道批量移植后（registry 声明 kind=relay），**已落库的行**
// 还是老形态（vendor + 占位/手改 URL）。靠 re-auth 翻不了——UpsertUpstream 的
// 冲突目标是 (kind,name)，kind 变了会另起一行，旧行连同密文与模型挂成孤儿；
// 用户手改 base_url / 手动启用更会让行处于「看起来开了、路径全错」的状态
// （真库实测：minimax 被手改成 api.minimax.chat、qodercn 停在
// oauth-only.invalid 且 enabled=1）。
//
// 所以移植的最后一公里是**原地翻转**：sidecar 清单声明 kind=relay 的
// provider，其贡献行（名字 = <provider>-oauth-<指纹前8>）翻成
// kind=relay + base_url=sidecar://<provider>。enabled 不动——它同时是属主的
// 「停止贡献」开关，对齐器没有资格替属主做启停决定（真库里这几行恰好都是
// enabled，但那是用户自己开的，不是这里的功劳）。
//
// 只碰 relay 化这一种翻转：vendor 行的 base_url 是管理员可管事实
// （cline/loomy/raccoon 的直连端点），对齐器不改 vendor 行的任何字段。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// oauthEndpointDecl 是 sidecar 清单里一家 provider 的形态声明（对齐依据）。
type oauthEndpointDecl struct {
	Kind    string
	APIBase string
}

// reconcileOAuthUpstreamsFromSidecar 从 sidecar 清单取声明并跑一轮对齐。
// sidecar 不可用时静默跳过（下一次启动再试；行保持老形态，推理本来就不可用）。
func (a *App) reconcileOAuthUpstreamsFromSidecar(ctx context.Context) {
	if a.sidecar == nil {
		return
	}
	if st := a.sidecar.Status(); !st.Available {
		return
	}
	raw, err := a.sidecar.Call(ctx, "oauth.list", map[string]any{})
	if err != nil {
		a.log.Warn("app: OAuth 行对齐取清单失败（跳过本轮）", "error", err)
		return
	}
	var list struct {
		Providers []sidecarProvider `json:"providers"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		a.log.Warn("app: OAuth 行对齐解析清单失败（跳过本轮）", "error", err)
		return
	}
	decl := make(map[string]oauthEndpointDecl, len(list.Providers))
	for _, p := range list.Providers {
		decl[p.ID] = oauthEndpointDecl{Kind: p.Kind, APIBase: p.APIBase}
	}
	flipped, err := a.reconcileOAuthUpstreams(ctx, decl)
	if err != nil {
		a.log.Warn("app: OAuth 行对齐失败", "error", err)
		return
	}
	if len(flipped) > 0 {
		a.log.Info("app: OAuth 贡献行已对齐到 relay 形态", "rows", flipped)
	}
}

// reconcileOAuthUpstreams 把声明为 relay 的 provider 的贡献行原地翻转为
// relay 形态，返回翻转的行名列表。只翻 kind != relay 的行；已对齐的行幂等跳过。
func (a *App) reconcileOAuthUpstreams(ctx context.Context, decl map[string]oauthEndpointDecl) ([]string, error) {
	// 只收 relay 声明：vendor 行不归对齐器管（base_url 是管理员可管事实）。
	relay := make(map[string]oauthEndpointDecl, len(decl))
	for id, d := range decl {
		if d.Kind == "relay" && strings.HasPrefix(d.APIBase, "sidecar://") {
			relay[id] = d
		}
	}
	if len(relay) == 0 {
		return nil, nil
	}
	ups, err := a.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: 列上游失败: %w", err)
	}
	var flipped []string
	for _, u := range ups {
		provider, ok := oauthRowProvider(u.Name, relay)
		if !ok || u.Kind == "relay" {
			continue
		}
		want := relay[provider]
		// 顺序固定：先改 base_url 再翻 kind——SetUpstreamKind 对 relay 有
		// 「base_url 必须已是 sidecar:// 形态」的写入闸，反序会被拒。
		// base_url 对 relay 的语义是「sidecar 内的 provider 锚点」——必须锚到
		// adapter 的注册键（provider id），不是行名（minimax-oauth-key_xxx 不是
		// ADAPTERS 的键，锚错一行都调不通）。
		if err := a.store.Repos().Models.UpdateUpstream(ctx, u.ID, u.Name, want.APIBase, u.Enabled); err != nil {
			return flipped, fmt.Errorf("app: 改写 %s base_url 失败: %w", u.Name, err)
		}
		if err := a.store.Repos().Models.SetUpstreamKind(ctx, u.ID, "relay"); err != nil {
			return flipped, fmt.Errorf("app: 翻转 %s kind 失败: %w", u.Name, err)
		}
		flipped = append(flipped, u.Name)
	}
	return flipped, nil
}

// oauthRowProvider 从贡献行的确定性名字反查 provider id：
// "<provider>-oauth-<指纹前8>" / "<provider>-contrib-<指纹前8>"（两代命名）。
// 只认「前缀命中声明表」的行——provider 本身带连字符时（当前清单没有），
// 前缀匹配按声明表的键逐一尝试，天然取最长命中前的确切键。
func oauthRowProvider(name string, relay map[string]oauthEndpointDecl) (string, bool) {
	for _, suffix := range []string{"-oauth-", "-contrib-"} {
		if !strings.Contains(name, suffix) {
			continue
		}
		cand := strings.SplitN(name, suffix, 2)[0]
		if _, ok := relay[cand]; ok {
			return cand, true
		}
	}
	return "", false
}
