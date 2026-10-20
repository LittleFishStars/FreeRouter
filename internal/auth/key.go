package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// API Key 的格式与展示常量。
//
// 为什么要有可识别前缀 `fr-`：Key 会被写进配置文件、环境变量、CI secret、
// 客户端库。可识别前缀让两个问题立刻可判：这是不是我们的 Key（便于泄漏扫描
// 与日志脱敏规则命中），以及这是哪一类凭据（与上游凭据、会话令牌区分）。
const (
	// KeyPrefix 是 Key 明文的前缀。
	KeyPrefix = "fr-"

	// keyRandomBytes 32 字节随机值 ≈ 256 位熵。
	// 这正是"可以用 SHA-256 而不是 argon2id"的理由：密码是低熵的人选项，
	// Key 不是，离线爆破对 32 字节随机值不可行。
	keyRandomBytes = 32

	// keyDisplayHead / keyDisplayTail 是列表展示保留的首尾字符数。
	// 首尾各留几位是唯一同时满足"用户能认出是哪把"与"截图不足以拼出凭据"的做法。
	keyDisplayHead = 4
	keyDisplayTail = 4
)

// base62Alphabet 是 base62 的字符表。
// 顺序自定为 0-9A-Za-z（大写在前）：与 Go 标准库的 big.Int.Text(62) 不同，
// 但本项目的 Key 只在自身内部生成与校验，取一个可读性更好的顺序即可。
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// keyWidth 是 32 字节按 base62 编码后的固定宽度（ceil(256 / log2(62)) = 43）。
const keyWidth = 43

// base62EncodeFixed 把随机字节编码成定宽 base62 串。
//
// 为什么要定宽（左补 '0'）：不定宽会让"长度"成为一个可观察的旁道
// （首字节小的 Key 更短），也让日志对齐与正则校验变复杂。
func base62EncodeFixed(raw []byte) string {
	n := new(big.Int).SetBytes(raw)
	base := big.NewInt(62)
	mod := new(big.Int)
	buf := make([]byte, 0, keyWidth)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		buf = append(buf, base62Alphabet[mod.Int64()])
	}
	for len(buf) < keyWidth {
		buf = append(buf, base62Alphabet[0])
	}
	// buf 是低位在前，反转得到正常顺序。
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// DisplayPrefix 把 Key 明文压成展示用的 `fr-xxxx…xxxx`。
//
// 为什么存这一列而不是每次从明文算：明文不落库，列表页只有它可用；
// 而"存一份前缀"不构成风险——首尾片段不足以拼出可用凭据。
func DisplayPrefix(plaintext string) string {
	body := strings.TrimPrefix(plaintext, KeyPrefix)
	if len(body) <= keyDisplayHead+keyDisplayTail {
		return KeyPrefix + body
	}
	return fmt.Sprintf("%s%s…%s", KeyPrefix, body[:keyDisplayHead], body[len(body)-keyDisplayTail:])
}

// LooksLikeKey 判断一个字符串是否是本系统的 Key 格式。
//
// 用途：判定链第 2 步"识别 Key 前缀"——不是本系统格式的 Authorization
// 根本不必查库，可以在最便宜的阶段拒掉。
func LooksLikeKey(raw string) bool {
	if !strings.HasPrefix(raw, KeyPrefix) {
		return false
	}
	body := raw[len(KeyPrefix):]
	if len(body) != keyWidth {
		return false
	}
	for i := 0; i < len(body); i++ {
		if !strings.ContainsRune(base62Alphabet, rune(body[i])) {
			return false
		}
	}
	return true
}

// KeyPolicy 是 Key 相关的策略参数。
//
// 全部来自配置与组策略，**不硬编码**：这些取值都属于"会变"的语义
// （见 03-groups.md 对 Key 数量上限归属的两次定案）。
type KeyPolicy struct {
	// DefaultMaxKeysPerAccount 来自 limits.default_keys_per_account。
	// 语义是"所属组都没有设置 max_keys 时的兜底值"。
	DefaultMaxKeysPerAccount int
}

