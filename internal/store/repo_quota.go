package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QuotaUnit 是额度计量维度（05-storage.md 3.1 / 3.3：旧名 dimension 及其
// requests/tokens 两值枚举已于 2026-10-20 废弃，与账户级统一拼写并容纳第三维 credit）。
type QuotaUnit string

const (
	UnitRequest QuotaUnit = "request"
	UnitToken   QuotaUnit = "token"
	UnitCredit  QuotaUnit = "credit"
)

// QuotaPeriod 是周期粒度。
type QuotaPeriod string

const (
	PeriodDay   QuotaPeriod = "day"
	PeriodMonth QuotaPeriod = "month"
)

func (u QuotaUnit) valid() bool {
	switch u {
	case UnitRequest, UnitToken, UnitCredit:
		return true
	}
	return false
}

func (p QuotaPeriod) valid() bool {
	switch p {
	case PeriodDay, PeriodMonth:
		return true
	}
	return false
}

// TotalScope 是"合计哨兵"：account_quotas / group_quota_pools 里 abstract_model_id = 0
// 的行代表全模型合计（即原「账户总额度」「组合计池」）。
// 0 不是合法模型 id（abstract_models.id 从 1 起），因此哨兵不会与真实模型碰撞。
//
// 与 API 形状的对应（api/02-admin.md 2026-10-21 三数组定案）：组策略 JSON 的
// pool[] 项里 "model": null 就是这一维——管理 API 收到 model:null 时以
// abstractModelID = TotalScope(0) 调 SetPool，收到 "model":"smart" 时先按名解析出
// 模型 id 再调 SetPool(ctx, groupID, modelID, …)。**"null → 0" 的翻译只发生在
// API 边界一次**，仓储以下全部用 int64 行键，不再有第三种"未设"表示。
// account_quotas[] / model_quota_caps[] 两个数组同理（合计行 vs 按模型行）。
const TotalScope int64 = 0

// ErrQuotaExhausted 表示额度不足（条件 UPDATE 影响 0 行且该行存在）。
// 独立类型而不是 ErrNotFound 的包装：上层据此返回 429 而不是 404。
//
// 携带**打满行的完整标识**（api/03-errors.md 2026-10-21 定案）：
//   - quota{unit, period, model?} 机器可读对象：AbstractModelID = 0（TotalScope）时
//     合计行、无 model；> 0 时是按模型限额行，调用方按 id 解析出模型名填进 model，
//     客户端据此决定"换模型"还是"等重置"。错误里没有模型名是刻意的——持久层不知道
//     名字，把"按 id 查名"塞进 Error() 会让 429 热路径依赖另一张表的读取。
//   - PeriodAnchor：命中行的周期锚点，Retry-After = 锚点 + period 粒度 - now。
//     没有它调用方只能猜重置时刻；文档把 Retry-After 定为"指向命中行的重置时刻"。
//   - Used/Limit：组装 message（"已用 X / 上限 Y"）用，避免上层再回查一次同一条行。
type ErrQuotaExhausted struct {
	Scope           string // account | pool
	Unit            QuotaUnit
	Period          QuotaPeriod
	AbstractModelID int64  // 0 = 合计行；> 0 = 按模型限额行
	PeriodAnchor    string // 'YYYY-MM-DD'，该行 pool/quota_used 所属周期起点
	Used            int64
	Limit           int64
}

// ResetAt 返回命中行的周期重置时刻（UTC）。Retry-After = ResetAt - now 由调用方计算。
//
// 为什么公式是 锚点+粒度 而不是"下一次整日/整月"（05-storage.md 7.4 / security/03-groups 第 4 节）：
// 行只在被惰性重置时归零，锚点记录的是"这个数字属于哪个周期"；如果按墙钟推断，
// 一个几天没流量的账户会得到"明天 0 点重置"的错误承诺，而它的行还挂着上期锚点，
// 下一次写入才真正归零。承诺必须与归零的实际机制同源。
// 锚点解析失败（脏数据）返回 ok=false：调用方宁可不发 Retry-After，
// 也不能发一个错误的时刻让客户端定时重试。
func (e ErrQuotaExhausted) ResetAt() (time.Time, bool) {
	anchor, err := time.Parse("2006-01-02", e.PeriodAnchor)
	if err != nil {
		return time.Time{}, false
	}
	switch e.Period {
	case PeriodMonth:
		return anchor.AddDate(0, 1, 0), true
	default:
		return anchor.AddDate(0, 0, 1), true
	}
}

