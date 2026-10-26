// task-19 契约测试：admin_inbox 幂等与排序、密文仓储的三条红线、
// 审计 acting_class 写入口、上游形态变更的值域与匹配校验、迁移增量可重复。
// 全部 t.TempDir()，不污染仓库。
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- admin_inbox

// TestContractInboxAppendListCountRead：投递→列表→计数→标已读→撤回。
func TestContractInboxAppendListCountRead(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox
	acct := addTenantAccount(t, s)
	rel := "77"

	id1, err := in.Append(ctx, InboxModelMissing, SeverityWarning, "渠道 A 下模型 X 消失",
		`{"real_model_id":77,"change":"disappeared"}`, &rel)
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if id1 == "" {
		t.Fatal("返回 id 为 0")
	}
	n, err := in.CountUnread(ctx)
	if err != nil || n != 1 {
		t.Fatalf("未读数 = %d, %v，期望 1", n, err)
	}

	// detail_json 原样往返（不解析）。
	item, err := in.Get(ctx, id1)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if item.DetailJSON != `{"real_model_id":77,"change":"disappeared"}` {
		t.Errorf("detail_json 被改写过：%q", item.DetailJSON)
	}
	if item.Kind != InboxModelMissing || item.Severity != SeverityWarning {
		t.Errorf("kind/severity 往返不符：%+v", item)
	}
	if item.RelatedID == nil || *item.RelatedID != rel {
		t.Errorf("related_id = %v，期望 %s", item.RelatedID, rel)
	}
	if item.Read() {
		t.Error("新条目应为未读")
	}

	// 标已读 → 未读数归零 → 撤回。
	if err := in.MarkRead(ctx, id1, acct); err != nil {
		t.Fatalf("MarkRead 失败: %v", err)
	}
	if n, _ := in.CountUnread(ctx); n != 0 {
		t.Errorf("标已读后未读数 = %d，期望 0", n)
	}
	item, _ = in.Get(ctx, id1)
	if item.ReadBy == nil || *item.ReadBy != acct {
		t.Errorf("read_by = %v，期望 %s", item.ReadBy, acct)
	}
	if err := in.MarkUnread(ctx, id1); err != nil {
		t.Fatalf("MarkUnread 失败: %v", err)
	}
	if n, _ := in.CountUnread(ctx); n != 1 {
		t.Errorf("撤回后未读数 = %d，期望 1", n)
	}
	item, _ = in.Get(ctx, id1)
	if item.ReadAt != nil || item.ReadBy != nil {
		t.Errorf("撤回后仍带已读痕迹：%+v", item)
	}

	// MarkRead 幂等；MarkReadBatch 空列表是正常输入。
	if err := in.MarkRead(ctx, id1, acct); err != nil {
		t.Fatalf("重复 MarkRead 应幂等: %v", err)
	}
	// read_by 引用 accounts(id)，所以第二个标记人必须是真实账户（FK 会拒造出来的 id）。
	acct2 := addTenantAccount(t, s)
	if err := in.MarkRead(ctx, id1, acct2); err != nil {
		t.Fatalf("改标记人应幂等: %v", err)
	}
	item, _ = in.Get(ctx, id1)
	if item.ReadBy == nil || *item.ReadBy != acct2 {
		t.Errorf("重复标记应把 read_by 更新为最新：%v", item.ReadBy)
	}
	if n, err := in.MarkReadBatch(ctx, nil, acct); err != nil || n != 0 {
		t.Errorf("空批量 = %d, %v，期望 0, nil", n, err)
	}
	// 不存在的行：ErrNotFound。
	if err := in.MarkRead(ctx, tid3, acct); !errors.Is(err, ErrNotFound) {
		t.Errorf("标记不存在的行应 ErrNotFound，得到 %v", err)
	}
	if _, err := in.Get(ctx, tid3); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get 不存在应 ErrNotFound，得到 %v", err)
	}
}

// TestContractInboxDetailRawRoundTrip：detail_json **非 JSON 也原样读回**。
//
// 为什么钉成测试而不是靠注释（code-review 要求）：「原样返回不解析」这件事
// 靠注释活不长——将来有人在仓储层顺手 Unmarshal 一遍再序列化回去，注释还在、
// 行为已经变了，而且非 JSON 的载荷会静默变成空对象。
func TestContractInboxDetailRawRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox

	for _, raw := range []string{
		"not json at all",
		`{"broken": `,
		`[1,2,3]`,
		`"just a string"`,
		"",
	} {
		rel := strconv.Itoa(len(raw) + 1)
		id, err := in.Append(ctx, InboxModelChanged, SeverityInfo, "属性变化", raw, &rel)
		if err != nil {
			t.Fatalf("投递 detail=%q 失败: %v", raw, err)
		}
		item, err := in.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get 失败: %v", err)
		}
		if item.DetailJSON != raw {
			t.Errorf("detail_json 往返 = %q，期望原样 %q", item.DetailJSON, raw)
		}
	}
}

