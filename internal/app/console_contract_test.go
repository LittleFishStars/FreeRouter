package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/store"
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

//  6. 审核批准端到端：注册出来的 pending 账户（仅 contributor 容器类）经管理面
//     「批准」后**真的进了组**、状态变 active、拿到 tenant 类、额度行按选择落库。
//
// 这是 2026-11-01 那个 500 的回归锁：handler 此前只发 status+groups+quota，
// 桥先转 active 再落组，落组时账户还没有 tenant 类 → AddMember 报
// 「无 tenant 类，不能入组」→ 事务回滚 → 500「管理面存储操作失败」，
// 审核队列里点「确认批准」永远失败。走真 SQLite + 真中间件链，
// 因为「闸在哪」本身就是被验证的对象（桥的字段顺序、store 的入组前置条件）。
func TestContractAdminApprovePendingAccountEndToEnd(t *testing.T) {
	a := newConsoleTestApp(t)
	h := a.Handler()
	ctx := context.Background()

	// 建首个 admin（向导）+ 登录，拿到管理面会话。
	if rec := doConsole(t, h, http.MethodPost, "/api/setup/init",
		`{"username":"root","password":"correct-horse-battery"}`, "", ""); rec.Code != http.StatusCreated {
		t.Fatalf("init 应 201，得 %d（%s）", rec.Code, rec.Body.String())
	}
	login := doConsole(t, h, http.MethodPost, "/api/auth/login",
		`{"username":"root","password":"correct-horse-battery"}`, "", "")
	if login.Code != http.StatusOK {
		t.Fatalf("登录应 200，得 %d（%s）", login.Code, login.Body.String())
	}
	cookie := "fr_session=" + login.Result().Cookies()[0].Value

	// 自助注册一个账户：落库形态是 pending + 仅 contributor 容器类、不进组。
	if rec := doConsole(t, h, http.MethodPost, "/api/register",
		`{"username":"newbie","password":"correct-horse-battery"}`, "", ""); rec.Code != http.StatusCreated {
		t.Fatalf("注册应 201，得 %d（%s）", rec.Code, rec.Body.String())
	}
	pend, err := a.store.Repos().Accounts.GetAccountByUsername(ctx, "newbie")
	if err != nil {
		t.Fatalf("读取注册账户失败：%v", err)
	}
	if pend.Status != store.AccountPending {
		t.Fatalf("注册后状态应 pending，得 %q", pend.Status)
	}
	// 回归锁的前置条件必须真的成立，否则这条测试测不到那个 500。
	var hasTenant bool
	for _, c := range pend.Classes {
		if c == store.ClassTenant {
			hasTenant = true
		}
	}
	if hasTenant {
		t.Fatalf("注册出来的 pending 账户不应带 tenant 类（回归前提），实得 %v", pend.Classes)
	}

	// 默认组 id（批准表单里的「加入组」）。
	defID, err := a.store.EnsureDefaultGroup(ctx, 0)
	if err != nil {
		t.Fatalf("取默认组失败：%v", err)
	}

	// 批准：组 + 额度（这里顺带用 -1 = 不限，一并锁住新定案）。
	rec := doConsole(t, h, http.MethodPost,
		"/api/admin/accounts/pending/"+pend.ID+"/approve",
		fmt.Sprintf(`{"groups":[%q],"quota":{"unit":"token","limit":-1,"period":"day"}}`, defID),
		cookie, "XMLHttpRequest")
	if rec.Code != http.StatusOK {
		t.Fatalf("批准应 200，实得 %d（%s）", rec.Code, rec.Body.String())
	}

	// ① 状态转 active；② 组真的写进 group_members（不是"接口说成功了"）。
	after, err := a.store.Repos().Accounts.GetAccount(ctx, pend.ID)
	if err != nil {
		t.Fatalf("批准后读账户失败：%v", err)
	}
	if after.Status != store.AccountActive {
		t.Fatalf("批准后状态应 active，得 %q", after.Status)
	}
	hasTenant = false
	for _, c := range after.Classes {
		if c == store.ClassTenant {
			hasTenant = true
		}
	}
	if !hasTenant {
		t.Fatalf("批准后应获得 tenant 类，实得 %v", after.Classes)
	}
	groups, err := a.store.Repos().Groups.ListAccountGroups(ctx, pend.ID)
	if err != nil {
		t.Fatalf("读账户所属组失败：%v", err)
	}
	if len(groups) != 1 || groups[0] != defID {
		t.Fatalf("批准后应恰好属于默认组 %s，实得 %v", defID, groups)
	}

	// ③ 额度 -1 = 不限 → 落库形态是**没有这一行**（额度域只有「有行且 >=0」
	// 与「无行」两种事实；存 -1 会让 used+cost<=limit 恒假、设了不限反而全拒）。
	qs, err := a.store.Repos().Quotas.AccountQuota(ctx, pend.ID, store.TotalScope,
		store.UnitToken, store.PeriodDay)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("quota=-1 应落成「无行」（不限，AccountQuota 报 ErrNotFound），实得 %v（行=%+v）", err, qs)
	}

	// ④ 账户本人此后能发 Key（tenant 类到位的最直接外部表现）。
	info, err := a.store.Repos().Accounts.AccountStatusOf(ctx, pend.ID)
	if err != nil {
		t.Fatalf("读账户状态失败：%v", err)
	}
	if info.Status != string(store.AccountActive) {
		t.Fatalf("AccountStatusOf 应报 active，得 %q", info.Status)
	}
	// 自助面（账户本人点「刷新状态」）看到的类集合必须含 tenant——否则
	// 本人登录后仍然发不出 Key，会以为批准没生效。
	if len(info.Classes) != 2 { // contributor（注册时的容器类，保留）+ tenant
		t.Fatalf("批准后的类集合应含 contributor 与 tenant，实得 %v", info.Classes)
	}
}
