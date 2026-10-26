package idgen

// idgen 的守卫测试。
//
// 这个包是全站 id 形状的唯一权威，所以它守的是「形状」本身：
//   - New() 的产物必须恒被 Valid 接受（否则每次写入都在造非法 id）；
//   - Valid 必须拒掉 uuid.Parse 会接受的那些异形态（大写、花括号、urn 前缀）——
//     这正是我们不用 Parse 的原因，也是这条测试存在的全部理由；
//   - Parse 的归一化边界（trim / 小写）必须精确，多归一或少归一都会让
//     「同一枚 UUID 在库里开出两行」。

import (
	"strings"
	"testing"
)

// TestNewShape 钉住 New() 的输出形状：36 字符、小写、版本位 4、变体位合法。
func TestNewShape(t *testing.T) {
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		id := New()
		if len(id) != 36 {
			t.Fatalf("id 长度应为 36，得 %d：%q", len(id), id)
		}
		if strings.ToLower(id) != id {
			t.Fatalf("id 应恒小写，得 %q", id)
		}
		if !Valid(id) {
			t.Fatalf("New() 的产物必须被 Valid 接受，得 %q", id)
		}
		if id[14] != '4' {
			t.Fatalf("版本位应为 4（v4），得 %q：%q", id[14], id)
		}
		switch id[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("变体位应为 RFC 4122 的 8/9/a/b，得 %q：%q", id[19], id)
		}
		if seen[id] {
			t.Fatalf("两次 New() 撞号（v4 的随机性失效？）：%q", id)
		}
		seen[id] = true
	}
}

// TestValidRejectsForeignShapes 钉住「uuid.Parse 接受但我们不接受」的那些形状。
//
// 这一条是本次改造最容易悄悄退化的地方：如果哪天有人把 Valid 的实现换成
// `_, err := uuid.Parse(s); return err == nil`，本测试必须变红——因为那会让
// 大写/花括号/urn 形态的 id 进入库，而 TEXT 主键不做大小写归一，同一枚 UUID
// 会以两种写法存成两行。
func TestValidRejectsForeignShapes(t *testing.T) {
	const canonical = "550e8400-e29b-41d4-a716-446655440000"
	if !Valid(canonical) {
		t.Fatalf("标准形状被拒：%q", canonical)
	}
	cases := map[string]string{
		"空串":           "",
		"旧前缀形状":        "acc_1",
		"大写":           "550E8400-E29B-41D4-A716-446655440000",
		"花括号":          "{550e8400-e29b-41d4-a716-446655440000}",
		"urn 前缀":       "urn:uuid:550e8400-e29b-41d4-a716-446655440000",
		"去连字符（32 位）":   "550e8400e29b41d4a716446655440000",
		"缺一段":          "550e8400-e29b-41d4-a716",
		"连字符错位":        "550e840-0e29b-41d4-a716-446655440000",
		"非十六进制字符":      "550e840g-e29b-41d4-a716-446655440000",
		"版本位不是 4（v1）":  "550e8400-e29b-11d4-a716-446655440000",
		"版本位是 7（v7）":   "550e8400-e29b-71d4-a716-446655440000",
		"变体位非法":        "550e8400-e29b-41d4-c716-446655440000",
		"全零（nil UUID）": "00000000-0000-0000-0000-000000000000",
		"尾部多一字符":       canonical + " ",
		"旧合计哨兵 0":      "0",
	}
	for name, s := range cases {
		if Valid(s) {
			t.Errorf("%s：应被拒，实得合法：%q", name, s)
		}
	}
}

// TestParseNormalizes 钉住归一化的作用范围：只 trim + 转小写，别的都不做。
func TestParseNormalizes(t *testing.T) {
	const canonical = "550e8400-e29b-41d4-a716-446655440000"
	for _, in := range []string{
		canonical,
		"  " + canonical + "  ",
		strings.ToUpper(canonical),
		"\t" + strings.ToUpper(canonical) + "\n",
	} {
		got, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q) 失败: %v", in, err)
		}
		if got != canonical {
			t.Fatalf("Parse(%q) 应归一为 %q，实得 %q", in, canonical, got)
		}
	}
	// 非法输入必须返回 ErrInvalid 且给空串（调用方据此 400，不会拿到半截值）。
	for _, bad := range []string{"", "acc_1", "nope", canonical[:35]} {
		got, err := Parse(bad)
		if err == nil {
			t.Errorf("Parse(%q) 应报错", bad)
		}
		if got != "" {
			t.Errorf("Parse(%q) 失败时应给空串，实得 %q", bad, got)
		}
	}
}

// TestMustParsePanicsOnBadConstant 钉住 MustParse 的用途边界：只给源码常量用，
// 非法就崩（带着一个空 id 跑起来比启动即崩难查得多）。
func TestMustParsePanicsOnBadConstant(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustParse 对非法常量应 panic")
		}
	}()
	_ = MustParse("acc_1")
}
