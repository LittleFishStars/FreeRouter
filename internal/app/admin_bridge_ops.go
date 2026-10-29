// admin_bridge_ops.go —— 上游/凭据/鉴定/审计/收件箱五组桥（task-23）。
//
// 桥要点：
//   - relay base_url 空值补定案值 sidecar://<name>（task-20 桥清单 ②）；
//   - Inbox：AdminInboxFilter → store.InboxFilter 映射 + related_id 原样透传；
//     SetRead/MarkReadBatch 与 inbox_read 审计**同事务**（06-audit §3.1）；
//   - Audit Export：csv|jsonl 两种（csv 按 RFC4180 精神转义引号/逗号，
//     jsonl 逐行 JSON）——§13 待定项先两者都给；
//   - 凭据 DTO 无任何明文/引用字段（§9 红线，类型层已消灭，桥照搬）。
//
// store 缺口（回报 Lead 转达 storage）：
//
//	① verifications 面按「鉴定条目」建模，库里鉴定状态在凭据行上——
//	  桥按「凭据即鉴定条目」映射（ListVerifications = ListCredentials 的
//	  verify 投影；GetVerification/Override 走 LookupCredential/MarkVerified）；
//	  §10.1 的 stage/stage_results/recheck 列不存在，字段留空（已回报）。
//	② 上游 SecretPresent：upstream_secrets 有行即 true（getUpstreamByID
//	  的 secret_id 非空近似——不读密文本身）。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// relayPrefix 与 httpapi 侧的 relayBaseURLPrefix、upstream.RelayBaseURLPrefix
// 同值（三处同串靠 admin_test 与 bridge 测试共同锁住）。
const relayPrefix = "sidecar://"

// ---------------------------------------------------------------------------
// 上游
// ---------------------------------------------------------------------------

type upstreamsBridge struct{ b *adminBridge }

func upstreamToAdmin(u *store.Upstream) *httpapi.AdminUpstream {
	out := &httpapi.AdminUpstream{
		ID:         u.ID,
		Kind:       u.Kind,
		Name:       u.Name,
		BaseURL:    u.BaseURL,
		Enabled:    u.Enabled,
		LastSeenAt: u.LastSeenAt,
		// SecretPresent：secret_id 非空近似（有密文行的外键）——绝不读密文。
		SecretPresent: u.SecretID != nil,
	}
	if u.OwnerAccountID != nil {
		s := *u.OwnerAccountID
		out.OwnerAccountID = &s
	}
	return out
}

func (ub *upstreamsBridge) ListUpstreams(ctx context.Context, limit, offset int) ([]httpapi.AdminUpstream, int64, error) {
	rows, err := ub.b.repos.Models.ListUpstreams(ctx)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	total := int64(len(rows))
	lo, hi := pageBounds(len(rows), limit, offset)
	out := make([]httpapi.AdminUpstream, 0, hi-lo)
	for _, u := range rows[lo:hi] {
		out = append(out, *upstreamToAdmin(&u))
	}
	return out, total, nil
}

func (ub *upstreamsBridge) GetUpstream(ctx context.Context, id string) (*httpapi.AdminUpstream, error) {
	uid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	// ModelRepo 没有公开的 GetUpstream——经 ListUpstreams 精确匹配
	//（小表全列 + 内存命中；正式 GetUpstream 已列入 storage 缺口）。
	rows, err := ub.b.repos.Models.ListUpstreams(ctx)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	for i := range rows {
		if rows[i].ID == uid {
			return upstreamToAdmin(&rows[i]), nil
		}
	}
	return nil, fmt.Errorf("%w: 上游不存在", httpapi.ErrAdminNotFound)
}