// TestContractInboxUnreadIdempotent：同一 (kind, related_id) 未读期间不重复写，
// 第二次只更新 detail_json（01-catalog §3.5；lead 裁决 4）。
//
// 这是收件箱能用的前提：目录刷新每 10 分钟一轮，裸 INSERT 一天就是上百条。
func TestContractInboxUnreadIdempotent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox
	rel := "5001"

	id1, err := in.Append(ctx, InboxModelMissing, SeverityError, "模型消失", `{"v":1}`, &rel)
	if err != nil {
		t.Fatalf("首次投递失败: %v", err)
	}
	// 第二次（模拟下一轮刷新读到同一变动）。
	id2, err := in.Append(ctx, InboxModelMissing, SeverityError, "模型消失", `{"v":2}`, &rel)
	if err != nil {
		t.Fatalf("重复投递失败: %v", err)
	}
	if id1 != id2 {
		t.Errorf("未读期间重复投递应命中同一行：%s vs %s", id1, id2)
	}
	if n, _ := in.CountUnread(ctx); n != 1 {
		t.Fatalf("未读期间重复写后行数 = %d，期望 1", n)
	}
	item, _ := in.Get(ctx, id1)
	if item.DetailJSON != `{"v":2}` {
		t.Errorf("重复投递应把 detail_json 更新为最新值，实际 %q", item.DetailJSON)
	}

	// 已读之后再投同一变动：必须**新开一条**（那是"又发生了一次"）。
	if err := in.MarkRead(ctx, id1, addTenantAccount(t, s)); err != nil {
		t.Fatalf("MarkRead 失败: %v", err)
	}
	id3, err := in.Append(ctx, InboxModelMissing, SeverityError, "模型消失", `{"v":3}`, &rel)
	if err != nil {
		t.Fatalf("已读后再投失败: %v", err)
	}
	if id3 == id1 {
		t.Error("已读后再投应新开一条，实际命中了旧行")
	}

	// related_id 为 NULL 的条目彼此不去重（无关联对象就构不成"同一件事"）。
	a, err := in.Append(ctx, InboxGlobalBudget, SeverityError, "全站熔断", `{}`, nil)
	if err != nil {
		t.Fatalf("投递无关联对象失败: %v", err)
	}
	b, err := in.Append(ctx, InboxGlobalBudget, SeverityError, "全站熔断", `{}`, nil)
	if err != nil {
		t.Fatalf("再投一次失败: %v", err)
	}
	if a == b {
		t.Error("related_id IS NULL 的条目不该互相覆盖（0003 索引刻意排除 NULL）")
	}
}

// TestContractInboxSeverityOnlyUpgrades：冲突时 severity 只升不降（lead 裁决 4）。
//
// 为什么必须"只升"：一轮刷新里同一变动可能先被报 error（真故障）后被报 info
// （抖动恢复），降级会让真故障从待办页顶部滑走——而列表页只按秩排序。
func TestContractInboxSeverityOnlyUpgrades(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox
	rel := "6001"

	id, err := in.Append(ctx, InboxChannelFailing, SeverityError, "渠道失联", `{"seq":1}`, &rel)
	if err != nil {
		t.Fatalf("投递 error 失败: %v", err)
	}
	// 后续低等级投递不得降级。
	for _, down := range []InboxSeverity{SeverityWarning, SeverityInfo} {
		if _, err := in.Append(ctx, InboxChannelFailing, down, "渠道失联", `{"seq":2}`, &rel); err != nil {
			t.Fatalf("投递 %s 失败: %v", down, err)
		}
		item, err := in.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get 失败: %v", err)
		}
		if item.Severity != SeverityError {
			t.Errorf("%s 投递后 severity 被降级为 %s，期望仍为 error", down, item.Severity)
		}
	}
	// 同等级重复投：保持原值，detail 更新。
	if _, err := in.Append(ctx, InboxChannelFailing, SeverityError, "渠道失联", `{"seq":3}`, &rel); err != nil {
		t.Fatalf("同秩投递失败: %v", err)
	}
	item, _ := in.Get(ctx, id)
	if item.Severity != SeverityError || item.DetailJSON != `{"seq":3}` {
		t.Errorf("同秩投递结果 %+v，期望 severity=error 且 detail 更新", item)
	}

	// 低→高必须升级（否则真故障会被先到的 info 压住）。
	rel2 := "6002"
	id2, err := in.Append(ctx, InboxModelChanged, SeverityInfo, "属性变化", `{}`, &rel2)
	if err != nil {
		t.Fatalf("投递 info 失败: %v", err)
	}
	if _, err := in.Append(ctx, InboxModelChanged, SeverityError, "属性变化", `{"v":"downsized"}`, &rel2); err != nil {
		t.Fatalf("升级为 error 失败: %v", err)
	}
	item2, _ := in.Get(ctx, id2)
	if item2.Severity != SeverityError {
		t.Errorf("info→error 未升级，实际 %s", item2.Severity)
	}
}

