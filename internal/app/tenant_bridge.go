package app

// tenant_bridge.go —— httpapi.TenantUsageStore 的装配桥（租户面读模型）。
//
// 与 console_routes.go 同一条纪律：只翻译不判定、字段逐个抄、错误原样透传。
// httpapi 不认识 store；这里是唯一同时认识两边的数据翻译层。
//
// 为什么账户 id 用字符串进出桥：Principal 里的 AccountID 是认证面的字符串
// 形状（auth.Account.ID），桥内 parseID 转成 store 的 int64 主键——两个面在
// 装配层翻译是既有约定（与 authLoginStores/accountProfileBridge 一致）。

import (
	"context"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

type tenantBridge struct {
	st   *store.Store
	reps *store.Repos
}

func newTenantBridge(st *store.Store) *tenantBridge {
	return &tenantBridge{st: st, reps: st.Repos()}
}

var _ httpapi.TenantUsageStore = (*tenantBridge)(nil)

func (b *tenantBridge) parse(accountID string) (int64, error) {
	return parseID(accountID)
}

func (b *tenantBridge) AccountSnapshot(ctx context.Context, accountID string) (string, int64, int64, int64, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return "", 0, 0, 0, err
	}
	a, err := b.reps.Accounts.GetAccount(ctx, id)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return a.DisplayName, a.RPMLimit, a.TPMLimit, a.MaxConcurrency, nil
}

func (b *tenantBridge) GroupNames(ctx context.Context, accountID string) ([]string, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return nil, err
	}
	gids, err := b.reps.Groups.GroupIDsOfAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, gid := range gids {
		g, err := b.reps.Groups.GetGroup(ctx, gid)
		if err != nil {
			continue // 组在遍历中被删：跳过该行（展示面不因装饰失败 500）
		}
		names = append(names, g.Name)
	}
	return names, nil
}

func (b *tenantBridge) KeyStats(ctx context.Context, accountID string) (int64, int64, int64, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return 0, 0, 0, err
	}
	keys, err := b.reps.Keys.ListKeysByAccount(ctx, id)
	if err != nil {
		return 0, 0, 0, err
	}
	var total, active int64
	for _, k := range keys {
		total++
		if k.Status == store.KeyActive {
			active++
		}
	}
	used7d, err := b.reps.Usage.CountKeysUsedSince(ctx, id, time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return total, active, 0, err
	}
	return total, active, used7d, nil
}

func (b *tenantBridge) QuotaRows(ctx context.Context, accountID string) ([]httpapi.UsageQuotaRow, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return nil, err
	}
	states, err := b.reps.Quotas.ListAccountQuotas(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.UsageQuotaRow, 0, len(states))
	for _, q := range states {
		name := ""
		if q.AbstractModelID > 0 {
			if m, err := b.reps.Models.GetAbstractModel(ctx, q.AbstractModelID); err == nil {
				name = m.Name
			}
		}
		out = append(out, httpapi.UsageQuotaRow{
			ModelID: q.AbstractModelID,
			Model:   name,
			Unit:    string(q.Unit),
			Period:  string(q.Period),
			Limit:   q.Limit,
			Used:    q.Used,
			Anchor:  q.PeriodAnchor,
		})
	}
	return out, nil
}