// KeyManager 管理 API Key 的生命周期。
type KeyManager struct {
	policy KeyPolicy
	stores Stores

	// now / rand 注入：测试要能确定性地造出过期 Key 与固定明文。
	now  func() time.Time
	rand io.Reader
}

// NewKeyManager 构造 Key 管理器。
func NewKeyManager(policy KeyPolicy, stores Stores) *KeyManager {
	return &KeyManager{
		policy: policy,
		stores: stores,
		now:    time.Now,
		rand:   rand.Reader,
	}
}

// WithClock 注入时钟（测试用）。
func (m *KeyManager) WithClock(now func() time.Time) *KeyManager {
	m.now = now
	return m
}

// CreateResult 是创建 Key 的结果。
//
// 为什么明文单独一个字段并在文档里反复强调"只返回一次"：
// 它的生命周期只有这一次调用栈；调用方一旦把它写进日志或持久化，
// 就等于把"可吊销的凭据"变成"所有读过日志的人都持有的凭据"。
type CreateResult struct {
	Plaintext string // **仅此一次**
	Key       *APIKey
}

// Create 为 subject 所属账户创建一把 Key。
//
// 校验顺序（与 04-authz.md 的判定链同精神：便宜的、与外状态无关的检查在前）：
//  1. 归属账户必须带 tenant 类——Key 是"调用模型"的凭据，只有 tenant 提供调用能力；
//     给没有调用能力的主体发调用凭证是"发得出、用不了"的纯状态污染。
//  2. 组策略必须允许建 Key（放开类布尔，deny 优先）。
//  3. 数量未超上限（放开类数值，多组取最大值）。
func (m *KeyManager) Create(ctx context.Context, s Subject, name string) (*CreateResult, error) {
	if m.stores.Keys == nil || m.stores.Accounts == nil {
		return nil, Errorf(CodeInternal, nil, "KeyManager 缺少存储依赖")
	}
	if s.AccountID == "" {
		return nil, Errorf(CodeInvalidRequest, nil, "缺少账户 ID")
	}

	acct, err := m.stores.Accounts.AccountByID(ctx, s.AccountID)
	if err != nil {
		return nil, err
	}
	if !acct.HasClass(ClassTenant) {
		return nil, Errorf(CodeForbidden, nil, "归属账户没有 tenant 类，不能持有 API Key")
	}
	if !acct.Status.CanCallModels() {
		return nil, Errorf(CodeForbidden, nil, "账户状态 %s 不允许创建 API Key", acct.Status)
	}

	// 组策略三段校验（05-keys.md §1）：tenant 类✓（上）→ 允许建 Key → 数量未超上限。
	// 两个放开类键来自**同一次** GroupsForAccount 读取，不各读一遍。
	canCreate, limit, err := m.keyCreationPolicy(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	if !canCreate {
		return nil, Errorf(CodeForbidden, nil, "所属组策略禁止创建 API Key")
	}

	existing, err := m.stores.Keys.ListKeysByAccount(ctx, acct.ID)
	if err != nil {
		return nil, err
	}
	if used := countAgainstKeyQuota(existing); limit > 0 && used >= limit {
		return nil, Errorf(CodeConflict, nil, "已达到 Key 数量上限（%d）", limit)
	}

	plaintext, err := m.generate()
	if err != nil {
		return nil, err
	}
	now := m.now()
	key := &APIKey{
		ID:        newID("key"),
		AccountID: acct.ID,
		KeyHash:   HashToken(plaintext),
		Prefix:    DisplayPrefix(plaintext),
		Name:      name,
		Status:    KeyActive,
		CreatedAt: now,
		// 白名单建为空：默认拒绝。若白名单默认等于组授权，
		// 那么"新建 Key"这个动作本身就等于把全部模型授权一次性发出去。
	}
	if err := m.stores.Keys.CreateKey(ctx, key); err != nil {
		return nil, err
	}
	return &CreateResult{Plaintext: plaintext, Key: key}, nil
}

// maxKeysFor 计算该账户实际生效的 Key 数量上限。
//
// 放开类合并规则（03-groups.md §3 定案）：Key 数量上限归放开类，
// 多组取**最大值**，而不是取最严。为什么它不能归限制类：
// 该约束管的是"创建能力"，与额度、速率这类共享资源不是一回事；
// 取 min 会让"被加进一个额度更小的组"顺带把 Key 数量压掉，只制造意外不产生护栏。
// keyCreationPolicy 一次读组，返回（能否建 Key，Key 数量上限）。
//
// 合并规则（03-groups.md §3/§4）：
//   - CanCreateKey 是放开类布尔：deny 优先——任一所属组显式 false 即禁；
//     未设（nil）不参与，全未设 = 允许（默认组兜底“至少一组有策略”的不变式）。
//   - MaxKeys 是放开类数值：取**最大值**（定案），非 min；无组设置时
//     落配置默认 DefaultMaxKeysPerAccount。
func (m *KeyManager) keyCreationPolicy(ctx context.Context, accountID string) (bool, int, error) {
	if m.stores.Policies == nil {
		return true, m.policy.DefaultMaxKeysPerAccount, nil
	}
	groups, err := m.stores.Policies.GroupsForAccount(ctx, accountID)
	if err != nil {
		return false, 0, err
	}
	canCreate := true
	best := 0
	seen := false
	for _, g := range groups {
		if g.CanCreateKey != nil && !*g.CanCreateKey {
			canCreate = false
		}
		if g.MaxKeys != nil {
			seen = true
			if *g.MaxKeys > best {
				best = *g.MaxKeys
			}
		}
	}
	if !seen {
		return canCreate, m.policy.DefaultMaxKeysPerAccount, nil
	}
	return canCreate, best, nil
}

// countAgainstKeyQuota 统计占配额的数量。
//
// 为什么 revoked 不计入：revoked 是单向不可恢复的状态（05-keys.md §2）。
// 若它继续占用名额，用户达到上限后主动吊销一把泄露的 Key 就会永久少一个名额，
// 而恢复无路——这等于惩罚"发现泄露立刻吊销"这个正确行为。
// disabled 计入：它是可恢复的临时状态，用户随时能把它变回可用。
func countAgainstKeyQuota(keys []APIKey) int {
	n := 0
	for _, k := range keys {
		if k.Status != KeyRevoked {
			n++
		}
	}
	return n
}

// generate 生成一把新的 Key 明文：`fr-` + 32 字节 base62。
func (m *KeyManager) generate() (string, error) {
	raw := make([]byte, keyRandomBytes)
	if _, err := io.ReadFull(m.rand, raw); err != nil {
		return "", fmt.Errorf("生成 API Key 明文: %w", err)
	}
	return KeyPrefix + base62EncodeFixed(raw), nil
}

// Rotate 轮换一把 Key：在存储层的**单个事务**里吊销旧 Key 并写入新 Key。
//
// 为什么要原子：分两步（先 Revoke 再 Create）之间进程崩溃会留下
// "旧 Key 已废、新 Key 未生效"的状态，用户被锁在门外且无凭据可用——
// 而轮换的动机通常正是"旧 Key 可能泄露了，我急着换"。
func (m *KeyManager) Rotate(ctx context.Context, s Subject, keyID, name string) (*CreateResult, error) {
	old, err := m.mustOwnKey(ctx, s, keyID)
	if err != nil {
		return nil, err
	}
	if old.Status == KeyRevoked {
		// revoked 是单向的（05-keys.md §2）：可以被恢复的吊销不是吊销。
		return nil, Errorf(CodeConflict, nil, "已吊销的 Key 不能再轮换，请新建")
	}
	plaintext, err := m.generate()
	if err != nil {
		return nil, err
	}
	now := m.now()
	fresh := &APIKey{
		ID:        newID("key"),
		AccountID: old.AccountID,
		KeyHash:   HashToken(plaintext),
		Prefix:    DisplayPrefix(plaintext),
		Name:      name,
		Status:    KeyActive,
		CreatedAt: now,
	}
	// 轮换不设过期时间：旧 Key 的 expires_at 是"用户给这把 Key 定的寿命"，
	// 直接继承会让新 Key 在同一个即将到来的时刻一起失效。
	if err := m.stores.Keys.RotateKey(ctx, old.ID, fresh); err != nil {
		return nil, err
	}
	return &CreateResult{Plaintext: plaintext, Key: fresh}, nil
}

// List 返回该账户全部 Key（只含前缀与元数据，永不回显明文）。
func (m *KeyManager) List(ctx context.Context, s Subject) ([]APIKey, error) {
	if m.stores.Keys == nil {
		return nil, Errorf(CodeInternal, nil, "KeyManager 缺少存储依赖")
	}
	keys, err := m.stores.Keys.ListKeysByAccount(ctx, s.AccountID)
	if err != nil {
		return nil, err
	}
	// 防御性清理：即使存储层实现有误，也绝不把哈希对外返回。
	for i := range keys {
		keys[i].KeyHash = nil
	}
	return keys, nil
}

// Rename 改名（只影响可读性，不影响鉴权）。
func (m *KeyManager) Rename(ctx context.Context, s Subject, keyID, name string) error {
	if _, err := m.mustOwnKey(ctx, s, keyID); err != nil {
		return err
	}
	return m.stores.Keys.RenameKey(ctx, keyID, name)
}

// Disable 停用一把 Key。可恢复：它表达的是"先停一下"（换客户端、排查异常用量）。
func (m *KeyManager) Disable(ctx context.Context, s Subject, keyID string) error {
	k, err := m.mustOwnKey(ctx, s, keyID)
	if err != nil {
		return err
	}
	if k.Status == KeyRevoked {
		return Errorf(CodeConflict, nil, "已吊销的 Key 不可停用")
	}
	return m.stores.Keys.SetKeyStatus(ctx, keyID, KeyDisabled)
}

// Enable 恢复一把被停用的 Key。
func (m *KeyManager) Enable(ctx context.Context, s Subject, keyID string) error {
	k, err := m.mustOwnKey(ctx, s, keyID)
	if err != nil {
		return err
	}
	// 单向性由本层守住，不依赖调用方自觉，也不依赖存储层的 CHECK：
	// 表结构里 status 的 CHECK 只约束取值，约束不了"从 revoked 转回去"这条转移。
	if k.Status == KeyRevoked {
		return Errorf(CodeConflict, nil, "已吊销的 Key 不可恢复（吊销是单向路径）")
	}
	return m.stores.Keys.SetKeyStatus(ctx, keyID, KeyActive)
}

// Revoke 即时吊销一把 Key。不可恢复。
//
// 为什么"立即"是硬要求：吊销的语义是"从此刻起不可用"。
// 把"稍后生效"写进设计，等于把泄露处置的窗口期写进设计。
func (m *KeyManager) Revoke(ctx context.Context, s Subject, keyID string) error {
	k, err := m.mustOwnKey(ctx, s, keyID)
	if err != nil {
		return err
	}
	if k.Status == KeyRevoked {
		return nil // 幂等：重复吊销不是错误
	}
	return m.stores.Keys.SetKeyStatus(ctx, keyID, KeyRevoked)
}

// Usable 校验一把 Key 明文，返回其记录（不含归属账户状态）。
//
// 这是转发热路径上的第一跳，必须一次索引探测完成：
// 先按格式排除（不查库），再按 key_hash 唯一索引取回，再做一次常数时间比较。
//
// 为什么取回后还要再比较一次：存储层若被改成"前缀匹配 + 全表过滤"，
// 一次 == 就可能变成时序侧信道。本层不假设存储层的实现质量。
func (m *KeyManager) Usable(ctx context.Context, raw string) (*APIKey, error) {
	if m.stores.Keys == nil {
		return nil, Errorf(CodeInternal, nil, "KeyManager 缺少存储依赖")
	}
	if !LooksLikeKey(raw) {
		return nil, ErrInvalidAPIKey
	}
	hash := HashToken(raw)
	key, err := m.stores.Keys.KeyByHash(ctx, hash)
	if err != nil {
		// 不存在与"存储出错"对外都是同一句话：Key 是机器凭据，
		// 区分这两种只会给攻击者一个探测面；底层原因由调用方记日志。
		return nil, ErrInvalidAPIKey
	}
	if !ConstantTimeEqual(key.KeyHash, hash) {
		return nil, ErrInvalidAPIKey
	}
	if !key.Usable(m.now()) {
		// 已吊销 / 已停用 / 已过期统一返回同一个错误码（401）：
		// 客户端动作相同——换一把有效 Key。
		return nil, ErrInvalidAPIKey
	}
	return key, nil
}

// mustOwnKey 取 Key 并校验归属（不匹配返回 404 而不是 403：
// 04-authz.md 要求"读他人 Key"一律 404 或空集，不泄露资源存在性）。
func (m *KeyManager) mustOwnKey(ctx context.Context, s Subject, keyID string) (*APIKey, error) {
	if m.stores.Keys == nil {
		return nil, Errorf(CodeInternal, nil, "KeyManager 缺少存储依赖")
	}
	key, err := m.stores.Keys.KeyByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if key.AccountID != s.AccountID {
		return nil, ErrNotFound
	}
	return key, nil
}

// newID 生成带类型前缀的随机 ID（key-xxxx / sess-xxxx）。
//
// 为什么 ID 不用数据库自增：本层不做持久化，而"先插库再回读 ID"会让
// Create 变成两次往返。ID 只需要在库内唯一，不承担可读性。
func newID(kind string) string {
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// 随机源不可用时退化为时间戳：ID 唯一性的最终保证在存储层的唯一索引。
		return fmt.Sprintf("%s-%d", kind, time.Now().UnixNano())
	}
	return kind + "-" + base62EncodeFixed(b[:])
}

