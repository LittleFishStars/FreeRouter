// admin_ops.go —— 上游/凭据/鉴定/审计/设置/收件箱端点（02-admin §9–§12，task-20 M7）。
//
// 上游：
//   GET    /api/admin/upstreams              列表
//   POST   /api/admin/upstreams              新建（kind/base_url 组合校验 §9 表）
//   PATCH  /api/admin/upstreams/{id}         改名称、base_url、启用（kind 出现即 400）
//   POST   /api/admin/upstreams/{id}/kind    变更调用形态（§9 独立端点，界面 🔒）
//   POST   /api/admin/upstreams/{id}/test    连通性测试（能力 nil → 404 not_found）
// 凭据：
//   GET    /api/admin/credentials            列表（owner/provider/verify_status/status）
//   GET    /api/admin/credentials/{id}       详情（**无明文**——DTO 无该字段）
//   POST   /api/admin/credentials/{id}/suspend|ban|notify
// 鉴定：
//   GET    /api/admin/verifications          队列（verdict/provider/source）
//   GET    /api/admin/verifications/{id}     详情+证据
//   POST   /api/admin/verifications/{id}/override  人工改判（**必填理由**）
// 审计：
//   GET    /api/admin/audit                  查询（from/to/account_id/acting_class/action/result）
//   GET    /api/admin/audit/export           导出（?format=csv|jsonl，默认 jsonl）
//   GET    /api/admin/audit/{id}             单条
// 设置：
//   GET    /api/admin/settings               全量分组文档
//   PATCH  /api/admin/settings               批量改（未知键 400）
//   POST   /api/admin/settings/ip-check      源 IP 自检
// 收件箱：
//   GET    /api/admin/inbox                  列表（kind/severity/read/时间，severity 降序）
//   GET    /api/admin/inbox/count            未读数（导航角标）
//   GET    /api/admin/inbox/{id}             详情
//   PATCH  /api/admin/inbox/{id}             标已读/未读
//   POST   /api/admin/inbox/read             批量已读（ids）
//
// relay 的 base_url 判据（§9 + lead 2026-10-22 确认）：库里 upstreams.base_url
// 是 NOT NULL，relay 行的**定案值是 `sidecar://<provider>`**（task-12；
// internal/upstream/validate.go 的 ValidateTarget 与 internal/app 的
// sidecarScheme 同源解析）。所以"relay 不接受 base_url"的准确判据是
// **"传了非 sidecar:// 形态的端点地址"**——一刀切拒任何字符串会把
// bootstrap 自己落的 relay 行拒掉。空值放行（桥补定案值 sidecar://<name>）。
// **报错而不是忽略**（§9：忽略会让管理员以为配的地址生效了）。

package httpapi

