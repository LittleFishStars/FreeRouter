package meter

import (
	"context"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// 哨兵错误：Code 全部复用 auth 的定义，调用方用 errors.Is(err, meter.ErrRateLimited)
// 判类别。四个 429 码客户端动作完全不同，绝不能互相顶替
// （docs/api/03-errors.md §3.4"动作不同才拆码"）。
// too_many_attempts 由 auth 的登录路径产生，本包不重复定义——
// 但它的 Retry-After 契约与这里的三个码一致（见 auth/errors.go 429 码组注释）。
var (
	// ErrRateLimited 速率超限（RPM / TPM / 并发排队超时）。
	ErrRateLimited = auth.Errorf(auth.CodeRateLimited, nil, "速率超限")

	// ErrQuotaExceeded 账户额度耗尽：等周期重置或请管理员调高，重试无意义。
	ErrQuotaExceeded = auth.Errorf(auth.CodeQuotaExceeded, nil, "账户额度已用尽")

	// ErrPoolExhausted 组级共享池耗尽（03-groups.md §4）：换 Key、调参数、
	// 换账号全部无效——这正是它必须独立成码的理由。
	// 具体是哪个组通过 WithDetail 只进日志，不进响应体（不泄露组归属）。
	ErrPoolExhausted = auth.Errorf(auth.CodePoolExhausted, nil, "组共享额度已用尽")

	// ErrGloballyFused 全局预算熔断（PLAN 9.1）。全站行为不是账户行为，
	// 文案不能写成"你的额度"。解除依赖人工调整，故**不带** Retry-After：
	// 编造一个重置时刻是误导。
	ErrGloballyFused = auth.Errorf(auth.CodeQuotaExceeded, nil, "全站当日预算已熔断")
)

// Config 是全局闸门参数，全部来自 config.limits.*（不硬编码）。
type Config struct {
	// GlobalRPM 全站每分钟请求上限；**0 = 不限制**。
	GlobalRPM int

	// GlobalConcurrency 全局在途上限（06-runtime.md §2.3，默认 200）；**0 = 不限制**。
	GlobalConcurrency int

	// GlobalDailyTokens 全局日预算；**0 = 不熔断**。
	// 与额度域的 0 方向相反：那里的 0 是 account_quotas 行 limit=0（零额度全拒），
	// 这里的 0 是"没配就不启用这道全站闸"。
	GlobalDailyTokens int64

	// QueueTimeout 并发排队的等待上限。超时返回 ErrRateLimited（并发维度）。
	//
	// 口径冲突与取舍：06-runtime.md §2.3 写"队列超过 queue_timeout 返回 503"，
	// 而 docs/api/03-errors.md 的 503 码表里只有 no_candidate（候选耗尽），
	// 且 05-keys.md §4 明确"并发超限 → 429 + 有值 Retry-After"。
	// 按"错误码分册为准 + 客户端动作一致（等一会儿再来）"取 429。
	// 若有定案改为 503，只需改这一处（rateLimited 的 dimension 分支不变）。
	QueueTimeout time.Duration
}

// AccountLimits 是某账户**生效后**的速率参数。
//
// 数值由调用方算好传进来（组策略 min 与账户列的叠加在 auth.EffectivePolicy
// 那一侧完成）；本包只认数字，不读存储、不做合并，也**不做** config 默认
// 回退（03-groups.md §3.5：limits.default_* 只在审批/导入时写初值）。
//
// 哨兵语义三列统一（01-accounts.md 2026-10-20 定案，与 accounts 行的
// rpm_limit/tpm_limit/max_concurrency 完全同形）：
//
//	**-1 = 不限（这道闸不启用）；0 = 全拒（一个请求都过不去）；>0 = 上限。**
//
// 0 与 -1 意思完全相反：中间件装配时绝不能把“未配置”翻译成 0——
// 未配置就是 -1，把“没配”读成 0 等于把账户直接捾死。
// （旧注释“TPM 0 = 不限制”已废；三列禁止 NULL，由 storage NOT NULL 保证。）
type AccountLimits struct {
	RPM            int   // -1 不限；0 全拒；>0 上限
	TPM            int64 // 同上
	MaxConcurrency int   // 同上
}

// minuteWindow 是一个按 UTC 分钟对齐的固定窗口计数器。
//
// 为什么固定窗口而不是令牌桶：RPM/TPM 的定义就是"每分钟 N 次"，
// 一次比较即可判定，且 Retry-After 能精确到窗口边界（令牌桶只能给出
// "补一个令牌要多久"，对客户端不如"到下一个整分"直观）。
// 突发整形在这个场景没有价值——上游看的就是分钟计数。
type minuteWindow struct {
	count  int
	tokens int64
	minute time.Time // 零值表示尚未使用
}

// acctState 是一个账户的进程内状态：分钟窗口 + 在途计数。
type acctState struct {
	mu  sync.Mutex
	win minuteWindow
	// inflight 是在途请求数，acquire/release 成对维护。
	inflight int
	// wake 是"有槽位释放"的单次脉冲 channel。
	//
	// 为什么用"关闭并替换"而不是带缓冲 channel：缓冲为 1 的脉冲会丢掉
	// 并发的多次释放（两个槽位同时释放只唤醒一个等待者），而关闭再替换
	// 是 broadcast 语义，所有等待者都会醒来重查条件——条件重查是必要的，
	// 因为它们可能被管理员调低的限额再次挡住。
	wake chan struct{}
}

func newAcctState() *acctState { return &acctState{wake: make(chan struct{})} }

// Limiter 是 RPM / TPM / 并发 / 全局 RPM / 全局日预算的进程内闸门。
//
// 为什么进程内：这些是**秒级**状态，落库会把每次入站请求变成一次 SQLite 写
// （05-storage.md §7.3"转发热路径上不能做的事"）；跨节点一致性留给 P1 的
// Redis 方案（PLAN 第 8 章）。因此本包的语义承诺只对**单进程**成立，
// 这一点在包文档里写明，不做隐含假设。
type Limiter struct {
	cfg Config
	now func() time.Time // 注入时钟：窗口滚动与排队超时都不靠 sleep

	mu         sync.Mutex
	accts      map[string]*acctState
	global     minuteWindow  // 全局 RPM 窗口
	globalIn   int           // 全局在途
	globalWake chan struct{} // 全局并发释放广播

	// 全局日预算（熔断）。fused 是本日已结算的 token 数。
	fused    int64
	fusedDay time.Time
}

// NewLimiter 构造闸门。Config 的零值全部是安全的"不启用该闸"，
// 装配缺参数不会把全站拒死——"必须配"是配置校验层的职责。
func NewLimiter(cfg Config) *Limiter {
	return &Limiter{
		cfg:        cfg,
		now:        time.Now,
		accts:      make(map[string]*acctState),
		globalWake: make(chan struct{}),
	}
}

// WithClock 注入时钟（测试用）。
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.now = now
	return l
}

