package meter

import (
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// ---------------------------------------------------------------------------
// 账户周期额度：account_quotas 行语义（01-accounts.md 2026-10-20 行化定案）
// ---------------------------------------------------------------------------
//
// 本域与**速率域（limiter.go）的哨兵语义刻意不同**（评审点名分开建模）：
//
//	速率域：accounts 三列，-1 = 不限，0 = 全拒（值里的 -1 有含义）。
//	额度域：account_quotas 行，**"不限" = 没有这一行**；有行则 limit ≥ 0，
//	        limit = 0 只表示"零额度全拒"（默认拒绝）。**-1 在额度域非法**——
//	 它一旦出现就是上游装配错误（把速率域的哨兵漏进了额度），本包对它
//	 返回 internal_error 而不是猜意图：额度域的每一次"当作不限"都是放闸。
// ---------------------------------------------------------------------------

// Period 是额度周期。day / month 是唯一事实源（03-groups.md §3）；
// week 不是文档周期，预留常量是为了让"任务板上的'日周月额度'"与文档
// day/month 的出入有一个显式的挂靠点——在 lead 对齐前，判定支持但不鼓励。
type Period string

const (
	PeriodDay   Period = "day"
	PeriodMonth Period = "month"
	// PeriodWeek 见 Period 注释：无文档事实源，组策略不应产生。
	PeriodWeek Period = "week"
)

// Valid 报告周期取值是否受支持。
func (p Period) Valid() bool {
	switch p {
	case PeriodDay, PeriodWeek, PeriodMonth:
		return true
	}
	return false
}

// Anchor 返回该周期在 now 时刻的锚点（惰性重置的键；对齐 account_quotas.period_anchor）。
//
// 为什么用 UTC 日界：03-groups.md §10 待定明确"当前与 usage_daily.day 用
// 同一套 UTC 日界"——个人额度、组池、全局熔断必须同刻归零，否则
// "额度早 8 点重置、熔断 UTC 零点重置"无法向管理员解释。
func (p Period) Anchor(now time.Time) string {
	t := now.UTC()
	switch p {
	case PeriodDay:
		return t.Format("2006-01-02")
	case PeriodWeek:
		y, w := t.ISOWeek()
		return "W" + itoa(y) + "-" + pad2(w)
	case PeriodMonth:
		// 与 store 的 periodAnchor 完全同口径（"当月 1 日"全日期格式，
		// 不是 "2006-01" 紧凑格式）：store 的 ErrQuotaExhausted.ResetAt
		// 用 time.Parse("2006-01-02", PeriodAnchor) 解析锚点，紧凑格式
		// 解析失败 → 脏锚点 → Retry-After 永远发不出。同口径是
		// task-14 的硬要求（只读 store 口径，不另算一套）。
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	default:
		return "unknown"
	}
}

// ResetAt 返回该周期下一次重置的时刻（quota_exceeded / pool_exhausted 的
// Retry-After 来源：docs/api/03-errors.md §5.2 "能算出重置时刻时给值"）。
func (p Period) ResetAt(now time.Time) time.Time {
	t := now.UTC()
	switch p {
	case PeriodDay:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	case PeriodWeek:
		// ISO 周：周一 00:00 UTC 为界。回退到本周一再加 7 天。
		offset := (int(t.Weekday()) + 6) % 7 // Monday=0 … Sunday=6
		day0 := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -offset)
		return day0.AddDate(0, 0, 7)
	case PeriodMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	default:
		return now.Add(time.Hour) // 未知周期：给一个不承诺的保守值
	}
}

// itoa/pad2 只服务 Anchor 的紧凑格式。
func itoa(n int) string { return strconv.Itoa(n) }

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// QuotaRow 是一条 account_quotas 行的进程内快照（由调用方从 store 读出）。
//
// 为什么 meter 不持有 QuotaRepository：判定所需数字全部由中间件组装层
// 读出传入（同 limiter.AccountLimits 的处理），本包保持"只认数字"的纯判定；
// 读接口的窄化形状归 storeiface/lead 清单，而不是 meter 的私有依赖。
type QuotaRow struct {
	// Total=true 对应 storage 的 abstract_model_id=0（合计行：
	// "这个人本周期总共能烧多少"）；false 是按模型行（"在 smart 上能烧多少"）。
	Total   bool
	ModelID string // Total=false 时必填；与请求的抽象模型 ID 对齐
	Unit    string // request / token / credit
	Period  Period
	Limit   int64 // 有行则 ≥ 0；0 = 零额度全拒；**负数非法**
	Used    int64 // 当期已用（锚点匹配 Period.Anchor(now) 的当期）
}