import (
	"encoding/json"
	"github.com/LittleFishStars/FreeRouter/internal/idgen"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// relayBaseURLPrefix 与 upstream.RelayBaseURLPrefix、app.sidecarScheme 同值。
// 不复用其常量：internal/upstream 与 internal/app 都不是 httpapi 的依赖方向
// （upstream 反过来实现 httpapi 的消费面）；三处同串有 admin_test 的
// 一致性断言锁住。
const relayBaseURLPrefix = "sidecar://"

func (s *Server) mountAdminOps(r chi.Router, deps *AdminDeps) {
	// 上游
	r.Get("/upstreams", s.handleAdminUpstreamsList(deps))
	r.Post("/upstreams", s.handleAdminUpstreamsCreate(deps))
	r.Get("/upstreams/{id}", s.handleAdminUpstreamDetail(deps))
	r.Patch("/upstreams/{id}", s.handleAdminUpstreamPatch(deps))
	r.Post("/upstreams/{id}/kind", s.handleAdminUpstreamKind(deps))
	r.Post("/upstreams/{id}/test", s.handleAdminUpstreamTest(deps))

	// 凭据
	r.Get("/credentials", s.handleAdminCredentialsList(deps))
	r.Get("/credentials/{id}", s.handleAdminCredentialDetail(deps))
	r.Post("/credentials/{id}/suspend", s.handleAdminCredentialSuspend(deps))
	r.Post("/credentials/{id}/ban", s.handleAdminCredentialBan(deps))
	r.Post("/credentials/{id}/notify", s.handleAdminCredentialNotify(deps))
	r.Post("/credentials/{id}/balance-refresh", s.handleAdminCredentialBalanceRefresh(deps))
	r.Post("/credentials/{id}/checkin", s.handleAdminCredentialCheckin(deps))

	// 签到：手动整轮 / 状态 / 开关（调度器本体在 app 包，这里只是触发器）
	r.Post("/checkin/run", s.handleAdminCheckinRun(deps))
	r.Get("/checkin/status", s.handleAdminCheckinStatus(deps))
	r.Put("/checkin/enabled", s.handleAdminCheckinEnabled(deps))
	// 鉴定
	r.Get("/verifications", s.handleAdminVerificationsList(deps))
	r.Get("/verifications/{id}", s.handleAdminVerificationDetail(deps))
	r.Post("/verifications/{id}/override", s.handleAdminVerificationOverride(deps))
	r.Post("/verifications/{id}/l2", s.handleAdminVerificationL2(deps))
	r.Post("/verifications/{id}/recheck", s.handleAdminVerificationRecheck(deps))

	// 审计：静态 /export 先于 /{id}（chi 字面量优先，注册顺序兜可读性）
	r.Get("/audit", s.handleAdminAuditList(deps))
	r.Get("/audit/export", s.handleAdminAuditExport(deps))
	r.Get("/audit/{id}", s.handleAdminAuditDetail(deps))

	// 设置
	r.Get("/settings", s.handleAdminSettingsGet(deps))
	r.Patch("/settings", s.handleAdminSettingsPatch(deps))
	r.Post("/settings/ip-check", s.handleAdminSettingsIPCheck(deps))

	// 收件箱：/count 同理先于 /{id}
	r.Get("/inbox", s.handleAdminInboxList(deps))
	r.Get("/inbox/count", s.handleAdminInboxCount(deps))
	r.Post("/inbox/read", s.handleAdminInboxReadBatch(deps))
	r.Get("/inbox/{id}", s.handleAdminInboxDetail(deps))
	r.Patch("/inbox/{id}", s.handleAdminInboxPatch(deps))
}

func adminUpstreamID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "上游")
}
func adminCredentialID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "凭据")
}
func adminVerificationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "鉴定条目")
}
func adminAuditID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "审计条目")
}
func adminInboxID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return idParamValid(w, r, "id", "收件箱条目")
}

// ---------------------------------------------------------------------------
// 上游
// ---------------------------------------------------------------------------

func (s *Server) handleAdminUpstreamsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游存储", deps.Upstreams != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		rows, total, err := deps.Upstreams.ListUpstreams(r.Context(), limit, offset)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		out := make([]AdminUpstreamOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminUpstream(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func (s *Server) handleAdminUpstreamDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游存储", deps.Upstreams != nil) {
			return
		}
		id, ok := adminUpstreamID(w, r)
		if !ok {
			return
		}
		row, err := deps.Upstreams.GetUpstream(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminUpstream(row)})
	}
}

// adminKindValid 是 kind 闭集合（§9 表 + 0001 CHECK 枚举）。
func adminKindValid(k string) bool { return k == "relay" || k == "vendor" || k == "selfhosted" }

// validateKindBaseURL 执行 §9 的三类字段要求。返回 (给桥的 base_url, 错误消息)。
//
//	relay：端点形态的地址一律拒（非 sidecar:// 前缀）；空串放行——列
//	      NOT NULL，桥落定案值 sidecar://<name>（task-12 定案）。
//	vendor / selfhosted：必填非空端点地址。协议形状校验不在这里重复
//	      （internal/upstream/validate.go 的 ValidateTarget 是唯一权威，
//	      装配层保存/调用时执行）——admin 侧只拦"形态类错误"，两处判据
//	      同源不同层，不写第三份 URL 校验。
func validateKindBaseURL(kind, baseURL string) (string, string) {
	switch kind {
	case "relay":
		if baseURL != "" && !strings.HasPrefix(baseURL, relayBaseURLPrefix) {
			return "", "relay 类上游不接受端点地址：调用路径是 Node sidecar → providers-hub adapter，base_url 只允许定案形态 " + relayBaseURLPrefix + "<provider>（留空由系统填写）——报错而非忽略，是为了不让管理员误以为地址生效了"
		}
		return baseURL, ""
	case "vendor", "selfhosted":
		if strings.TrimSpace(baseURL) == "" {
			return "", "vendor/selfhosted 类上游必须提供 base_url（官方或自建端点地址）"
		}
		if strings.HasPrefix(baseURL, relayBaseURLPrefix) {
			return "", relayBaseURLPrefix + " 形态只属于 relay 类上游的定案值"
		}
		return baseURL, ""
	}
	return "", "kind 取值须为 relay/vendor/selfhosted"
}