func truncMinute(t time.Time) time.Time { return t.Truncate(time.Minute) }

// untilNextMinute 返回距下一个整分的时长（用作 RPM/TPM 的 Retry-After）。
func untilNextMinute(now time.Time) time.Duration {
	next := truncMinute(now).Add(time.Minute)
	if d := next.Sub(now); d > 0 {
		return d
	}
	return time.Minute
}

func (l *Limiter) state(accountID string) *acctState {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.accts[accountID]
	if !ok {
		st = newAcctState()
		l.accts[accountID] = st
	}
	return st
}

// checkAccountWindow 在同一把账户锁里判定（不递增）。调用方持锁。
func checkAccountWindow(w *minuteWindow, lim AccountLimits, tokenCost int64, minute time.Time) (string, bool) {
	w2 := *w
	if minute != w2.minute {
		w2 = minuteWindow{minute: minute} // 跨分钟：隐式重置
	}
	// RPM：0 = 全拒；-1 = 不限（跳过）；>0 = 上限（2026-10-20 定案，
	// 旧的“RPM<=0 全拒、TPM 0=不限”双口径作废）。
	if lim.RPM == 0 {
		return "rpm", false
	}
	if lim.RPM > 0 && w2.count >= lim.RPM {
		return "rpm", false
	}
	// TPM：哨兵与 RPM 完全同形（-1 不限 / 0 全拒），不再有“0 = 不限制”的旧口径。
	if lim.TPM == 0 {
		return "tpm", false
	}
	if lim.TPM > 0 && w2.tokens+tokenCost > lim.TPM {
		return "tpm", false
	}
	return "", true
}

func rateLimited(dimension string, ra time.Duration) error {
	return ErrRateLimited.WithDetail(dimension, "").WithRetryAfter(ra)
}

