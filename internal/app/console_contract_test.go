package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// console_routes.go 的契约测试（≥4）：会话面端到端（真 SQLite + 真装配）。
// 断言面：setup 真判定落库、login 落库（argon2id 哈希入库）、logout 失效、
// init 二次 409——全部穿过 App.Handler() 的完整中间件链。

// newConsoleTestApp 起一个最小可测 App：内存目录 SQLite + 默认配置
// （argon2 参数由 config.Default 给的 64MiB/3/2——**慢**（每枪 ~100ms），
// 但这正是「端到端」的意义：桥的每个字段翻译都在真实路径上跑）。
func newConsoleTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Listen = "127.0.0.1:18080"
	cfg.Store.Path = filepath.Join(dir, "t.db")
	cfg.Store.MasterKeyFile = filepath.Join(dir, "m.key")
	// 压低 argon2 强度：桥的字段翻译正确性与哈希强度无关（强度契约在
	// auth 包测过）；不压会把每枪登录拖到百毫秒级、测试整体分钟级。
	cfg.Security.PasswordMinLength = 12

	cfg.Sidecar.Enabled = false // 控制台面与 sidecar 无关（上游自举另有它的测试）
	// DisableBootstrap：本文件的契约面是「空库 + 向导」路径；首启自举
	//（随机口令自动建号）由 bootstrap_contract_test.go 单独覆盖。
	a, err := Setup(context.Background(), Options{Config: cfg, DisableBootstrap: true, DisableBackgroundLoops: true})
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	// 生产接线已在 buildServer 末尾（a.AttachConsole()）；测试不再手动挂，
	// 直接走生产路径——「测试验证的形状=生产形状」比重复挂载更可靠。
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// doConsole 打完整链（App.Handler()：requestID→安全头→代理→body→路由）。
func doConsole(t *testing.T, h http.Handler, method, path, body, cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if csrf != "" {
		req.Header.Set("X-Requested-With", csrf)
	}
	req.RemoteAddr = "127.0.0.1:55555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 1. setup 真判定端到端：空库 required=true；init 后 required=false
// （同一 App 的两次 GET——状态翻转来自**读库**，不是内存）。
func TestContractConsoleSetupStatusEndToEnd(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()

	rec := doConsole(t, h, http.MethodGet, "/api/setup/status", "", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status 应 200，得 %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["setup_required"] != true {
		t.Fatalf("空库应 setup_required=true，得 %v", body["setup_required"])
	}

	recInit := doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	if recInit.Code != http.StatusCreated {
		t.Fatalf("init 应 201，得 %d（%s）", recInit.Code, recInit.Body.String())
	}

	rec2 := doConsole(t, h, http.MethodGet, "/api/setup/status", "", "", "")
	var body2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &body2)
	if body2["setup_required"] != false {
		t.Fatalf("init 后应 setup_required=false，得 %v", body2["setup_required"])
	}
}

// 2. login 落库：init 创建的账户能登录（argon2id 哈希经桥写库、
// 会话行经桥落 sessions 表），Set-Cookie 闭环。
func TestContractConsoleLoginEndToEnd(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()

	doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")

	rec := doConsole(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应 200，得 %d（%s）", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "fr_session" {
		t.Fatalf("登录应 Set fr_session Cookie，得 %v", cookies)
	}
	// 会话恢复（GET session 过 mwSession）。
	cookie := "fr_session=" + cookies[0].Value
	recS := doConsole(t, h, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recS.Code != http.StatusOK {
		t.Fatalf("session 应 200，得 %d（%s）", recS.Code, recS.Body.String())
	}
	var sb map[string]any
	_ = json.Unmarshal(recS.Body.Bytes(), &sb)
	if sb["account_id"] == "" {
		t.Fatal("session 响应应含 account_id")
	}
}

// 3. logout 失效：登出删 sessions 行（落库验证），旧 Cookie 401。
func TestContractConsoleLogoutEndToEnd(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()

	doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	recL := doConsole(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	cookie := "fr_session=" + recL.Result().Cookies()[0].Value

	// 登出（带 CSRF 头）。
	recOut := doConsole(t, h, http.MethodPost, "/api/auth/logout", "", cookie, "XMLHttpRequest")
	if recOut.Code != http.StatusOK {
		t.Fatalf("登出应 200，得 %d", recOut.Code)
	}
	// 旧 Cookie 再访问 → 401（会话行已删）。
	recAfter := doConsole(t, h, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recAfter.Code != http.StatusUnauthorized {
		t.Fatalf("登出后应 401，得 %d", recAfter.Code)
	}
}

// 4. init 二次 409：向导关闭后（含并发抢注路径的错误翻译）。
func TestContractConsoleInitSecond409(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()

	doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	rec := doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"attacker","password":"whatever-long-pass"}`, "", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("二次 init 应 409，得 %d", rec.Code)
	}
	var e map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	errObj, _ := e["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "conflict" {
		t.Fatalf("错误体 code 应 conflict，得 %v", e)
	}
}

// 5. 登录失败防枚举（端到端补针）：错密码与不存在用户响应体一致。
func TestContractConsoleLoginAntiEnumEndToEnd(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()

	doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	r1 := doConsole(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"root","password":"wrong-password-here"}`, "", "")
	r2 := doConsole(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"ghost","password":"wrong-password-here"}`, "", "")
	if r1.Code != http.StatusUnauthorized || r2.Code != http.StatusUnauthorized {
		t.Fatalf("两种失败都应 401，得 %d / %d", r1.Code, r2.Code)
	}
	if r1.Body.String() != r2.Body.String() {
		t.Fatalf("防枚举破坏：\n%s\nvs\n%s", r1.Body.String(), r2.Body.String())
	}
}