func (s *Server) handleAdminUpstreamsCreate(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游存储", deps.Upstreams != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			Kind    string `json:"kind"`
			Name    string `json:"name"`
			BaseURL string `json:"base_url,omitempty"`
			Enabled *bool  `json:"enabled,omitempty"`
			// APIKey 是可选的一次性写入：创建后立即加密入库，**任何读接口
			// 都不返回**（05-keys §3 只回一次的口径同样适用于上游密钥）。
			// relay 类上游不需要（凭据在 DSH），vendor/selfhosted 通常需要。
			APIKey string `json:"api_key,omitempty"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if !adminKindValid(body.Kind) {
			adminWriteParamErr(w, "kind", "kind 取值须为 relay/vendor/selfhosted")
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			adminWriteParamErr(w, "name", "上游名不能为空")
			return
		}
		baseURL, verr := validateKindBaseURL(body.Kind, body.BaseURL)
		if verr != "" {
			adminWriteParamErr(w, "base_url", verr)
			return
		}
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		u := AdminUpstream{Kind: body.Kind, Name: body.Name, BaseURL: baseURL, Enabled: enabled}
		audit := adminBaseAudit(p, r, "upstream.create", "upstream:"+body.Name, "ok")
		id, auditID, err := deps.Upstreams.CreateUpstream(r.Context(), u, audit)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		u.ID = id
		// 密钥材料：创建后立即写（加密在 store 层，桥只转交明文一次）。
		// 失败不回滚上游行：先有上游再有密钥是合法中间态（运营补密钥即可），
		// 但必须在响应里明说——否则前端会以为密钥已生效。
		secretWritten := false
		if strings.TrimSpace(body.APIKey) != "" {
			if _, serr := deps.Upstreams.SetUpstreamSecret(r.Context(), id, body.APIKey,
				adminBaseAudit(p, r, "upstream.secret_set", "upstream:"+body.Name, "ok")); serr != nil {
				mapAdminError(w, serr, "上游密钥")
				return
			}
			secretWritten = true
		}
		resp := renderAdminUpstream(&u)
		// renderAdminUpstream 返回结构体（响应形状是编译期固定的契约）；
		// 密钥是否写入通过 SecretPresent 字段表达（与详情接口同源）。
		resp.SecretPresent = secretWritten
		adminWriteOK(w, http.StatusCreated, resp, auditID)
	}
}

// handleAdminUpstreamPatch PATCH /upstreams/{id}。
//
// kind 出现即 400：kind 变更走独立端点（§9"改调用方式"与"改名字"不是
// 同类操作，PATCH 一条 SQL 换调用路径是事故温床）。
// relay 行改 base_url 仍走同一判据（非 sidecar:// 拒）。
func (s *Server) handleAdminUpstreamPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游存储", deps.Upstreams != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminUpstreamID(w, r)
		if !ok {
			return
		}
		// 一次解码同时拿到「出现了哪些字段」与「解成结构体的值」。
		//
		// 为什么不能像早期实现那样调两次 adminDecodeBoth：那个函数每次都
		// io.ReadAll(r.Body)，第一次读完 body 就空了，第二次必然报
		// 「请求体不能为空」——PATCH 上游永远 400（实测踩到）。
		var in AdminUpstreamUpdate
		fieldsMap, ok := adminDecodeBoth(w, r, &in)
		if !ok {
			return
		}
		if _, has := fieldsMap["kind"]; has {
			adminWriteParamErr(w, "kind", "kind 变更不走 PATCH——请使用 POST /upstreams/{id}/kind（它改变调用路径，是独立的高危动作）")
			return
		}
		cur, err := deps.Upstreams.GetUpstream(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		if in.BaseURL != nil {
			if _, verr := validateKindBaseURL(cur.Kind, *in.BaseURL); verr != "" {
				adminWriteParamErr(w, "base_url", verr)
				return
			}
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
			adminWriteParamErr(w, "name", "上游名不能为空串")
			return
		}
		if len(fieldsMap) == 0 {
			adminWriteParamErr(w, "body", "PATCH 至少要改一个字段")
			return
		}
		audit := adminBaseAudit(p, r, "upstream.update", "upstream:"+id, "ok")
		row, auditID, err := deps.Upstreams.UpdateUpstream(r.Context(), id, in, audit)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminUpstream(row), auditID)
	}
}

// handleAdminUpstreamKind POST /upstreams/{id}/kind——改调用形态。
//
// §9：单独端点 + 界面 🔒。relay↔端点类的切换语义由桥保证（改 relay 补
// 定案值；改出 relay 且现值是定案值时报错要求先补端点——admin 层已
// 用 ErrAdminConflict 通道回 409）。
func (s *Server) handleAdminUpstreamKind(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游存储", deps.Upstreams != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminUpstreamID(w, r)
		if !ok {
			return
		}
		var body struct {
			Kind string `json:"kind"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if !adminKindValid(body.Kind) {
			adminWriteParamErr(w, "kind", "kind 取值须为 relay/vendor/selfhosted")
			return
		}
		audit := adminBaseAudit(p, r, "upstream.kind_change", "upstream:"+id, "ok")
		row, auditID, err := deps.Upstreams.SetUpstreamKind(r.Context(), id, body.Kind, audit)
		if err != nil {
			mapAdminError(w, err, "上游")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminUpstream(row), auditID)
	}
}

