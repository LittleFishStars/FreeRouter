package app

// group_policy_codec.go —— 管理面「策略整文档」⇄ store「键值行 + 池行 + 按模型上限行」的编解码。
//
// ## 为什么必须有这个文件（此前的真实缺陷）
//
// 桥早期把整份策略 JSON 塞进 group_policies 的一个假键 "admin_doc"。写侧
// 因此必然失败（SetPolicy 只接受已知策略键，见 effective_policy.go 的
// policyKindOf），而**即使写成功也不会生效**：运行时的策略合并
// （GroupRepo.LoadPolicySnapshot）逐键读真实键值表，假键里的文档它根本不读。
// 也就是说「管理面改组策略」这件事在数据层没有落点——PUT 500 只是把一个
// 更严重的静默失效暴露出来了。
//
// ## 编解码口径
//
// 文档字段名以 docs/api/02-admin.md §6.1 为准，键名以 docs/security/03-groups.md
// 的键表（= store 的 PolicyKey 常量）为准：
//
//	allow_create_key      -> can_create_key        (bool)
//	max_keys              -> max_keys              (int)
//	account_rpm_limit     -> rpm_limit             (int)
//	account_tpm_limit     -> tpm_limit             (int)
//	account_max_concurrency -> max_concurrency     (int)
//	account_quotas[]      -> quota_{unit}_{period} (int，逐维度×周期)
//	model_quota_caps[]    -> group_model_quota_caps 表行（声明表，不是键）
//	pool[]                -> group_quota_pools 表行（model=null 是合计池）
//
// 「未设 = 键不存在」而不是写 0/-1：0 是「全拒」、-1 是「不限」，两者语义
// 相反且都不同于「没配」；删行才是没配（03-groups 第 3 节的哨兵定案）。
//
// model 字段是**抽象模型名**（对外唯一命名空间），落库要翻成 id——翻不到即
// 整单报错：给不可见模型设限额是死配置（03-groups 4.5 的保存期判据）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// policyDoc 是管理面策略文档的解析形状。
type policyDoc struct {
	AllowCreateKey      *bool         `json:"allow_create_key"`
	MaxKeys             *int64        `json:"max_keys"`
	AccountRPMLimit     *int64        `json:"account_rpm_limit"`
	AccountTPMLimit     *int64        `json:"account_tpm_limit"`
	AccountMaxConc      *int64        `json:"account_max_concurrency"`
	AccountQuotas       []quotaRowDoc `json:"account_quotas"`
	ModelQuotaCaps      []capRowDoc   `json:"model_quota_caps"`
	Pool                []poolRowDoc  `json:"pool"`
}

type quotaRowDoc struct {
	Unit   string `json:"unit"`
	Limit  int64  `json:"limit"`
	Period string `json:"period"`
}

type capRowDoc struct {
	Model  string `json:"model"`
	Unit   string `json:"unit"`
	Limit  int64  `json:"limit"`
	Period string `json:"period"`
}

type poolRowDoc struct {
	Model  *string `json:"model"` // null = 合计池
	Unit   string  `json:"unit"`
	Period string  `json:"period"`
	Limit  int64   `json:"limit"`
}

// quotaPolicyKey 把 (unit, period) 折成策略键（quota_request_day 等）。
func quotaPolicyKey(unit, period string) (store.PolicyKey, error) {
	k := store.PolicyKey("quota_" + unit + "_" + period)
	switch k {
	case store.PolicyQuotaRequestDay, store.PolicyQuotaRequestMonth,
		store.PolicyQuotaTokenDay, store.PolicyQuotaTokenMonth,
		store.PolicyQuotaCreditDay, store.PolicyQuotaCreditMonth:
		return k, nil
	}
	return "", fmt.Errorf("额度维度/周期组合非法：%s/%s（unit 取 request|token|credit，period 取 day|month）", unit, period)
}