// TestContractInboxOrderAndFilters：排序秩 + 各筛选项 + 分页。
func TestContractInboxOrderAndFilters(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox

	// 故意乱序投递：info / error / warning。created_at 同秒，靠 id 兜底。
	for _, c := range []struct {
		kind InboxKind
		sv   InboxSeverity
		rel  string
	}{
		{InboxModelAdded, SeverityInfo, "10"},
		{InboxPoolExhausted, SeverityError, "20"},
		{InboxAccountPending, SeverityWarning, "30"},
		{InboxKeyAnomaly, SeverityError, "40"},
	} {
		rel := c.rel
		if _, err := in.Append(ctx, c.kind, c.sv, "t", `{}`, &rel); err != nil {
			t.Fatalf("投递 %s 失败: %v", c.kind, err)
		}
	}

	all, err := in.List(ctx, InboxFilter{})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("列表长度 = %d，期望 4", len(all))
	}
	// 秩降序：两条 error 在前，然后 warning，最后 info。
	// 同秩内部次序（created_at 全等时按 id 字典序 tiebreak）**不可断言**：
	// UUID 无时间序，"id 倒序=后投在前"的自增假设已作废。只钉秩的块结构。
	wantOrder := []InboxKind{InboxKeyAnomaly, InboxPoolExhausted, InboxAccountPending, InboxModelAdded}
	// error 块 = {key_anomaly, pool_exhausted} 两种排列之一；warning 与 info 各一条。
	errorOK := (all[0].Kind == InboxKeyAnomaly && all[1].Kind == InboxPoolExhausted) ||
		(all[0].Kind == InboxPoolExhausted && all[1].Kind == InboxKeyAnomaly)
	if !errorOK || all[2].Kind != InboxAccountPending || all[3].Kind != InboxModelAdded {
		t.Errorf("秩块结构失效：got %v（期望 [error,error,warning,info]）",
			[]InboxKind{all[0].Kind, all[1].Kind, all[2].Kind, all[3].Kind})
	}
	_ = wantOrder
	// 断言秩单调不增（把"默认按 severity 降序"钉成可执行契约）。
	for i := 1; i < len(all); i++ {
		if severityRank(all[i-1].Severity) < severityRank(all[i].Severity) {
			t.Fatalf("排序违例：第 %d 位秩 %d < 第 %d 位秩 %d",
				i, severityRank(all[i-1].Severity), i+1, severityRank(all[i].Severity))
		}
	}

	// 筛选项。
	if got, _ := in.List(ctx, InboxFilter{Kind: InboxModelAdded}); len(got) != 1 {
		t.Errorf("按 kind 筛 = %d 条，期望 1", len(got))
	}
	if got, _ := in.List(ctx, InboxFilter{Severity: SeverityError}); len(got) != 2 {
		t.Errorf("按 severity 筛 = %d 条，期望 2", len(got))
	}
	if got, _ := in.List(ctx, InboxFilter{Limit: 2}); len(got) != 2 {
		t.Errorf("Limit=2 未生效：%d", len(got))
	}
	page2, _ := in.List(ctx, InboxFilter{Limit: 2, Offset: 2})
	if len(page2) != 2 || page2[0].Kind != InboxAccountPending {
		t.Errorf("Offset 分页异常：%+v", page2)
	}

	// 未读/已读筛选 + 按类计数。
	// 标已读的是「error 秩的那两条之一」（同秩并列次序不可断言）——用 kind
	// 找出 key_anomaly 那条，保证下面 byKind[key_anomaly]==0 的断言稳定。
	var markID string
	for _, it := range all {
		if it.Kind == InboxKeyAnomaly {
			markID = it.ID
			break
		}
	}
	if markID == "" {
		t.Fatal("列表里没有 key_anomaly 条目")
	}
	if err := in.MarkRead(ctx, markID, addTenantAccount(t, s)); err != nil {
		t.Fatalf("MarkRead 失败: %v", err)
	}
	unread, _ := in.List(ctx, InboxFilter{UnreadOnly: true})
	read, _ := in.List(ctx, InboxFilter{ReadOnly: true})
	if len(unread) != 3 || len(read) != 1 {
		t.Errorf("未读/已读 = %d/%d，期望 3/1", len(unread), len(read))
	}
	byKind, err := in.CountUnreadByKind(ctx)
	if err != nil || byKind[InboxModelAdded] != 1 || byKind[InboxKeyAnomaly] != 0 {
		t.Errorf("按类未读计数 = %v, %v（已读的 key_anomaly 不该计入）", byKind, err)
	}
}

// TestContractInboxValueDomainRejected：kind/severity 非枚举值必须被拒（lead 裁决 1/2）。
//
// 为什么这是硬要求：两列都没有 DB CHECK（0001 不可改），而拼错的 severity 在
// CASE ... ELSE 1 下会**静默排到末尾**——看起来能用、实际是"最紧急的事沉底"。
func TestContractInboxValueDomainRejected(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox
	rel := "1"

	// 非法 severity（含文档里出现过的旧拼写）→ 可判定错误。
	for _, bad := range []InboxSeverity{"", "warn", "critical", "high", "ERROR"} {
		_, err := in.Append(ctx, InboxModelAdded, bad, "t", `{}`, &rel)
		if !errors.Is(err, ErrInvalidInboxSeverity) {
			t.Errorf("severity %q 应被拒并返回 ErrInvalidInboxSeverity，得到 %v", bad, err)
		}
	}
	// 非法 kind（含 02-admin 的旧词表——别名映射归文档层，代码里只出现规范名）。
	for _, bad := range []InboxKind{"", "registration", "model_change", "failure", "breaker_alert", "upstream_model_added"} {
		_, err := in.Append(ctx, bad, SeverityInfo, "t", `{}`, &rel)
		if !errors.Is(err, ErrInvalidInboxKind) {
			t.Errorf("kind %q 应被拒并返回 ErrInvalidInboxKind，得到 %v", bad, err)
		}
	}
	// 十类 kind 全部可用（裁决 2 的词表完整覆盖）。
	for _, k := range []InboxKind{InboxAccountPending, InboxModelAdded, InboxModelMissing,
		InboxModelChanged, InboxChannelFailing, InboxCredentialInvalid,
		InboxVerificationFailed, InboxPoolExhausted, InboxGlobalBudget, InboxKeyAnomaly} {
		rel := string(k) // related_id 用 kind 字符串本身（各 kind 互不相同，满足幂等键唯一）
		if _, err := in.Append(ctx, k, SeverityInfo, "t", `{}`, &rel); err != nil {
			t.Errorf("合法 kind %s 被拒: %v", k, err)
		}
	}
	// title 为空拒绝。
	if _, err := in.Append(ctx, InboxModelAdded, SeverityInfo, "   ", `{}`, &rel); err == nil {
		t.Error("空白 title 应被拒")
	}
}