func (e ErrQuotaExhausted) Error() string {
	if e.AbstractModelID != TotalScope {
		return fmt.Sprintf("store: %s 额度不足（%s/%s，模型行 %d）",
			e.Scope, e.Unit, e.Period, e.AbstractModelID)
	}
	return fmt.Sprintf("store: %s 额度不足（%s/%s，合计）", e.Scope, e.Unit, e.Period)
}

// QuotaRepo 是额度域仓储：账户级周期额度、组级共享池、组侧按模型上限声明。
//
// 为什么三样东西在一个仓储里：它们共享同一套形状（(id, mid, unit, period) 复合主键 +
// 惰性周期重置 + 条件 UPDATE 扣减），拆成三个仓储只会让"同一条 SQL 的第三种写法"扩散。
//
// 为什么池与上限声明也在额度域而不是组仓储：group_policies 是策略键值表，
// 而这两者是带计数器的闸门；05-storage.md 3.3 明确它们刻意不进 group_policies。
type QuotaRepo struct{ base }

// QuotaState 是一行账户额度或一行池的当前状态。
type QuotaState struct {
	OwnerID         int64 // account_id 或 group_id（按调用方法解释）
	AbstractModelID int64 // 0 = 合计
	Unit            QuotaUnit
	Period          QuotaPeriod
	Limit           int64
	Used            int64
	PeriodAnchor    string
	UpdatedAt       int64
}

// SetAccountQuota 设置/更新账户的额度行。
//
// limit = 0 是合法的（"全拒"）；"不限"由 DeleteAccountQuota 删行表达，不用 -1：
// NULL/-1 参与比较会得到 UNKNOWN 或意外为真，而条件 UPDATE 只接受"真"。
// abstractModelID 传 TotalScope(0) 表示全模型合计。
func (r *QuotaRepo) SetAccountQuota(ctx context.Context, accountID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, limit int64) error {
	if accountID == 0 {
		return errors.New("store: 额度必须归属某个账户")
	}
	if limit < 0 {
		return fmt.Errorf("store: quota_limit 不能为负（%d）；「不限」请删行", limit)
	}
	return r.setQuota(ctx, "account_quotas", "account_id", "quota_limit", "quota_used",
		accountID, abstractModelID, unit, period, limit)
}

// SetPool 设置/更新组级共享池上限。
//
// 允许把上限调到低于当前 pool_used（应急关闸）：后续请求全被拒是正确表现，
// 而拒绝管理员关闸不是（05-storage.md 3.3）。所以这里没有 used<=limit 的校验，
// 表上也没有那条 CHECK。
// pool_limit 必须严格为正：0 无法写入（"停用池 = 删池行"，见 3.3）。
func (r *QuotaRepo) SetPool(ctx context.Context, groupID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, limit int64) error {
	if groupID == 0 {
		return errors.New("store: 池必须归属某个组")
	}
	if limit <= 0 {
		return fmt.Errorf("store: pool_limit 必须为正数（%d）。停用池请用 DeletePool 删行", limit)
	}
	return r.setQuota(ctx, "group_quota_pools", "group_id", "pool_limit", "pool_used",
		groupID, abstractModelID, unit, period, limit)
}