func (b *tenantBridge) RequestLogs(ctx context.Context, accountID string, f httpapi.UsageLogFilter) ([]httpapi.UsageLogRow, int64, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return nil, 0, err
	}
	// 字段逐个抄（本文件纪律）：httpapi 的筛选条件必须全部落到 store 的同形字段上，
	// 漏抄一个就是“前端发了、后端不读”的静默失效（审计 P3 的 error_code）。
	sf := store.RequestLogFilter{
		Limit: f.Limit, Offset: f.Offset, Failed: f.Failed, ErrorCode: f.ErrorCode,
	}
	if !f.From.IsZero() {
		sf.From = f.From.Unix()
	}
	if !f.To.IsZero() {
		sf.To = f.To.Unix()
	}
	if f.KeyID != nil {
		sf.KeyID = f.KeyID
	}
	if f.Model != "" {
		m, err := b.reps.Models.GetAbstractModelByName(ctx, f.Model)
		if err != nil {
			if err == store.ErrNotFound {
				// 名字不存在 = 该模型没有日志：返回空而不是错误。
				return []httpapi.UsageLogRow{}, 0, nil
			}
			return nil, 0, err
		}
		sf.ModelID = &m.ID
	}
	rows, total, err := b.reps.Usage.ListRequestLogsByAccount(ctx, id, sf)
	if err != nil {
		return nil, 0, err
	}
	out := make([]httpapi.UsageLogRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, httpapi.UsageLogRow{
			ID: row.ID, TS: row.TS, Endpoint: row.Endpoint,
			AbstractModel: row.AbstractModel,
			KeyID:         row.KeyID, KeyName: row.KeyName, KeyPrefix: row.KeyPrefix,
			Status: row.Status, HTTPStatus: row.HTTPStatus, ErrorCode: row.ErrorCode,
			RetryCount: row.RetryCount,
			TTFTMs:     row.TTFTMs, TotalMs: row.TotalMs,
			InTokens: row.InTokens, OutTokens: row.OutTokens,
		})
	}
	return out, total, nil
}

func (b *tenantBridge) SessionsOf(ctx context.Context, accountID string) ([]httpapi.UsageSessionRow, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return nil, err
	}
	rows, err := b.reps.Sessions.ListSessionsByAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.UsageSessionRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, httpapi.UsageSessionRow{
			ID: row.ID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
			IP: row.IP, UA: row.UA,
		})
	}
	return out, nil
}

func (b *tenantBridge) RevokeSession(ctx context.Context, accountID, sessionID string) error {
	id, err := b.parse(accountID)
	if err != nil {
		return err
	}
	// 归属校验：会话必须属于该账户（不确认他人会话的存在，404 同形）。
	rows, err := b.reps.Sessions.ListSessionsByAccount(ctx, id)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == sessionID {
			return b.reps.Sessions.DeleteSession(ctx, sessionID)
		}
	}
	return store.ErrNotFound
}

func (b *tenantBridge) AllowedModels(ctx context.Context, accountID string, keyID *string) ([]httpapi.UsageModelRow, error) {
	id, err := b.parse(accountID)
	if err != nil {
		return nil, err
	}
	allowed := map[int64]bool{}
	for _, gid := range mustList(b.reps.Groups.GroupIDsOfAccount(ctx, id)) {
		for _, mid := range mustList(b.reps.Groups.ListGroupModels(ctx, gid)) {
			allowed[mid] = true
		}
	}
	if keyID != nil && *keyID != "" {
		kid, err := parseID(*keyID)
		if err != nil {
			return nil, store.ErrNotFound
		}
		allowIDs, err := b.reps.Keys.KeyAllowlist(ctx, kid)
		if err != nil {
			return nil, err
		}
		set := map[int64]bool{}
		for _, mid := range allowIDs {
			set[mid] = true
		}
		// Key 白名单是「只能收窄」的判定（05-keys.md §5）：交集为空 = 该 Key 不可用。
		for mid := range allowed {
			if !set[mid] {
				delete(allowed, mid)
			}
		}
	}
	models, err := b.reps.Models.ListAbstractModels(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.UsageModelRow, 0, len(models))
	for _, m := range models {
		if !allowed[m.ID] {
			continue
		}
		replacement := ""
		if m.ReplacementModelID != nil {
			if r, err := b.reps.Models.GetAbstractModel(ctx, *m.ReplacementModelID); err == nil {
				replacement = r.Name
			}
		}
		out = append(out, httpapi.UsageModelRow{
			Name: m.Name, DisplayName: m.DisplayName,
			Status: string(m.Status), Replacement: replacement,
		})
	}
	return out, nil
}

// mustList 把 (slice, err) 折成 slice：装饰性读失败降级为空，不让整页 500。
func mustList[T any](s []T, err error) []T {
	if err != nil {
		return nil
	}
	return s
}
