package app

// policy_bridge_entry_test.go —— 回归测试：策略快照转换不得 panic。
//
// 为什么专门测这个：`snapshotToGroupPolicies` 与 `keyRowToAuth` 曾用
// `strconv.FormatInt(x, 0)` 把 id 转成字符串，而 FormatInt 的 base 合法范围
// 是 2–36（base=0 是 ParseInt 的"自动探测前缀"语义，FormatInt 没有这个语义）
// —— 实测直接 panic：`strconv: illegal AppendInt/FormatInt base`。
//
// 这条路径此前零测试覆盖，所以缺陷一直潜伏：只要管理员给组配了「按模型
// 额度上限」或「共享池」，或 Key 鉴权走 keyRowToAuth，进程就会崩。本测试
// 用带 ModelCaps + Pools 的快照把该分支钉死。

import (
	"context"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// TestSnapshotToGroupPoliciesNoPanic 覆盖 ModelCaps / Pools / Numbers 三个分支。
func TestSnapshotToGroupPoliciesNoPanic(t *testing.T) {
	snap := &store.PolicySnapshot{
		AccountID: "acct-7",
		GroupIDs:  []string{"grp-3"},
		Groups: []store.GroupPolicySet{{
			GroupID: "grp-3",
			// Numbers 走 policyKeyToUnitPeriod（6 键闭集合之一）。
			Numbers: map[store.PolicyKey]int64{store.PolicyRPMLimit: 60},
		}},
		// 关键：这两个切片非空才会执行到 FormatInt。
		ModelCaps: []store.ModelCap{{
			GroupID: "grp-3", AbstractModelID: "am-42",
			Unit: store.UnitRequest, Period: store.PeriodDay, Cap: 50,
		}},
		Pools: []store.PoolRef{{
			GroupID: "grp-3", AbstractModelID: store.TotalScope,
			Unit: store.UnitToken, Period: store.PeriodMonth, Limit: 100000,
		}},
	}

	// 不 panic 即通过；再校验两处 id 原样透传（UUID 改造后无十进制渲染）。
	got, err := snapshotToGroupPolicies(context.Background(), nil, snap)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 个 GroupPolicy，got %d", len(got))
	}
	if got[0].Group.ID != "grp-3" {
		t.Fatalf("组 id 应原样透传，got %q", got[0].Group.ID)
	}
	for k, v := range got[0].ModelQuotas {
		if k.Model != "am-42" {
			t.Fatalf("按模型上限的 model 应原样透传，got %q（值 %d）", k.Model, v)
		}
	}
	if len(got[0].Pools) != 1 {
		t.Fatalf("应有 1 个池，got %d", len(got[0].Pools))
	}
	if got[0].Pools[0].GroupID != "grp-3" {
		t.Fatalf("池的 GroupID 应原样透传，got %q", got[0].Pools[0].GroupID)
	}
}

// TestKeyRowToAuthNoPanic 覆盖 keyRowToAuth 的两处 id 转换。
func TestKeyRowToAuthNoPanic(t *testing.T) {
	name := "prod"
	k := &store.APIKey{
		ID: "key-11", AccountID: "acct-7", Prefix: "fr-abc", Name: &name, Status: store.KeyActive,
	}
	out, err := keyRowToAuth(k, []byte("hash"))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	// id 直接透传（UUID 改造后 auth 与 store 同形状，无十进制转换）。
	if out.ID != "key-11" || out.AccountID != "acct-7" {
		t.Fatalf("id 应原样透传，got ID=%q AccountID=%q", out.ID, out.AccountID)
	}
	if out.Name != "prod" || out.Prefix != "fr-abc" {
		t.Fatalf("字段搬运错误: %+v", out)
	}
}
