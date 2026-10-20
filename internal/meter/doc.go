// Package meter 实现速率、额度与并发闸门——纯进程内状态 + 可注入的判定位，
// 不做 HTTP、不碰 SQLite。
//
// 架构边界（为什么这样切）：
//   - 与 internal/auth 同构：本包只暴露可被中间件调用的方法，
//     429/503 的映射、Retry-After 头的写出属于 internal/httpapi。
//   - 计量的**持久部分**（usage_daily、账户 quota_used、组池 pool_used）在
//     internal/store：终态结算必须与 requests 落库同事务（03-groups.md §4），
//     一个进程内包无权也不会跨事务写它们。本包负责的是：
//     ① 速率/并发的**进程内闸门**（秒级窗口，落库只会更差）；
//     ② 额度/池判定的**纯函数与 0 语义**——数字从 store 读出来交给它判，
//     判定即扣减的条件 UPDATE 留在 store 的领域。
//   - 依赖方向 meter → auth（取错误类型与 Policy 形状），反向禁止。
//
// 语义事实源：docs/security/03-groups.md（合并与池）、05-keys.md §4（速率账户级）、
// docs/architecture/06-runtime.md §2.3（全局闸门与 queue_timeout）、
// docs/api/03-errors.md §3（错误码）。
//
// 关键定案（写错会被 Contract 测试抓出来，main 7af1e78 后口径）：
//   - 两套哨兵刻意分开建模（评审点名）：
//     **速率域**（limiter.AccountLimits，对齐 accounts 三列）：-1 = 不限，
//     0 = 全拒；**额度域**（quota.QuotaRow，对齐 account_quotas 行）：
//     无行 = 不限，有行 limit ≥ 0，limit = 0 = 零额度全拒；
//     **-1 流入额度域即装配错误**，判定函数对负数返回 internal_error 而非猜意图。
//   - 全局域沿用 config 口径：GlobalRPM / GlobalConcurrency 0 = 不限制，
//     GlobalDailyTokens 0 = 不熔断；limits.default_* 只写初值，
//     不参与运行期合并（03-groups.md §3.5）。
//   - 额度与速率闸门挂在**账户**上，不随 Key 变化（05-keys.md §4）：
//     同一账户两把 Key 读同一个桶，"开两把 Key 分担流量"绕不过任何一道闸。
//   - 四类拒绝必须可区分（rate_limited / quota_exceeded / pool_exhausted /
//     too_many_attempts），客户端动作完全不同；too_many_attempts 在 auth 产生，
//     本包不复述。
package meter