// TestContractSeverityRankSQLMatchesGo：SQL 秩表达式与 Go 镜像必须同形。
//
// 为什么专门钉这条（code-review 指出的三处矛盾）：排序键的映射一旦在 Go 与 SQL
// 里各写一份，漂移的后果没人看得出来（"Go 认为 error 最高、库排出来 info 在前"）。
// 现在只有一个定义源 severityRankCase；这条测试把两处的等价性锁住。
func TestContractSeverityRankSQLMatchesGo(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	in := s.Repos().Inbox

	cases := []InboxSeverity{SeverityInfo, SeverityWarning, SeverityError}
	for i, sv := range cases {
		rel := strconv.Itoa(7000 + i)
		if _, err := in.Append(ctx, InboxModelChanged, sv, "秩校验", `{}`, &rel); err != nil {
			t.Fatalf("投递 %s 失败: %v", sv, err)
		}
	}
	// 用 SQL 侧的秩表达式直接问库：它的排名必须与 Go 的 severityRank 一致。
	rows, err := s.writeDB.QueryContext(ctx,
		`SELECT severity, `+fmt.Sprintf(severityRankCase, "severity")+
			` AS rank, COUNT(1) FROM admin_inbox GROUP BY severity, rank ORDER BY rank DESC`)
	if err != nil {
		t.Fatalf("跑 SQL 秩表达式失败: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[InboxSeverity]int{}
	n := 0
	for rows.Next() {
		var sv string
		var rank int
		var c int
		if err := rows.Scan(&sv, &rank, &c); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		seen[InboxSeverity(sv)] = rank
		n++
	}
	if n != len(cases) {
		t.Fatalf("SQL 分组数 = %d，期望 %d", n, len(cases))
	}
	for _, sv := range cases {
		if seen[sv] != severityRank(sv) {
			t.Errorf("severity %s：SQL 秩 = %d，Go 秩 = %d（两处映射漂移）", sv, seen[sv], severityRank(sv))
		}
	}
}

// ---------------------------------------------------------------- 密文仓储

// TestContractSecretRoundTrip：PutSecret→GetSecret 往返，含密钥轮换（upsert）。
func TestContractSecretRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	sec := s.Repos().Secrets

	const plain = "sk-abcdef-0123456789-SECRET"
	id1, err := sec.PutSecret(ctx, up, []byte(plain))
	if err != nil {
		t.Fatalf("PutSecret 失败: %v", err)
	}
	got, err := sec.GetSecret(ctx, id1)
	if err != nil || string(got) != plain {
		t.Fatalf("往返 = %q, %v，期望 %q", got, err, plain)
	}
	// 按上游取同一份。
	if got2, err := sec.GetSecretByUpstream(ctx, up); err != nil || string(got2) != plain {
		t.Errorf("按上游取 = %q, %v", got2, err)
	}
	// HasSecret 不解密即知有密钥。
	if ok, err := sec.HasSecret(ctx, up); err != nil || !ok {
		t.Errorf("HasSecret = %v, %v，期望 true", ok, err)
	}
	// upstreams.secret_id 被同事务指过去。
	var sid *string
	if err := s.writeDB.QueryRow(`SELECT secret_id FROM upstreams WHERE id = ?`, up).Scan(&sid); err != nil {
		t.Fatalf("读 secret_id 失败: %v", err)
	}
	if sid == nil || *sid != id1 {
		t.Errorf("secret_id = %v，期望 %s（PutSecret 未关联上游）", sid, id1)
	}

	// 轮换：同上游第二次写必须覆盖而不是撞唯一索引（ux_upstream_secrets_upstream）。
	id2, err := sec.PutSecret(ctx, up, []byte("sk-ROTATED-new-key"))
	if err != nil {
		t.Fatalf("轮换密钥失败（写成 INSERT 会撞 UNIQUE）: %v", err)
	}
	if id2 != id1 {
		t.Errorf("轮换后 secret_id 变了：%s → %s（upstream_id 唯一，应原地覆盖）", id1, id2)
	}
	if got, err := sec.GetSecret(ctx, id1); err != nil || string(got) != "sk-ROTATED-new-key" {
		t.Errorf("轮换后读到 %q, %v", got, err)
	}
	var rows int
	if err := s.writeDB.QueryRow(`SELECT COUNT(1) FROM upstream_secrets`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("密文行数 = %d，期望 1（一次轮换不该新增一行）", rows)
	}

	// 删除：行没了、引用置空、再读报 ErrSecretNotFound。
	if err := sec.DeleteSecret(ctx, id1); err != nil {
		t.Fatalf("DeleteSecret 失败: %v", err)
	}
	if _, err := sec.GetSecret(ctx, id1); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("删除后应 ErrSecretNotFound，得到 %v", err)
	}
	if err := sec.DeleteSecret(ctx, id1); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("重复删除应 ErrSecretNotFound，得到 %v", err)
	}
	if err := s.writeDB.QueryRow(`SELECT secret_id FROM upstreams WHERE id = ?`, up).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if sid != nil {
		t.Errorf("删除密文后 secret_id 仍指向已删行 = %s（悬空引用）", *sid)
	}
	if ok, _ := sec.HasSecret(ctx, up); ok {
		t.Error("HasSecret 在删除后仍报 true")
	}

	// 空明文拒绝：空密钥会让上游返回难以归因的 401。
	if _, err := sec.PutSecret(ctx, up, nil); err == nil {
		t.Error("空明文应被拒")
	}
	if _, err := sec.PutSecret(ctx, tid3, []byte("x")); err == nil {
		t.Error("无归属上游应被拒")
	}
}

