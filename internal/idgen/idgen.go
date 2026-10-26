// Package idgen 生成与校验全站对外的 id（UUID v4 文本形态）。
//
// # 为什么是 UUID 而不是原来的「前缀 + 自增十进制」
//
// 2026-10-26 用户决定：所有 id 改用 UUID，**库内主键一起换**——于是对外 id 与
// 库内 id 是同一串字符，原来 `internal/idcode` 那条「整型 rowid ↔ 字符串 id」的
// 接缝整体消失（不是被改写，是被删掉）。消失的接缝包括桥层 35 处 encodeID 与
// 54 处 decodeID，以及两条聚合 SQL 里的 `CAST(secret_ref AS INTEGER)`。
//
// 前缀（acc_ / grp_ / rm_ …）一并废弃。它原本的两个价值在新形状下都不成立：
//
//   - **防跨资源误用**（把 acc_1 传进 groups 端点）：由「在错误的表里按主键查不到
//     → 404」承担。这本来就是最终判定，前缀只是把它提前一次，而提前一次的代价是
//     全站 id 多一个需要维护的枚举表（前缀常量表漏登记一个新资源，就是运行期才炸
//     的解码失败）。
//   - **日志可读**（看到 acc_1203 能直接 SELECT）：改由「日志同时打字段名」承担
//     （account_id=<uuid>），可枚举性本来就不是需求。
//
// 换来的是三件实际的事：id 不可枚举（不泄露池子规模，05-storage.md 的越权枚举
// 顾虑从根上消失）、跨环境搬运不会撞号、以及新资源不需要登记前缀。
//
// # 为什么 v4 而不是 v7
//
// 用户选定 v4（纯随机）。代价写清楚：主键索引从顺序写入变成随机写入（B-tree
// 页分裂），在本地 SQLite + 万级行的规模下测不出差异；v7（时间有序）能省掉这份
// 代价并让 id 自带创建时间，但那是「以后可以再议」的优化，不是这次的选项。
//
// # 边界
//
// 本包只做**形状**：生成、校验、归一化。它不查库、不认识任何资源类型、
// 不关心 id 指向哪张表——那些是调用方（store 的插入路径、httpapi 的参数校验）
// 的职责。把它做成一个只有三个函数的薄包，正是为了让「id 是什么形状」这件事
// 全站只有一处定义。
package idgen

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalid 表示传入的 id 不是合法的 UUID 文本。
//
// 为什么是一个哨兵而不区分「太短 / 字符非法 / 版本不对」：调用方（管理面 handler）
// 对任何非法 id 的动作都相同——400 invalid_request + param 指明是哪个字段。
// 区分具体原因只会让 handler 多一堆没有下游差别的分支。
var ErrInvalid = errors.New("idgen: 非法的 id（期望 UUID 文本）")

// New 生成一个新 id：小写、带连字符、36 字符的 UUID v4。
//
// 为什么返回 string 而不是 uuid.UUID：库里的列是 TEXT，Go 层的 id 也统一用
// string（见 0009 迁移与 uuid-migration-contract §1.3）——让 uuid.UUID 出现在
// 签名里，等于每个调用点多一次 .String()，而没有任何一处需要用到 UUID 的结构。
//
// 为什么不怕撞号：v4 是 122 位随机，生日界到需要担心碰撞的数量级还差十几个
// 数量级；而且真撞了会被主键唯一约束拒绝（写入报错），不会静默产生两条同 id 的行。
func New() string {
	return uuid.NewString()
}

// Valid 只判形状（不查库）：长度、字符集、连字符位置、版本位与变体位。
//
// 为什么不用 uuid.Parse 直接判：Parse 接受 `{...}` 包裹、urn:uuid: 前缀等
// 多种输入形态，也接受大写。那些都不是本系统对外承诺的形状——把 `urn:uuid:...`
// 当成合法 id 传进 URL，会在库里以另一种写法存下同一枚 UUID（TEXT 主键不做
// 大小写归一），于是「同一个对象两个 id」。所以这里显式要求：36 字符、
// 8-4-4-4-12 分段、全小写十六进制、版本位是 4、变体位是 8/9/a/b。
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	// 分段位置：第 8/13/18/23 位必须是连字符（0 起算）。
	for _, i := range [4]int{8, 13, 18, 23} {
		if s[i] != '-' {
			return false
		}
	}
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isLowerHex(s[i]) {
			return false
		}
	}
	// 版本位（第 14 段首位）= 4；变体位（第 19 位）= 8/9/a/b。
	// 为什么判这两个：不判的话 `...-1...` 这种 v1 时间戳 UUID 也能进来，
	// 而 v1 带 MAC 与时间信息，与「不可枚举」这个改造目标直接冲突。
	if s[14] != '4' {
		return false
	}
	switch s[19] {
	case '8', '9', 'a', 'b':
	default:
		return false
	}
	return true
}

// Parse 校验并归一化（trim + 转小写）；非法返回 ErrInvalid。
//
// 归一化为什么放在这一层而不是让调用方各自 trim：URL 参数与人粘贴的 id 带空格
// 是常态，而「带空格的同一个 id 被当成两个不同 id」是数据问题不是显示问题。
// 大小写：Valid 只认小写，所以归一化的全部作用是把人手工输入的大写 UUID 收编成
// 库里唯一的形状——不做这一步，`550E8400-…` 会在库里开出第二行。
func Parse(s string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if !Valid(t) {
		return "", ErrInvalid
	}
	return t, nil
}

// MustParse 是 Parse 的 panic 版，只给装配期的常量用（测试夹具、内置默认值）。
//
// 为什么允许 panic 而不是返回 error：调用点写的是源码里的字面量，非法就是
// 编程错误，让它崩在启动/测试初始化阶段比让它带着一个空 id 跑起来好排查得多。
// 运行期（HTTP 请求、库里的值）一律走 Parse，不许用这个函数。
func MustParse(s string) string {
	t, err := Parse(s)
	if err != nil {
		panic("idgen: 非法的 id 常量：" + s)
	}
	return t
}

func isLowerHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
}