// setQuota 是账户额度与组池的共用写入（两张表同形，只有标识列与列名不同）。
//
// 为什么共用而不是复制两遍：ON CONFLICT 的列集合、惰性锚点、更新列名三处
// 一旦在两份代码里漂移，就会出现"改了池忘了改账户额度"的不对称 bug。
// 表名/列名来自本包内联常量（不是用户输入），拼接安全。
func (r *QuotaRepo) setQuota(ctx context.Context, table, ownerCol, limitCol, usedCol string,
	ownerID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, limit int64) error {
	if !unit.valid() {
		return fmt.Errorf("store: 非法 unit %q", unit)
	}
	if !period.valid() {
		return fmt.Errorf("store: 非法 period %q", period)
	}
	anchor := periodAnchor(period, time.Now().UTC())

	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s(%s, abstract_model_id, unit, period, %s, %s, period_anchor, updated_at)
		         VALUES(?,?,?,?,?,0,?,?)
		         ON CONFLICT(%s, abstract_model_id, unit, period) DO UPDATE SET
		             %s = excluded.%s,
		             updated_at = excluded.updated_at`,
			table, ownerCol, limitCol, usedCol, ownerCol, limitCol, limitCol),
		ownerID, abstractModelID, string(unit), string(period), limit, anchor, now())
	if err != nil {
		return fmt.Errorf("store: 设置额度失败(%s): %w", table, err)
	}
	return nil
}

// DeleteAccountQuota 删行 = 该维度不限。
func (r *QuotaRepo) DeleteAccountQuota(ctx context.Context, accountID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) error {
	return r.deleteQuota(ctx, "account_quotas", "account_id", accountID, abstractModelID, unit, period)
}

// DeletePool 删池行 = 停用池（"保留配置但停用"这个需求主动不做，见 3.3）。
func (r *QuotaRepo) DeletePool(ctx context.Context, groupID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) error {
	return r.deleteQuota(ctx, "group_quota_pools", "group_id", groupID, abstractModelID, unit, period)
}

func (r *QuotaRepo) deleteQuota(ctx context.Context, table, ownerCol string, ownerID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) error {
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?`,
			table, ownerCol),
		ownerID, abstractModelID, string(unit), string(period))
	if err != nil {
		return fmt.Errorf("store: 删除额度行失败(%s): %w", table, err)
	}
	return nil
}

// AccountQuota 读账户的某一行额度。ErrNotFound = 没有这一行 = 该维度不限。
func (r *QuotaRepo) AccountQuota(ctx context.Context, accountID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) (*QuotaState, error) {
	return r.getQuota(ctx, "account_quotas", "account_id", "quota_limit", "quota_used",
		accountID, abstractModelID, unit, period)
}

// Pool 读某组某维度的池状态。ErrNotFound = 未设池 = 不限制。
func (r *QuotaRepo) Pool(ctx context.Context, groupID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) (*QuotaState, error) {
	return r.getQuota(ctx, "group_quota_pools", "group_id", "pool_limit", "pool_used",
		groupID, abstractModelID, unit, period)
}

