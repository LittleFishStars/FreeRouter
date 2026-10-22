package meter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// QuotaStore → internal/store 仓储的 adapter（M5 装配的对接层）。
//
// 为什么 adapter 在 meter 而不是在 httpapi：它翻译的正是 meter.QuotaRow /
// auth.PoolRef 的语义（哨兵、当期行、合计行），离语义定义处越近，漂移越
// 容易被本包的契约测试抓到；httpapi 只该关心 HTTP 形状。
//
// 为什么现在允许 import internal/store：task-5 期间 storage 分支未合入，
// meter 只能凭窄接口独立编译；main 已合入 store（merge 189d4ce），
// adapter 作为**装配层**正是窄接口存在的目的——判定面（quota.go/limiter.go）
// 依旧只认数字、不认仓储，这条边界不因 adapter 的存在而软化。
//
// id 口径：store 的主键是 int64；meter.QuotaRow.ModelID 是 string。
// 这里**不**把模型名翻译进 Row——429 热路径不碰模型表（lead 定案的分层
// 先验），名字由错误组装侧（httpapi）拿 QuotaDetail.Model（此时是 id 字符串）
// 按需解析。ModelID 刻意用 strconv 而不是查表，保持 adapter 零额外读取。

// StoreAdapter 把 store.QuotaRepo + 组成员关系接到 meter.QuotaStore。
type StoreAdapter struct {
	quotas *store.QuotaRepo
	groups groupMemberReader
}

// groupMemberReader 是"账户 → 所属组 id 列表"的最小读面。
//
// 为什么单独一个接口而不把 GroupRepo 全量搬来：GroupPools 只需要
// "这个账户在哪些组"（group_members 一条 JOIN），策略键值的解析
// （can_create_key 等）是 authz 装配的事，额度域不需要。
type groupMemberReader interface {
	GroupIDsOfAccount(ctx context.Context, accountID int64) ([]int64, error)
}

// NewStoreAdapter 构造。参数任一为 nil 会在第一次调用时返回 internal_error。
func NewStoreAdapter(quotas *store.QuotaRepo, groups groupMemberReader) *StoreAdapter {
	return &StoreAdapter{quotas: quotas, groups: groups}
}

// AccountQuotaRows 实现 meter.QuotaStore。
//
// 语义对齐点（docs/security/01-accounts.md 行化定案）：
//   - 无行 = 该维度不限：由"返回空切片"表达，PrecheckQuota 的遍历天然跳过；
//   - store 的 QuotaState.Limit 恒 ≥ 0（"不限"是删行，不是 -1）——
//     负 Limit 若真的出现（不该可能，DDL 无此形状）按装配错误上抛，
//     不静默吞掉（额度域的 -1 语义陷阱，评审定案）；
//   - period_anchor 已过期的行：store 读路径保证 Used=0 与当期锚点
//     （惰性重置），adapter 不再做二次归零——**单一职责**，归零只有
//     store 一个写点，adapter 只搬运。
func (a *StoreAdapter) AccountQuotaRows(ctx context.Context, accountID string) ([]QuotaRow, error) {
	if a.quotas == nil {
		return nil, auth.Errorf(auth.CodeInternal, nil, "StoreAdapter 缺少 QuotaRepo")
	}
	id, err := parseID(accountID, "account")
	if err != nil {
		return nil, err
	}
	states, err := a.quotas.ListAccountQuotas(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("meter adapter: 读账户额度行: %w", err)
	}
	rows := make([]QuotaRow, 0, len(states))
	for _, st := range states {
		if st.Limit < 0 {
			return nil, auth.Errorf(auth.CodeInternal, nil,
				"store 返回负额度上限 %d（额度域不允许 -1；行化语义=无行不限）", st.Limit)
		}
		p := Period(st.Period)
		if !p.Valid() {
			return nil, auth.Errorf(auth.CodeInternal, nil, "store 返回未知周期 %q", st.Period)
		}
		rows = append(rows, QuotaRow{
			Total:   st.AbstractModelID == store.TotalScope,
			ModelID: modelIDString(st.AbstractModelID),
			Unit:    string(st.Unit),
			Period:  p,
			Limit:   st.Limit,
			Used:    st.Used,
		})
	}
	return rows, nil
}

