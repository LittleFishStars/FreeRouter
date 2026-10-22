package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/idcode"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// TestContractAdminSettingsWired 守住「设置面四个依赖真的接上了」：
// 旧装配里 SettingsView/Patch/IPCheck/Refresh/UpstreamTest 全为 nil，
// 设置页只能回 404/未实现——而 handler 的 not-wired 分支让这个缺口
// 不会 panic，也就不会被普通测试发现。
func TestContractAdminSettingsWired(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), store.Options{
		Path:          dir + "/t.db",
		MasterKeyFile: dir + "/master.key",
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Default()
	log := slog.Default()
	sb := &settingsBridge{b: &adminBridge{st: st, repos: st.Repos()}, cfg: cfg}

	// GetSettings 必须给出 security/limits 两组，且含运行时开关。
	raw, err := sb.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	var doc map[string]map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("设置视图不是合法 JSON: %v", err)
	}
	if _, ok := doc["security"]["registration_enabled"]; !ok {
		t.Fatal("视图缺 security.registration_enabled")
	}
	if _, ok := doc["limits"]["default_rpm"]; !ok {
		t.Fatal("视图缺 limits.default_rpm")
	}

	// Patch 白名单：可热改键成功 + 落库 + 改内存；只读键整批拒绝。
	audit := httpapi.AdminAuditEntry{AccountID: "1", ActingClass: "admin", Result: "ok", Action: "system.settings.patch"}
	auditID, err := sb.PatchSettings(context.Background(), map[string]json.RawMessage{
		"security.registration_enabled": json.RawMessage(`false`),
	}, audit)
	if err != nil {
		t.Fatalf("PatchSettings 白名单键应成功: %v", err)
	}
	if auditID == "" {
		t.Fatal("PatchSettings 应返回 auditID")
	}
	if cfg.Security.RegistrationEnabled {
		t.Fatal("内存配置未被更新（改了但没生效）")
	}
	got, err := st.Repos().Settings.Get(context.Background(), "security.registration_enabled")
	if err != nil || got != "false" {
		t.Fatalf("settings 表未持久化: got=%q err=%v", got, err)
	}

	if _, err := sb.PatchSettings(context.Background(), map[string]json.RawMessage{
		"limits.default_rpm": json.RawMessage(`120`),
	}, audit); err == nil {
		t.Fatal("只读键（limits.default_rpm）应被拒绝")
	}

	// IPCheck：behind_proxy=false 时直连地址可用；非 IP 输入拒绝。
	cfg.Server.BehindProxy = false
	res, err := sb.CheckIP(context.Background(), "203.0.113.9")
	if err != nil || res == nil || !res.Allowed {
		t.Fatalf("直连模式下合法 IP 应 allowed: res=%+v err=%v", res, err)
	}
	if bad, _ := sb.CheckIP(context.Background(), "not-an-ip"); bad == nil || bad.Allowed {
		t.Fatal("非法 IP 应 allowed=false")
	}
	_ = log

	// UpstreamTest：未知 id 形状应报 not found；relay 且 sidecar 为 nil 时
	// 返回 ok=false 的**结构性结论**而不是 error。
	tb := &upstreamTesterBridge{store: st, sidecar: nil, cfg: cfg, httpc: &http.Client{}}
	if _, err := tb.TestUpstream(context.Background(), "bad_id"); err == nil {
		t.Fatal("非法 id 应报错")
	}
	// 造一条 relay 上游行。
	ctx := context.Background()
	var uid int64
	if err := st.WithTx(ctx, func(tx *store.Repos) error {
		id, _, err := tx.Models.UpsertUpstreamWithCreated(ctx, "relay", "测试 relay", "sidecar://fake_provider", nil, true)
		uid = id
		return err
	}); err != nil {
		t.Fatalf("建 upstream 行: %v", err)
	}
	out, err := tb.TestUpstream(ctx, idcode.Encode(idcode.Upstream, uid))
	if err != nil {
		t.Fatalf("relay 测试应返回结构化结论: %v", err)
	}
	if out["ok"] != false {
		t.Fatalf("sidecar 为 nil 时 relay 应 ok=false，got %+v", out)
	}
}

// TestContractAdminRefreshNotWired 守住未装配时的降级：nil 刷新器必须
// 返回 ErrAdminNotWired（handler 转 503/未实现），而不是 panic。
func TestContractAdminRefreshNotWired(t *testing.T) {
	r := &refreshBridge{refresher: nil}
	if _, err := r.RefreshModels(context.Background()); err == nil {
		t.Fatal("nil refresher 应返回未接线错误")
	}
	var a App
	if a.buildRefreshBridge() != nil {
		t.Fatal("未启动刷新器的 App 应返回 nil")
	}
	_ = bytes.MinRead
}
