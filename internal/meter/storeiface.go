package meter

import (
	"context"
	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"time"
)

// 本文件与 auth/storeiface.go 同一架构边界：只声明**读取能力**，不出现 SQL、
// 表名、事务。storage 分支实现它；测试用内存假体。
//
// 为什么 meter 也走窄接口而不是 import internal/store（评审升级条③）：
// 与 auth 同一理由——表结构属于 storage 的领域，两处各写一份必然漂移；
// 且 meter 的纯判定面（CheckQuotaRow/PrecheckQuota/CheckPools/limiter）
// 已经"只认数字"，接口只存在于"从哪拿数字"这一条装配路径上。

// QuotaStore 是 account_quotas 行与组池的只读快照接口。
type QuotaStore interface {
	// AccountQuotaRows 返回该账户**当期**的额度行（合计行 + 全部按模型行）。
	// 锚点过期由存储层负责归零（惰性重置：读时 period_anchor != 当期 → Used=0）；
	// 本包不猜存储的时区口径，只要求"返回的就是当期数字"。
	// 无行 = 该维度不限（01-accounts.md 行化定案）。
	AccountQuotaRows(ctx context.Context, accountID string) ([]QuotaRow, error)

	// GroupPools 返回指定账户所属全部组的共享池快照（Used 为当期）。
	// 与 auth.PolicyRepository.GroupsForAccount 的 Pools 字段二选一即可：
	// 池快照随组策略走时调用方不必再经这里——本方法服务于"策略与用量
	// 分两张表读"的存储形状（05-storage.md 3.3 的 group_quota_pools）。
	GroupPools(ctx context.Context, accountID string) ([]auth.PoolRef, error)
}

// PrecheckFromStore 是判定链第 6 步额度的装配入口：读行 + 逐行判定。
// store 为 nil 时跳过额度预检（单机/未接入存储的降级路径；速率与池闸照常）。
//
// 为什么单独一个装配函数：中间件不该自己写"取行→过滤模型→取成本"的循环——
// 那是最容易把"无行=不限"写成"limit=0=不限"的地方（0 与 -1 的反向语义陷阱），
// 收敛到一个函数，评审只看一处。
func PrecheckFromStore(ctx context.Context, store QuotaStore, accountID, modelID string, cost UsageCost, now time.Time) (time.Duration, error) {
	if store == nil {
		return 0, nil
	}
	rows, err := store.AccountQuotaRows(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return PrecheckQuota(rows, modelID, cost, now)
}
