// Package auth 实现账户、权限类、API Key、会话与登录防护的纯领域逻辑。
//
// 架构边界（为什么这样切）：
//   - 本包**不做 HTTP**：只暴露可被 HTTP 中间件调用的纯接口
//     （Authenticate / ResolveClass / SessionService / KeyService …），
//     协议解析与状态码映射属于 internal/httpapi。
//   - 本包**不碰 SQLite**：所有持久化都通过 storeiface.go 里的窄接口注入，
//     由 internal/store（storage 分支）实现；在此之前测试用内存假体。
//   - 本包**不做鉴定**：verified / suspect / unverified / trusted 四态属于 models 层。
//
// 语义唯一事实源：docs/security/01-accounts.md、02-authn.md、03-groups.md、
// 04-authz.md、05-keys.md。事实源优先级：用户指令 > docs/PLAN.md > 分册。
//
// 已定案的几条硬语义（写错会被 Contract 测试抓出来）：
//   - 类级提权已取消：登录一次即该账户全部权限，会话 24 小时固定窗口，不做滑动续期。
//   - 额度挂在账户级，Key 没有独立额度（Key 只保留认证 / 白名单收窄 / 生命周期）。
//   - 多组时：放开类取并集（布尔开关 deny 优先、Key 数量取最大值），
//     限制类取最严（逐 (unit, period) 键取 min），共享池不合并、
//     逐个都要满足（池的实现归 internal/meter 与本包的 EffectivePolicy 描述）。
//   - 密码用 argon2id（64 MiB / t=3 / p=2），API Key 与恢复码以外的机器凭据用 SHA-256。
package auth