func (s *Server) handleAdminUpstreamTest(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "上游测试器", deps.UpstreamTest != nil) {
			return
		}
		id, ok := adminUpstreamID(w, r)
		if !ok {
			return
		}
		res, err := deps.UpstreamTest.TestUpstream(r.Context(), id)
		if err != nil {
			// 测试失败是"探到的事实"不是请求错误：502 不合适，用
			// 200 + 结果体里的 ok:false 形状更可用（装配方在 res 里给
			// {ok, latency_ms, error}；传输级失败也照此回显）。
			adminWriteOK(w, http.StatusOK, map[string]any{
				"upstream_id": id, "ok": false, "error": err.Error(),
			}, "")
			return
		}
		adminWriteOK(w, http.StatusOK, res, "")
	}
}

// ---------------------------------------------------------------------------
// 凭据
// ---------------------------------------------------------------------------

func (s *Server) handleAdminCredentialsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "凭据存储", deps.Credentials != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := AdminCredentialFilter{
			Provider: q.Get("provider"), VerifyStatus: q.Get("verify_status"),
			Status: q.Get("status"), Limit: limit, Offset: offset,
		}
		if o := q.Get("owner"); o != "" {
			if _, err := idgen.Parse(o); err != nil {
				adminWriteParamErr(w, "owner", "owner 须为 UUID")
				return
			}
			f.OwnerAccountID = o
		}
		if f.VerifyStatus != "" && !adminVerifyStatusValid(f.VerifyStatus) {
			adminWriteParamErr(w, "verify_status", "verify_status 须为 verified/suspect/unverified/trusted")
			return
		}
		if f.Status != "" && !adminCredStatusValid(f.Status) {
			adminWriteParamErr(w, "status", "status 须为 pending/active/invalid/expired/disabled")
			return
		}
		rows, total, err := deps.Credentials.ListCredentials(r.Context(), f)
		if err != nil {
			mapAdminError(w, err, "凭据")
			return
		}
		out := make([]AdminCredentialOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminCredential(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func adminVerifyStatusValid(v string) bool {
	switch v {
	case "verified", "suspect", "unverified", "trusted":
		return true
	}
	return false
}

func adminCredStatusValid(v string) bool {
	switch v {
	case "pending", "active", "invalid", "expired", "disabled":
		return true
	}
	return false
}

func (s *Server) handleAdminCredentialDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "凭据存储", deps.Credentials != nil) {
			return
		}
		id, ok := adminCredentialID(w, r)
		if !ok {
			return
		}
		row, err := deps.Credentials.GetCredential(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "凭据")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminCredential(row)})
	}
}

// handleAdminCredentialSuspend / Ban——都落 status=disabled，差别在审计
// action（credential.suspend / credential.ban）。值域映射理由见
// AdminCredentialStore 注释（CHECK 枚举无 banned）。
func (s *Server) handleAdminCredentialSuspend(deps *AdminDeps) http.HandlerFunc {
	return s.handleAdminCredentialStatus(deps, "disabled", "credential.suspend")
}

func (s *Server) handleAdminCredentialBan(deps *AdminDeps) http.HandlerFunc {
	return s.handleAdminCredentialStatus(deps, "disabled", "credential.ban")
}

func (s *Server) handleAdminCredentialStatus(deps *AdminDeps, status, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "凭据存储", deps.Credentials != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminCredentialID(w, r)
		if !ok {
			return
		}
		audit := adminBaseAudit(p, r, action, "credential:"+id, "ok")
		row, auditID, err := deps.Credentials.SetCredentialStatus(r.Context(), id, status, audit)
		if err != nil {
			mapAdminError(w, err, "凭据")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminCredential(row), auditID)
	}
}