// UsageCost 是一次请求在三个计量维度上各消耗多少。
//
// 为什么一次传三维而不是单维：行化后同一账户可以同时挂着 request/day 与
// credit/month 两行——一次请求要**逐行按各自 unit 取对应成本**判定，
// 单维入参逼调用方"猜哪一维生效"，正是旧 QuotaUnit 单列的死因。
type UsageCost struct {
	Request int64 // 通常 = 1（"一次请求 = 1"，与 token 无关）
	Token   int64 // in + out（预检用估计值，结算用实际值）
	Credit  int64 // 按价格乘数折算（乘数表归 models/管理面）
}

// costFor 按行的 unit 取本次消耗。unit 不在三值内 = 装配错误。
func (c UsageCost) costFor(unit string) (int64, bool) {
	switch unit {
	case "request":
		return c.Request, true
	case "token":
		return c.Token, true
	case "credit":
		return c.Credit, true
	}
	return 0, false
}

// CheckQuotaRow 判定单行闸门：used+cost 是否越过 limit。
// 返回 (retryAfter, error)。error ∈ {ErrQuotaExceeded, internal_error(非法行)}。
//
// "无行 = 不限"由调用方的遍历表达（不在 rows 里的维度根本没有这一闸）；
// 本函数只回答"这一行拦不拦"。
func CheckQuotaRow(row QuotaRow, cost int64, now time.Time) (time.Duration, error) {
	if row.Limit < 0 {
		// 额度域的负数不是"不限"——那是速率域的哨兵。出现即上游装配错误，
		// 绝不能静默当"不限"：额度域每一次误放闸都是真金白银的上游消耗。
		return 0, auth.Errorf(auth.CodeInternal, nil,
			"account_quotas 行非法 limit=%d（额度域负数无意义；-1 是速率域哨兵，不得流入）", row.Limit)
	}
	if row.Limit == 0 {
		// 零额度全拒：连 cost=0 也拒——"额度 0 却能高频打无效请求"正是
		// 05-keys.md §4 要求速率由全局默认兜底、额度由 0 兜底的分工。
		return row.Period.ResetAt(now).Sub(now), ErrQuotaExceeded.
			WithQuota(row.Unit, string(row.Period), row.ModelID)
	}
	if row.Used+cost > row.Limit {
		return row.Period.ResetAt(now).Sub(now), ErrQuotaExceeded.
			WithQuota(row.Unit, string(row.Period), row.ModelID)
	}
	return 0, nil
}

