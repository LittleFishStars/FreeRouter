package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// policy_bridge.go —— 判定链第 5 步（组授权 ∩ Key 白名单）的装配桥。
//
// 缺口背景（task-22）：AuthzService.ModelCallable/EffectiveModels 已实现
// 有测试，但生产路径无调用点——v1 是「已鉴权即全可见」。本文件把它钉
// 到真实仓储上，供 routingCandidates（调用端点）与 modelCatalog
//（/v1/models 列表）两处消费。
//
// 桥的纪律（与 bridges.go 相同）：只翻译不判定；错误只换壳不吞。
// 合并只有一条路径：auth.MergeGroupPolicies（fail-closed 已与 store
// ComposeEffectivePolicy 同语义——store 侧那份管速率三列，这里管可见性，
// 两个消费点各取所需，不是第三份合并实现）。

// ---------------------------------------------------------------------------
// PolicyRepository 桥：store LoadPolicySnapshot → []auth.GroupPolicy
// ---------------------------------------------------------------------------

// policyBridge 实现 auth.PolicyRepository。
//
// 为什么用 LoadPolicySnapshot 而不是逐组查三张表：snapshot 是装配层
// 已有的单一装载路径（rateLimitsOf 同款）——同一账户在限速与可见性
// 两个判定上读**同一份快照形状**，不会出现"限速看见了组 A、可见性没
// 看见"的分叉；且 snapshot 的解码失败会报错而不是静默跳组（静默跳组
// = fail-open）。
type policyBridge struct {
	groups *store.GroupRepo
	models *store.ModelRepo // 模型 id → 名（可见性对外的口径是名，不是 id）
}

func (b policyBridge) GroupsForAccount(ctx context.Context, accountID string) ([]auth.GroupPolicy, error) {
	id, err := parseID(accountID)
	if err != nil {
		// id 形状不对 = 主体不是本进程签发的：**无组**（fail-closed 的
		// 合并结果 = 全 false + 空可见集），不是"全部放开"。
		return nil, nil
	}
	snap, err := b.groups.LoadPolicySnapshot(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("app: 装载策略快照: %w", err)
	}
	return snapshotToGroupPolicies(ctx, b.models, snap)
}

// snapshotToGroupPolicies 把 store 快照翻成 auth 的合并输入形状。
//
// 字段逐个抄（手工对齐，编译期暴露漂移）：
//   - VisibleModels 是 []int64（id）：auth 侧合并后按**名**比较（v1 的
//     model 参数是名）。这里查一次 ModelRepo 把 id 全部翻成名——一次
//     IN 查询，不是 N 次（可见集的规模 = 组授权的模型数，个位数量级）。
//   - 布尔/数值键：指针直传（nil 语义两侧一致 = 未设）。
//   - 池：store.PoolRef 无 Used（快照不含计数器——池判定走 TryDebit
//     的条件 UPDATE，可见性路径用不到 Used；补 0 让形状完整）。
func snapshotToGroupPolicies(ctx context.Context, models *store.ModelRepo, snap *store.PolicySnapshot) ([]auth.GroupPolicy, error) {
	out := make([]auth.GroupPolicy, 0, len(snap.Groups))
	for i := range snap.Groups {
		g := &snap.Groups[i]
		// 组元数据（名称等）：合并算法不消费，但保留 ID 供审计侧追溯。
		gp := auth.GroupPolicy{
			Group: auth.Group{ID: g.GroupID},
		}
		if g.CanCreateKey != nil {
			v := *g.CanCreateKey
			gp.CanCreateKey = &v
		}
		if g.MaxKeys != nil {
			v := int(*g.MaxKeys)
			gp.MaxKeys = &v
		}
		// 限制类数字键：逐键转 auth.QuotaKey（-1 已在 store 装载侧剔除）。
		// 键→(unit,period) 的映射表与 store 的 policyKeyToQuotaKey 逐字
		// 同口径（闭集合 6 键；store 侧函数私有，桥侧复制表并注释互指）。
		for k, v := range g.Numbers {
			qk := policyKeyToUnitPeriod(k)
			if qk == nil {
				continue // 未知键（store 装载侧已滤已知键，防御性再滤）
			}
			if gp.Quotas == nil {
				gp.Quotas = map[auth.QuotaKey]int64{}
			}
			gp.Quotas[auth.QuotaKey{Unit: qk.unit, Period: qk.period}] = v
		}
		for _, c := range snap.ModelCaps {
			if c.GroupID != g.GroupID {
				continue
			}
			if gp.ModelQuotas == nil {
				gp.ModelQuotas = map[auth.ModelQuotaKey]int64{}
			}
			gp.ModelQuotas[auth.ModelQuotaKey{
				Model:  c.AbstractModelID,
				Unit:   string(c.Unit),
				Period: string(c.Period),
			}] = c.Cap
		}
		for _, p := range snap.Pools {
			if p.GroupID != g.GroupID {
				continue
			}
			gp.Pools = append(gp.Pools, auth.PoolRef{
				GroupID:         p.GroupID,
				AbstractModelID: p.AbstractModelID,
				Unit:            string(p.Unit),
				Period:          string(p.Period),
				Limit:           p.Limit,
				Used:            0, // 快照不含计数器；可见性路径不消费
			})
		}
		out = append(out, gp)
	}

	// 可见模型：id → 名（组归属不影响——∪ 的输入本来就是跨组并集，
	// 挂在哪个 GroupPolicy 上对合并没有差别；挂第一组只是形状需要）。
	if len(snap.VisibleModels) > 0 {
		names, err := modelNamesOf(ctx, models, snap.VisibleModels)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 {
			out[0].Models = names
		}
		// 无组却 VisibleModels 非空是自相矛盾的快照（并集来自组表）——
		// 防御性丢弃并记入合并的空输入（fail-closed 方向）。
	}
	return out, nil
}