// SetAllowlist 设置 Key 的模型白名单（只能收窄，不能放宽）。
//
// 规则链（05-keys.md §5 / 04-authz.md §2）：
//  1. 白名单元素必须是**抽象模型**（真实模型名对租户不可见，出现在白名单里
//     本身就是泄露；但本层拿不到真实模型目录——models 层的职责——
//     所以只能校验「⊆ 组授权」：不在授权集里的名字无论是不是真实模型都会被拒，
//     两个错误在判定上同形）；
//  2. ⊆ (∪ 所属组的抽象模型)：放宽权是组策略（KeyAllowlistWiden）管的事，
//     而它管的是「能否改自己白名单」的动作，不是「白名单可以超出组授权」；
//     后者在数学上也无效果（最终可用 = 交集）；
//  3. 写的是整表替换：收窄语义下「去掉一项」和「换成子集」是同一个动作，
//     增量 add/remove 两个方法只会多两个错用面。
func (m *KeyManager) SetAllowlist(ctx context.Context, s Subject, keyID string, abstractModelIDs []string) error {
	if m.stores.Keys == nil || m.stores.Policies == nil {
		return Errorf(CodeInternal, nil, "KeyManager 缺少存储/策略依赖")
	}
	if _, err := m.mustOwnKey(ctx, s, keyID); err != nil {
		return err
	}
	groups, err := m.stores.Policies.GroupsForAccount(ctx, s.AccountID)
	if err != nil {
		return err
	}
	authorized := make(map[string]bool)
	for _, g := range groups {
		for _, mdl := range g.Models {
			authorized[mdl] = true
		}
	}
	seen := make(map[string]bool, len(abstractModelIDs))
	for _, mdl := range abstractModelIDs {
		if mdl == "" || seen[mdl] {
			return Errorf(CodeInvalidRequest, nil, "白名单含空项或重复项")
		}
		seen[mdl] = true
		if !authorized[mdl] {
			// 与「模型不存在」同形的 403：白名单是本人动作，超集拒绝不需要
			// 隐藏存在性（他本来就能通过 EffectiveModels 看到授权集）。
			return Errorf(CodeForbidden, nil, "白名单只能收窄：模型 %q 不在组授权内", mdl)
		}
	}
	return m.stores.Keys.SetKeyAllowlist(ctx, keyID, abstractModelIDs)
}