// GroupPools 实现 meter.QuotaStore：账户所属**全部**组的池快照。
//
// 为什么逐组 ListPoolsByGroup 而不是写一条大 JOIN：池行数 = 组数 × (unit,
// period, model) 组合数，量级个位数起步；而"组归属"将来要走缓存
// （03-groups §7），一次成员读 + N 次池读的结构让缓存替换只影响第一跳。
// 快照仅供 PrecheckQuota 的**预检**——真扣减走 store.TryDebitPool 的条件
// UPDATE（判定即扣减，免双花），快照旧一点最多造成一次多余的失败重试，
// 不会超发。
func (a *StoreAdapter) GroupPools(ctx context.Context, accountID string) ([]auth.PoolRef, error) {
	if a.quotas == nil || a.groups == nil {
		return nil, auth.Errorf(auth.CodeInternal, nil, "StoreAdapter 缺少仓储依赖")
	}
	id, err := parseID(accountID, "account")
	if err != nil {
		return nil, err
	}
	gids, err := a.groups.GroupIDsOfAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("meter adapter: 读账户组归属: %w", err)
	}
	var pools []auth.PoolRef
	for _, gid := range gids {
		states, err := a.quotas.ListPoolsByGroup(ctx, gid)
		if err != nil {
			return nil, fmt.Errorf("meter adapter: 读组 %d 池: %w", gid, err)
		}
		for _, st := range states {
			if st.Limit <= 0 {
				return nil, auth.Errorf(auth.CodeInternal, nil,
					"组 %d 池上限非法 %d（DDL CHECK pool_limit>0，出现即数据损坏）", gid, st.Limit)
			}
			pools = append(pools, auth.PoolRef{
				GroupID:         fmt.Sprintf("%d", gid),
				AbstractModelID: st.AbstractModelID,
				Unit:            string(st.Unit),
				Period:          string(st.Period),
				Limit:           st.Limit,
				Used:            st.Used,
			})
		}
	}
	return pools, nil
}

// DebitContext 是一次"判定即扣减"的完整描述（供 mw_quota 调用）。
//
// 为什么 adapter 上再给一个扣减入口而不是让中间件直接调 store：
// 错误翻译（ErrQuotaExhausted → auth 哨兵 + quota 四元组 + ResetAt 的
// Retry-After）是纯映射逻辑，收在 adapter 一处，中间件只认 auth 错误——
// 否则每条扣减调用点都要重抄一遍翻译，抄漏一处就是一个不带 quota 对象的
// 429（客户端无法自动降级）。
type DebitContext struct {
	AccountID       string
	AbstractModelID int64 // 0 = 只扣合计行
	GroupIDs        []int64
	Cost            UsageCost
}