// handleAdminCredentialBalanceRefresh POST /api/admin/credentials/{id}/balance-refresh。
//
// 解密凭据 → sidecar 查上游余额 → 落库并返回展示串。上游拒绝/网络失败
// **不是 5xx**（可重试的运行时条件）：200 + ok=false + reason，前端显示原因。
func (s *Server) handleAdminCredentialBalanceRefresh(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "余额刷新", deps.BalanceRefresh != nil) {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		res, err := deps.BalanceRefresh.RefreshCredentialBalance(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "余额刷新")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// handleAdminCredentialCheckin POST /api/admin/credentials/{id}/checkin。
//
// 手动单签：显式操作永远放行（不受开关与到期判定约束，hub 同款语义）。
// 上游拒绝/网络失败不是 5xx：200 + ok=false + kind/message（与余额刷新同口径）。
func (s *Server) handleAdminCredentialCheckin(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "签到", deps.CheckinRun != nil) {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		res, err := deps.CheckinRun.RunCredentialCheckin(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "签到")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// handleAdminCheckinRun POST /api/admin/checkin/run —— 手动跑一轮（只签当前到期的）。
func (s *Server) handleAdminCheckinRun(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "签到", deps.CheckinScheduler != nil) {
			return
		}
		res, err := deps.CheckinScheduler.RunCheckinRound(r.Context())
		if err != nil {
			mapAdminError(w, err, "签到")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// handleAdminCheckinStatus GET /api/admin/checkin/status —— 自动签到状态视图。
func (s *Server) handleAdminCheckinStatus(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "签到", deps.CheckinScheduler != nil) {
			return
		}
		st, err := deps.CheckinScheduler.CheckinState(r.Context())
		if err != nil {
			mapAdminError(w, err, "签到状态")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": st})
	}
}

// handleAdminCheckinEnabled PUT /api/admin/checkin/enabled —— 自动签到开关。
func (s *Server) handleAdminCheckinEnabled(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "签到", deps.CheckinScheduler != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		audit := adminBaseAudit(p, r, "checkin.enabled", "checkin:auto", "ok")
		if err := deps.CheckinScheduler.SetCheckinEnabled(r.Context(), body.Enabled, audit); err != nil {
			mapAdminError(w, err, "签到开关")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"enabled": body.Enabled}})
	}
}

func (s *Server) handleAdminCredentialNotify(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "凭据存储", deps.Credentials != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminCredentialID(w, r)
		if !ok {
			return
		}
		var body struct {
			Message string `json:"message,omitempty"`
		}
		if r.ContentLength > 0 {
			if _, ok := adminDecodeBoth(w, r, &body); !ok {
				return
			}
		}
		audit := adminBaseAudit(p, r, "credential.notify", "credential:"+id, "ok")
		auditID, err := deps.Credentials.NotifyCredential(r.Context(), id, body.Message, audit)
		if err != nil {
			mapAdminError(w, err, "凭据")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"credential_id": id, "notified": true}, auditID)
	}
}

// ---------------------------------------------------------------------------
// 鉴定
// ---------------------------------------------------------------------------

func (s *Server) handleAdminVerificationsList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "鉴定存储", deps.Verifications != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := AdminVerificationFilter{Verdict: q.Get("verdict"), Provider: q.Get("provider"),
			Source: q.Get("source"), Limit: limit, Offset: offset}
		if f.Verdict != "" && !adminVerifyStatusValid(f.Verdict) {
			adminWriteParamErr(w, "verdict", "verdict 须为 verified/suspect/unverified/trusted")
			return
		}
		rows, total, err := deps.Verifications.ListVerifications(r.Context(), f)
		if err != nil {
			mapAdminError(w, err, "鉴定条目")
			return
		}
		if rows == nil {
			rows = []AdminVerification{}
		}
		adminListEnvelope(w, rows, total, limit, offset)
	}
}

func (s *Server) handleAdminVerificationDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "鉴定存储", deps.Verifications != nil) {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		row, err := deps.Verifications.GetVerification(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "鉴定条目")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": row})
	}
}