// encodePolicyDoc 把策略文档翻成「键值行 + 池行 + 上限行」三张表的写入意图。
func encodePolicyDoc(ctx context.Context, models *store.ModelRepo, doc json.RawMessage) (
	policies map[store.PolicyKey]string,
	caps []store.ModelCap,
	pools []store.PoolRef,
	err error,
) {
	var d policyDoc
	if len(doc) == 0 {
		doc = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, nil, nil, fmt.Errorf("策略文档形状非法：%w", err)
	}
	policies = map[store.PolicyKey]string{}
	put := func(k store.PolicyKey, v any) {
		b, _ := json.Marshal(v)
		policies[k] = string(b)
	}
	if d.AllowCreateKey != nil {
		put(store.PolicyCanCreateKey, *d.AllowCreateKey)
	}
	if d.MaxKeys != nil {
		if *d.MaxKeys < -1 {
			return nil, nil, nil, fmt.Errorf("max_keys 不得小于 -1（-1=不限，0=全拒）")
		}
		put(store.PolicyMaxKeys, *d.MaxKeys)
	}
	if d.AccountRPMLimit != nil {
		put(store.PolicyRPMLimit, *d.AccountRPMLimit)
	}
	if d.AccountTPMLimit != nil {
		put(store.PolicyTPMLimit, *d.AccountTPMLimit)
	}
	if d.AccountMaxConc != nil {
		put(store.PolicyMaxConcurrency, *d.AccountMaxConc)
	}
	for _, q := range d.AccountQuotas {
		k, kerr := quotaPolicyKey(q.Unit, q.Period)
		if kerr != nil {
			return nil, nil, nil, kerr
		}
		if q.Limit < 0 {
			return nil, nil, nil, fmt.Errorf("account_quotas.limit 不得为负（不限=不出现该行）")
		}
		put(k, q.Limit)
	}
	modelID := func(name string) (string, error) {
		m, err := models.GetAbstractModelByName(ctx, name)
		if err != nil {
			return "", fmt.Errorf("抽象模型 %q 不存在（给不可见模型设限额是死配置）", name)
		}
		return m.ID, nil
	}
	for _, c := range d.ModelQuotaCaps {
		if c.Model == "" {
			return nil, nil, nil, fmt.Errorf("model_quota_caps[].model 必填（合计额度走 account_quotas）")
		}
		mid, merr := modelID(c.Model)
		if merr != nil {
			return nil, nil, nil, merr
		}
		unit := store.QuotaUnit(c.Unit)
		period := store.QuotaPeriod(c.Period)
		if c.Limit <= 0 {
			return nil, nil, nil, fmt.Errorf("model_quota_caps[].limit 必须为正（不限=不出现该行）")
		}
		caps = append(caps, store.ModelCap{AbstractModelID: mid, Unit: unit, Period: period, Cap: c.Limit})
	}
	for _, pl := range d.Pool {
		mid := store.TotalScope
		if pl.Model != nil && *pl.Model != "" {
			var merr error
			if mid, merr = modelID(*pl.Model); merr != nil {
				return nil, nil, nil, merr
			}
		}
		if pl.Limit <= 0 {
			return nil, nil, nil, fmt.Errorf("pool[].limit 必须为正（停用池=不出现该行）")
		}
		pools = append(pools, store.PoolRef{
			AbstractModelID: mid,
			Unit:            store.QuotaUnit(pl.Unit),
			Period:          store.QuotaPeriod(pl.Period),
			Limit:           pl.Limit,
		})
	}
	return policies, caps, pools, nil
}

