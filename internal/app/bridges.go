// bridges.go —— app 层的「桥」：把 store 仓储钉到各功能包的窄接口上。
//
// 为什么集中一个文件：每个桥都只有几行（查表→翻译→返回），分散在
// 各处会让人看不出「到底有几层翻译在跑」；集中在这里，code-review
// 巡检时一张文件就核对完所有依赖方向的合法性。
//
// 桥的三条纪律（从 auth/meter 两轮实现里沉淀）：
//  1. 只翻译，不判定——任何「什么算有效」的分支属于功能包；
//  2. 类型对齐靠手工逐字段（store.Account → auth.Account），宁可啰嗦
//     也不反射——装配层的字段漂移要编译期暴露，不要运行期才炸；
//  3. 错误只换壳不吞——store.ErrNotFound 原样透传，功能包自己哨兵化。
package app

import (
	"context"
	"fmt"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// unixTime 把 Unix 秒指针翻成 time.Time 指针（nil 透传）。
func unixTime(sec *int64) *time.Time {
	if sec == nil {
		return nil
	}
	t := time.Unix(*sec, 0)
	return &t
}

// ---------------------------------------------------------------------------
// Key 鉴权桥（auth.KeyAuthStore 两读）
// ---------------------------------------------------------------------------

// keyAuthBridge 把 store 的 Key/Account 仓储接到 auth.KeyAuthStore。
type keyAuthBridge struct {
	keys     *store.KeyRepo
	accounts *store.AccountRepo
}

func (b keyAuthBridge) KeyByHash(ctx context.Context, keyHash []byte) (*auth.APIKey, error) {
	k, err := b.keys.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, err // store.ErrNotFound 原样透传（哨兵在 auth 侧收敛）
	}
	out := &auth.APIKey{
		ID:        fmt.Sprintf("%s", k.ID),
		AccountID: fmt.Sprintf("%s", k.AccountID),
		KeyHash:   keyHash,
		Prefix:    k.Prefix,
		Status:    auth.KeyStatus(k.Status),
	}
	if k.Name != nil {
		out.Name = *k.Name
	}
	if k.ExpiresAt != nil {
		// store 用 Unix 秒；auth 用 time.Time。这里不做时区加工——
		// 过期判定只比较绝对时刻，时区是展示层的事。
		out.ExpiresAt = unixTime(k.ExpiresAt)
	}
	return out, nil
}

func (b keyAuthBridge) AccountByID(ctx context.Context, accountID string) (*auth.Account, error) {
	id, err := parseID(accountID)
	if err != nil {
		return nil, store.ErrNotFound
	}
	a, err := b.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	return accountToAuth(a), nil
}

// accountToAuth 把 store.Account 翻译成 auth.Account。
// 字段逐个抄而不是整结构转换：两个包的字段集会各自演化（auth 侧加
// 会话字段、store 侧加审计列），整结构转换会把「漏字段」变成零值静默。
func accountToAuth(a *store.Account) *auth.Account {
	classes := make(auth.ClassSet, len(a.Classes))
	for _, c := range a.Classes {
		classes[auth.Class(c)] = true
	}
	out := &auth.Account{
		ID:          fmt.Sprintf("%s", a.ID),
		DisplayName: a.DisplayName,
		Username:    a.Username,
		Classes:     classes,
		Status:      auth.AccountStatus(a.Status),
		// 强制改密是账户事实：会话校验要每次读它，漏译会让「首启随机口令」
		// 变成可长期使用的凭据。
		MustChangePassword: a.MustChangePassword,
		// CreatedAt 用零值：store 行有这列，但 Key 鉴权路径不消费它，
		// 显式声明「知道有、故意不译」——比静默漏译可审。
		CreatedAt: time.Time{},
	}
	if a.Note != nil {
		out.Note = *a.Note
	}
	if a.LastLoginAt != nil {
		out.LastLoginAt = unixTime(a.LastLoginAt)
	}
	return out
}

// ---------------------------------------------------------------------------
// meter 桥（groupMemberReader + RateLimitsSource 素材）
// ---------------------------------------------------------------------------

