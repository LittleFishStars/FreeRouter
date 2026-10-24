package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// 本文件守「登录响应与会话响应都带账户状态 status」（审计 2.5 的 E 项）。
//
// 为什么值得一条测试：前端 types.ts 的 Session.status 驱动 /pending 让路与
// 「账户被停用」提示。后端两份响应曾经都没有这个字段（类型说了、数据没有），
// 界面就永远拿不到状态——这类缺口不会被任何一侧的编译检查发现。
//
// 现状（2026-10-24 核对）：handleAuthLogin 与 handleAuthSession 均已输出该字段，
// 值取自 Principal.Status（Validate 时从账户行实时读入，不新增查询）。这条测试
// 把它钉成契约，防止后续重构把字段去掉而无人发现。
func TestContractSessionResponsesCarryAccountStatus(t *testing.T) {
	h := newM3Harness(t)
	h.store.putAccount("acc-1", "tenant", auth.StatusActive, auth.ClassTenant)
	h.seedPassword(t, "tenant", m3Password)

	// —— 登录响应 ——
	rec := h.loginOK(t, "tenant", m3Password)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应 200，得 %d：%s", rec.Code, rec.Body.String())
	}
	var login map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatalf("登录响应不是合法 JSON：%v", err)
	}
	if login["status"] != string(auth.StatusActive) {
		t.Fatalf("登录响应的 status = %v，期望 %q", login["status"], auth.StatusActive)
	}
	if login["account_id"] != "acc-1" {
		t.Fatalf("登录响应的 account_id = %v，期望 acc-1", login["account_id"])
	}

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录响应没有下发会话 Cookie")
	}
	cookie := SessionCookieName + "=" + cookies[0].Value

	// —— 会话恢复响应（与登录同形，前端恢复态走这条）——
	recS := h.do(t, http.MethodGet, "/api/auth/session", "", cookie, "")
	if recS.Code != http.StatusOK {
		t.Fatalf("会话应 200，得 %d：%s", recS.Code, recS.Body.String())
	}
	var sess map[string]any
	if err := json.Unmarshal(recS.Body.Bytes(), &sess); err != nil {
		t.Fatalf("会话响应不是合法 JSON：%v", err)
	}
	if sess["status"] != string(auth.StatusActive) {
		t.Fatalf("会话响应的 status = %v，期望 %q", sess["status"], auth.StatusActive)
	}
	// 状态字段与 must_change_password 必须是两个独立字段：前端用前者决定「能不能用」，
	// 后者决定「用之前要不要先改密」，混成一个就无法表达「激活但需改密」。
	if _, ok := sess["must_change_password"]; !ok {
		t.Fatalf("会话响应缺少 must_change_password：%v", sess)
	}
}