// TestContractSecretPlaintextNeverInDatabase：**字节级**证据（lead/code-review 要求）。
//
// 为什么不只测代码路径：契约要能交付，唯一的交付物是"把库文件（含 -wal）当
// 原始字节读出来，明文不存在"。代码路径的断言挡不住将来有人在别处顺手写日志、
// 或把明文塞进某个索引键。
func TestContractSecretPlaintextNeverInDatabase(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	sec := s.Repos().Secrets

	const plain = "sk-PLAINTEXT-MUST-NEVER-BE-IN-DB-9e1c"
	if _, err := sec.PutSecret(ctx, up, []byte(plain)); err != nil {
		t.Fatalf("PutSecret 失败: %v", err)
	}
	// 再走一次解密（让明文在内存里过一遍），随后 Close 会 checkpoint 把 WAL 合进主库。
	if _, err := sec.GetSecretByUpstream(ctx, up); err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	needle := []byte(plain)
	checked := 0
	for _, name := range []string{"freerouter.db", "freerouter.db-wal", "freerouter.db-shm", "master.key"} {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		checked++
		if bytesContains(data, needle) {
			t.Errorf("明文出现在 %s（%d 字节）——明文永不入库的红线破了", name, len(data))
		}
	}
	if checked < 1 {
		t.Fatal("一个库文件都没读到，断言落空（等于没测）")
	}
	// 主密钥本身也不该进库（0001 的表里搜一遍）。
	key, err := os.ReadFile(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("读主密钥失败: %v", err)
	}
	db, err := os.ReadFile(filepath.Join(dir, "freerouter.db"))
	if err != nil {
		t.Fatalf("读库文件失败: %v", err)
	}
	if bytesContains(db, key) {
		t.Error("主密钥出现在库文件里（锁和钥匙放进同一个抽屉）")
	}
}

// bytesContains 用 bytes.Contains 做子串搜索（库文件是二进制，明文以字节序列出现）。
func bytesContains(haystack, needle []byte) bool {
	return bytes.Contains(haystack, needle)
}

// bytesEqual 是 bytes.Equal 的直传（测试里表达"两次 nonce 不该相同"更顺口）。
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

// TestContractSecretNonceUniqueAndTamperDetected：nonce 每次不同 + 篡改必须失败。
//
// 为什么 nonce 随机是硬要求：GCM 在同一密钥下复用 nonce = 密钥流复用，
// 两个密文异或就得到两个明文的异或——不是"弱一点"，是灾难性失效。
func TestContractSecretNonceUniqueAndTamperDetected(t *testing.T) {
	key := make([]byte, MasterKeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	plain := []byte("same-plaintext-every-time")

	var nonces, cts [][]byte
	for i := 0; i < 8; i++ {
		ct, nc, err := SealSecret(key, plain)
		if err != nil {
			t.Fatalf("SealSecret 失败: %v", err)
		}
		if len(nc) != 12 {
			t.Fatalf("nonce 长度 = %d，期望 GCM 标准 12 字节", len(nc))
		}
		if bytesContains(nc, plain) || bytesContains(ct, plain) {
			t.Fatal("明文痕迹出现在密文或 nonce 里")
		}
		nonces = append(nonces, nc)
		cts = append(cts, ct)
	}
	for i := 1; i < len(nonces); i++ {
		if bytesEqual(nonces[0], nonces[i]) {
			t.Errorf("第 %d 次加密的 nonce 与第一次相同（GCM 密钥流复用）", i+1)
		}
		if bytesEqual(cts[0], cts[i]) {
			t.Errorf("第 %d 次密文与第一次相同（nonce 未随机）", i+1)
		}
	}

	// 往返正确。
	got, err := OpenSecret(key, cts[0], nonces[0])
	if err != nil || string(got) != string(plain) {
		t.Fatalf("OpenSecret = %q, %v", got, err)
	}

	// 篡改密文任意一字节 → 认证失败，且**返回的明文必须为 nil**（不能是脏字节）。
	bad := append([]byte(nil), cts[0]...)
	bad[len(bad)/2] ^= 0x01
	out, err := OpenSecret(key, bad, nonces[0])
	if !errors.Is(err, ErrSecretDecrypt) {
		t.Errorf("篡改密文应 ErrSecretDecrypt，得到 %v", err)
	}
	if out != nil {
		t.Errorf("解密失败时返回了 %d 字节（应为 nil；复用 dst 会漏脏字节）", len(out))
	}
	// 篡改认证标签（尾部）同样必须失败。
	tamperTag := append([]byte(nil), cts[0]...)
	tamperTag[len(tamperTag)-1] ^= 0x80
	if out, err := OpenSecret(key, tamperTag, nonces[0]); err == nil || out != nil {
		t.Errorf("篡改标签未检出：%v / %q", err, out)
	}
	// nonce 被改 → 失败。
	badNonce := append([]byte(nil), nonces[0]...)
	badNonce[0] ^= 0xFF
	if out, err := OpenSecret(key, cts[0], badNonce); err == nil || out != nil {
		t.Errorf("nonce 被改未检出：%v / %q", err, out)
	}
	// nonce 长度不对必须前置拒绝（不交给 GCM 猜）。
	if _, err := OpenSecret(key, cts[0], nonces[0][:8]); !errors.Is(err, ErrSecretDecrypt) {
		t.Errorf("短 nonce 应 ErrSecretDecrypt，得到 %v", err)
	}
	// 错误密钥 → 失败，且错误信息里不含密钥字节。
	wrong := make([]byte, MasterKeySize)
	if out, err := OpenSecret(wrong, cts[0], nonces[0]); err == nil || out != nil {
		t.Errorf("错误密钥未检出：%v", err)
	}
	// 密钥长度不对 → 报错且**不把密钥内容写进错误信息**。
	if _, _, err := SealSecret([]byte("short"), plain); err == nil {
		t.Error("非 32 字节密钥应被拒")
	} else if strings.Contains(err.Error(), "short") {
		t.Errorf("错误信息泄漏了密钥内容：%v", err)
	}
	// 密文被截断（不足最小密文）也必须失败而不是 panic。
	if _, err := OpenSecret(key, []byte{}, nonces[0]); err == nil {
		t.Error("空密文应失败")
	}
}

// TestContractSecretTamperInStore：从库侧篡改（模拟 DB 页被改写），GetSecret 必须失败。
func TestContractSecretTamperInStore(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	sec := s.Repos().Secrets

	id, err := sec.PutSecret(ctx, up, []byte("another-secret-value"))
	if err != nil {
		t.Fatalf("PutSecret 失败: %v", err)
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`UPDATE upstream_secrets SET ciphertext = substr(ciphertext, 1, length(ciphertext) - 3)
		  WHERE id = ?`, id); err != nil {
		t.Fatalf("篡改失败: %v", err)
	}
	got, err := sec.GetSecret(ctx, id)
	if !errors.Is(err, ErrSecretDecrypt) {
		t.Errorf("截断密文应 ErrSecretDecrypt，得到 %v", err)
	}
	if got != nil {
		t.Errorf("失败时返回了 %d 字节明文（应为 nil）", len(got))
	}
	// 错误信息里既无密钥也无密文片段。
	if strings.Contains(err.Error(), "another-secret-value") {
		t.Error("错误信息里出现了明文字符串")
	}
	// 不存在的 id → ErrSecretNotFound（与"密文坏了"可区分：处置流程不同）。
	if _, err := sec.GetSecret(ctx, tid3); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("不存在的密文应 ErrSecretNotFound，得到 %v", err)
	}
}

