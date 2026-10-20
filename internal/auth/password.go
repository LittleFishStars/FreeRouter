package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id 参数（2026-10-20 定案为**必须写死在代码里**，不得留到实现时调）。
//
// 为什么必须定死而不是"跑得动就行"：取消类级提权之后，
// "管理面被盗 = 全站失守"的抵抗力**全部**落在登录这一环上；
// 一个没有下界的参数值等于没有防线——实现者按"跑得动"来选通常选到 8 MiB。
const (
	// ArgonMemory 64 MiB：离线爆破的成本主要由内存带宽决定。
	ArgonMemory uint32 = 64 * 1024
	// ArgonTime 3 次迭代：与 64 MiB 配合，单次校验约几十毫秒，
	// 交互登录无感，但把离线爆破的每次尝试成本抬到与内存同量级。
	ArgonTime uint32 = 3
	// ArgonThreads 2：单次校验占 2 核。取值过高会让并发登录把 CPU 打满，
	// 反而形成一条廉价的拒绝服务路径。
	ArgonThreads uint8 = 2
	// ArgonSaltLen 16 字节随机 salt，每条记录独立，防彩虹表。
	ArgonSaltLen = 16
	// ArgonKeyLen 32 字节 tag，长度对齐 SHA-256。
	ArgonKeyLen uint32 = 32
)

// argonFormat 是自描述哈希串的版本前缀。
//
// 为什么用自描述串而不是"参数另存一列"：参数升级（例如把 t 从 3 抬到 4）
// 时必须能同时容纳新旧两种哈希，否则升级要么重建全表、要么把老用户踢下线。
// 自描述串让"校验时按记录里的参数算、校验成功后按新参数重算写回"成为可能。
const argonFormat = "argon2id"

// argonParams 描述一次哈希所用的参数。
type argonParams struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	Salt    []byte
	KeyLen  uint32
}