func (r *QuotaRepo) getQuota(ctx context.Context, table, ownerCol, limitCol, usedCol string,
	ownerID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) (*QuotaState, error) {
	var q QuotaState
	var u, p string
	err := r.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s, abstract_model_id, unit, period, %s, %s, period_anchor, updated_at
		           FROM %s WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?`,
			ownerCol, limitCol, usedCol, table, ownerCol),
		ownerID, abstractModelID, string(unit), string(period)).
		Scan(&q.OwnerID, &q.AbstractModelID, &u, &p, &q.Limit, &q.Used, &q.PeriodAnchor, &q.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: 读取额度失败(%s): %w", table, err)
	}
	q.Unit, q.Period = QuotaUnit(u), QuotaPeriod(p)
	return &q, nil
}

// ListAccountQuotas 列出账户的全部额度行（含按模型行）。合计行以 AbstractModelID=0 出现。
func (r *QuotaRepo) ListAccountQuotas(ctx context.Context, accountID int64) ([]QuotaState, error) {
	return r.listQuota(ctx, "account_quotas", "account_id", "quota_limit", "quota_used", accountID)
}

// ListPoolsByGroup 列出某组的全部池（组详情页的"哪些快满了"）。
func (r *QuotaRepo) ListPoolsByGroup(ctx context.Context, groupID int64) ([]QuotaState, error) {
	return r.listQuota(ctx, "group_quota_pools", "group_id", "pool_limit", "pool_used", groupID)
}

func (r *QuotaRepo) listQuota(ctx context.Context, table, ownerCol, limitCol, usedCol string, ownerID int64) ([]QuotaState, error) {
	rows, err := r.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s, abstract_model_id, unit, period, %s, %s, period_anchor, updated_at
		           FROM %s WHERE %s = ?
		           ORDER BY abstract_model_id, unit, period`, ownerCol, limitCol, usedCol, table, ownerCol),
		ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: 列出额度失败(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []QuotaState
	for rows.Next() {
		var q QuotaState
		var u, p string
		if err := rows.Scan(&q.OwnerID, &q.AbstractModelID, &u, &p,
			&q.Limit, &q.Used, &q.PeriodAnchor, &q.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: 扫描额度失败: %w", err)
		}
		q.Unit, q.Period = QuotaUnit(u), QuotaPeriod(p)
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历额度失败: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- 扣减原语

// TryDebitAccountQuota 判定 + 递增压进同一条条件 UPDATE，以受影响行数为成败。
//
// 为什么不用"先 SELECT 判定、再 UPDATE 扣减"：两步之间存在窗口，N 个并发请求会都读到
// used < limit 然后各自扣减成功，额度被超发且超发量随并发度线性增长；更坑的是它在低并发
// 的开发环境永远不出现。条件 UPDATE 把窗口从根上消掉——这不是性能优化，是正确性要求。
//
// 返回 nil 表示扣减成功；返回 ErrQuotaExhausted 表示额度不足（应拒绝该请求），
// 错误值携带打满行的 (abstract_model_id, period_anchor, used, limit) 供上层组装
// quota 对象与 Retry-After；返回 nil 且未扣减的情况只有一个：没有这一行（= 该维度
// 不限）。"0 行"的两种成因在慢路径（0 行）上用一次整行读取区分——快路径（扣减成功）
// 不多付任何读取。
//
// 为什么必须先惰性重置再扣减：分开到不同事务会出现"已跨周期但尚未归零"的窗口，
// 窗口内的请求会拿上期的 used 去比本期的 limit。本方法把两步放在同一个调用里，
// 由调用方的事务包住（05-storage.md 7.2：与请求落库同一事务）。
func (r *QuotaRepo) TryDebitAccountQuota(ctx context.Context, accountID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, cost int64) error {
	return r.tryDebit(ctx, "account_quotas", "account_id", "quota_limit", "quota_used",
		"account", accountID, abstractModelID, unit, period, cost)
}

// TryDebitPool 是组级共享池的同形扣减。语义与 TryDebitAccountQuota 完全一致。
func (r *QuotaRepo) TryDebitPool(ctx context.Context, groupID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, cost int64) error {
	return r.tryDebit(ctx, "group_quota_pools", "group_id", "pool_limit", "pool_used",
		"pool", groupID, abstractModelID, unit, period, cost)
}

func (r *QuotaRepo) tryDebit(ctx context.Context, table, ownerCol, limitCol, usedCol, scope string,
	ownerID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, cost int64) error {
	if !unit.valid() {
		return fmt.Errorf("store: 非法 unit %q", unit)
	}
	if !period.valid() {
		return fmt.Errorf("store: 非法 period %q", period)
	}
	if cost < 0 {
		return fmt.Errorf("store: 扣减量不能为负（%d）", cost)
	}
	anchor := periodAnchor(period, time.Now().UTC())
	ts := now()

	// ① 惰性重置：幂等，未跨周期时受影响 0 行。
	if _, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET %s = 0, period_anchor = ?, updated_at = ?
		         WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?
		           AND period_anchor <> ?`, table, usedCol, ownerCol),
		anchor, ts, ownerID, abstractModelID, string(unit), string(period), anchor); err != nil {
		return fmt.Errorf("store: 额度惰性重置失败(%s): %w", table, err)
	}

	// ② 条件扣减：0 行 = 不足或无行；1 行 = 成功。
	res, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET %s = %s + ?, updated_at = ?
		         WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?
		           AND period_anchor = ?
		           AND %s + ? <= %s`, table, usedCol, usedCol, ownerCol, usedCol, limitCol),
		cost, ts, ownerID, abstractModelID, string(unit), string(period), anchor, cost)
	if err != nil {
		return fmt.Errorf("store: 额度扣减失败(%s): %w", table, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 读取扣减行数失败: %w", err)
	}
	if n == 0 {
		// 0 行有两种成因：无行（= 不设限，放行）或有行但打满（= 拒绝）。
		// 慢路径整行取回：一次 getQuota 同时回答"存在吗"与错误体需要的
		// anchor/used/limit——比 COUNT+再查一遍少一次往返，且拒绝路径的
		// 信息必须是**判定瞬间**的行值，分两次查会读到别的写事务改过的值。
		q, err := r.getQuota(ctx, table, ownerCol, limitCol, usedCol,
			ownerID, abstractModelID, unit, period)
		if errors.Is(err, ErrNotFound) {
			return nil // 无行 = 不设限（05-storage.md 3.3）
		}
		if err != nil {
			return err
		}
		return ErrQuotaExhausted{
			Scope: scope, Unit: unit, Period: period,
			AbstractModelID: abstractModelID,
			PeriodAnchor:    q.PeriodAnchor,
			Used:            q.Used,
			Limit:           q.Limit,
		}
	}
	return nil
}

// CreditQuota 是"无条件记账"：终态结算时把实际消耗累加进去，不做闸门判定。
//
// 为什么判定与结算是两个方法而不是一个：消耗时机在流结束（7.3：在途请求不占额度），
// 所以结算时额度**可能**已经越过上限——这是"终态一次结算"的必然结果，
// 必须记账（少扣方向可接受），而不是像 tryDebit 那样拒绝写入。
// 无行 = 不限 = 跳过，与文档"不限 = 无行 = 根本不参与扣减"一致。
func (r *QuotaRepo) CreditQuota(ctx context.Context, accountQuotas, poolCredits []QuotaCredit) error {
	for _, c := range accountQuotas {
		if err := r.credit(ctx, "account_quotas", "account_id", "quota_used",
			c.OwnerID, c.AbstractModelID, c.Unit, c.Period, c.Cost); err != nil {
			return err
		}
	}
	for _, c := range poolCredits {
		if err := r.credit(ctx, "group_quota_pools", "group_id", "pool_used",
			c.OwnerID, c.AbstractModelID, c.Unit, c.Period, c.Cost); err != nil {
			return err
		}
	}
	return nil
}

// QuotaCredit 是一条记账量。
type QuotaCredit struct {
	OwnerID         int64 // account_id 或 group_id
	AbstractModelID int64 // 0 = 合计
	Unit            QuotaUnit
	Period          QuotaPeriod
	Cost            int64
}

func (r *QuotaRepo) credit(ctx context.Context, table, ownerCol, usedCol string,
	ownerID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod, cost int64) error {
	if cost == 0 {
		return nil
	}
	if cost < 0 {
		return fmt.Errorf("store: 记账量不能为负（%d）", cost)
	}
	if !unit.valid() || !period.valid() {
		return fmt.Errorf("store: 非法额度维度 %q/%q", unit, period)
	}
	anchor := periodAnchor(period, time.Now().UTC())
	ts := now()

	// 先归零跨周期的行，再无条件累加；两条语句都在调用方事务内。
	if _, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET %s = 0, period_anchor = ?, updated_at = ?
		         WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?
		           AND period_anchor <> ?`, table, usedCol, ownerCol),
		anchor, ts, ownerID, abstractModelID, string(unit), string(period), anchor); err != nil {
		return fmt.Errorf("store: 额度记账前重置失败(%s): %w", table, err)
	}
	if _, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET %s = %s + ?, updated_at = ?
		         WHERE %s = ? AND abstract_model_id = ? AND unit = ? AND period = ?`,
			table, usedCol, usedCol, ownerCol),
		cost, ts, ownerID, abstractModelID, string(unit), string(period)); err != nil {
		return fmt.Errorf("store: 额度记账失败(%s): %w", table, err)
	}
	return nil
}