// ---------------------------------------------------------------- 审计包装

// TestContractAppendfCarriesActingClass：管理面审计写入口把 acting_class 落进**列**。
//
// 为什么不是 detail 里的一个键（lead 裁决 5）：acting_class 是可索引列，
// 也是"一个账户多重身份时说清是哪一重做的"的唯一载体。
func TestContractAppendfCarriesActingClass(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	acct := addTenantAccount(t, s)
	logs := s.Repos().Logs

	id, err := logs.Appendf(ctx, &acct, "admin", ActionCategoryKey+".revoke", "ok",
		map[string]any{"key_id": 9, "actor": "self"})
	if err != nil {
		t.Fatalf("Appendf 失败: %v", err)
	}
	got, err := logs.List(ctx, AuditFilter{Limit: 10})
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %d 条, %v", len(got), err)
	}
	e := got[0]
	if e.ID != id {
		t.Errorf("id 往返 = %s，期望 %s", e.ID, id)
	}
	if e.ActingClass == nil || *e.ActingClass != "admin" {
		t.Errorf("acting_class 未落列：%v", e.ActingClass)
	}
	if e.Action != ActionCategoryKey+".revoke" || e.Result != "ok" {
		t.Errorf("action/result 不符：%+v", e)
	}
	if e.AccountID == nil || *e.AccountID != acct {
		t.Errorf("account_id = %v，期望 %s", e.AccountID, acct)
	}
	if e.Detail["key_id"] != float64(9) {
		t.Errorf("detail 往返异常：%+v", e.Detail)
	}

	// 身份未定（登录失败一类）：actingClass 空 → 列 NULL，而不是写空串。
	if _, err := logs.Appendf(ctx, nil, "", ActionCategoryAuth+".login", "denied", nil); err != nil {
		t.Fatalf("无身份审计失败: %v", err)
	}
	var clsIsNull int
	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_log WHERE action = ? AND acting_class IS NULL`,
		ActionCategoryAuth+".login").Scan(&clsIsNull); err != nil {
		t.Fatal(err)
	}
	if clsIsNull != 1 {
		t.Errorf("空 actingClass 应落 NULL，命中 %d 行", clsIsNull)
	}
	// 九类前缀常量与 06-audit §3 的分组数一致（漂移即红：少一类就有事件无法归类）。
	if n := countActionCategories(); n != 9 {
		t.Errorf("action 类别常量 = %d，期望 9（06-audit §3 九类）", n)
	}
}

// countActionCategories 数一遍九类常量是否都在（编译期之外的二次保险）。
func countActionCategories() int {
	set := map[string]bool{
		ActionCategoryAuth: true, ActionCategoryAccount: true, ActionCategoryGroup: true,
		ActionCategoryKey: true, ActionCategoryCred: true, ActionCategoryAbstract: true,
		ActionCategoryReal: true, ActionCategoryQuota: true, ActionCategorySystem: true,
	}
	return len(set)
}

// ---------------------------------------------------------------- 上游写域

// TestContractUpdateUpstream：改名/改地址/启停 + 存在性与重名校验。
func TestContractUpdateUpstream(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s)
	models := s.Repos().Models

	if err := models.UpdateUpstream(ctx, up, "hub-renamed", "sidecar://hub-renamed", false); err != nil {
		t.Fatalf("UpdateUpstream 失败: %v", err)
	}
	ups, err := models.ListUpstreams(ctx)
	if err != nil || len(ups) != 1 {
		t.Fatalf("ListUpstreams = %+v, %v", ups, err)
	}
	if ups[0].Name != "hub-renamed" || ups[0].BaseURL != "sidecar://hub-renamed" || ups[0].Enabled {
		t.Errorf("更新未生效：%+v", ups[0])
	}
	// 空值/不存在/重名。
	if err := models.UpdateUpstream(ctx, up, "", "x", true); err == nil {
		t.Error("空 name 应拒")
	}
	if err := models.UpdateUpstream(ctx, up, "y", "", true); err == nil {
		t.Error("空 base_url 应拒")
	}
	if err := models.UpdateUpstream(ctx, tid3, "y", "z", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的上游应 ErrNotFound，得到 %v", err)
	}
	if _, err := models.UpsertUpstream(ctx, "relay", "other", "sidecar://other", nil, true); err != nil {
		t.Fatalf("建第二个上游失败: %v", err)
	}
	if err := models.UpdateUpstream(ctx, up, "other", "sidecar://hub", true); err == nil {
		t.Error("改成重名应被 ux_upstreams_kind_name 拒")
	}
}

// TestContractSetUpstreamKind：形态变更的值域与"形态/地址匹配"校验。
//
// 为什么匹配要校验：kind 决定调用路径。改成 relay 却留着 http 地址，等于让
// 运行时按一个永远不会被使用的端点做判断；反之 vendor 留 sidecar:// 锚点则
// 必然连不上。这一条把只能靠日志反推的错误挡在写入时。
func TestContractSetUpstreamKind(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	up := aggregateTestUpstream(t, s) // relay + sidecar://hub
	models := s.Repos().Models

	// 非法值域。
	for _, bad := range []string{"", "RELAY", "proxy", "vendor " + ""} {
		if err := models.SetUpstreamKind(ctx, up, bad); err == nil {
			t.Errorf("非法 kind %q 应被拒", bad)
		}
	}
	// relay→vendor：当前是 provider 锚点，必须报错并给出可执行提示。
	err := models.SetUpstreamKind(ctx, up, "vendor")
	if err == nil {
		t.Fatal("relay 带 sidecar:// 锚点改成 vendor 应被拒")
	}
	if !strings.Contains(err.Error(), "真实端点地址") {
		t.Errorf("错误信息应给出处置指引，实际：%v", err)
	}
	// 先把地址换成真实端点，再改形态：应成功。
	if err := models.UpdateUpstream(ctx, up, "hub", "https://api.example.invalid/v1", true); err != nil {
		t.Fatalf("改地址失败: %v", err)
	}
	if err := models.SetUpstreamKind(ctx, up, "vendor"); err != nil {
		t.Fatalf("改形态失败: %v", err)
	}
	ups, _ := models.ListUpstreams(ctx)
	if ups[0].Kind != "vendor" {
		t.Errorf("kind = %s，期望 vendor", ups[0].Kind)
	}
	// vendor→relay 又被拒（地址不是 sidecar:// 形态）。
	if err := models.SetUpstreamKind(ctx, up, "relay"); err == nil {
		t.Error("带真实端点的上游改成 relay 应被拒")
	}
	// 幂等：同形态重复设置成功（不改任何东西）。
	if err := models.SetUpstreamKind(ctx, up, "vendor"); err != nil {
		t.Errorf("同形态重复设置应幂等，得到 %v", err)
	}
	// 不存在的上游。
	if err := models.SetUpstreamKind(ctx, tid3, "vendor"); !errors.Is(err, ErrNotFound) {
		t.Errorf("应 ErrNotFound，得到 %v", err)
	}
}

// TestContractCredentialStatusAndFilter：status 值域、筛选形状、与审计同事务。
func TestContractCredentialStatusAndFilter(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	models := s.Repos().Models
	owner := addTenantAccount(t, s)

	c1, err := models.CreateCredential(ctx, owner, "prov_a", "ext-1", "标签一", "ref://1")
	if err != nil {
		t.Fatalf("建凭据失败: %v", err)
	}
	c2, err := models.CreateCredential(ctx, owner, "prov_b", "ext-2", "标签二", "ref://2")
	if err != nil {
		t.Fatalf("建凭据 2 失败: %v", err)
	}
	c3, err := models.CreateCredential(ctx, owner, "prov_b", "ext-3", "标签三", "ref://3")
	if err != nil {
		t.Fatalf("建凭据 3 失败: %v", err)
	}

	// 初始 pending（库里 CHECK 的值域）。
	list, err := models.ListCredentials(ctx, CredentialFilter{Status: CredPending})
	if err != nil || len(list) != 3 {
		t.Fatalf("pending 列表 = %d 条, %v，期望 3", len(list), err)
	}
	// 排序 = created_at 倒序。三条在同一测试秒内创建（created_at 全等），
	// 并列时按 id 字典序 tiebreak——UUID 无时间序，**插入序不再是可断言量**。
	// 这条测试钉住的是排序键存在且不报错；「最新在前」的字面断言要跨秒
	// 写入才能做出确定性输入，旧实现（自增 id）的"id 序=插入序"假设已随
	// UUID 改造作废。集合正确性由上面的 len==3 与下面的筛选断言覆盖。
	if list[0].ID != c1 && list[0].ID != c2 && list[0].ID != c3 {
		t.Errorf("列表首元素应是建出的三条之一，实际 %+v", list)
	}
	// 筛选：owner / provider / 组合。
	byOwner, _ := models.ListCredentials(ctx, CredentialFilter{OwnerAccountID: &owner})
	if len(byOwner) != 3 {
		t.Errorf("按 owner 筛 = %d，期望 3", len(byOwner))
	}
	byProv, _ := models.ListCredentials(ctx, CredentialFilter{Provider: "prov_b"})
	if len(byProv) != 2 {
		t.Errorf("按 provider 筛 = %d，期望 2", len(byProv))
	}
	both, _ := models.ListCredentials(ctx, CredentialFilter{Provider: "prov_a", OwnerAccountID: &owner})
	if len(both) != 1 || both[0].ID != c1 {
		t.Errorf("组合筛 = %+v，期望只 [c1]", both)
	}
	if got, _ := models.ListCredentials(ctx, CredentialFilter{Limit: 2}); len(got) != 2 {
		t.Errorf("Limit 未生效：%d", len(got))
	}
	// 非法筛选值拒绝（不静默返回空集——空集与"筛错了"必须可区分）。
	if _, err := models.ListCredentials(ctx, CredentialFilter{Status: CredentialStatus("revoked")}); err == nil {
		t.Error("库里 CHECK 外的状态筛选应报错")
	}
	if _, err := models.ListCredentials(ctx, CredentialFilter{VerifyStatus: VerifyStatus("bogus")}); err == nil {
		t.Error("非法鉴定状态筛选应报错")
	}

	// 状态写入：suspend/ban 都落 disabled（差别在审计的 reason，不在状态机）。
	if err := models.SetCredentialStatus(ctx, c1, CredDisabled); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	got, _ := models.LookupCredential(ctx, c1)
	if got.Status != string(CredDisabled) {
		t.Errorf("status = %s，期望 disabled", got.Status)
	}
	if err := models.SetCredentialStatus(ctx, c2, CredActive); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	if err := models.SetCredentialStatus(ctx, tid3, CredActive); !errors.Is(err, ErrCredentialNotFound) {
		t.Errorf("不存在的凭据应 ErrCredentialNotFound，得到 %v", err)
	}
	// 库里 CHECK 之外的值（02-credentials §5.2 的 revoked/degraded）必须在 Go 层被拒：
	// 否则错误会带着 SQLite 的 CHECK 文案漏进 API 响应。
	for _, bad := range []CredentialStatus{"revoked", "degraded", "banned", "suspended", ""} {
		if err := models.SetCredentialStatus(ctx, c1, bad); err == nil {
			t.Errorf("状态 %q 不在库里 CHECK 值域，应被 Go 层先拒", bad)
		}
	}

	// 与审计同事务：WithTx 里写状态 + 写审计，中途报错必须一起回滚。
	wantErr := errors.New("boom")
	err = s.WithTx(ctx, func(tx *Repos) error {
		if e := tx.Models.SetCredentialStatus(ctx, c2, CredDisabled); e != nil {
			return e
		}
		if _, e := tx.Logs.Appendf(ctx, &owner, "admin", ActionCategoryCred+".ban", "ok",
			map[string]any{"credential_id": c2, "reason": "abuse"}); e != nil {
			return e
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx 应传出错误，实际 %v", err)
	}
	if g, _ := models.LookupCredential(ctx, c2); g.Status != string(CredActive) {
		t.Errorf("回滚后 status = %s，期望仍是 active（写与审计没做到同生同死）", g.Status)
	}
	if g, _ := models.ListCredentials(ctx, CredentialFilter{Status: CredDisabled}); len(g) != 1 || g[0].ID != c1 {
		t.Errorf("回滚后 disabled 列表 = %+v，期望只 [c1]", g)
	}
	var auditRows int
	if err := s.writeDB.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_log WHERE action = ?`, ActionCategoryCred+".ban").
		Scan(&auditRows); err != nil {
		t.Fatalf("数审计行失败: %v", err)
	}
	if auditRows != 0 {
		t.Errorf("回滚后仍有 %d 条 cred.ban 审计（写与审计没同生同死）", auditRows)
	}
}