// modelNamesOf 把抽象模型 id 批量翻成名（一次 IN 查询）。
//
// 为什么必须翻名：v1 的 model 参数、白名单存储的元素、/v1/models 的
// 列表项，对外口径全部是**名**（C2：抽象层契约）；id 只在存储内部。
// 不存在的 id（坏行）跳过——合并侧少一个模型是"宁少算"方向。
func modelNamesOf(ctx context.Context, models *store.ModelRepo, ids []string) ([]string, error) {
	all, err := models.ListAbstractModels(ctx, true) // includeDisabled：可见性含 deprecated（迁移期必须可见）
	if err != nil {
		return nil, fmt.Errorf("app: 读抽象模型目录: %w", err)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, m := range all {
		if want[m.ID] {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------------------------------------------------------------------------
// authzKeyStore 桥：Key 三读（EffectiveModels 的依赖面）
// ---------------------------------------------------------------------------

// authzKeyBridge 实现 auth 侧的窄接口 authzKeyStore（三读）。
//
// KeyDenylist 返回 []int64（store 形状）→ 翻成 []string（auth 形状，
// 名口径）。白名单 id 查不到名（模型被删）= 该白名单项失效——交集
// 自然收窄，与 05-keys §5「组收窄自动生效」同一条无损规则。
type authzKeyBridge struct {
	keys   *store.KeyRepo
	models *store.ModelRepo
}

func (b authzKeyBridge) KeyByHash(ctx context.Context, keyHash []byte) (*auth.APIKey, error) {
	k, err := b.keys.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, err
	}
	return keyRowToAuth(k, keyHash)
}

func (b authzKeyBridge) KeyByID(ctx context.Context, keyID string) (*auth.APIKey, error) {
	id, err := parseID(keyID)
	if err != nil {
		return nil, store.ErrNotFound
	}
	k, err := b.keys.GetKey(ctx, id)
	if err != nil {
		return nil, err
	}
	return keyRowToAuth(k, nil)
}

func (b authzKeyBridge) KeyDenylist(ctx context.Context, keyID string) ([]string, error) {
	id, err := parseID(keyID)
	if err != nil {
		return nil, store.ErrNotFound
	}
	ids, err := b.keys.KeyDenylist(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil // 空黑名单 = 不收窄（组授权全集），不报错
	}
	names, err := modelNamesOf(ctx, b.models, ids)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// keyRowToAuth 把 store 的 Key 行翻成 auth.APIKey（与 bridges.go 的
// keyAuthBridge 同一字段对齐逻辑——判定链只需要 ID/AccountID/哈希/状态）。
func keyRowToAuth(k *store.APIKey, keyHash []byte) (*auth.APIKey, error) {
	out := &auth.APIKey{
		ID:        k.ID,
		AccountID: k.AccountID,
		KeyHash:   keyHash,
		Prefix:    k.Prefix,
		Status:    auth.KeyStatus(k.Status),
	}
	if k.Name != nil {
		out.Name = *k.Name
	}
	if k.ExpiresAt != nil {
		t := unixTime(k.ExpiresAt)
		out.ExpiresAt = t
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 组装：判定链第 5 步的可见性判定器
// ---------------------------------------------------------------------------

// visibilityGate 是装配好的第 5 步判定器。
//
// 两个方法对应两个消费点：
//   - VisibleModels(ctx, principal)：/v1/models 的列表过滤（catalog 侧）；
//   - ModelVisible(ctx, principal, model)：v1 三端点的单模型判定
//     （candidates 侧——不可见 → ErrModelNotFound → 404）。
//
// 无主体（测试的 InjectTestAccount 占位通道 / 未挂 mw_keyauth 的部署态）
// 的语义：**空可见集**。判定链第 5 步的位置在第 4 步（账户实时状态）
// 之后——没有主体就没有"过完第 4 步"这回事，全可见等于把 1~4 步全部
// 废掉。这与 fail-closed 裁决同一条脊柱。
type visibilityGate struct {
	authz *auth.AuthzService
}

func newVisibilityGate(a *App) *visibilityGate {
	return &visibilityGate{
		authz: auth.NewAuthzNarrow(
			authzKeyBridge{keys: a.store.Repos().Keys, models: a.store.Repos().Models},
			accountReader{accounts: a.store.Repos().Accounts},
			policyBridge{groups: a.store.Repos().Groups, models: a.store.Repos().Models},
		),
	}
}

// accountReader 是判定链的账户读通道（authz 只需要 AccountByID——
// Key 两读已由 authzKeyBridge 承担；TouchLastLogin 等登录面方法不在
// 判定链依赖上，见 auth.AccountByIDOnly 接口注释）。
type accountReader struct {
	accounts *store.AccountRepo
}

// AccountByID 实现 auth.AccountByIDOnly。
func (r accountReader) AccountByID(ctx context.Context, id string) (*auth.Account, error) {
	nid, err := parseID(id)
	if err != nil {
		return nil, store.ErrNotFound
	}
	a, err := r.accounts.GetAccount(ctx, nid)
	if err != nil {
		return nil, err
	}
	return accountToAuth(a), nil
}

// VisibleModels 返回主体的实际可用集合（组授权 ∩ Key 白名单，名的口径）。
func (g *visibilityGate) VisibleModels(ctx context.Context, accountID, keyID string) ([]string, error) {
	if accountID == "" || keyID == "" {
		return nil, nil // 无主体 → 空集（fail-closed，见类型注释）
	}
	return g.authz.EffectiveModels(ctx, accountID, keyID)
}

// ModelVisible 报告 model 是否在主体可见集内。
// 大小写不敏感（与 auth.ModelCallable 同口径——名是契约但大小写不是）。
func (g *visibilityGate) ModelVisible(ctx context.Context, accountID, keyID, model string) (bool, error) {
	if accountID == "" || keyID == "" {
		return false, nil // 无主体 → 不可见（与空集一致）
	}
	return g.authz.ModelCallable(ctx, accountID, keyID, model)
}

// unitPeriod 是 policyKey 的 (unit, period) 拆解。
type unitPeriod struct{ unit, period string }

// policyKeyToUnitPeriod 与 store 的 policyKeyToQuotaKey 同一张表
// （闭集合 6 键；store 侧私有不导出，这里复制并在两处注释互指——
// 新增额度键必须同时改两表，评审看 diff 即对）。
func policyKeyToUnitPeriod(key store.PolicyKey) *unitPeriod {
	switch key {
	case store.PolicyQuotaRequestDay:
		return &unitPeriod{"request", "day"}
	case store.PolicyQuotaRequestMonth:
		return &unitPeriod{"request", "month"}
	case store.PolicyQuotaTokenDay:
		return &unitPeriod{"token", "day"}
	case store.PolicyQuotaTokenMonth:
		return &unitPeriod{"token", "month"}
	case store.PolicyQuotaCreditDay:
		return &unitPeriod{"credit", "day"}
	case store.PolicyQuotaCreditMonth:
		return &unitPeriod{"credit", "month"}
	}
	return nil
}