// TryDebit 对合计行 + 命中模型行逐 (unit, period) 执行条件扣减。
//
// 顺序：先合计（mid=0）后模型（mid>0）——两道闸独立生效，第一个拒的
// 决定错误内容；已扣的行**不回滚**：条件 UPDATE 每行独立原子，先扣的
// 合计行多扣的量恰好等于"这个请求确实发起过"的计量事实（03-groups §4
// 的定案：多扣比不扣危险的方向论述见 quota.go Ledger 注释，此处取
// "失败请求不扣"由调用方保证 cost 只在终态传入）。
//
// 返回的 error 是 auth.Error（Code=quota_exceeded/pool_exhausted），
// 携带 QuotaDetail{Unit,Period,Model} 与 Retry-After=命中行重置时刻；
// 无行 = 不限 = 直接放行。
func (a *StoreAdapter) TryDebit(ctx context.Context, dc DebitContext) error {
	if a.quotas == nil {
		return auth.Errorf(auth.CodeInternal, nil, "StoreAdapter 缺少 QuotaRepo")
	}
	accID, err := parseID(dc.AccountID, "account")
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	// 账户额度：先合计行（mid=0）再按模型行——与 PrecheckQuota 的行遍历
	// 同一顺序，保证"预检拒的行"与"扣减拒的行"是同一道闸。
	// mid 去重：AbstractModelID=TotalScope（无按模型行）时列表是 [0,0]，
	// 不去重会对合计行**扣两次**——每次请求凭空吃掉两份额度（复现于
	// TestContractAdapterDebitsRealStore：limit=2 第一发后 used 即 2）。
	mids := []int64{store.TotalScope}
	if dc.AbstractModelID != store.TotalScope {
		if dc.AbstractModelID < 0 {
			return auth.Errorf(auth.CodeInternal, nil, "模型 id 非法 %d", dc.AbstractModelID)
		}
		mids = append(mids, dc.AbstractModelID)
	}
	for _, mid := range mids {
		for _, up := range []struct{ unit, period string }{
			{"request", "day"}, {"request", "month"},
			{"token", "day"}, {"token", "month"},
			{"credit", "day"}, {"credit", "month"},
		} {
			cost, ok := dc.Cost.costFor(up.unit)
			if !ok || cost == 0 {
				continue // 该维度无消耗即无行可扣（无行=不限）
			}
			err := a.quotas.TryDebitAccountQuota(ctx, accID, mid, store.QuotaUnit(up.unit), store.QuotaPeriod(up.period), cost)
			if err == nil {
				continue
			}
			if qe, ok := asQuotaExhausted(err); ok {
				return translateQuota(qe, dc.AbstractModelID, now)
			}
			return fmt.Errorf("meter adapter: 扣账户额度: %w", err)
		}
	}

	// 组池：逐组逐 (unit, period, mid) 扣。池是 ALL_AND（03-groups §4），
	// 全部通过才算过；先扣后拒时已扣池不回滚（与账户行同理）。
	for _, gid := range dc.GroupIDs {
		for _, up := range []struct{ unit, period string }{
			{"request", "day"}, {"request", "month"},
			{"token", "day"}, {"token", "month"},
			{"credit", "day"}, {"credit", "month"},
		} {
			cost, ok := dc.Cost.costFor(up.unit)
			if !ok || cost == 0 {
				continue
			}
			for _, mid := range mids {
				err := a.quotas.TryDebitPool(ctx, gid, mid, store.QuotaUnit(up.unit), store.QuotaPeriod(up.period), cost)
				if err == nil {
					continue
				}
				if qe, ok := asQuotaExhausted(err); ok {
					return translatePool(qe, gid, dc.AbstractModelID, now)
				}
				return fmt.Errorf("meter adapter: 扣组 %d 池: %w", gid, err)
			}
		}
	}
	return nil
}