// ---------------------------------------------------------------- 迁移增量

// TestContractMigration0003Repeatable：0003 的部分唯一索引在重复开库下不报错，
// 且索引真的存在（约束生效由幂等测试反向证明，这里查元数据）。
func TestContractMigration0003Repeatable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "freerouter.db")
	keyPath := filepath.Join(dir, "master.key")
	ctx := context.Background()

	open := func() *Store {
		s, err := Open(ctx, Options{Path: dbPath, MasterKeyFile: keyPath, BusyTimeout: 2 * time.Second})
		if err != nil {
			t.Fatalf("Open 失败: %v", err)
		}
		return s
	}
	s1 := open()
	s1.Close()
	s2 := open()
	defer s2.Close()

	var sqlText string
	if err := s2.writeDB.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='ux_admin_inbox_open'`).
		Scan(&sqlText); err != nil {
		t.Fatalf("0003 的部分唯一索引不存在: %v", err)
	}
	if !strings.Contains(sqlText, "read_at IS NULL") {
		t.Errorf("索引定义缺未读条件：%s", sqlText)
	}
	// 已应用集恰好覆盖 embed 的迁移集（不重复应用）。
	var applied int
	if err := s2.writeDB.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if want := embeddedMigrationCount(t); applied != want {
		t.Errorf("schema_migrations = %d 行，期望 %d", applied, want)
	}
}
