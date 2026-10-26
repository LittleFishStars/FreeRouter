package store

import (
	"context"
	"testing"
)

// TestContractSetKeyExpiryClearSemantics 守 SetKeyExpiry 的「清空」语义。
//
// 为什么它值得一条契约测试：这一层的 nil 只有一种含义——写 NULL（永不过期）。
// 「字段缺省 = 不改」的判定发生在 httpapi 的解码层，永远走不到这里。两层各自的
// 语义一旦被写混（例如把 nil 当成「忽略」），表现是「过期时间改得动、清不掉」，
// 而两边各自的单元测试都还是绿的。
func TestContractSetKeyExpiryClearSemantics(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acctID := mustAccount(t, s)

	first := int64(1_800_000_000)
	id, err := s.Repos().Keys.CreateKey(ctx,
		APIKey{AccountID: acctID, Prefix: "fr-abcd…wxyz", ExpiresAt: &first},
		make([]byte, 32))
	if err != nil {
		t.Fatalf("建 Key 失败: %v", err)
	}

	read := func() *int64 {
		t.Helper()
		k, err := s.Repos().Keys.GetKey(ctx, id)
		if err != nil {
			t.Fatalf("读 Key 失败: %v", err)
		}
		return k.ExpiresAt
	}

	// 1) 创建时写入的时刻必须读得回来（否则后面两步的对照无意义）。
	if got := read(); got == nil || *got != first {
		t.Fatalf("创建后的 expires_at = %v，期望 %d", got, first)
	}

	// 2) 改成另一个时刻：写入不是「只能清空」。
	second := first + 86400
	if err := s.Repos().Keys.SetKeyExpiry(ctx, id, &second); err != nil {
		t.Fatalf("设置过期时间失败: %v", err)
	}
	if got := read(); got == nil || *got != second {
		t.Fatalf("设置后的 expires_at = %v，期望 %d", got, second)
	}

	// 3) nil = 清空（写 NULL）。这是审计 P1 的核心动作：必须真的落成 NULL。
	if err := s.Repos().Keys.SetKeyExpiry(ctx, id, nil); err != nil {
		t.Fatalf("清空过期时间失败: %v", err)
	}
	if got := read(); got != nil {
		t.Fatalf("清空后的 expires_at = %v，期望 nil（NULL = 永不过期）", *got)
	}

	// 4) 对已经是 NULL 的行再清一次：不报错（requireAffected 不能把
	//    「值本来就是这个」误判成「行不存在」）。
	if err := s.Repos().Keys.SetKeyExpiry(ctx, id, nil); err != nil {
		t.Fatalf("重复清空应当幂等，实际报错: %v", err)
	}

	// 5) 清空之后重新设定：状态可双向流动（清空不是单向动作）。
	third := second + 86400
	if err := s.Repos().Keys.SetKeyExpiry(ctx, id, &third); err != nil {
		t.Fatalf("清空后再设置失败: %v", err)
	}
	if got := read(); got == nil || *got != third {
		t.Fatalf("重新设置后的 expires_at = %v，期望 %d", got, third)
	}

	// 6) 对不存在的 Key：必须报错（映射到 HTTP 404），不能静默成功。
	if err := s.Repos().Keys.SetKeyExpiry(ctx, tid3, &third); err == nil {
		t.Fatal("对不存在的 Key 设过期时间应当报错")
	}
}