// groupIDsOfAccount 满足 meter.StoreAdapter 的 groupMemberReader。
// 组无 disabled 态（删组即停用，成员随 CASCADE 退回默认组——task-12 定案），
// group_members 行本身就是「关系当前有效」的唯一事实。
func (a *App) groupIDsOfAccount(ctx context.Context, accountID string) ([]string, error) {
	return a.store.Repos().Groups.GroupIDsOfAccount(ctx, accountID)
}

// rateLimitsOf 给 mwRate 的 RateLimitsSource：返回**组策略 min 叠加后**的
// 生效速率上界（03-groups §4：限制类逐键取最严，未设 = -1 不参与 min，
// 全部未设才落回账户列）。
func (a *App) rateLimitsOf(ctx context.Context, accountID string) (meter.AccountLimits, bool) {
	id, err := parseID(accountID)
	if err != nil {
		// 账户 ID 形状不对说明主体不是本进程签发的——按无配置处理
		//（跳过账户闸，全局闸照常），与「未装配策略」同语义。
		return meter.AccountLimits{}, false
	}
	snap, err := a.store.Repos().Groups.LoadPolicySnapshot(ctx, id)
	if err != nil {
		return meter.AccountLimits{}, false
	}
	// 合并只走 store 的单一实现（ComposeEffectivePolicy，账户列已作为
	// fallback 算入）。不在装配层重算，也不取 auth 侧那份同名合并
	// ——两份实现本轮已被发现布尔语义相反（fail-closed vs fail-open），
	// 已在任务板挂账待裁决；在此之前速率/额度一律以 store 那份为准。
	ep := store.ComposeEffectivePolicy(*snap)
	return meter.AccountLimits{
		// -1 不限 / 0 全拒的哨兵语义在 store 快照上就是这套（2026-10-20
		// 额度域定案），meter 已按同一定案解释——直传不加工。
		RPM:            int(ep.RPMLimit),
		TPM:            ep.TPMLimit,
		MaxConcurrency: int(ep.MaxConcurrency),
	}, true
}

// groupIDsSource 给 mwQuota 的 GroupIDsSource（账户 UUID → 组 UUID 列表）。
func (a *App) groupIDsSource(ctx context.Context, accountID string) ([]string, bool) {
	id, err := parseID(accountID)
	if err != nil {
		return nil, false
	}
	gids, err := a.groupIDsOfAccount(ctx, id)
	if err != nil {
		return nil, false
	}
	return gids, true
}

// ---------------------------------------------------------------------------
// 模型目录桥（upstream 的 real_model 仓储面 + httpapi.ModelCatalog）
// ---------------------------------------------------------------------------

// modelCatalog 把模型目录接到 httpapi.ModelCatalog。
//
// M6 口径：对外只暴露抽象模型（红线 C2，docs/models/02-abstract.md §6.2），
// 因此这里读 abstract_models 而不是 real_models——租户看到的 id 是「请求
// 时能用的模型名」，真实模型只在管理端可见。includeDisabled=false 让
// disabled 不出现、deprecated 仍可见（弃用模型必须能被看见才能迁移）。
type modelCatalog struct {
	models *store.ModelRepo
}

func (m modelCatalog) ListModelIDs(ctx context.Context) ([]string, error) {
	rows, err := m.models.ListAbstractModels(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// parseID 校验 id 形状并原样返回。
//
// 2026-10-26 UUID 改造后 auth 侧与 store 侧的 id 是**同一串**，这个函数
// 不再是"翻译点"，只剩形状校验（非法形状说明上游装配有 bug，回错误而不是
// 带着一个查不到的 id 继续走）。函数名与位置保留：它是"哪里碰过 id"的
// grep 锚点。
func parseID(s string) (string, error) {
	if _, err := idgen.Parse(s); err != nil {
		return "", fmt.Errorf("app: 非法的 id 形状 %q（期望 UUID）", s)
	}
	return idgen.MustParse(s), nil
}
