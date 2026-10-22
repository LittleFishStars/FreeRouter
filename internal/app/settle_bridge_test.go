package app

// settle_bridge_test.go —— M1 终态结算的**落库证据**（app 侧，真 SQLite）。
//
// httpapi 侧契约测试（internal/httpapi/settle_contract_test.go）守的是
// 「结算被调用几次、带什么」；本文件守的是「调用之后库里真有了数据」：
//   - requests 表按终态各一行（成功 + 失败 = 2 行）；
//   - usage_daily 只累加成功那次（失败请求没有 resolved_real_model_id 时不汇总）；
//   - account_quotas 的 request/day 只被成功请求 +1（失败请求 QuotaRequest=0）。
//
// 为什么直接开第二个 sql.DB 读 requests 而不加仓储读口：requests 的读面
// 属于报表里程碑（M7 看板），现在为测试加一个生产读口会把「谁在读 requests」
// 这件事提前定死；用同库只读连接读计数是测试侧的取证手段，不进生产 API。

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/ir"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// newSettleTestApp 起最小装配：临时库 + 关 sidecar（结算与上游无关）。
func newSettleTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:18081"
	cfg.Store.Path = filepath.Join(dir, "t.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "m.key")
	cfg.Sidecar.Enabled = false

	a, err := Setup(context.Background(), Options{Config: cfg})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestContractSettleBridgeWritesRequestAndUsage：成功 + 失败各结算一次，
// 断言 requests 两行、usage_daily 只累加成功那次、请求额度只 +1。
func TestContractSettleBridgeWritesRequestAndUsage(t *testing.T) {
	a := newSettleTestApp(t)
	ctx := context.Background()
	repos := a.store.Repos()

	// 造实体：账户 + 抽象模型 + 真实模型（usage_daily 的三元组齐才累加）。
	acctID, err := repos.Accounts.CreateAccount(ctx, store.AccountAuth{
		Account:      store.Account{DisplayName: "结算测试", Username: "settle_case"},
		PasswordHash: "x",
	}, []store.Class{store.ClassTenant})
	if err != nil {
		t.Fatalf("建账户: %v", err)
	}
	amID, err := repos.Models.CreateAbstractModel(ctx, store.AbstractModel{
		Name: "smart", DisplayName: "smart", Status: store.ModelActive,
	})
	if err != nil {
		t.Fatalf("建抽象模型: %v", err)
	}
	if amID == 0 {
		t.Fatal("抽象模型 id 不应为 0")
	}
	upID, err := repos.Models.UpsertUpstream(ctx, "selfhosted", "test-up", "http://127.0.0.1:1", nil, true)
	if err != nil {
		t.Fatalf("建上游: %v", err)
	}
	rmID, err := repos.Models.UpsertRealModel(ctx, store.RealModel{
		UpstreamID: upID, UpstreamModelID: "deepseek-chat", DisplayName: "deepseek-chat",
	})
	if err != nil {
		t.Fatalf("建真实模型: %v", err)
	}
	// 请求额度行：day/10（会计数 -1 行 → 1）。
	if err := repos.Quotas.SetAccountQuota(ctx, acctID, store.TotalScope,
		store.UnitRequest, store.PeriodDay, 10); err != nil {
		t.Fatalf("建额度行: %v", err)
	}

	bridge := a.newSettler().(*settleBridge)
	now := time.Now().UTC()

	// ① 成功请求：request/token 维度都记。
	if err := bridge.Settle(ctx, httpapi.SettleInput{
		RequestID: "r-ok", Endpoint: "openai-chat",
		AccountID: fmt.Sprint(acctID), KeyID: "",
		ModelName: "smart", RealModelID: rmID,
		Status: "ok", HTTPStatus: 200,
		RetryCount: 0, TTFTMs: 12, TotalMs: 34,
		Usage: ir.Ptr(ir.Usage{InputTokens: ir.Ptr(10), OutputTokens: ir.Ptr(5)}), EndedAt: now,
	}); err != nil {
		t.Fatalf("成功请求结算失败: %v", err)
	}

	// ② 失败请求：QuotaRequest=0（不扣 request 维度），但仍写一行。
	if err := bridge.Settle(ctx, httpapi.SettleInput{
		RequestID: "r-err", Endpoint: "openai-chat",
		AccountID: fmt.Sprint(acctID),
		ModelName: "smart",
		Status:    "error", HTTPStatus: 502, ErrorCode: "upstream_unavailable",
		TTFTMs: -1, TotalMs: 5,
		EndedAt: now,
	}); err != nil {
		t.Fatalf("失败请求结算失败: %v", err)
	}

	// ① 额度行：request/day used 恰为 1（成功 1 + 失败 0）。
	q, err := repos.Quotas.AccountQuota(ctx, acctID, store.TotalScope,
		store.UnitRequest, store.PeriodDay)
	if err != nil {
		t.Fatalf("读额度行: %v", err)
	}
	if q.Used != 1 {
		t.Fatalf("request/day used = %d，期望 1（失败请求 QuotaRequest=0）", q.Used)
	}

	// ② requests：两次结算 → 两行（终态各一行）。
	db, err := sql.Open("sqlite", a.store.Path())
	if err != nil {
		t.Fatalf("打开只读连接: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests WHERE account_id = ?`, acctID).Scan(&n); err != nil {
		t.Fatalf("读 requests: %v", err)
	}
	if n != 2 {
		t.Fatalf("requests 行数 = %d，期望 2（成功 + 失败各一行）", n)
	}
	// 失败行的 error_code 必须落库（对账靠它分组）。
	var errCode sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT error_code FROM requests WHERE account_id = ? AND status = 'error'`, acctID).
		Scan(&errCode); err != nil {
		t.Fatalf("读失败行: %v", err)
	}
	if !errCode.Valid || errCode.String != "upstream_unavailable" {
		t.Fatalf("失败行 error_code = %+v，期望 upstream_unavailable", errCode)
	}

	// ③ usage_daily：只有成功那行（失败行缺 real_model_id，按 store 规则不汇总）。
	rows, err := repos.Usage.UsageByAccountRange(ctx, acctID,
		now.Format("2006-01-02"), now.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("读 usage_daily: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usage_daily 行数 = %d，期望 1（只汇总成功请求）", len(rows))
	}
	if rows[0].Requests != 1 || rows[0].InTokens != 10 || rows[0].OutTokens != 5 {
		t.Fatalf("usage_daily = %+v，期望 requests=1 in=10 out=5", rows[0])
	}
}