// ---------------------------------------------------------------- 组侧按模型上限声明

// ModelCap 是组侧「每人按模型上限」的一行声明（group_model_quota_caps，3.3.1）。
//
// 为什么它不带计数器：cap 是策略（管理员设了什么，全组共享一行），
// 消耗必须按人记账（记在 account_quotas 的 mid>0 行）。把计数器挂组行上会把
// 「每人限额」错实现成「共享池再加一维」，两者是完全不同的闸。
type ModelCap struct {
	GroupID         int64
	AbstractModelID int64
	Unit            QuotaUnit
	Period          QuotaPeriod
	Cap             int64
}

// SetGroupModelCap 设置/更新一条按模型每人上限。cap 必须为正：「每人不限」= 没有这一行。
func (r *QuotaRepo) SetGroupModelCap(ctx context.Context, c ModelCap) error {
	if c.GroupID == 0 || c.AbstractModelID == 0 {
		return errors.New("store: 按模型上限必须同时归属组与抽象模型（合计走 SetPool/SetAccountQuota）")
	}
	if c.Cap <= 0 {
		return fmt.Errorf("store: cap 必须为正（%d）；「不限」请删行", c.Cap)
	}
	if !c.Unit.valid() || !c.Period.valid() {
		return fmt.Errorf("store: 非法上限维度 %q/%q", c.Unit, c.Period)
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO group_model_quota_caps(group_id, abstract_model_id, unit, period, cap)
		 VALUES(?,?,?,?,?)
		 ON CONFLICT(group_id, abstract_model_id, unit, period) DO UPDATE SET
		     cap = excluded.cap`,
		c.GroupID, c.AbstractModelID, string(c.Unit), string(c.Period), c.Cap)
	if err != nil {
		return fmt.Errorf("store: 设置按模型上限失败: %w", err)
	}
	return nil
}

// DeleteGroupModelCap 删行 = 该维度每人不限。
func (r *QuotaRepo) DeleteGroupModelCap(ctx context.Context, groupID, abstractModelID int64, unit QuotaUnit, period QuotaPeriod) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM group_model_quota_caps
		  WHERE group_id = ? AND abstract_model_id = ? AND unit = ? AND period = ?`,
		groupID, abstractModelID, string(unit), string(period))
	if err != nil {
		return fmt.Errorf("store: 删除按模型上限失败: %w", err)
	}
	return nil
}

