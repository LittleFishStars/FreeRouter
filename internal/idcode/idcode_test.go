package idcode

import "testing"

// 对外 id 编解码是**跨队友的接缝契约**（存储层读回 / 管理面输出都走它），
// 因此这里的断言是契约而不是实现细节：形状、可逆性、拒绝面。

func TestContractIDEncodeShape(t *testing.T) {
	cases := []struct {
		prefix string
		id     int64
		want   string
	}{
		{Inbox, 42, "inb_42"},
		{Group, 1, "grp_1"},
		{Account, 1203, "acc_1203"},
		{RealModel, 7, "rm_7"},
		// 非正值 = 尚未落库，返回空串而不是错误（调用方判空更顺手）。
		{Account, 0, ""},
		{Account, -1, ""},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := Encode(c.prefix, c.id); got != c.want {
			t.Errorf("Encode(%q, %d) = %q，期望 %q", c.prefix, c.id, got, c.want)
		}
	}
}

func TestContractIDRoundTrip(t *testing.T) {
	for _, prefix := range []string{Account, Group, APIKey, Credential, Upstream, RealModel, AbstractModel, Inbox, Audit, Session, Verification, Setting} {
		for _, id := range []int64{1, 9, 10, 999, 1 << 40} {
			s := Encode(prefix, id)
			got, err := Decode(prefix, s)
			if err != nil {
				t.Fatalf("%s_%d 解码失败: %v", prefix, id, err)
			}
			if got != id {
				t.Fatalf("%s_%d 往返后 = %d", prefix, id, got)
			}
		}
	}
}

// 前缀必须匹配：拿账户 id 去解组，必须是错误而不是"解出 1"。
// 这条守的是"管理面用错资源的 id 去查错表"。
func TestContractIDPrefixMustMatch(t *testing.T) {
	if _, err := Decode(Group, Encode(Account, 1)); err == nil {
		t.Fatal("acc_1 解成 Group 应当失败")
	}
	if _, err := Decode(Inbox, "inb_7"); err != nil {
		t.Fatalf("正确前缀不该失败: %v", err)
	}
}

// 拒绝面：形状不对一律 ErrInvalid，不返回半个数字。
func TestContractIDRejectsMalformed(t *testing.T) {
	bad := []string{
		"",                            // 空
		"inb_",                        // 只有前缀
		"inb_0",                       // 零
		"inb_-3",                      // 负号
		"inb_+3",                      // 正号
		"inb_007",                     // 前导零：同一行两个合法写法
		"inb_7x",                      // 尾随垃圾
		"inb_ 7",                      // 空格
		"7",                           // 无前缀
		"grp_7",                       // 前缀不符（按 Inbox 解）
		"inb__7",                      // 双下划线
		"inb_99999999999999999999999", // 越界
	}
	for _, s := range bad {
		if _, err := Decode(Inbox, s); err == nil {
			t.Errorf("Decode(%q) 应当失败却成功了", s)
		}
	}
}

func TestContractIDDecodeOptional(t *testing.T) {
	if id, ok, err := DecodeOptional(Inbox, ""); err != nil || ok || id != 0 {
		t.Fatalf("空值应为 (0,false,nil)，得 (%d,%v,%v)", id, ok, err)
	}
	if id, ok, err := DecodeOptional(Inbox, "inb_5"); err != nil || !ok || id != 5 {
		t.Fatalf("正常值应为 (5,true,nil)，得 (%d,%v,%v)", id, ok, err)
	}
	if _, ok, err := DecodeOptional(Inbox, "junk"); err == nil || ok {
		t.Fatal("非法值应返回错误")
	}
}