// Allow 是速率闸门的热路径入口：**判定 + 递增在同一把锁内**完成。
//
// 为什么必须合一：先查后加在并发下会双花——两个请求同时读到 count=N < limit，
// 各自放行，实际放行 N+2。这不是性能优化而是正确性要求（与共享池的
// "判定即扣减"同一条纪律，03-groups.md §4）。
//
// 返回的 Retry-After 指向下一个整分（客户端只需知道"等到下一分钟"）。
func (l *Limiter) Allow(accountID string, lim AccountLimits, tokenCost int64) (time.Duration, error) {
	st := l.state(accountID)
	now := l.now()
	minute := truncMinute(now)

	st.mu.Lock()
	if dim, ok := checkAccountWindow(&st.win, lim, tokenCost, minute); !ok {
		st.mu.Unlock()
		return untilNextMinute(now), rateLimited(dim, untilNextMinute(now))
	}
	if st.win.minute != minute {
		st.win = minuteWindow{minute: minute}
	}
	st.win.count++
	st.win.tokens += tokenCost
	st.mu.Unlock()

	// 全局 RPM：账户维通过后才查。顺序理由是对外错误归因——
	// 账户自己撞墙时应告诉他"你的速率"，而不是"全站繁忙"。
	l.mu.Lock()
	if l.cfg.GlobalRPM > 0 {
		if l.global.minute != minute {
			l.global = minuteWindow{minute: minute}
		}
		if l.global.count >= l.cfg.GlobalRPM {
			l.mu.Unlock()
			ra := untilNextMinute(now)
			return ra, rateLimited("rpm(global)", ra)
		}
		l.global.count++
	}
	l.mu.Unlock()
	return 0, nil
}

// CheckRate 是 Allow 的只读形式：判定但**不消耗**。供预检与测试使用，
// 热路径必须用 Allow（见其注释）。
func (l *Limiter) CheckRate(accountID string, lim AccountLimits, tokenCost int64) (time.Duration, error) {
	st := l.state(accountID)
	now := l.now()
	minute := truncMinute(now)

	st.mu.Lock()
	dim, ok := checkAccountWindow(&st.win, lim, tokenCost, minute)
	st.mu.Unlock()
	if !ok {
		ra := untilNextMinute(now)
		return ra, rateLimited(dim, ra)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg.GlobalRPM > 0 && l.global.minute == minute && l.global.count >= l.cfg.GlobalRPM {
		ra := untilNextMinute(now)
		return ra, rateLimited("rpm(global)", ra)
	}
	return 0, nil
}

// AcquireConcurrency 依次占用账户槽位与全局槽位（06-runtime.md §2.3：
// 先组级再全局；任一取不到都排队）。
//
// 排队上限由 QueueTimeout 决定；超时或 ctx 取消都返回错误。ctx 取消
// 必须**立即**归还已取得的账户槽位——客户端已经走了的请求继续占着槽位，
// 等于让一次客户端超时永久吃掉该账户一路并发（06-runtime.md §3.2 的
// 取消传播在 meter 侧的落点）。
//
// 返回的 release 必须调用（defer）；内部用 sync.Once 保证重复调用无害。
func (l *Limiter) AcquireConcurrency(ctx context.Context, accountID string, lim AccountLimits) (release func(), err error) {
	if lim.MaxConcurrency == 0 {
		// 0 = 全拒（哨兵语义，与 RPM/TPM 一致；-1 不走这里，在下方 try 里放行）。
		// 这里**不**排队：没有等待的意义。
		return nil, rateLimited("concurrency", l.queueTimeout())
	}
	st := l.state(accountID)

	// 账户槽位。
	if err := l.acquireOne(ctx, st, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		if lim.MaxConcurrency < 0 || st.inflight < lim.MaxConcurrency { // -1 = 不限
			st.inflight++
			return true
		}
		return false
	}, func() <-chan struct{} { return st.wake }, "concurrency"); err != nil {
		return nil, err
	}

	// 全局槽位。
	if err := l.acquireGlobal(ctx); err != nil {
		// 已占的账户槽位必须归还，否则该账户永久少一路并发。
		l.releaseAcct(st)
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			l.releaseAcct(st)
			l.releaseGlobal()
		})
	}, nil
}

// acquireOne 用"条件重查 + 广播唤醒"实现无固定容量信号量的获取。
//
// 为什么不用固定容量的 buffered channel 当信号量：上限会在运行期被管理员
// 调高/调低（账户级速率是管理面可改的），固定容量 chan 无法原地改大小，
// 只能重建——而重建时已排队的等待者会挂在旧 channel 上永久阻塞。
func (l *Limiter) acquireOne(ctx context.Context, st *acctState, try func() bool, wake func() <-chan struct{}, dimension string) error {
	deadline := l.now().Add(l.queueTimeout())
	for {
		if try() {
			return nil
		}
		// 取当前广播 channel 必须在重试条件之后、等待之前读取，
		// 否则可能错过"刚释放"的那次广播并多等一个超时周期。
		ch := wake()
		if err := l.wait(ctx, ch, deadline); err != nil {
			return l.queueError(ctx, dimension)
		}
	}
}

