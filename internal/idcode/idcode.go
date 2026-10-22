// Package idcode 定义「库内整型主键 ↔ 对外字符串 id」的唯一编解码规则。
//
// 为什么需要这个包（不是过度设计）：docs/api/02-admin.md 里所有对外 id 都是
// 带资源前缀的字符串（示例写作 `grp_01J8...`、`aud_01J8...`、`inb_01J8...`），
// 而库里全部是 `INTEGER PRIMARY KEY`（docs/architecture/05-storage.md）。
// 这条接缝同时卡在存储层（读回什么形状）与管理面（输出什么形状）之间——
// 没有唯一实现，两个队友必然各写一套，届时对外 id 前后不一致、无法反查。
//
// 规则：`<prefix>_<十进制 id>`，例如 `inb_42`、`grp_7`。
//
// 为什么选十进制而不是 base32/ULID：
//   - 可逆且零歧义——运维在日志里看到 `acc_1203` 能直接 `SELECT ... WHERE id=1203`，
//     排查时少一次解码（这是每天发生的事，而"看起来像不透明 id"不是需求）；
//   - 不需要改列语义、不需要新依赖、不需要迁移；
//   - 文档示例里的 `01J8...` 是**占位写法**（带着省略号），表达的是"对外是
//     带前缀的字符串"这个契约形状，不是字面格式要求。已把 02-admin 的示例
//     一并回写成真实格式，避免"实现反向迁就文档措辞"。
//
// 前缀仍然有价值：它让 id 自带资源类型（跨资源引用时不用回查是哪张表），
// 也让"把 acc_1 传进 groups 端点"这类错误在解码层就失败，而不是去查错表。
package idcode

import (
	"errors"
	"strconv"
	"strings"
)

// 各资源的前缀常量（集中一处，避免拼写漂移；新资源必须在这里登记）。
const (
	Account       = "acc"
	Group         = "grp"
	APIKey        = "key"
	Credential    = "cred"
	Upstream      = "up"
	RealModel     = "rm"
	AbstractModel = "am"
	Inbox         = "inb"
	Audit         = "aud"
	Session       = "ses"
	Verification  = "ver"
	Setting       = "set"
)

// ErrInvalid 表示传入的对外 id 形状不合法（前缀不符、非数字、越界、空）。
//
// 为什么是一个哨兵而不是包装出具体原因：调用方（管理面 handler）对
// 任何非法 id 的动作都相同——400 invalid_request + param 指明是哪个字段。
// 区分"没数字"与"越界"只会让 handler 多一堆无意义的分支。
var ErrInvalid = errors.New("idcode: 非法的对外 id")

// Encode 把库内整型 id 编成对外字符串。id <= 0 返回空串——
// 库内自增 id 从 1 起，非正值一律视为"还没有 id"（未落库的对象），
// 调用方判空比判错误更顺手（新建对象的响应里没有 id 是正常态）。
func Encode(prefix string, id int64) string {
	if id <= 0 || prefix == "" {
		return ""
	}
	return prefix + "_" + strconv.FormatInt(id, 10)
}

// Decode 把对外字符串解回库内 id，并校验前缀。
//
// 前缀必须匹配：传 `acc_1` 去解 Group 会失败。这条不是形式主义——
// 管理面端点会拿到用户提供的 id，混用两种资源 id 若不在此拦下，
// 就会拿 acc 的 id 去查 groups 表，命中别人的行或者静默 not_found。
func Decode(prefix, s string) (int64, error) {
	want := prefix + "_"
	if len(s) <= len(want) || !strings.HasPrefix(s, want) {
		return 0, ErrInvalid
	}
	rest := s[len(want):]
	// 拒绝 "+7" / " 7" / "7abc"：只允许纯数字，且不允许前导零
	//（前导零会让同一行有两个合法写法，破坏"id 可比对"的直觉）。
	if rest[0] == '0' {
		return 0, ErrInvalid
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, ErrInvalid
		}
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalid
	}
	return id, nil
}

// DecodeOptional 用于"这个字段可以不填"的场合（如筛选参数、可选归属）。
// 空串返回 (0, false, nil)——不是错误；非法值才返回错误。
func DecodeOptional(prefix, s string) (int64, bool, error) {
	if strings.TrimSpace(s) == "" {
		return 0, false, nil
	}
	id, err := Decode(prefix, s)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