// decodePolicyDoc 反向：从三张表拼回管理面文档（读侧展示）。
func decodePolicyDoc(ctx context.Context, repos *store.Repos, gid string) json.RawMessage {
	out := map[string]any{}
	keys := []store.PolicyKey{
		store.PolicyCanCreateKey, store.PolicyMaxKeys,
		store.PolicyRPMLimit, store.PolicyTPMLimit, store.PolicyMaxConcurrency,
		store.PolicyQuotaRequestDay, store.PolicyQuotaRequestMonth,
		store.PolicyQuotaTokenDay, store.PolicyQuotaTokenMonth,
		store.PolicyQuotaCreditDay, store.PolicyQuotaCreditMonth,
	}
	fieldOf := map[store.PolicyKey]string{
		store.PolicyCanCreateKey:      "allow_create_key",
		store.PolicyMaxKeys:           "max_keys",
			store.PolicyRPMLimit:          "account_rpm_limit",
		store.PolicyTPMLimit:          "account_tpm_limit",
		store.PolicyMaxConcurrency:    "account_max_concurrency",
	}
	quotas := []map[string]any{}
	for _, k := range keys {
		raw, err := repos.Groups.GetPolicy(ctx, gid, k)
		if err != nil || raw == "" {
			continue // 未设 = 不出现（读端把缺失视为不限）
		}
		if f, has := fieldOf[k]; has {
			var v any
			if json.Unmarshal([]byte(raw), &v) == nil {
				out[f] = v
			}
			continue
		}
		// 额度键：从键名反解 unit/period。
		var n int64
		if json.Unmarshal([]byte(raw), &n) != nil {
			continue
		}
		unit, period, ok := splitQuotaKey(k)
		if !ok {
			continue
		}
		quotas = append(quotas, map[string]any{"unit": unit, "period": period, "limit": n})
	}
	if len(quotas) > 0 {
		out["account_quotas"] = quotas
	}
	if caps, err := repos.Quotas.ListGroupModelCaps(ctx, gid, nil); err == nil && len(caps) > 0 {
		rows := []map[string]any{}
		for _, c := range caps {
			name := modelName(ctx, repos, c.AbstractModelID)
			rows = append(rows, map[string]any{"model": name, "unit": string(c.Unit), "period": string(c.Period), "limit": c.Cap})
		}
		out["model_quota_caps"] = rows
	}
	if pools, err := repos.Quotas.ListPoolsByGroup(ctx, gid); err == nil && len(pools) > 0 {
		rows := []map[string]any{}
		for _, pl := range pools {
			var model any
			if pl.AbstractModelID != store.TotalScope {
				model = modelName(ctx, repos, pl.AbstractModelID)
			}
			rows = append(rows, map[string]any{"model": model, "unit": string(pl.Unit), "period": string(pl.Period), "limit": pl.Limit})
		}
		out["pool"] = rows
	}
	b, _ := json.Marshal(out)
	return b
}

// splitQuotaKey 从 quota_request_day 反解出 request/day。
func splitQuotaKey(k store.PolicyKey) (string, string, bool) {
	s := string(k)
	const prefix = "quota_"
	if len(s) <= len(prefix) {
		return "", "", false
	}
	rest := s[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '_' {
			return rest[:i], rest[i+1:], true
		}
	}
	return "", "", false
}

// modelName 按 id 取对外名（读侧展示；查不到回 id 字符串而不是空）。
func modelName(ctx context.Context, repos *store.Repos, id string) string {
	if m, err := repos.Models.GetAbstractModel(ctx, id); err == nil {
		return m.Name
	}
	return id
}

// applyPolicyDoc 把策略文档整组替换进三张表（键值 / 池 / 按模型上限）。
//
// 必须在调用方的事务里执行（tx *store.Repos）：三张表半套生效的状态
// （键换了、池没换）会让合并结果不可解释。
//
// 池与上限是「行」而不是键：先删该组全部行再按文档写入，与键值表的
// 整组替换同一语义（PUT = 集合 A → 集合 B 的完整记录）。
func applyPolicyDoc(ctx context.Context, tx *store.Repos, gid string, doc json.RawMessage) error {
	policies, caps, pools, err := encodePolicyDoc(ctx, tx.Models, doc)
	if err != nil {
		return err
	}
	if err := tx.Groups.SetGroupPolicyKeys(ctx, gid, policies); err != nil {
		return err
	}
	// 清旧池行与旧上限行，再写新行。
	oldPools, err := tx.Quotas.ListPoolsByGroup(ctx, gid)
	if err != nil {
		return err
	}
	for _, pl := range oldPools {
		if err := tx.Quotas.DeletePool(ctx, gid, pl.AbstractModelID, pl.Unit, pl.Period); err != nil {
			return err
		}
	}
	oldCaps, err := tx.Quotas.ListGroupModelCaps(ctx, gid, nil)
	if err != nil {
		return err
	}
	for _, c := range oldCaps {
		if err := tx.Quotas.DeleteGroupModelCap(ctx, gid, c.AbstractModelID, c.Unit, c.Period); err != nil {
			return err
		}
	}
	for _, c := range caps {
		cc := c
		cc.GroupID = gid
		if err := tx.Quotas.SetGroupModelCap(ctx, cc); err != nil {
			return err
		}
	}
	for _, pl := range pools {
		if err := tx.Quotas.SetPool(ctx, gid, pl.AbstractModelID, pl.Unit, pl.Period, pl.Limit); err != nil {
			return err
		}
	}
	return nil
}