func (ub *upstreamsBridge) CreateUpstream(ctx context.Context, in httpapi.AdminUpstream, audit httpapi.AdminAuditEntry) (string, string, error) {
	baseURL := in.BaseURL
	if in.Kind == "relay" && baseURL == "" {
		// task-20 桥清单 ②：relay 空值补定案值（列 NOT NULL，UpsertUpstream
		// 的非空校验不能被空串绊倒）。
		baseURL = relayPrefix + in.Name
	}
	var newID string
	var auditID string
	err := ub.b.st.WithTx(ctx, func(tx *store.Repos) error {
		// ownerAccountID 指针：owner 字符串 id → int64。
		var owner *string
		if in.OwnerAccountID != nil {
			oid, derr := decodeID(*in.OwnerAccountID)
			if derr != nil {
				return derr
			}
			owner = &oid
		}
		id, _, err := tx.Models.UpsertUpstreamWithCreated(ctx, in.Kind, in.Name, baseURL, owner, in.Enabled)
		if err != nil {
			return err
		}
		newID = id
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return "", "", mapStoreErr(err)
	}
	return newID, auditID, nil
}

func (ub *upstreamsBridge) UpdateUpstream(ctx context.Context, id string, in httpapi.AdminUpstreamUpdate, audit httpapi.AdminAuditEntry) (*httpapi.AdminUpstream, string, error) {
	uid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = ub.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Models.GetUpstreamByID(ctx, uid)
		if err != nil {
			return err
		}
		name, baseURL, enabled := cur.Name, cur.BaseURL, cur.Enabled
		if in.Name != nil {
			name = *in.Name
		}
		if in.BaseURL != nil {
			baseURL = *in.BaseURL
		}
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		// relay 行保持定案值不被改写（store 的 UpdateUpstream 校验一致，
		// 这里补空值语义：relay 留空＝维持）。
		if cur.Kind == "relay" && strings.TrimSpace(baseURL) == "" {
			baseURL = cur.BaseURL
		}
		if err := tx.Models.UpdateUpstream(ctx, uid, name, baseURL, enabled); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	// 读回（ListUpstreams 命中）。
	rows, err := ub.b.repos.Models.ListUpstreams(ctx)
	if err == nil {
		for i := range rows {
			if rows[i].ID == uid {
				return upstreamToAdmin(&rows[i]), auditID, nil
			}
		}
	}
	return nil, auditID, nil
}

func (ub *upstreamsBridge) SetUpstreamKind(ctx context.Context, id, kind string, audit httpapi.AdminAuditEntry) (*httpapi.AdminUpstream, string, error) {
	uid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = ub.b.st.WithTx(ctx, func(tx *store.Repos) error {
		cur, err := tx.Models.GetUpstreamByID(ctx, uid)
		if err != nil {
			return err
		}
		// 改 relay 且现值非定案形态：先改写 base_url 成 sidecar://<name>
		//（UpstreamKind 的形态校验要求 base_url 与新 kind 匹配——桥负责
		// 把「管理员只换了形态」补全成自洽行）。
		if kind == "relay" && !strings.HasPrefix(cur.BaseURL, relayPrefix) {
			if err := tx.Models.UpdateUpstream(ctx, uid, cur.Name, relayPrefix+cur.Name, cur.Enabled); err != nil {
				return err
			}
		}
		if err := tx.Models.SetUpstreamKind(ctx, uid, kind); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	rows, err := ub.b.repos.Models.ListUpstreams(ctx)
	if err == nil {
		for i := range rows {
			if rows[i].ID == uid {
				return upstreamToAdmin(&rows[i]), auditID, nil
			}
		}
	}
	return nil, auditID, nil
}

// ---------------------------------------------------------------------------
// 凭据（含鉴定面）
// ---------------------------------------------------------------------------

type credentialsBridge struct{ b *adminBridge }

func credentialToAdmin(c *store.Credential) *httpapi.AdminCredential {
	out := &httpapi.AdminCredential{
		ID:             c.ID,
		OwnerAccountID: c.OwnerAccountID,
		Provider:       c.Provider,
		ExternalID:     c.ExternalID,
		ExternalLabel:  c.ExternalLabel,
		Status:         c.Status,
		LoginEnabled:   c.LoginEnabled,
		VerifyStatus:   string(c.VerifyStatus),
		CreatedAt:      c.CreatedAt,
	}
	if c.LastUsedAt > 0 {
		lu := c.LastUsedAt
		out.LastUsedAt = &lu
	}
	if c.VerifyDetail != "" {
		out.VerifyDetail = json.RawMessage(c.VerifyDetail)
	}
	return out
}

func (cb *credentialsBridge) CountCredentials(ctx context.Context, ownerAccountID string) (int64, error) {
	oid, err := decodeID(ownerAccountID)
	if err != nil {
		return 0, err
	}
	rows, err := cb.b.repos.Models.ListCredentials(ctx, store.CredentialFilter{OwnerAccountID: &oid})
	if err != nil {
		return 0, mapStoreErr(err)
	}
	return int64(len(rows)), nil
}

func (cb *credentialsBridge) ListCredentials(ctx context.Context, f httpapi.AdminCredentialFilter) ([]httpapi.AdminCredential, int64, error) {
	sf := store.CredentialFilter{Provider: f.Provider, Limit: f.Limit, Offset: f.Offset}
	if f.OwnerAccountID != "" {
		oid, err := decodeID(f.OwnerAccountID)
		if err != nil {
			return nil, 0, err
		}
		sf.OwnerAccountID = &oid
	}
	if f.Status != "" {
		sf.Status = store.CredentialStatus(f.Status)
	}
	if f.VerifyStatus != "" {
		sf.VerifyStatus = store.VerifyStatus(f.VerifyStatus)
	}
	rows, err := cb.b.repos.Models.ListCredentials(ctx, sf)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	out := make([]httpapi.AdminCredential, 0, len(rows))
	for i := range rows {
		out = append(out, *credentialToAdmin(&rows[i]))
	}
	return out, int64(len(out)), nil
}

func (cb *credentialsBridge) GetCredential(ctx context.Context, id string) (*httpapi.AdminCredential, error) {
	cid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	c, err := cb.b.repos.Models.LookupCredential(ctx, cid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return credentialToAdmin(c), nil
}

func (cb *credentialsBridge) SetCredentialStatus(ctx context.Context, id, status string, audit httpapi.AdminAuditEntry) (*httpapi.AdminCredential, string, error) {
	cid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = cb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Models.SetCredentialStatus(ctx, cid, store.CredentialStatus(status)); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	c, err := cb.b.repos.Models.LookupCredential(ctx, cid)
	if err != nil {
		return nil, auditID, nil
	}
	return credentialToAdmin(c), auditID, nil
}

// NotifyCredential 给贡献者的通知：MVP 落审计一条（cred.notify），真实
// 通知通道（站内信/email）等后续里程碑——消息**只存摘要与长度**，
// 不存原文（06-audit §4 脱敏线：管理员写的文本可能含用户内容）。
func (cb *credentialsBridge) NotifyCredential(ctx context.Context, id, message string, audit httpapi.AdminAuditEntry) (string, error) {
	cid, err := decodeID(id)
	if err != nil {
		return "", errAdminNotImplemented
	}
	var auditID string
	err = cb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if _, err := tx.Models.LookupCredential(ctx, cid); err != nil {
			return err
		}
		detail := map[string]any{"credential": cid, "notified": true}
		if message != "" {
			detail["message_len"] = len(message) // 只存长度，原文不进审计
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return "", mapStoreErr(err)
	}
	return auditID, nil
}

// ---------------------------------------------------------------------------
// 鉴定（凭据行上的 verify 投影）
// ---------------------------------------------------------------------------

type verificationsBridge struct {
	b *adminBridge
	// l2 是深度鉴定探针（可选）。为 nil 表示没有装配运行时 L2 能力，
	// TriggerL2 会返回 ErrAdminNotWired——让管理员看到的不是「已鉴定」。
	//
	// 为什么挂在结构体而不是全局：测试要交替注入真探针与 nil，包级变量
	// 会在多个测试用例之间残留状态（和 modelsLookup 的构造时闭包同理）。
	l2 L2Probe
}

// L2Probe 是 L2 深度鉴定的探针边界。
//
// L2 的真实内容（上下文窗口二分 / 视觉已知答案图 / logprobs 分布 / 流式
// 真实性）需要真实调用上游并取得结果才能形成判定——这些是**运行时探针**
// 的职责（它知道拿哪把凭据、打到哪个端点、怎么解析返回）。
//
// Run 返回 OK=false 表示这次探针失败且不可判（上游超时/凭据失效等），
// 调用方把它当作「未鉴定」而不是「可疑」——误杀成本高于误放。
//
// 当前仓库没有这套探针本体（贡献流程的 L0/L1 是目录级探测），所以
// verificationsBridge 构造时 l2 恒为 nil → TriggerL2 返回 404。这个接口
// 是为「把真实探针挂上来」预留的边界，不是伪 L2 的桩。
type L2Probe interface {
	// Run 执行一次深度鉴定。credID 是被测凭据行、claimedModel 是声称的模型。
	Run(credID int64, claimedModel string) L2Outcome
}

// L2Outcome 是 L2 探针的输出。
type L2Outcome struct {
	// OK 是否可判地完成了（false = 无能力/上游异常，按「未鉴定」处理）。
	OK bool
	// Verdict 可取 verified/suspect/unverified（对应 store.VerifyState）。
	Verdict string
	// Evidence 是给人看的证据（脱敏，不落 API Key）。
	Evidence json.RawMessage
}

func credentialToVerification(c *store.Credential) *httpapi.AdminVerification {
	v := &httpapi.AdminVerification{
		ID:           c.ID,
		CredentialID: c.ID,
		Provider:     c.Provider,
		Verdict:      string(c.VerifyStatus),
	}
	if c.VerifyDetail != "" {
		// RawMessage 语义是「已是合法 JSON」——编码时对整个数组做校验，
		// 一行非法就炸掉**全部条目**的应答体（json.Encode 中途失败，200 已
		// 写、body 为空，前端表现为永久骨架——2026-10-28 用户实测：OAuth
		// 落库的 detail 是纯中文文本，反代供应商的行全数命中）。非 JSON 的
		// 历史文本包成字符串字段，保证应答体永远合法。
		if json.Valid([]byte(c.VerifyDetail)) {
			v.Evidence = json.RawMessage(c.VerifyDetail)
		} else {
			v.Evidence = json.RawMessage(fmt.Sprintf(`{"detail":%s}`,
				mustJSONString(c.VerifyDetail)))
		}
	}
	return v
}

func (vb *verificationsBridge) ListVerifications(ctx context.Context, f httpapi.AdminVerificationFilter) ([]httpapi.AdminVerification, int64, error) {
	sf := store.CredentialFilter{Provider: f.Provider, Limit: f.Limit, Offset: f.Offset}
	if f.Verdict != "" {
		sf.VerifyStatus = store.VerifyStatus(f.Verdict)
	}
	if f.Source != "" {
		// 库里无 source 列（§10.1 的 stage 概念未落库）——筛不精确命中，
		// 已列入 storage 缺口；这里按空过滤处理（不谎报筛选结果）。
		_ = f.Source
	}
	rows, err := vb.b.repos.Models.ListCredentials(ctx, sf)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	out := make([]httpapi.AdminVerification, 0, len(rows))
	for i := range rows {
		out = append(out, *credentialToVerification(&rows[i]))
	}
	return out, int64(len(out)), nil
}

func (vb *verificationsBridge) GetVerification(ctx context.Context, id string) (*httpapi.AdminVerification, error) {
	vid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	c, err := vb.b.repos.Models.LookupCredential(ctx, vid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return credentialToVerification(c), nil
}

func (vb *verificationsBridge) OverrideVerification(ctx context.Context, id, verdict, reason, operatorAccountID string, audit httpapi.AdminAuditEntry) (*httpapi.AdminVerification, string, error) {
	vid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = vb.b.st.WithTx(ctx, func(tx *store.Repos) error {
		// override 记录（含理由/操作人）存 verify_detail JSON——库无
		// 独立 override 列，detail 是鉴定证据的既有承载位。
		cur, err := tx.Models.LookupCredential(ctx, vid)
		if err != nil {
			return err
		}
		doc := map[string]any{}
		if cur.VerifyDetail != "" {
			_ = json.Unmarshal([]byte(cur.VerifyDetail), &doc)
		}
		doc["override"] = map[string]any{
			"verdict":  verdict,
			"reason":   reason,
			"operator": operatorAccountID,
		}
		b, _ := json.Marshal(doc)
		if err := tx.Models.MarkVerified(ctx, vid, store.VerifyStatus(verdict), string(b)); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	c, err := vb.b.repos.Models.LookupCredential(ctx, vid)
	if err != nil {
		return nil, auditID, nil
	}
	return credentialToVerification(c), auditID, nil
}

// TriggerL2 触发 L2 深度鉴定（02-admin §10.1）。
//
// L2 需要运行时探针（见 L2Probe 注释）。当前桥不装配任何探针，所以这里
// 恒返回 ErrAdminNotWired（404）——能力未接线不确认存在，跟「接口不存在」
// 同一行为。这比返回一个假的「已深度鉴定」诚实得多：没有真实基准的深度
// 鉴定只是换了个名字的目录探测，会骗过它要服务的判定器。
func (vb *verificationsBridge) TriggerL2(ctx context.Context, id string, audit httpapi.AdminAuditEntry) (*httpapi.AdminVerification, string, error) {
	if vb.l2 == nil {
		return nil, "", httpapi.ErrAdminNotWired
	}
	// 挂上真探针后：LookupCredential → l2.Run → MarkVerified（事务内写
	// verify_detail + 审计）。方法签名已按此形状留好，路径清晰。
	_ = id
	_ = audit
	return nil, "", httpapi.ErrAdminNotWired
}

// ToggleRecheck 加入/移出定时复检队列（02-admin §10.1）。
//
// 定时复检标志先从凭据行的 verify_detail 读再写回。调度器（多久跑一次）
// 未装配，所以这里只持久化开关，不做定时——但开关没有落库就白记了。
func (s *verificationsBridge) ToggleRecheck(ctx context.Context, id string, enabled bool, audit httpapi.AdminAuditEntry) (*httpapi.AdminVerification, string, error) {
	vid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	cur, err := s.b.repos.Models.LookupCredential(ctx, vid)
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	doc := map[string]any{}
	if cur.VerifyDetail != "" {
		_ = json.Unmarshal([]byte(cur.VerifyDetail), &doc)
	}
	doc["recheck"] = map[string]any{
		"enabled":  enabled,
		"last_at":  nil,
		"operator": audit.ActingClass,
	}
	b, _ := json.Marshal(doc)
	var auditID string
	if err := s.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if err := tx.Models.MarkVerified(ctx, vid, cur.VerifyStatus, string(b)); err != nil {
			return err
		}
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	}); err != nil {
		return nil, "", mapStoreErr(err)
	}
	c, err := s.b.repos.Models.LookupCredential(ctx, vid)
	if err != nil {
		return nil, auditID, nil
	}
	return credentialToVerification(c), auditID, nil
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

type auditBridge struct{ b *adminBridge }

func auditEntryToAdmin(e *store.AuditEntry) *httpapi.AdminAuditEntry {
	out := &httpapi.AdminAuditEntry{
		ID:     e.ID,
		TS:     e.TS,
		Action: e.Action,
		Result: e.Result,
	}
	if e.AccountID != nil {
		out.AccountID = *e.AccountID
	}
	if e.ActingClass != nil {
		out.ActingClass = *e.ActingClass
	}
	if e.Target != nil {
		out.Target = e.Target
	}
	if e.IP != nil {
		out.IP = *e.IP
	}
	if e.UA != nil {
		out.UA = *e.UA
	}
	if e.Detail != nil {
		if b, err := json.Marshal(e.Detail); err == nil {
			out.Detail = json.RawMessage(b)
		}
	}
	return out
}

func (ab *auditBridge) Append(ctx context.Context, e httpapi.AdminAuditEntry) (string, error) {
	accountID, actingClass, target := auditInToStore(e)
	detail := map[string]any{}
	if len(e.Detail) > 0 {
		var m map[string]any
		if err := json.Unmarshal(e.Detail, &m); err == nil {
			detail = m
		}
	}
	if e.IP != "" {
		detail["ip"] = e.IP
	}
	if e.UA != "" {
		detail["ua"] = e.UA
	}
	se := store.AuditEntry{
		AccountID:   accountID,
		ActingClass: adminStrPtr(actingClass),
		Action:      e.Action,
		Target:      adminStrPtr(target),
		Result:      e.Result,
		Detail:      detail,
	}
	if se.Result == "" {
		se.Result = "ok"
	}
	n, err := ab.b.repos.Logs.Append(ctx, se)
	if err != nil {
		return "", mapStoreErr(err)
	}
	return n, nil
}

// adminFilterToStore 把管理面审计筛选翻成 store 形状。
func adminFilterToStore(f httpapi.AdminAuditFilter) store.AuditFilter {
	sf := store.AuditFilter{Action: f.Action, FromTS: f.FromTS, ToTS: f.ToTS, Limit: f.Limit, Offset: f.Offset}
	if f.AccountID != "" {
		if id, err := decodeID(f.AccountID); err == nil {
			sf.AccountID = &id
		}
	}
	_ = f.ActingClass // store 的 AuditFilter 无 acting_class 列——已列缺口
	_ = f.Result      // 同上；List 返回后内存过滤兜底
	return sf
}

func (ab *auditBridge) List(ctx context.Context, f httpapi.AdminAuditFilter) ([]httpapi.AdminAuditEntry, int64, error) {
	// 总数用无分页全量拉（AuditFilter 有硬上限 1000——审计页够用；
	// 正式 COUNT 已列 storage 缺口）。
	sfAll := adminFilterToStore(f)
	sfAll.Limit = 0
	sfAll.Offset = 0
	all, err := ab.b.repos.Logs.List(ctx, sfAll)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	// store 筛不到的两维在内存补（acting_class/result）。
	filtered := make([]store.AuditEntry, 0, len(all))
	for _, e := range all {
		if f.ActingClass != "" {
			if e.ActingClass == nil || *e.ActingClass != f.ActingClass {
				continue
			}
		}
		if f.Result != "" && e.Result != f.Result {
			continue
		}
		filtered = append(filtered, e)
	}
	total := int64(len(filtered))
	lo, hi := pageBounds(len(filtered), f.Limit, f.Offset)
	out := make([]httpapi.AdminAuditEntry, 0, hi-lo)
	for _, e := range filtered[lo:hi] {
		out = append(out, *auditEntryToAdmin(&e))
	}
	return out, total, nil
}

func (ab *auditBridge) Get(ctx context.Context, id string) (*httpapi.AdminAuditEntry, error) {
	aid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	// LogRepo 无按 id 单读——全量 List（倒序）后内存命中；正式 GetAudit
	// 已列 storage 缺口（审计页详情是低频操作，全量代价可忍）。
	rows, err := ab.b.repos.Logs.List(ctx, store.AuditFilter{})
	if err != nil {
		return nil, mapStoreErr(err)
	}
	for i := range rows {
		if rows[i].ID == aid {
			return auditEntryToAdmin(&rows[i]), nil
		}
	}
	return nil, fmt.Errorf("%w: 审计行不存在", httpapi.ErrAdminNotFound)
}

func (ab *auditBridge) Export(ctx context.Context, f httpapi.AdminAuditFilter, format string) ([]byte, string, error) {
	rows, _, err := ab.List(ctx, f)
	if err != nil {
		return nil, "", err
	}
	var sb strings.Builder
	if format == "csv" {
		sb.WriteString("id,ts,account_id,acting_class,action,target,result\n")
		for i := range rows {
			e := &rows[i]
			sb.WriteString(csvField(e.ID) + "," + csvField(fmt.Sprintf("%d", e.TS)) + "," +
				csvField(e.AccountID) + "," + csvField(e.ActingClass) + "," +
				csvField(e.Action) + "," + csvField(strDeref(e.Target)) + "," + csvField(e.Result) + "\n")
		}
	} else {
		for i := range rows {
			b, _ := json.Marshal(rows[i])
			sb.Write(b)
			sb.WriteString("\n")
		}
	}
	return []byte(sb.String()), "audit-export." + format, nil
}

// csvField 按 CSV 惯例转义（含逗号/引号/换行即整字段加引号并双写引号）。
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ---------------------------------------------------------------------------
// 收件箱
// ---------------------------------------------------------------------------

type inboxBridge struct{ b *adminBridge }

func inboxItemToAdmin(i *store.InboxItem) *httpapi.AdminInboxItem {
	out := &httpapi.AdminInboxItem{
		ID:        i.ID,
		Kind:      string(i.Kind),
		Severity:  string(i.Severity),
		Title:     i.Title,
		CreatedAt: i.CreatedAt,
		ReadAt:    i.ReadAt,
	}
	if i.DetailJSON != "" {
		out.Detail = json.RawMessage(i.DetailJSON)
	}
	if i.RelatedID != nil {
		// related_id 就是关联对象的 UUID（对外 id 与库内主键同值）。
		// 旧实现按 kind 选前缀再编码（lead 裁决①：kind 隐含目标表），
		// 前缀体系随 UUID 改造一并废弃——kind 仍然隐含目标表，只是不再
		// 把它编进 id 里（见 internal/idgen 包注释）。
		rel := *i.RelatedID
		out.RelatedID = &rel
	}
	if i.ReadBy != nil {
		s := *i.ReadBy
		out.ReadBy = &s
	}
	return out
}

// inboxRelatedPrefix 按 kind 映射关联对象前缀（06-audit §3.1 的十个 kind）。

func (ib *inboxBridge) List(ctx context.Context, f httpapi.AdminInboxFilter) ([]httpapi.AdminInboxItem, int64, error) {
	sf := store.InboxFilter{
		Kind:     store.InboxKind(f.Kind),
		Severity: store.InboxSeverity(f.Severity),
		FromTS:   f.FromTS, ToTS: f.ToTS,
		Limit: f.Limit, Offset: f.Offset,
	}
	if f.Read != nil {
		if *f.Read {
			sf.ReadOnly = true
		} else {
			sf.UnreadOnly = true
		}
	}
	// store 的 List 自带 severity 降序（task-19 已实现）——桥不重排。
	rows, err := ib.b.repos.Inbox.List(ctx, sf)
	if err != nil {
		return nil, 0, mapStoreErr(err)
	}
	out := make([]httpapi.AdminInboxItem, 0, len(rows))
	for i := range rows {
		out = append(out, *inboxItemToAdmin(&rows[i]))
	}
	// 总数：List 不回 count——同条件去分页再拉一次（CountUnread 只覆盖
	// 未读维度；正式 CountInbox 已列 storage 缺口）。
	sfAll := sf
	sfAll.Limit, sfAll.Offset = 0, 0
	all, err := ib.b.repos.Inbox.List(ctx, sfAll)
	if err != nil {
		return out, int64(len(out)), nil
	}
	return out, int64(len(all)), nil
}

func (ib *inboxBridge) CountUnread(ctx context.Context) (int64, error) {
	n, err := ib.b.repos.Inbox.CountUnread(ctx)
	if err != nil {
		return 0, mapStoreErr(err)
	}
	return n, nil
}

func (ib *inboxBridge) Get(ctx context.Context, id string) (*httpapi.AdminInboxItem, error) {
	iid, err := decodeID(id)
	if err != nil {
		return nil, errAdminNotImplemented
	}
	item, err := ib.b.repos.Inbox.Get(ctx, iid)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return inboxItemToAdmin(item), nil
}

func (ib *inboxBridge) SetRead(ctx context.Context, id string, read bool, readBy string, audit httpapi.AdminAuditEntry) (*httpapi.AdminInboxItem, string, error) {
	iid, err := decodeID(id)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	rid, err := decodeID(readBy)
	if err != nil {
		return nil, "", errAdminNotImplemented
	}
	var auditID string
	err = ib.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if read {
			if err := tx.Inbox.MarkRead(ctx, iid, rid); err != nil {
				return err
			}
		} else {
			if err := tx.Inbox.MarkUnread(ctx, iid); err != nil {
				return err
			}
		}
		// inbox_read 与状态变更同事务（06-audit §3.1 定案）。
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return nil, "", mapStoreErr(err)
	}
	item, err := ib.b.repos.Inbox.Get(ctx, iid)
	if err != nil {
		return nil, auditID, nil
	}
	return inboxItemToAdmin(item), auditID, nil
}

func (ib *inboxBridge) MarkReadBatch(ctx context.Context, ids []string, readBy string, audit httpapi.AdminAuditEntry) (int, string, error) {
	rid, err := decodeID(readBy)
	if err != nil {
		return 0, "", errAdminNotImplemented
	}
	nids := make([]string, 0, len(ids))
	for _, s := range ids {
		iid, derr := decodeID(s)
		if derr != nil {
			return 0, "", errAdminNotImplemented
		}
		nids = append(nids, iid)
	}
	var updated int64
	var auditID string
	err = ib.b.st.WithTx(ctx, func(tx *store.Repos) error {
		n, err := tx.Inbox.MarkReadBatch(ctx, nids, rid)
		if err != nil {
			return err
		}
		updated = n
		aid, aerr := appendAudit(ctx, tx.Logs, audit)
		if aerr != nil {
			return aerr
		}
		auditID = aid
		return nil
	})
	if err != nil {
		return 0, "", mapStoreErr(err)
	}
	return int(updated), auditID, nil
}

// SetUpstreamSecret 写入/替换上游密钥材料（AES-GCM 加密由 store 层做）。
//
// 为什么明文只在这里出现一次：加密与落库都在 store.PutSecret 内完成，
// 桥不缓存明文、不记日志、不回显——与 API Key 的「只回一次」同口径。
func (ub *upstreamsBridge) SetUpstreamSecret(ctx context.Context, id, plaintext string, audit httpapi.AdminAuditEntry) (string, error) {
	uid, err := decodeID(id)
	if err != nil {
		return "", fmt.Errorf("%w: 上游 id 形状非法", httpapi.ErrAdminNotFound)
	}
	var auditID string
	err = ub.b.st.WithTx(ctx, func(tx *store.Repos) error {
		if _, err := tx.Secrets.PutSecret(ctx, uid, []byte(plaintext)); err != nil {
			return err
		}
		var aerr error
		auditID, aerr = appendAudit(ctx, tx.Logs, audit)
		return aerr
	})
	return auditID, err
}

// mustJSONString 把任意文本编码成 JSON 字符串字面量（含中文与控制字符）。
// Encode 失败即 panic 的场景不存在——文本来自库内 detail 列，UTF-8 由
// SQLite 保证；这里的 panic 分支只防御未来的编码器行为变更。
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"unencodable detail"`
	}
	return string(b)
}