// ListGroupModelCaps 列出某组的按模型上限；modelID 为 nil 时列出该组全部。
func (r *QuotaRepo) ListGroupModelCaps(ctx context.Context, groupID int64, modelID *int64) ([]ModelCap, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT group_id, abstract_model_id, unit, period, cap
		           FROM group_model_quota_caps WHERE group_id = ?`)
	args := []any{groupID}
	if modelID != nil {
		q.WriteString(" AND abstract_model_id = ?")
		args = append(args, *modelID)
	}
	q.WriteString(" ORDER BY abstract_model_id, unit, period")

	rows, err := r.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 读取按模型上限失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ModelCap
	for rows.Next() {
		var c ModelCap
		var u, p string
		if err := rows.Scan(&c.GroupID, &c.AbstractModelID, &u, &p, &c.Cap); err != nil {
			return nil, fmt.Errorf("store: 扫描按模型上限失败: %w", err)
		}
		c.Unit, c.Period = QuotaUnit(u), QuotaPeriod(p)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历按模型上限失败: %w", err)
	}
	return out, nil
}

// MinCapForAccount 是 EffectivePolicy 重算的存储侧支点：给定账户所属各组与目标模型，
// 返回各 (unit, period) 上的最小 cap（无组设置 = 该维度不出现在结果里）。
//
// 为什么把 min 计算放进存储层而不是上层循环查表：这是"成员关系/策略变更时重算"的
// 一次性写前读，SQL 一条聚合就够；放进上层会变成 N 次查询 + 一次手写 min，
// 而手写 min 最容易漏的是"某个组没设这一维"。
func (r *QuotaRepo) MinCapForAccount(ctx context.Context, accountID int64, abstractModelID int64) (map[string]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT c.unit, c.period, MIN(c.cap)
		   FROM group_model_quota_caps c
		   JOIN group_members m ON m.group_id = c.group_id
		  WHERE m.account_id = ? AND c.abstract_model_id = ?
		  GROUP BY c.unit, c.period`, accountID, abstractModelID)
	if err != nil {
		return nil, fmt.Errorf("store: 计算生效上限失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int64{}
	for rows.Next() {
		var u, p string
		var min sql.NullInt64
		if err := rows.Scan(&u, &p, &min); err != nil {
			return nil, fmt.Errorf("store: 扫描生效上限失败: %w", err)
		}
		if min.Valid {
			out[string(QuotaUnit(u))+"/"+string(QuotaPeriod(p))] = min.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历生效上限失败: %w", err)
	}
	return out, nil
}

// periodAnchor 返回周期起点日期。day 用 UTC 日期；month 用当月 1 日。
// 与 usage_daily.day 共用同一日界（当前设计；见 05-storage.md 第 9 节的待定项）。
//
// 为什么 period 与 period_anchor 分两列：period 说明"周期粒度"，anchor 说明
// "这个数字属于哪一个具体周期"。只有粒度就无法判断跨日/跨月该不该清零，
// 而从 updated_at 反推也不行——可能连续几天没有任何请求，updated_at 停在旧日期。
func periodAnchor(period QuotaPeriod, t time.Time) string {
	switch period {
	case PeriodMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	default:
		return t.Format("2006-01-02")
	}
}

// 编译期确保 fmt.Errorf 用到的哨兵实现了 error 接口（值类型 error 容易误写成指针）。
var _ error = ErrQuotaExhausted{}
