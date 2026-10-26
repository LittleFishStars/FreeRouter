package store

import (
	"context"
	"errors"
	"testing"
)

// 本文件守的是上游写域的**对外签名契约**（task-3 第 3 点，admin-api 桥接断点）：
//  1. UpsertUpstream 的两返回值签名不变（既有自举/路由装配调用零改动）；
//  2. UpsertUpstreamWithCreated 额外给出 created（201/200 与审计决策用）；
//  3. GetUpstreamByID 提供跨包可用的读取入口，且 ErrNotFound 语义与包内一致。
//
// 不断言 SQL 文本或内部字段顺序——那些是实现细节；改实现不该让这些测试失败。

func TestContractUpsertUpstreamWithCreatedAndGetByID(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models

	// ① 首次 upsert：created=true，返回可用 id。
	id, created, err := models.UpsertUpstreamWithCreated(ctx, "relay", "p1", "sidecar://p1", nil, true)
	if err != nil {
		t.Fatalf("首次 UpsertUpstreamWithCreated 失败: %v", err)
	}
	if !created {
		t.Fatal("首次写入必须报告 created=true")
	}
	if id == "" {
		t.Fatal("首次写入必须返回非零 id")
	}

	// ② 旧签名仍然可用，并与新方法指向同一行（kind+name 唯一约束）。
	idOld, err := models.UpsertUpstream(ctx, "relay", "p1", "sidecar://p1-v2", nil, false)
	if err != nil {
		t.Fatalf("旧签名 UpsertUpstream 失败: %v", err)
	}
	if idOld != id {
		t.Fatalf("同 kind+name 的 upsert 必须命中同一行：id=%s，旧签名返回 %s", id, idOld)
	}

	// ③ 二次写入：created=false，且更新了 base_url/enabled。
	id2, created2, err := models.UpsertUpstreamWithCreated(ctx, "relay", "p1", "sidecar://p1-v3", nil, true)
	if err != nil {
		t.Fatalf("二次 UpsertUpstreamWithCreated 失败: %v", err)
	}
	if created2 {
		t.Fatal("冲突更新必须报告 created=false")
	}
	if id2 != id {
		t.Fatalf("冲突更新 id 漂移：%s != %s", id2, id)
	}

	// ④ GetUpstreamByID 读回完整行（base_url 已被第二次写入改动）。
	got, err := models.GetUpstreamByID(ctx, id)
	if err != nil {
		t.Fatalf("GetUpstreamByID 失败: %v", err)
	}
	if got.ID != id || got.Kind != "relay" || got.Name != "p1" {
		t.Fatalf("读回字段漂移：%+v", got)
	}
	if got.BaseURL != "sidecar://p1-v3" {
		t.Fatalf("base_url 应为最后一次写入值，got %q", got.BaseURL)
	}
	if !got.Enabled {
		t.Fatal("enabled 应为最后一次写入值 true")
	}

	// ⑤ 不同 kind 同名是另一行（唯一键是 kind+name）。
	idVendor, createdVendor, err := models.UpsertUpstreamWithCreated(ctx, "vendor", "p1", "https://api.example.invalid/v1", nil, true)
	if err != nil {
		t.Fatalf("vendor 同名写入失败: %v", err)
	}
	if !createdVendor || idVendor == id {
		t.Fatalf("不同 kind 必须新建行：created=%v id=%s（relay id=%s）", createdVendor, idVendor, id)
	}

	// ⑥ 不存在 → ErrNotFound（不是空行，也不是 nil error）。
	if _, err := models.GetUpstreamByID(ctx, tid3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应返回 ErrNotFound，got %v", err)
	}

	// ⑦ 校验失败不改库，且 created 必须是 false（避免调用方把错误当成功）。
	cases := []struct {
		name             string
		kind, n, baseURL string
	}{
		{"非法 kind", "weird", "x", "https://example.invalid"},
		{"空 name", "vendor", "", "https://example.invalid"},
		{"空 base_url", "vendor", "x", ""},
	}
	for _, tc := range cases {
		gotID, gotCreated, err := models.UpsertUpstreamWithCreated(ctx, tc.kind, tc.n, tc.baseURL, nil, true)
		if err == nil {
			t.Errorf("%s：应报错", tc.name)
		}
		if gotID != "" || gotCreated {
			t.Errorf("%s：失败路径必须返回 (0,false)，got (%s,%v)", tc.name, gotID, gotCreated)
		}
	}
}

// TestContractUpsertUpstreamWithCreatedInTx 覆盖管理面的真实调用形态：
// admin-api 的桥接在 WithTx 里调用，created 必须在事务视图里同样准确，
// 且事务回滚时整行不落库（GetUpstreamByID 应 ErrNotFound）。
func TestContractUpsertUpstreamWithCreatedInTx(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	var txID string
	err := s.WithTx(ctx, func(tx *Repos) error {
		id, created, err := tx.Models.UpsertUpstreamWithCreated(ctx, "selfhosted", "tx-up", "https://llm.example.invalid", nil, true)
		if err != nil {
			return err
		}
		if !created || id == "" {
			t.Fatalf("事务内首次写入 created/id 不对：created=%v id=%q", created, id)
		}
		// 事务内立即可读（同一 *sql.Tx）。
		row, err := tx.Models.GetUpstreamByID(ctx, id)
		if err != nil {
			return err
		}
		if row.Name != "tx-up" {
			t.Fatalf("事务内读回字段漂移：%+v", row)
		}
		txID = id
		// 故意回滚：验证「写入与审计同生同死」的语义没有在仓储层被破坏。
		return errors.New("测试用主动回滚")
	})
	if err == nil {
		t.Fatal("事务应返回错误")
	}
	if _, err := s.Repos().Models.GetUpstreamByID(ctx, txID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("回滚后行必须不存在，got %v", err)
	}
}