// handleAdminVerificationOverride POST /api/admin/verifications/{id}/override。
//
// **理由必填**（§10 表"override 必填理由"）：人工改判是绕过机器判定的
// 唯一通道，无理由的改判在追溯时等于"某管理员动了手脚但没人知道为什么"。
// 缺理由 400 先于任何存储写。
func (s *Server) handleAdminVerificationOverride(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "鉴定存储", deps.Verifications != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		var body struct {
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if !adminVerifyStatusValid(body.Verdict) {
			adminWriteParamErr(w, "verdict", "verdict 须为 verified/suspect/unverified/trusted")
			return
		}
		if strings.TrimSpace(body.Reason) == "" {
			adminWriteParamErr(w, "reason", "人工改判必须填写理由")
			return
		}
		audit := adminBaseAudit(p, r, "verification.override", "verification:"+id, "ok")
		row, auditID, err := deps.Verifications.OverrideVerification(r.Context(), id, body.Verdict, body.Reason, p.AccountID, audit)
		if err != nil {
			mapAdminError(w, err, "鉴定条目")
			return
		}
		adminWriteOK(w, http.StatusOK, row, auditID)
	}
}

// handleAdminVerificationL2 POST /api/admin/verifications/{id}/l2。
//
// 触发 L2 深度鉴定。能力未装配（无运行时探针）→ 桥返回 ErrAdminNotWired，
// mapAdminError 把它映射成 404——管理员看到的不是「已鉴定」，而是「这项能力
// 还没接线」。这是刻意的：没有真实基准的伪 L2 会骗过它要服务的判定器。
func (s *Server) handleAdminVerificationL2(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "鉴定存储", deps.Verifications != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		audit := adminBaseAudit(p, r, "verification.l2", "verification:"+id, "ok")
		row, auditID, err := deps.Verifications.TriggerL2(r.Context(), id, audit)
		if err != nil {
			mapAdminError(w, err, "鉴定条目")
			return
		}
		adminWriteOK(w, http.StatusOK, row, auditID)
	}
}