// Credit 对合计行 + 命中模型行 + 组池做**无条件记账**（终态结算，
// store.CreditQuota 语义）。与 TryDebit 的区别：不做闸门判定——
// 结算时额度可能已越过上限（预扣估计偏差 + 并发），那是
// 05-storage 7.4"本周期总量可能超上限"的既定结果；少扣方向可接受，
// 但**必须记账**（不记会让对账与额度护栏同时失真）。无行 = 该维度
// 不限 = 跳过（与 TryDebit 的无行语义一致）。
func (a *StoreAdapter) Credit(ctx context.Context, cc CreditContext) error {
	if a.quotas == nil {
		return auth.Errorf(auth.CodeInternal, nil, "StoreAdapter 缺少 QuotaRepo")
	}
	accID, err := parseID(cc.AccountID, "account")
	if err != nil {
		return err
	}
	mids := []int64{store.TotalScope}
	if cc.AbstractModelID != store.TotalScope {
		if cc.AbstractModelID < 0 {
			return auth.Errorf(auth.CodeInternal, nil, "模型 id 非法 %d", cc.AbstractModelID)
		}
		mids = append(mids, cc.AbstractModelID)
	}
	// 账户行：每 (mid, unit, period) 记一次（CreditQuota 内部逐条 upsert）。
	for _, mid := range mids {
		var credits []store.QuotaCredit
		for _, up := range []struct{ unit, period string }{
			{"request", "day"}, {"request", "month"},
			{"token", "day"}, {"token", "month"},
			{"credit", "day"}, {"credit", "month"},
		} {
			cost, ok := cc.Cost.costFor(up.unit)
			if !ok || cost == 0 {
				continue
			}
			credits = append(credits, store.QuotaCredit{
				OwnerID:         accID,
				AbstractModelID: mid,
				Unit:            store.QuotaUnit(up.unit),
				Period:          store.QuotaPeriod(up.period),
				Cost:            cost,
			})
		}
		if len(credits) > 0 {
			if err := a.quotas.CreditQuota(ctx, credits, nil); err != nil {
				return fmt.Errorf("meter adapter: 记账户额度: %w", err)
			}
		}
	}
	// 组池：同形无条件记账。
	for _, gid := range cc.GroupIDs {
		var credits []store.QuotaCredit
		for _, up := range []struct{ unit, period string }{
			{"request", "day"}, {"request", "month"},
			{"token", "day"}, {"token", "month"},
			{"credit", "day"}, {"credit", "month"},
		} {
			cost, ok := cc.Cost.costFor(up.unit)
			if !ok || cost == 0 {
				continue
			}
			for _, mid := range mids {
				credits = append(credits, store.QuotaCredit{
					OwnerID:         gid,
					AbstractModelID: mid,
					Unit:            store.QuotaUnit(up.unit),
					Period:          store.QuotaPeriod(up.period),
					Cost:            cost,
				})
			}
		}
		if len(credits) > 0 {
			if err := a.quotas.CreditQuota(ctx, nil, credits); err != nil {
				return fmt.Errorf("meter adapter: 记组 %d 池: %w", gid, err)
			}
		}
	}
	return nil
}

var _ QuotaLedger = (*StoreAdapter)(nil)

// translateQuota 把 store.ErrQuotaExhausted 翻成 auth 哨兵 + 四元组。
func translateQuota(qe store.ErrQuotaExhausted, modelID int64, now time.Time) error {
	e := ErrQuotaExceeded.WithQuota(
		string(qe.Unit), string(qe.Period), modelIDString(modelID))
	if reset, ok := qe.ResetAt(); ok {
		if d := reset.Sub(now); d > 0 {
			e = e.WithRetryAfter(d)
		}
	}
	return e
}

// translatePool 同形翻译组池耗尽（GroupID 只进日志不进响应体）。
func translatePool(qe store.ErrQuotaExhausted, groupID, modelID int64, now time.Time) error {
	e := ErrPoolExhausted.
		WithQuota(string(qe.Unit), string(qe.Period), modelIDString(modelID)).
		WithDetail(string(qe.Unit), fmt.Sprintf("%d", groupID))
	if reset, ok := qe.ResetAt(); ok {
		if d := reset.Sub(now); d > 0 {
			e = e.WithRetryAfter(d)
		}
	}
	return e
}

// asQuotaExhausted 提取 store 的额度耗尽错误（值类型，errors.As）。
func asQuotaExhausted(err error) (store.ErrQuotaExhausted, bool) {
	var qe store.ErrQuotaExhausted
	if errors.As(err, &qe) {
		return qe, true
	}
	return qe, false
}

// modelIDString 把 store 的 int64 模型 id 变成 meter 的 string 形态；
// 合计行（0）返回空串 = 错误体 quota.model 缺省（03-errors §6 定案）。
func modelIDString(mid int64) string {
	if mid == store.TotalScope {
		return ""
	}
	return fmt.Sprintf("%d", mid)
}

func parseID(s, kind string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(s, "%d", &id); err != nil || id <= 0 {
		return 0, auth.Errorf(auth.CodeInternal, nil, "meter adapter: 非法%s id %q（store 主键是 int64）", kind, s)
	}
	return id, nil
}