// PrecheckQuota 执行判定链第 6 步的额度预检（04-authz.md §4）：
// 合计行（abstract_model_id=0）+ 命中本次模型的行，逐行判定，任一不过即拒。
//
// 顺序不做优化（先合计后模型）：行与行之间是"都生效的独立闸门"，
// 短路顺序只影响错误归因不影响结果；按 rows 出现顺序判，第一个拒的就是
// 返回给日志的维度（Dimension 字段）。
//
// 纯函数：不读存储、不调时钟（now 注入）。0 语义、无行跳过、超 1 个即拒
// 都是表驱动测试的直接断言面。
func PrecheckQuota(rows []QuotaRow, modelID string, cost UsageCost, now time.Time) (time.Duration, error) {
	for _, row := range rows {
		if !row.Total && row.ModelID != modelID {
			continue // 别的模型的限额行与本次请求无关
		}
		c, ok := cost.costFor(row.Unit)
		if !ok {
			return 0, auth.Errorf(auth.CodeInternal, nil, "额度行未知计量维度 %q", row.Unit)
		}
		if ra, err := CheckQuotaRow(row, c, now); err != nil {
			return ra, err
		}
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// 组级共享池（逐个都要满足；原子扣减在 store 的条件 UPDATE，
// 这里是纯判定与进程内结算视图）
// ---------------------------------------------------------------------------

// CheckPools 对全部池做"逐个都要满足"的判定（03-groups.md §4：ALL_AND，
// 不是 min——min 会丢掉"是哪个组的池先耗尽"的归因）。
//
// 返回第一个被拒的池：调用方要它的 GroupID 进日志/审计（不进响应体）。
// 池 limit 是严格正整数（"不设池 = 没有这一行"），PoolRef 快照里出现
// Limit<=0 与额度域同理是装配错误。
// 纯函数；生产路径的原子扣减是 store 的条件 UPDATE（判定即扣减）。
func CheckPools(pools []auth.PoolRef, cost int64) (denied *auth.PoolRef, err error) {
	for i := range pools {
		p := &pools[i]
		if p.Limit <= 0 {
			return nil, auth.Errorf(auth.CodeInternal, nil,
				"池 %s/%s/%s/%s 上限非法 %d（严格正整数；不设池=没有这一行）", p.GroupID, modelTag(p.AbstractModelID), p.Unit, p.Period, p.Limit)
		}
		if p.PoolExhausted(cost) {
			return p, ErrPoolExhausted.WithDetail(p.Unit, p.GroupID).
				WithQuota(p.Unit, p.Period, modelTag(p.AbstractModelID)).
				WithRetryAfter(periodReset(p.Period))
		}
	}
	return nil, nil
}

// periodReset 把池的周期字符串翻译成"距重置还有多久"（03-groups.md §4：
// 池耗尽的 Retry-After 指向 period_anchor + 1 日 / 1 月）。
func periodReset(period string) time.Duration {
	now := time.Now()
	p := Period(period)
	if !p.Valid() {
		p = PeriodDay
	}
	return p.ResetAt(now).Sub(now)
}

// ---------------------------------------------------------------------------
// 进程内结算视图（Ledger）
// ---------------------------------------------------------------------------

// Ledger 是"当期用量 + 池消耗"的进程内镜像，服务两个场景：
//  1. store（internal/store，storage 分支在写）接入前的测试与单机兜底；
//  2. 演示"先全判后全扣"的结算顺序——生产路径的原子性由 store 的
//     条件 UPDATE + 单写者事务承担，这里用同一把锁达成等价语义。
//
// 为什么不做周期重置的后台任务：重置是**惰性**的（03-groups.md §4）——
// 锚点进键，周期一切换旧键自然读不到，没有陈旧状态要清，也就没有
// 需要泄漏防护的 goroutine（06-runtime.md §3）。
type Ledger struct {
	mu  sync.Mutex
	now func() time.Time // 注入时钟：锚点判定必须可测试

	// usage 键 = accountID|modelTag|unit/period@anchor。modelTag：合计行 "Σ"。
	usage map[string]int64
	// pools 键 = groupID|unit/period（池锚点由 store 管理；进程内镜像按锚点进键）。
	pools map[string]int64
}

// NewLedger 构造空账本。
func NewLedger() *Ledger {
	return &Ledger{
		now:   time.Now,
		usage: make(map[string]int64),
		pools: make(map[string]int64),
	}
}

// WithClock 注入时钟（测试用）。
func (g *Ledger) WithClock(now func() time.Time) *Ledger {
	g.now = now
	return g
}

func (g *Ledger) usageKey(accountID, modelID, unit string, period Period) string {
	tag := modelID
	if modelID == "" {
		tag = "Σ" // 合计行标记：与 05-storage.md 的 abstract_model_id=0 同义
	}
	return accountID + "|" + tag + "|" + unit + "/" + string(period) + "@" + period.Anchor(g.now())
}

// SettleAccount 终态结算账户在某一行的用量（流结束后按实际用量扣）。
//
// modelID 传 "" = 合计行；非空 = 该模型行。调用方对**每个受影响的行各调一次**
// （合计行 + 命中模型行），与 PrecheckQuota 的行遍历对称。
// 失败请求（未产生 token）由调用方传 cost=0 或干脆不调——
// "没有消耗的尝试不扣"是 03-groups.md §4 的定案，在调用侧执行。
func (g *Ledger) SettleAccount(accountID, modelID, unit string, period Period, cost int64) {
	if cost == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.usage[g.usageKey(accountID, modelID, unit, period)] += cost
}

// Usage 返回某行当期已用（锚点过期自然读 0）。
func (g *Ledger) Usage(accountID, modelID, unit string, period Period) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.usage[g.usageKey(accountID, modelID, unit, period)]
}

// QuotaSnapshot 从账本构造某账户 + 本次模型的行快照（limits：
// row = (unit, period) → limit；不在 map 中的键 = 无行 = 不限）。
// 配合 PrecheckQuota 完成"账本自足"的单机判定路径。
func (g *Ledger) QuotaSnapshot(accountID, modelID string, limits map[auth.QuotaKey]int64, modelLimits map[string]map[auth.QuotaKey]int64) []QuotaRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	rows := make([]QuotaRow, 0, len(limits))
	for qk, limit := range limits {
		if limit < 0 {
			continue // 组策略上界 Unlimited：不落行（01-accounts.md：未设键 = -1 不落行）
		}
		rows = append(rows, QuotaRow{
			Total: true, Unit: qk.Unit, Period: Period(qk.Period), Limit: limit,
			Used: g.usage[g.usageKey(accountID, "", qk.Unit, Period(qk.Period))],
		})
	}
	if ml, ok := modelLimits[modelID]; ok {
		for qk, limit := range ml {
			if limit < 0 {
				continue
			}
			rows = append(rows, QuotaRow{
				ModelID: modelID, Unit: qk.Unit, Period: Period(qk.Period), Limit: limit,
				Used: g.usage[g.usageKey(accountID, modelID, qk.Unit, Period(qk.Period))],
			})
		}
	}
	return rows
}