func (l *Limiter) acquireGlobal(ctx context.Context) error {
	deadline := l.now().Add(l.queueTimeout())
	for {
		l.mu.Lock()
		if l.cfg.GlobalConcurrency <= 0 || l.globalIn < l.cfg.GlobalConcurrency {
			l.globalIn++
			l.mu.Unlock()
			return nil
		}
		ch := l.globalWake
		l.mu.Unlock()
		if err := l.wait(ctx, ch, deadline); err != nil {
			return l.queueError(ctx, "concurrency(global)")
		}
	}
}

func (l *Limiter) queueError(ctx context.Context, dimension string) error {
	if err := ctx.Err(); err != nil {
		return err // 客户端已走：把取消原样交回，由中间件当成不算鉴权失败
	}
	return rateLimited(dimension, l.queueTimeout())
}

func (l *Limiter) releaseAcct(st *acctState) {
	st.mu.Lock()
	if st.inflight > 0 {
		st.inflight--
	}
	close(st.wake)
	st.wake = make(chan struct{}) // 关闭后必须换新，否则下一次 close panic
	st.mu.Unlock()
}

func (l *Limiter) releaseGlobal() {
	l.mu.Lock()
	if l.globalIn > 0 {
		l.globalIn--
	}
	close(l.globalWake)
	l.globalWake = make(chan struct{})
	l.mu.Unlock()
}

// wait 等待广播、ctx 取消或排队截止。
//
// 用 timer 而不是 time.After：热路径上的 time.After 在超时前不可回收，
// 高并发排队时会积累大量未触发的定时器。
func (l *Limiter) wait(ctx context.Context, ch <-chan struct{}, deadline time.Time) error {
	d := deadline.Sub(l.now())
	if d <= 0 {
		return errQueueTimeout
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errQueueTimeout
	}
}

// errQueueTimeout 是内部哨兵：只用来在 wait 与 queueError 之间传递
// "排队超时"这一事实，对外永远被翻译成 ErrRateLimited。
var errQueueTimeout = &auth.Error{Code: auth.CodeRateLimited, Msg: "排队超时"}

func (l *Limiter) queueTimeout() time.Duration {
	if l.cfg.QueueTimeout <= 0 {
		// 0 在排队语义里不能读成"无限等"——那是一条把 goroutine 挂死的路径。
		return 5 * time.Second // 与 config 默认值一致
	}
	return l.cfg.QueueTimeout
}

// ---------------------------------------------------------------------------
// 全局日预算熔断
// ---------------------------------------------------------------------------

// CheckGlobalFuse 是判定链第 6 步的熔断预检。
// GlobalDailyTokens == 0 → 恒不熔断（见 Config 注释）。
func (l *Limiter) CheckGlobalFuse() error {
	if l.cfg.GlobalDailyTokens <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := dayStart(l.now())
	if l.fusedDay.Equal(day) && l.fused >= l.cfg.GlobalDailyTokens {
		return ErrGloballyFused
	}
	return nil
}

// RecordGlobalTokens 在**终态结算**时累加本日用量，并报告是否已熔断。
//
// 为什么在结算侧累加而不是请求开始时：与共享池"终态一次结算"同构
// （03-groups.md §4）——预扣就需要为失败/取消设计返还，而返还写错的方向
// 是多扣（无故拒全站），比少扣危险得多。
func (l *Limiter) RecordGlobalTokens(n int64) (fused bool) {
	if l.cfg.GlobalDailyTokens <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := dayStart(l.now())
	if !l.fusedDay.Equal(day) {
		// UTC 日界（与 usage_daily.day 同一套口径；05-storage.md 第 9 节的
		// 时区待定项若改为本地日界，这里必须跟着改，否则"额度早 8 点重置、
		// 熔断 UTC 零点重置"无法向管理员解释）。
		l.fused, l.fusedDay = 0, day
	}
	l.fused += n
	return l.fused >= l.cfg.GlobalDailyTokens
}

// GlobalUsage 返回本日已结算用量与阈值（观测用）。
func (l *Limiter) GlobalUsage() (used, limit int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fused, l.cfg.GlobalDailyTokens
}

// PruneIdle 回收长期不活动的账户状态，防止 map 无界增长。
// inflight > 0 的账户不回收：它的 wake channel 还被在途请求的 release 引用着。
func (l *Limiter) PruneIdle() {
	cutoff := truncMinute(l.now()).Add(-time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, st := range l.accts {
		st.mu.Lock()
		idle := st.win.minute.IsZero() || st.win.minute.Before(cutoff)
		n := st.inflight
		st.mu.Unlock()
		if idle && n == 0 {
			delete(l.accts, id)
		}
	}
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