// encodeArgon 输出 PHC 风格字符串：
// $argon2id$v=19$m=65536,t=3,p=2$<b64salt>$<b64hash>
func encodeArgon(p argonParams, hash []byte) string {
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonFormat, argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(p.Salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}

// decodeArgon 解析自描述串。**所有**格式错误都返回同一个错误，
// 因为对攻击者来说"这条记录格式坏了"与"密码错了"不应有区别。
func decodeArgon(s string) (argonParams, []byte, error) {
	var p argonParams
	parts := strings.Split(s, "$")
	// 期望: "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != argonFormat {
		return p, nil, errors.New("argon2id: 哈希串格式非法")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, errors.New("argon2id: 版本字段非法")
	}
	if version != argon2.Version {
		return p, nil, fmt.Errorf("argon2id: 不支持的版本 %d", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return p, nil, errors.New("argon2id: 参数字段非法")
	}
	// 参数下界保护：一条被人为写成 m=1,t=1,p=1 的记录会让校验变得几乎免费，
	// 从而把"慢哈希"的防线悄无声息地关掉。低于设计下界一律拒绝。
	if p.Memory < ArgonMemory || p.Time < ArgonTime {
		return p, nil, errors.New("argon2id: 记录中的参数低于安全下界")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, errors.New("argon2id: salt 解码失败")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, errors.New("argon2id: hash 解码失败")
	}
	p.Salt = salt
	p.KeyLen = uint32(len(hash))
	return p, hash, nil
}

// PasswordHasher 负责密码与恢复码的哈希与校验。
//
// 为什么是可注入的类型而不是一组包级函数：argon2id 的内存开销是
// 64 MiB × 并发数，测试里如果每个用例都真算一次会非常慢；
// 注入一个可替换的实现让 Contract 测试能聚焦语义而不是 CPU。
type PasswordHasher struct {
	// MinLength 来自配置 security.password_min_length（默认 12）。
	MinLength int

	// params 允许测试调低，生产路径永远是常量默认值。
	mem     uint32
	time    uint32
	threads uint8

	// sem 限制同时进行的哈希计算数量。
	//
	// 为什么需要：峰值内存 = 并发数 × 64 MiB。100 个并发登录就是 6.4 GiB，
	// 会把进程直接 OOM——而登录是公网暴露的入口，这个并发量是攻击者可控的。
	// 信号量把"内存放大"变成"登录排队"，宁可慢也不可被打死。
	sem chan struct{}
}

// NewPasswordHasher 用配置里的最小长度构造 hasher，其余参数取定案值。
func NewPasswordHasher(minLength int) *PasswordHasher {
	if minLength <= 0 {
		minLength = 12 // 兜底：配置缺失时绝不允许退化成"无限制"
	}
	// 并发上限取 max(2, NumCPU/2)：至少允许两个并发登录（避免单请求卡死），
	// 上限不超过核数的一半，给转发路径留出 CPU。
	limit := runtime.NumCPU() / 2
	if limit < 2 {
		limit = 2
	}
	return &PasswordHasher{
		MinLength: minLength,
		mem:       ArgonMemory,
		time:      ArgonTime,
		threads:   ArgonThreads,
		sem:       make(chan struct{}, limit),
	}
}

// ValidatePassword 按策略校验明文密码。
//
// 除长度外**不做**字符类别 / 字典 / 泄露库检查：02-authn.md 明确这三项
// 仍是待定，而"长度是唯一不需要额外基础设施就能提高离线破解成本的措施"。
// 自行加码会让"文档说只要求长度"与"实现却拒绝你的密码"产生无法解释的分歧。
func (h *PasswordHasher) ValidatePassword(plain string) error {
	if h == nil {
		return Errorf(CodeInvalidRequest, nil, "密码策略未初始化")
	}
	// 按 rune 计长度：中文口令的"12 个字符"若按字节算会变成 4 个字，
	// 用户看到的规则与实际判定不一致。
	if n := len([]rune(plain)); n < h.MinLength {
		return Errorf(CodeInvalidRequest, nil, "密码长度不足：需要至少 %d 个字符", h.MinLength)
	}
	return nil
}

// Hash 计算 argon2id 哈希，返回自描述串（可直接落 password_hash / recovery_code_hash 列）。
func (h *PasswordHasher) Hash(plain string) (string, error) {
	salt := make([]byte, ArgonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成 salt: %w", err)
	}
	p := argonParams{Memory: h.mem, Time: h.time, Threads: h.threads, Salt: salt, KeyLen: ArgonKeyLen}

	h.acquire()
	defer h.release()

	hash := argon2.IDKey([]byte(plain), p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return encodeArgon(p, hash), nil
}

// Verify 校验明文与自描述串是否匹配，并在**参数低于当前档**时返回 needRehash。
//
// 为什么返回 needRehash 而不是直接写回：本层没有写库权限（见 storeiface.go），
// 重算与写回由调用方在同一事务里完成；把"是否该重算"这个判断留在本层，
// 是为了让调用方不必自己解析 argon 参数。
func (h *PasswordHasher) Verify(encoded, plain string) (ok bool, needRehash bool, err error) {
	p, want, err := decodeArgon(encoded)
	if err != nil {
		// 格式坏掉的记录当作"校验失败"，而不是返回一个内部错误：
		// 后者会让攻击者知道"这个用户名存在，只是它的哈希坏了"。
		return false, false, nil
	}

	h.acquire()
	got := argon2.IDKey([]byte(plain), p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	h.release()

	// 定长常数时间比较，不短路：避免用比较耗时区分"前缀对/错"。
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	// 校验成功后若参数低于当前档，用新参数重算并写回——
	// 否则改参数只对未来的新密码生效，存量密码永远停在旧成本上。
	stale := p.Memory < h.mem || p.Time < h.time || p.Threads < h.threads || p.KeyLen < ArgonKeyLen
	return true, stale, nil
}

// dummyEncoded 是给"用户名不存在"分支用的等价工作量哈希。
//
// 防枚举（02-authn.md §9）：只统一文案不够，攻击者用时间差照样能刷出用户库。
// 因此不存在分支也必须跑一次**等价成本**的 argon2id。
var (
	dummyOnce    sync.Once
	dummyHashStr string
)

// DummyVerify 对不存在的账户执行一次等价哈希，然后必然返回 false。
//
// 每次调用都真算一次 argon2id（用固定的 self-describing 串），
// 而不是 sleep：sleep 的时长与实际校验时长不等价，而且会随参数调整漂移。
func (h *PasswordHasher) DummyVerify(plain string) bool {
	dummyOnce.Do(func() {
		// 用固定 salt 构造一条与本档参数相同的串，避免每次都要读随机源。
		salt := make([]byte, ArgonSaltLen)
		p := argonParams{Memory: h.mem, Time: h.time, Threads: h.threads, Salt: salt, KeyLen: ArgonKeyLen}
		h.acquire()
		hash := argon2.IDKey([]byte("freerouter-dummy"), p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
		h.release()
		dummyHashStr = encodeArgon(p, hash)
	})
	return h.VerifyOK(dummyHashStr, plain)
}

// VerifyOK 是 Verify 的布尔快捷形式（哑校验与 ConstantTimeEqual 分支只需要"对不对"）。
func (h *PasswordHasher) VerifyOK(encoded, plain string) bool {
	ok, _, _ := h.Verify(encoded, plain)
	return ok
}

// acquire / release 是并发闸门；nil hasher 时直接放行（单测里不构建 hasher 的场景）。
func (h *PasswordHasher) acquire() {
	if h != nil && h.sem != nil {
		h.sem <- struct{}{}
	}
}

func (h *PasswordHasher) release() {
	if h != nil && h.sem != nil {
		<-h.sem
	}
}

// HashToken 计算机器凭据（会话令牌 / API Key）的 SHA-256。
//
// 为什么 Key 用 SHA-256 而不是 argon2id：Key 是 32 字节随机值（≈256 位熵），
// 离线爆破不可行；慢哈希的唯一效果是让每一个入站请求都付出一次昂贵推导，
// 而鉴权发生在每一个请求上（04-authz.md §4 第 2 步）。
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// ConstantTimeEqual 是机器凭据比较的唯一样式。
//
// 为什么单独导出：让"比较 Key / 比较 token"在全项目里只有一种写法，
// 避免某处直接写 == 造成时序侧信道。
func ConstantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