// handleAdminVerificationRecheck POST /api/admin/verifications/{id}/recheck。
//
// 加入 / 移出定时复检队列（body.recheck 布尔）。
func (s *Server) handleAdminVerificationRecheck(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "鉴定存储", deps.Verifications != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminVerificationID(w, r)
		if !ok {
			return
		}
		var body struct {
			Recheck bool `json:"recheck"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		audit := adminBaseAudit(p, r, "verification.recheck", "verification:"+id, "ok")
		if b, err := json.Marshal(map[string]any{"recheck": body.Recheck}); err == nil {
			audit.Detail = b
		}
		row, auditID, err := deps.Verifications.ToggleRecheck(r.Context(), id, body.Recheck, audit)
		if err != nil {
			mapAdminError(w, err, "鉴定条目")
			return
		}
		adminWriteOK(w, http.StatusOK, row, auditID)
	}
}

// ---------------------------------------------------------------------------
// 审计
// 审计
// ---------------------------------------------------------------------------

// auditFilterFromQuery 解析审计查询参数（列表与导出共用一份解析，
// 两处各解析一遍迟早漂移）。
func auditFilterFromQuery(w http.ResponseWriter, r *http.Request) (AdminAuditFilter, bool) {
	f := AdminAuditFilter{}
	limit, offset, ok := adminCursorAndLimit(w, r)
	if !ok {
		return f, false
	}
	f.Limit, f.Offset = limit, offset
	q := r.URL.Query()
	var err error
	if f.FromTS, err = adminParseTime("from", q.Get("from")); err != nil {
		adminWriteParamErr(w, "from", err.Error())
		return f, false
	}
	if f.ToTS, err = adminParseTime("to", q.Get("to")); err != nil {
		adminWriteParamErr(w, "to", err.Error())
		return f, false
	}
	if a := q.Get("account_id"); a != "" {
		if _, derr := idgen.Parse(a); derr != nil {
			adminWriteParamErr(w, "account_id", "account_id 须为 UUID")
			return f, false
		}
		f.AccountID = a
	}
	f.ActingClass = q.Get("acting_class")
	if f.ActingClass != "" && !adminClassValid(f.ActingClass) {
		adminWriteParamErr(w, "acting_class", "acting_class 须为 admin/tenant/contributor")
		return f, false
	}
	f.Action = q.Get("action")
	f.Result = q.Get("result")
	if f.Result != "" {
		switch f.Result {
		case "ok", "denied", "error":
		default:
			adminWriteParamErr(w, "result", "result 须为 ok/denied/error")
			return f, false
		}
	}
	return f, true
}

func (s *Server) handleAdminAuditList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "审计存储", deps.Audit != nil) {
			return
		}
		f, ok := auditFilterFromQuery(w, r)
		if !ok {
			return
		}
		rows, total, err := deps.Audit.List(r.Context(), f)
		if err != nil {
			mapAdminError(w, err, "审计")
			return
		}
		out := make([]AdminAuditOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminAudit(&rows[i]))
		}
		adminListEnvelope(w, out, total, f.Limit, f.Offset)
	}
}

func (s *Server) handleAdminAuditDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "审计存储", deps.Audit != nil) {
			return
		}
		id, ok := adminAuditID(w, r)
		if !ok {
			return
		}
		row, err := deps.Audit.Get(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "审计条目")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminAudit(row)})
	}
}

// handleAdminAuditExport GET /api/admin/audit/export?format=csv|jsonl。
//
// §13 把格式列为待定——本轮两种都出、默认 jsonl（文档定稿后收敛，
// 已列入回报）。导出**只读不剪**：没有 DELETE /audit（append-only，§11）。
func (s *Server) handleAdminAuditExport(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "审计存储", deps.Audit != nil) {
			return
		}
		f, ok := auditFilterFromQuery(w, r)
		if !ok {
			return
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "jsonl"
		}
		if format != "csv" && format != "jsonl" {
			adminWriteParamErr(w, "format", "format 须为 csv 或 jsonl")
			return
		}
		rows, filename, err := deps.Audit.Export(r.Context(), f, format)
		if err != nil {
			mapAdminError(w, err, "审计导出")
			return
		}
		if filename == "" {
			filename = "audit." + format
		}
		ct := "application/x-ndjson"
		if format == "csv" {
			ct = "text/csv"
		}
		w.Header().Set("Content-Type", ct+"; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rows)
	}
}

// ---------------------------------------------------------------------------
// 设置
// ---------------------------------------------------------------------------

func (s *Server) handleAdminSettingsGet(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "设置读取器", deps.SettingsView != nil) {
			return
		}
		doc, err := deps.SettingsView.GetSettings(r.Context())
		if err != nil {
			mapAdminError(w, err, "设置")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": doc})
	}
}

// handleAdminSettingsPatch PATCH /api/admin/settings。
//
// 载荷是**扁平键值** {"<键>": <值>}。未知键 400：认识的键集合取
// GET 文档里各分组下的键并集——不在这里硬编码第二份键表（那是与
// config 域必然漂移的一处）。危险设置（registration_enabled /
// global_budget_cap / trusted_proxies / record_request_body /
// login_backoff / lockout_threshold）的 🔒 是界面职责，API 不加标记
// （§2.3）；admin_ip_allowlist 不提供（2026-10-19 定案）——它若出现
// 就是未知键，自然被 400 拒掉。
func (s *Server) handleAdminSettingsPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "设置写入器", deps.SettingsPatch != nil) ||
			!deps.need(w, "设置读取器", deps.SettingsView != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var patch map[string]json.RawMessage
		if _, ok := adminDecodeBoth(w, r, &patch); !ok {
			return
		}
		if len(patch) == 0 {
			adminWriteParamErr(w, "body", "PATCH 至少要改一个设置键")
			return
		}
		doc, err := deps.SettingsView.GetSettings(r.Context())
		if err != nil {
			mapAdminError(w, err, "设置")
			return
		}
		known := adminSettingKeys(doc)
		for k := range patch {
			if !known[k] {
				adminWriteParamErr(w, k, "未知设置键 "+k)
				return
			}
		}
		audit := adminBaseAudit(p, r, "system.settings_update", "settings", "ok")
		auditID, err := deps.SettingsPatch.PatchSettings(r.Context(), patch, audit)
		if err != nil {
			mapAdminError(w, err, "设置")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"patched": len(patch)}, auditID)
	}
}

// adminSettingKeys 把分组设置文档摊平成键集合（{"组":{"键":…}}）。
func adminSettingKeys(doc []byte) map[string]bool {
	out := map[string]bool{}
	var groups map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc, &groups); err != nil {
		return out
	}
	for _, g := range groups {
		for k := range g {
			out[k] = true
		}
	}
	return out
}

func (s *Server) handleAdminSettingsIPCheck(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "IP 自检器", deps.IPCheck != nil) {
			return
		}
		ip := ClientIPFromContext(r.Context())
		res, err := deps.IPCheck.CheckIP(r.Context(), ip)
		if err != nil {
			mapAdminError(w, err, "IP 自检")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": res})
	}
}

// ---------------------------------------------------------------------------
// 收件箱
// ---------------------------------------------------------------------------

func (s *Server) handleAdminInboxList(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "收件箱存储", deps.Inbox != nil) {
			return
		}
		limit, offset, ok := adminCursorAndLimit(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := AdminInboxFilter{Kind: q.Get("kind"), Severity: q.Get("severity"),
			Limit: limit, Offset: offset}
		// kind/severity 值域**不在这里校验**：词表常量由 storage 导出
		// （lead 裁决②），httpapi 重定义一份就是第二套词表。非法值
		// 表现为筛选命中空集，桥若要拒也走 mapAdminError 通道。
		if sv := f.Severity; sv != "" {
			switch sv {
			case "info", "warning", "error":
			default:
				adminWriteParamErr(w, "severity", "severity 须为 info/warning/error（06-audit §3.1 统一词表）")
				return
			}
		}
		if rv := q.Get("read"); rv != "" {
			b := rv == "true" || rv == "1"
			f.Read = &b
		}
		var err error
		if f.FromTS, err = adminParseTime("from", q.Get("from")); err != nil {
			adminWriteParamErr(w, "from", err.Error())
			return
		}
		if f.ToTS, err = adminParseTime("to", q.Get("to")); err != nil {
			adminWriteParamErr(w, "to", err.Error())
			return
		}
		rows, total, err := deps.Inbox.List(r.Context(), f)
		if err != nil {
			mapAdminError(w, err, "收件箱")
			return
		}
		out := make([]AdminInboxOut, 0, len(rows))
		for i := range rows {
			out = append(out, renderAdminInbox(&rows[i]))
		}
		adminListEnvelope(w, out, total, limit, offset)
	}
}

func (s *Server) handleAdminInboxCount(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "收件箱存储", deps.Inbox != nil) {
			return
		}
		n, err := deps.Inbox.CountUnread(r.Context())
		if err != nil {
			mapAdminError(w, err, "收件箱")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"unread": n}})
	}
}

func (s *Server) handleAdminInboxDetail(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "收件箱存储", deps.Inbox != nil) {
			return
		}
		id, ok := adminInboxID(w, r)
		if !ok {
			return
		}
		row, err := deps.Inbox.Get(r.Context(), id)
		if err != nil {
			mapAdminError(w, err, "收件箱条目")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": renderAdminInbox(row)})
	}
}

// handleAdminInboxPatch PATCH /api/admin/inbox/{id} {"read": true|false}。
// 用 PATCH 因为它是幂等字段更新（§12"为什么标记已读用 PATCH"）。
func (s *Server) handleAdminInboxPatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "收件箱存储", deps.Inbox != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		id, ok := adminInboxID(w, r)
		if !ok {
			return
		}
		var body struct {
			Read *bool `json:"read"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if body.Read == nil {
			adminWriteParamErr(w, "read", "PATCH 必须携带 read 布尔")
			return
		}
		action := "inbox_read"
		if !*body.Read {
			action = "inbox_unread"
		}
		audit := adminBaseAudit(p, r, action, "inbox:"+id, "ok")
		row, auditID, err := deps.Inbox.SetRead(r.Context(), id, *body.Read, p.AccountID, audit)
		if err != nil {
			mapAdminError(w, err, "收件箱条目")
			return
		}
		adminWriteOK(w, http.StatusOK, renderAdminInbox(row), auditID)
	}
}

// handleAdminInboxReadBatch POST /api/admin/inbox/read {"ids":[…]}。
// 契约允许"或带筛选条件"，本轮只实现 ids 集合（筛选式批量涉及
// "批量审计覆盖哪些行"的粒度定案，列入回报待 lead 定）。
func (s *Server) handleAdminInboxReadBatch(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "收件箱存储", deps.Inbox != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		if len(body.IDs) == 0 {
			adminWriteParamErr(w, "ids", "ids 至少一条（批量已读不接受空集合）")
			return
		}
		for _, one := range body.IDs {
			if _, err := idgen.Parse(one); err != nil {
				adminWriteParamErr(w, "ids", "收件箱 id 形状非法："+one)
				return
			}
		}
		audit := adminBaseAudit(p, r, "inbox_read", "inbox:batch", "ok")
		n, auditID, err := deps.Inbox.MarkReadBatch(r.Context(), body.IDs, p.AccountID, audit)
		if err != nil {
			mapAdminError(w, err, "收件箱")
			return
		}
		adminWriteOK(w, http.StatusOK, map[string]any{"marked_read": n}, auditID)
	}
}