// modelTag 把存储侧的合计哨兵转成错误体标识：合计池不填 model
// （docs/api/03-errors.md：pool 项 model:null=合计池）。UUID 改造后
// mid 已是 string，只剩哨兵翻译。
func modelTag(mid string) string {
	if mid == store.TotalScope {
		return ""
	}
	return mid
}

// poolKey 池键带锚点：period 字符串 + 当期 anchor（与 store 的 period_anchor 同义）。
func (g *Ledger) poolKey(groupID string, mid string, unit, period string) string {
	p := Period(period)
	if !p.Valid() {
		p = PeriodDay
	}
	return groupID + "|" + mid + "|" + unit + "/" + period + "@" + p.Anchor(g.now())
}

// ChargePools 对全部池做"逐个都要满足"的**原子**判定+扣减（进程内版本）。
//
// 为什么锁内一次做完：多池情况下"先扣 A 再判 B"在 B 拒绝时需要回滚 A；
// 锁内先全判后全扣，失败方一个字节都没写，不需要回滚逻辑——
// 而回滚写错的方向恰是多扣（无故停全组），比不扣危险（03-groups.md §4）。
func (g *Ledger) ChargePools(pools []auth.PoolRef, cost int64) (*auth.PoolRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for i := range pools {
		p := pools[i]
		if p.Limit <= 0 {
			return nil, auth.Errorf(auth.CodeInternal, nil,
				"池 %s/%s/%s/%s 上限非法 %d（严格正整数；不设池=没有这一行）", p.GroupID, modelTag(p.AbstractModelID), p.Unit, p.Period, p.Limit)
		}
		used := g.pools[g.poolKey(p.GroupID, p.AbstractModelID, p.Unit, p.Period)]
		if used+cost > p.Limit {
			return &p, ErrPoolExhausted.WithDetail(p.Unit, p.GroupID).
				WithQuota(p.Unit, p.Period, modelTag(p.AbstractModelID)).
				WithRetryAfter(periodReset(p.Period))
		}
	}
	for i := range pools {
		p := pools[i]
		g.pools[g.poolKey(p.GroupID, p.AbstractModelID, p.Unit, p.Period)] += cost
	}
	return nil, nil
}

// PoolUsed 返回池的当期消耗（观测/测试）。
func (g *Ledger) PoolUsed(groupID string, mid string, unit, period string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pools[g.poolKey(groupID, mid, unit, period)]
}

// SortedUsageKeys 返回账本内非零的用量的键（观测/测试，稳定排序）。
func (g *Ledger) SortedUsageKeys() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.usage))
	for k, v := range g.usage {
		if v > 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
