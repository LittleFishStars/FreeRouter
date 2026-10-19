> 分册：权限与安全 · 依赖：[01-accounts.md](01-accounts.md)、[03-groups.md](03-groups.md)、[04-authz.md](04-authz.md) · 对应总纲：PLAN.md 第 2.2 节、第 2.6 节、第 9.1 节、第 9.4 节、第 10 章

# API Key 生命周期与配额

# API Key 生命周期与配额
## 1. 归属硬约束
> **API Key 只能归属带 `tenant` 类的账户。**（PLAN 2.2、2.3、9.4）
| 关系 | 约束 | 为什么 |
|---|---|---|
| Key → 账户 | 归属账户**必须**带 `tenant` 类 | Key 是"调用模型"的凭据；只有 `tenant` 类提供调用能力（见 [01-accounts.md](01-accounts.md#2-三个权限类)）。允许 `contributor` 或纯 `admin` 账户持有 Key，等于给没有调用能力的主体发调用凭证，判定链第 4 步就会拒掉它——发得出、用不了，是纯粹的状态污染 |
| Key → 组 | **组不持有 Key**；组只约束 Key 的数量与额度上限 | 组一旦持有 Key 就等价于团队共享凭据，团队语义会从后门回来（见 [03-groups.md](03-groups.md#1-组是什么不是什么)） |
| Key → 凭据 | **无任何关系** | 凭据不可自选（PLAN 2.3）：用哪条上游凭据由策略引擎决定，与调用方持哪把 Key 无关 |

**为什么"能建 Key"这件事由组策略决定**（PLAN 2.6 放开类）：新建 Key 是创建能力，管理员需要在组粒度上决定"这批人能不能自建"。`tenant` 类回答"你有没有调用能力"，组策略回答"你这一批人允不允许建"，两者是不同的问题。

## 2. 生命周期

### 状态

| 状态 | 可否调用 | 进入方式 | 可否恢复 | 为什么需要 |
|---|---|---|---|---|
| `active` | ✅ | 创建后默认 | — | 正常态 |
| `disabled` | ❌ | 本人停用 / 管理员停用 | ✅ 可恢复 | 需要"临时停一下"的开关（换客户端、排查异常用量） |
| `revoked` | ❌ | 即时吊销（本人或管理员） | ❌ 不可恢复 | 泄露处置必须是一条**单向**路径：可以被恢复的吊销不是吊销 |
| `expired` | ❌ | 由 `expires_at` 到期推导 | ❌ | 到期是自动事实，不需要（也不应该）由人触发 |

**为什么 `disabled` 与 `revoked` 要分开**：前者是运营动作（"先停一下"），后者是安全动作（"这把已经泄露了"）。如果只有一种状态，那么每一次临时停用都会永久毁掉一把 Key，用户下一次就倾向于不报告异常，反而降低安全水位。

> **待定**：PLAN 第 8 章 MVP 条目同时出现"创建/禁用/过期"与第 9.4 节的"支持即时吊销"，两者是同一状态还是两个状态（本册按两个状态撰写），以及 `expires_at` 为空是否表示永不过期。

### 生命周期事件

| 事件 | 触发者 | 联动 |
|---|---|---|
| 创建 | 账户本人（需组策略允许） | **明文只返回一次**；默认额度 0、白名单为空（PLAN 9.1） |
| 改名 | 本人 | 仅影响可读性，不影响鉴权 |
| 停用 / 恢复 | 本人 / 管理员 | 停用立即 401；恢复立即生效 |
| 即时吊销 | 本人 / 管理员 | 立即 401，**不等过期**（PLAN 9.4） |
| 配置调整（额度、限速、白名单） | 本人 | 只能收窄到组策略之内（见第 4、5 节） |
| 账户 `disabled` | 管理员 | 名下**全部** Key 立即 401（见 [04-authz.md](04-authz.md#5-禁用联动)） |
| 账户被撤销 `tenant` 类 | 管理员 | 同上 |
| 账户被合并 | 管理员 | Key 改挂 target 账户后继续可用（见 [01-accounts.md](01-accounts.md#4-账户合并)） |

**为什么吊销要"立即"而不是"最多一个 TTL 后"**：吊销的语义是"从此刻起不可用"。如果实现层需要缓存，必须有失效机制（见 [04-authz.md](04-authz.md#5-禁用联动)）；把"稍后生效"写进设计，等于把泄露处置的窗口期写进设计。

**为什么吊销不删除历史 `requests`**：历史请求是**已经发生的计量事实**，也是贡献者归因链的一部分（`resolved_credential_id`，PLAN 3.7）。删 Key 就删历史，会让"这个月的用量到底是谁的"永久无解，也让贡献者的贡献统计出现空洞。

## 3. 格式、哈希与展示

| 项 | 取值 | 依据 |
|---|---|---|
| 格式 | `fr-` + 32 字节 base62 | PLAN 9.4 |
| 存储 | **只存 `key_hash`（SHA-256）** | PLAN 9.4 |
| 明文 | **仅创建时返回一次**，不落库、不落日志 | PLAN 9.4 |
| 列表展示 | `fr-xxxx…xxxx` 前缀 | PLAN 9.4 |
| 比较 | `crypto/subtle.ConstantTimeCompare` | PLAN 9.4 |
| 名字 | `name` 字段，人读用途 | PLAN 第 10 章 |

**为什么 `fr-` 前缀**：Key 是机器凭据，会被写进配置文件、环境变量、CI secret、客户端库。一个可识别的前缀让两个问题立刻可判：一是"这是不是我们的 Key"（便于泄漏扫描与日志脱敏规则命中），二是"这是哪一类凭据"（与上游凭据、会话令牌区分）。

**为什么用 SHA-256 而不是 argon2id**：密码是低熵的（人选出来的），所以必须用慢哈希抵抗离线爆破；API Key 是 32 字节随机值（≈256 位熵），离线爆破不可行，慢哈希的唯一效果是让每一个入站请求都付出一次昂贵的推导——而鉴权发生在每一个请求上（见 [04-authz.md](04-authz.md#4-运行时鉴权顺序入站模型请求) 第 2 步）。

**为什么只展示前缀**：列表页要能让人区分"这是我的哪把 Key"，又不能让人从截图里读回整把 Key。展示首尾各几位是唯一同时满足这两点的做法——用户凭开头认人，泄露的片段不足以拼出可用凭据。

**为什么明文只返回一次**：与密码重置、CSV 导入生成的随机密码同一条原则（见 [02-authn.md](02-authn.md#3-管理员导入账密)）。可以被反复读取的 Key 等价于一个所有读过列表页的人共同持有的凭据，吊销粒度退化为"整账户"。

## 4. 配额与限速维度

| 维度 | 字段 / 来源 | 生效值 | 超限响应 |
|---|---|---|---|
| 总额度 | `quota_limit` / `quota_used` | 见待定 | 拒绝 + 日志留痕 |
| 日 token | `daily_token_cap`（Key 字段） | **min(Key 值, 组策略)** | 拒绝 + 日志留痕 |
| 月 token | 组策略 `monthly_token_cap` | 见待定（Key 表无对应字段） | 拒绝 + 日志留痕 |
| RPM | `rpm_limit` | **min(Key 值, 组策略)** | **429 + 有值 `Retry-After`**（PLAN 9.5、验收 4） |
| TPM | `tpm_limit` | **min(Key 值, 组策略)** | 同上 |
| 并发 | `max_concurrency` | **min(Key 值, 组策略)** | 同上 |
| 全站 | 三维令牌桶（全局 / 按 Key / 按 IP） | 全局默认兜底 | 429 + `Retry-After` |
| 全站预算 | 全局预算熔断 | 管理员配置 | 拒绝 + 告警（PLAN 9.1） |

三条硬规则：

1. **每个 Key 强制有日 / 月 token 上限与并发上限**（PLAN 9.1）。Key 上没设时，必须由**全局默认兜底**填上，不能理解为"无限制"。
   - 为什么：无上限的 Key 是唯一一种"一次泄露 = 无限损失"的资源形态。兜底是让"忘记配置"的后果有界。
2. **Key 上的值只能 ≤ 组策略值**（收窄）。
   - 为什么：与 [04-authz.md](04-authz.md#2-三层收敛) 同一逻辑。Key 是用户完全控制的资源，若 Key 上的额度能超过组上限，组上限就成了摆设——用户建一把新 Key 即可自我提权。
3. **维度之间相互独立，任一触顶即拒绝**。
   - 为什么：日额度、RPM、并发防的是三种不同的滥用形态（总量刷、瞬时刷、占用式刷）。用其中一个去兜另外两个，会让某一类滥用在另一个维度上完全没有约束。

**为什么日额度与 RPM 的响应形态不同**：RPM 是"稍后就能用"，客户端需要 `Retry-After` 才能正确退避；日额度是"今天没戏"，重试没有意义（状态码是否复用 429 见 [04-authz.md](04-authz.md#9-待定)）。

**为什么用 `usage_daily` 而不是实时扫 `requests` 算日用量**：日额度要在每一个请求前判定（判定链第 6 步）。实时聚合全表会让鉴权成本随历史请求量增长，而 `usage_daily(day, key_id, abstract_model_id, real_model_id, ...)` 正好是这一维度的预聚合（PLAN 第 10 章）。

## 5. 模型白名单

| 项 | 规则 | 为什么 |
|---|---|---|
| 载体 | `key_model_allow(key_id, abstract_model_id)` | PLAN 第 10 章 |
| 默认值 | **空**（新建即空，PLAN 9.1） | 默认拒绝：验收 5 要求"新建 Key 不改任何配置时无法发起请求" |
| 方向 | **只能从"租户可访问集合"收窄**，即 ⊆ (∪ 所属组的抽象模型) | PLAN 2.6、11.3 |
| 元素 | 只能是**抽象模型** | 真实模型对租户不可见（PLAN 3.1）；白名单里出现真实模型名本身就是泄漏 |
| 最终可用 | `(∪ 所属组的抽象模型) ∩ Key 白名单` | 见 [04-authz.md](04-authz.md#2-三层收敛) |
| 组策略 | `Key 白名单能否放宽`（放开类） | 见 [03-groups.md](03-groups.md#3-组策略清单) 与其中的待定 |

**为什么默认是空而不是"继承组授权"**：如果白名单默认等于组授权，那么"新建 Key"这个动作本身就等于把该账户的全部模型授权一次性发出去。默认空让每一次授权扩大都成为一次**显式动作**，也让"这把 Key 到底能干什么"在列表页上可读。

**为什么白名单是 Key 级而不是账户级**：同一个账户常有多把 Key（一把给本地编辑器、一把给 CI、一把给手机）。收窄的动机通常是"这把 Key 只做某一件事"，这是 Key 的属性，不是人的属性。

**账户被移出某个组后，Key 白名单怎么变**：白名单**不变**，但最终可用集合会因组授权收窄而自动收窄（交集变小）。

- 为什么不做级联清理：级联删除会静默丢掉用户配置，而后把账户加回组时配置已经没了。保留白名单、让它被交集自动收窄，是无损且可逆的。

## 6. 吊销联动

| 触发 | 效果 | 依据 |
|---|---|---|
| Key 即时吊销 | 该 Key 立即 401 | PLAN 9.4 |
| Key 停用 | 该 Key 立即 401（可恢复） | 本册第 2 节 |
| Key 过期 | 该 Key 立即 401 | `expires_at` |
| 账户 `disabled` | 名下全部 Key 立即 401，**不依赖缓存** | PLAN 9.3、验收 8 |
| 撤销账户 `tenant` 类 | 名下全部 Key 立即 401 | 见 [01-accounts.md](01-accounts.md#2-三个权限类) |
| 账户被合并 | source 名下 Key 改挂 target，随后以 target 的身份继续服务 | 见 [01-accounts.md](01-accounts.md#4-账户合并) |
| 账户改密 | **不影响 Key** | 见下 |

**为什么改密不吊销 Key**：Key 与密码是两条独立的凭据链（一个是机器凭据，一个是人的登录凭据），它们之间没有推导关系——密码泄露不会让攻击者拿到 Key，Key 泄露也不会让他登录控制台。把两者绑在一起，会让用户每次改密码都要重新配置所有客户端，逼出一个"从不改密码"的习惯。账户禁用则是另一回事：它表示"这个人整体不可信了"，因此**所有**凭据一起失效。

**吊销后仍未结束的在途请求**：见待定。

## 7. Key 与账户 / 组的关系

```
账户（必须带 tenant 类）
  └── API Key（1..n，数量受组策略上限约束）
        ├── key_model_allow     → 只能收窄组授权
        ├── 额度 / 限速 / 并发   → 只能收窄组策略
        └── (无) 与上游凭据的任何关系

组（不持有 Key，只提供上限）
  ├── group_model      → 组授权（放开类，并集）
  └── group_policies   → 额度 / 限速 / 并发上限（限制类，最严）
```

| 问题 | 答案 | 为什么 |
|---|---|---|
| 一个账户能有几把 Key？ | 受组策略 `Key 数量上限` 约束（多组取**最大值**） | 该键归放开类（[03-groups.md](03-groups.md#3-组策略清单) 第 3 节定案） |
| 组能共用一把 Key 吗？ | **不能** | 组不持有 Key（PLAN 2.6） |
| 别人的 Key 我能看到吗？ | 不能（404 或空集） | PLAN 2.3 资源分表归属 |
| 管理员能看到我的 Key 吗？ | 能看到**前缀与元数据**（审计与处置需要），看不到明文 | PLAN 9.4：明文永不回显，连创建者本人之后也看不到 |
| 一把 Key 能跨账户吗？ | 不能 | 归属唯一 |

## 8. 数据结构与接口签名

```go
type KeyStatus string

const (
    KeyActive   KeyStatus = "active"
    KeyDisabled KeyStatus = "disabled" // 可恢复
    KeyRevoked  KeyStatus = "revoked"  // 单向，不可恢复
    KeyExpired  KeyStatus = "expired"  // 由 expires_at 推导
)

type APIKey struct {
    ID         string
    AccountID  string     // 必须带 tenant 类
    KeyHash    []byte     // SHA-256；不存明文
    Prefix     string     // 展示用：fr-xxxx…xxxx
    Name       string     // 人读
    Status     KeyStatus
    ExpiresAt  *time.Time
    QuotaLimit int64
    QuotaUsed  int64
    RPMLimit   *int
    TPMLimit   *int
    DailyCap   *int64
    MaxConc    *int
}

type KeyService interface {
    // Create：归属账户必须带 tenant 类；组策略必须允许建 Key；数量未超上限。
    // 返回值中的明文只在此刻出现一次。
    Create(subject Subject, name string) (plaintext string, key *APIKey, err error)

    List(subject Subject) ([]APIKey, error)          // 只回前缀，不含明文
    Rename(subject Subject, keyID, name string) error
    Disable(subject Subject, keyID string) error     // 可恢复
    Revoke(subject Subject, keyID string) error      // 不可恢复
    Delete(subject Subject, keyID string) error      // 若支持删除，见待定

    // SetLimits：只能收窄 —— 每个字段都必须 ≤ 组策略的对应值，否则拒绝。
    SetLimits(subject Subject, keyID string, limits KeyLimits) error

    // SetAllowlist：白名单必须 ⊆ ∪ 所属组的抽象模型；元素必须是抽象模型。
    SetAllowlist(subject Subject, keyID string, abstractModelIDs []string) error

    // Usable 供判定链调用：Key 自身状态 + 归属账户实时状态（不依赖长 TTL 缓存）。
    Usable(rawKey string) (*APIKey, error)
}

type KeyLimits struct {
    DailyCap   *int64
    MonthlyCap *int64 // 见第 4 节待定
    RPM        *int
    TPM        *int
    MaxConc    *int
}
```

## 9. 待定

> **待定**：`quota_limit` / `quota_used` 与 `daily_token_cap` 的关系——前者是总额度、是某种计费单位，还是与 `null` 相比的"总 token 上限"？总纲同时给了两个额度字段但未定义各自语义与优先级。

> **待定**：月 token 上限在 Key 上是否也能收窄（`api_keys` 表没有对应字段，目前月额度只能来自组策略）。

> **待定**：`disabled` 与 `revoked` 是否为两个独立状态（本册按两个撰写）。

> **待定**：`expires_at` 为空表示永不过期，还是表示"使用组默认有效期"。

> **待定**：Key 是否支持物理删除，还是只允许吊销 + 隐藏（涉及历史 `requests` 的归因链与外键）。

> **待定**：吊销一把正在被使用的 Key 时，已在途请求（已解析到上游、尚未返回）如何处理——立即中断、跑完但不再计费，还是跑完且计费。

> **待定**：Key 的 `Retry-After` 取值规则（固定值、按令牌桶补充速率计算，还是按日额度重置时间）。

## 10. 相关分册

- 账户、`tenant` 类的授予与撤销 → [01-accounts.md](01-accounts.md)
- 组策略上限与"Key 白名单能否放宽" → [03-groups.md](03-groups.md)
- 判定链、三层收敛、禁用联动 → [04-authz.md](04-authz.md)
- Key 相关事件的审计留痕 → [06-audit.md](06-audit.md)
- 用量与计量口径 → [../architecture/05-storage.md](../architecture/05-storage.md#37-请求记录与计量)
