package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// 首启自举的契约测试（2026-10-23 定案）：
//   - 空库启动 → 自动创建 admin + 20 字符随机口令 + initial-password.txt(0600)；
//   - banner 含同一串口令（终端展示路径）；
//   - 用该口令登录 → must_change_password=true，管理面 403
//     password_change_required；
//   - POST /api/account/password 改密成功 → 旧会话失效、口令文件删除、
//     新口令登录后 must_change_password=false 且管理面 200。
//
// 这些断言必须走真实 App.Handler()（完整中间件链 + 真 SQLite），
// 因为「闸」的位置（mwSession / mwAdmin）本身就是被验证的对象。

// newBootstrapTestApp 起一个带首启自举的最小 App。
func newBootstrapTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:18081"
	cfg.Store.Path = filepath.Join(dir, "t.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "m.key")
	cfg.Security.PasswordMinLength = 12
	cfg.Server.AutoOpenBrowser = false
	cfg.Sidecar.Enabled = false
	a, err := Setup(context.Background(), Options{Config: cfg})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestContractBootstrapCreatesAdminWithRandomPassword 覆盖「首启随机口令」本身。
func TestContractBootstrapCreatesAdminWithRandomPassword(t *testing.T) {
	a := newBootstrapTestApp(t)
	br := a.Bootstrap()
	if br == nil {
		t.Fatal("空库首启应产生 BootstrapResult")
	}
	if br.Username != "admin" {
		t.Fatalf("自举用户名应为 admin，得 %q", br.Username)
	}
	if got := len([]rune(br.Password)); got != 20 {
		t.Fatalf("一次性口令应为 20 字符（GenerateOneTimePassword 契约），得 %d", got)
	}
	if !strings.Contains(a.BootstrapBanner(), br.Password) {
		t.Fatal("banner 必须包含本次生成的口令（用户拿它登录的唯一途径）")
	}

	// 口令文件：存在、0600、内容含用户名与口令。
	fi, err := os.Stat(br.PasswordFile)
	if err != nil {
		t.Fatalf("口令文件应存在：%v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("口令文件权限应为 0600，得 %o", perm)
	}
	raw, err := os.ReadFile(br.PasswordFile)
	if err != nil {
		t.Fatalf("读取口令文件失败：%v", err)
	}
	if !strings.Contains(string(raw), br.Username) || !strings.Contains(string(raw), br.Password) {
		t.Fatal("口令文件内容应含用户名与口令")
	}

	// 库内账户：唯一且带强制改密标志；setup 向导随之关闭。
	ctx := context.Background()
	n, err := a.store.Repos().Accounts.CountAccounts(ctx)
	if err != nil || n != 1 {
		t.Fatalf("首启后账户数应为 1，得 %d（err=%v）", n, err)
	}
	acct, err := a.store.Repos().Accounts.GetAccountByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("读取自举账户失败：%v", err)
	}
	if !acct.MustChangePassword {
		t.Fatal("自举账户必须带 must_change_password=true")
	}

	rec := doConsole(t, a.Handler(), http.MethodGet, "/api/setup/status", "", "", "")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["setup_required"] != false {
		t.Fatalf("自举后 setup_required 应为 false，得 %v", body["setup_required"])
	}
}

// TestContractBootstrapForcesPasswordChange 覆盖「登录后强制改密 + 改密后放行」。
func TestContractBootstrapForcesPasswordChange(t *testing.T) {
	a := newBootstrapTestApp(t)
	h := a.Handler()
	br := a.Bootstrap()

	loginBody := fmt.Sprintf(`{"username":%q,"password":%q}`, br.Username, br.Password)
	rec := doConsole(t, h, http.MethodPost, "/api/auth/login", loginBody, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("初始口令登录应 200，得 %d（%s）", rec.Code, rec.Body.String())
	}
	var login map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	if login["must_change_password"] != true {
		t.Fatalf("登录响应应带 must_change_password=true，得 %v", login["must_change_password"])
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录应 Set 会话 Cookie")
	}
	cookie := "fr_session=" + cookies[0].Value

	// 管理面：强制改密窗口内必须 403 password_change_required。
	recAdmin := doConsole(t, h, http.MethodGet, "/api/admin/accounts", "", cookie, "")
	if recAdmin.Code != http.StatusForbidden {
		t.Fatalf("改密前管理面应 403，得 %d（%s）", recAdmin.Code, recAdmin.Body.String())
	}
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recAdmin.Body.Bytes(), &errBody)
	if errBody.Error.Code != "password_change_required" {
		t.Fatalf("错误码应为 password_change_required，得 %q", errBody.Error.Code)
	}

	// 改密 + 补全显示名。
	const newPassword = "brand-new-password-123"
	recChange := doConsole(t, h, http.MethodPost, "/api/account/password",
		fmt.Sprintf(`{"current_password":%q,"new_password":%q,"display_name":"站长"}`,
			br.Password, newPassword), cookie, "XMLHttpRequest")
	if recChange.Code != http.StatusOK {
		t.Fatalf("改密应 200，得 %d（%s）", recChange.Code, recChange.Body.String())
	}

	// 旧会话被吊销；口令文件被删除；显示名已更新。
	if recOld := doConsole(t, h, http.MethodGet, "/api/auth/session", "", cookie, ""); recOld.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应 401，得 %d", recOld.Code)
	}
	if _, err := os.Stat(br.PasswordFile); !os.IsNotExist(err) {
		t.Fatalf("改密成功后口令文件应被删除（err=%v）", err)
	}
	acct, err := a.store.Repos().Accounts.GetAccountByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("读取账户失败：%v", err)
	}
	if acct.DisplayName != "站长" {
		t.Fatalf("显示名应更新为「站长」，得 %q", acct.DisplayName)
	}
	if acct.MustChangePassword {
		t.Fatal("改密成功后 must_change_password 应清除")
	}

	// 新口令登录：不再要求改密，管理面放行。
	recNew := doConsole(t, h, http.MethodPost, "/api/auth/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, br.Username, newPassword), "", "")
	if recNew.Code != http.StatusOK {
		t.Fatalf("新口令登录应 200，得 %d（%s）", recNew.Code, recNew.Body.String())
	}
	var login2 map[string]any
	_ = json.Unmarshal(recNew.Body.Bytes(), &login2)
	if login2["must_change_password"] != false {
		t.Fatalf("改密后登录应带 must_change_password=false，得 %v", login2["must_change_password"])
	}
	newCookie := "fr_session=" + recNew.Result().Cookies()[0].Value
	if recAdmin2 := doConsole(t, h, http.MethodGet, "/api/admin/accounts", "", newCookie, ""); recAdmin2.Code != http.StatusOK {
		t.Fatalf("改密后管理面应 200，得 %d（%s）", recAdmin2.Code, recAdmin2.Body.String())
	}
}
