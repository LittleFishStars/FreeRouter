// admin_test.go —— 管理面（/api/admin/*）的契约测试（task-20，M7）。
//
// 断言面全部取自分册条文，每条测试标注它锁的是哪一节：
//   §2.2 无 admin 类 → 404 not_found，且与"路径不存在"**逐字节不可区分**
//   §2.2 404 的 type 出自 errors.go 控制台码表（不是 ir 表）
//   §2.3 危险写只是界面二次确认：API 层无额外标记，admin 与非 admin 同判据
//   §3.1/§3.2 列表信封与写响应 audit_id
//   §5.2 列表只给数量不给 Key 列表
//   §6 默认组不可删 → 409
//   §8.4 弃用三条硬校验（目标 active / 能力逐项 ≥ / 环检测）
//   §9 relay 不接受端点形态 base_url（报错不忽略）；凭据详情无明文
//   §10 override 必填理由
//   §12 inbox count 与 inbox_read 审计
//   红线：所有 admin 端点都过 admin 检查（路由表穷举）
//
// 假体实现的是 mw_admin.go 声明的窄接口——Lead 在 internal/app 把 store
// 仓储钉到同一组接口上，这里的假体形状就是装配契约的可执行副本。

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/config"
)

// ---------------------------------------------------------------------------
// 假体（按窄接口分域）
// ---------------------------------------------------------------------------

// adminAuditFake 记录全部审计写（同事务语义在假体里由"业务方法收到
// audit 就落一条"模拟——测试据此断言 action 值域与 actor 字段）。
type adminAuditFake struct {
	entries []AdminAuditEntry
	nextID  int
}

func (a *adminAuditFake) Append(_ context.Context, e AdminAuditEntry) (string, error) {
	a.nextID++
	e.ID = fmt.Sprintf("aud_%d", a.nextID)
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	a.entries = append(a.entries, e)
	return e.ID, nil
}

func (a *adminAuditFake) List(_ context.Context, f AdminAuditFilter) ([]AdminAuditEntry, int64, error) {
	var out []AdminAuditEntry
	for _, e := range a.entries {
		if f.Action != "" && !strings.HasPrefix(e.Action, f.Action) {
			continue
		}
		if f.AccountID != "" && e.AccountID != f.AccountID {
			continue
		}
		if f.Result != "" && e.Result != f.Result {
			continue
		}
		out = append(out, e)
	}
	return out, int64(len(out)), nil
}

func (a *adminAuditFake) Get(_ context.Context, id string) (*AdminAuditEntry, error) {
	for i := range a.entries {
		if a.entries[i].ID == id {
			return &a.entries[i], nil
		}
	}
	return nil, ErrAdminNotFound
}

func (a *adminAuditFake) Export(_ context.Context, f AdminAuditFilter, format string) ([]byte, string, error) {
	rows, _, err := a.List(context.Background(), f)
	if err != nil {
		return nil, "", err
	}
	var sb strings.Builder
	for _, e := range rows {
		if format == "csv" {
			sb.WriteString(e.Action + "," + e.Result + "\n")
			continue
		}
		b, _ := json.Marshal(e)
		sb.Write(b)
		sb.WriteString("\n")
	}
	return []byte(sb.String()), "audit." + format, nil
}

// accountsFake 是账户面假体（含 pending 队列与审计联动）。
type accountsFake struct {
	byID    map[string]*AdminAccount
	byName  map[string]string
	updates []AdminAccountUpdate
	created []AdminAccount
	audit   *adminAuditFake
	nextID  int
}

func newAccountsFake(a *adminAuditFake) *accountsFake {
	return &accountsFake{byID: map[string]*AdminAccount{}, byName: map[string]string{}, audit: a}
}

func (f *accountsFake) ListAccounts(_ context.Context, fl AdminAccountFilter) ([]AdminAccount, int64, error) {
	var out []AdminAccount
	for _, a := range f.byID {
		if fl.Status != "" && a.Status != fl.Status {
			continue
		}
		if fl.Class != "" {
			hit := false
			for _, c := range a.Classes {
				if c == fl.Class {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if fl.GroupID != "" {
			hit := false
			for _, g := range a.Groups {
				if g == fl.GroupID {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, *a)
	}
	return out, int64(len(out)), nil
}

func (f *accountsFake) GetAccount(_ context.Context, id string) (*AdminAccount, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminAccountNotFound
	}
	cp := *a
	return &cp, nil
}

func (f *accountsFake) CreateAccount(ctx context.Context, in AdminAccount, hash string, audit AdminAuditEntry) (string, string, error) {
	if _, dup := f.byName[in.Username]; dup {
		return "", "", fmt.Errorf("%w: username 已存在", ErrAdminConflict)
	}
	f.nextID++
	in.ID = fmt.Sprintf("acc_%d", f.nextID)
	if hash == "" {
		return "", "", fmt.Errorf("假体要求已哈希")
	}
	f.byName[in.Username] = in.ID
	cp := in
	f.byID[in.ID] = &cp
	f.created = append(f.created, in)
	auditID, err := f.audit.Append(ctx, audit)
	return in.ID, auditID, err
}

func (f *accountsFake) UpdateAccount(ctx context.Context, id string, in AdminAccountUpdate, audit AdminAuditEntry) (*AdminAccount, string, error) {
	a, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminAccountNotFound
	}
	if in.Status != nil {
		a.Status = *in.Status
	}
	if in.DisplayName != nil {
		a.DisplayName = *in.DisplayName
	}
	if in.Classes != nil {
		a.Classes = *in.Classes
	}
	if in.Groups != nil {
		a.Groups = *in.Groups
	}
	if in.Note != nil {
		a.Note = *in.Note
	}
	if in.Quota != nil {
		a.Quota = in.Quota
	}
	if in.RPMLimit != nil {
		a.RPMLimit = *in.RPMLimit
	}
	f.updates = append(f.updates, in)
	cp := *a
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *accountsFake) ResetPassword(ctx context.Context, id, hash string, revoke bool, audit AdminAuditEntry) (int, string, error) {
	if _, ok := f.byID[id]; !ok {
		return 0, "", ErrAdminAccountNotFound
	}
	n := 0
	if revoke {
		n = 2 // 假体固定回显两条会话吊销数（测试只断言字段存在与审计写入）
	}
	auditID, err := f.audit.Append(ctx, audit)
	return n, auditID, err
}

func (f *accountsFake) RevokeSessions(ctx context.Context, id string, audit AdminAuditEntry) (int, string, error) {
	if _, ok := f.byID[id]; !ok {
		return 0, "", ErrAdminAccountNotFound
	}
	auditID, err := f.audit.Append(ctx, audit)
	return 3, auditID, err
}

// groupsFake 是组面假体。
type groupsFake struct {
	byID           map[string]*AdminGroupDetail
	audit          *adminAuditFake
	nextID         int
	modelsReplaced [][]string
}

func newGroupsFake(a *adminAuditFake) *groupsFake {
	g := &groupsFake{byID: map[string]*AdminGroupDetail{}, audit: a}
	g.byID["grp_1"] = &AdminGroupDetail{
		AdminGroup: AdminGroup{ID: "grp_1", Name: "默认组", IsDefault: true, MemberCount: 4},
		Members:    []string{"acc_1"}, Models: []string{"smart"},
		Policies: json.RawMessage(`{"max_keys":5}`),
	}
	return g
}

func (f *groupsFake) ListGroups(_ context.Context, limit, offset int) ([]AdminGroup, int64, error) {
	var out []AdminGroup
	for _, g := range f.byID {
		out = append(out, g.AdminGroup)
	}
	return out, int64(len(out)), nil
}

func (f *groupsFake) GetGroup(_ context.Context, id string) (*AdminGroupDetail, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminGroupNotFound
	}
	cp := *g
	return &cp, nil
}

func (f *groupsFake) CreateGroup(ctx context.Context, g AdminGroup, models []string, policies json.RawMessage, audit AdminAuditEntry) (string, string, error) {
	f.nextID++
	g.ID = fmt.Sprintf("grp_%d", f.nextID+1)
	for _, x := range f.byID {
		if x.Name == g.Name {
			return "", "", fmt.Errorf("%w: 组名已存在", ErrAdminConflict)
		}
	}
	f.byID[g.ID] = &AdminGroupDetail{AdminGroup: g, Models: models, Policies: policies, Members: []string{}}
	auditID, err := f.audit.Append(ctx, audit)
	return g.ID, auditID, err
}

func (f *groupsFake) UpdateGroup(ctx context.Context, id string, name, desc *string, setDefault *bool, audit AdminAuditEntry) (*AdminGroup, string, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminGroupNotFound
	}
	if setDefault != nil && *setDefault {
		for _, x := range f.byID {
			if x.IsDefault && x.ID != id {
				return nil, "", fmt.Errorf("%w: 默认组唯一", ErrAdminConflict)
			}
		}
		g.IsDefault = true
	}
	if name != nil {
		g.Name = *name
	}
	if desc != nil {
		g.Description = *desc
	}
	cp := g.AdminGroup
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *groupsFake) DeleteGroup(ctx context.Context, id string, audit AdminAuditEntry) (string, error) {
	g, ok := f.byID[id]
	if !ok {
		return "", ErrAdminGroupNotFound
	}
	if g.IsDefault {
		return "", ErrAdminDefaultGroupProtected
	}
	delete(f.byID, id)
	auditID, err := f.audit.Append(ctx, audit)
	return auditID, err
}

func (f *groupsFake) DuplicateGroup(ctx context.Context, id, newName string, audit AdminAuditEntry) (*AdminGroup, string, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminGroupNotFound
	}
	f.nextID++
	ng := g.AdminGroup
	ng.ID = fmt.Sprintf("grp_%d", f.nextID+10)
	ng.Name = newName
	if ng.Name == "" {
		ng.Name = g.Name + " 副本"
	}
	ng.IsDefault = false
	cp := ng
	f.byID[ng.ID] = &AdminGroupDetail{AdminGroup: cp, Members: []string{}, Models: g.Models, Policies: g.Policies}
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *groupsFake) ListGroupMembers(_ context.Context, id string) ([]string, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminGroupNotFound
	}
	return g.Members, nil
}

func (f *groupsFake) AddGroupMembers(ctx context.Context, id string, accs []string, audit AdminAuditEntry) (string, error) {
	g, ok := f.byID[id]
	if !ok {
		return "", ErrAdminGroupNotFound
	}
	g.Members = append(g.Members, accs...)
	return f.audit.Append(ctx, audit)
}

func (f *groupsFake) RemoveGroupMember(ctx context.Context, id, acc string, audit AdminAuditEntry) (string, error) {
	g, ok := f.byID[id]
	if !ok {
		return "", ErrAdminGroupNotFound
	}
	out := g.Members[:0]
	for _, m := range g.Members {
		if m != acc {
			out = append(out, m)
		}
	}
	g.Members = out
	return f.audit.Append(ctx, audit)
}

func (f *groupsFake) ReplaceGroupModels(ctx context.Context, id string, models []string, audit AdminAuditEntry) ([]string, string, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminGroupNotFound
	}
	old := g.Models
	g.Models = models
	f.modelsReplaced = append(f.modelsReplaced, old)
	auditID, err := f.audit.Append(ctx, audit)
	return old, auditID, err
}

func (f *groupsFake) ReplaceGroupPolicies(ctx context.Context, id string, doc json.RawMessage, audit AdminAuditEntry) (json.RawMessage, string, error) {
	g, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminGroupNotFound
	}
	old := g.Policies
	g.Policies = doc
	auditID, err := f.audit.Append(ctx, audit)
	return old, auditID, err
}

// modelsFake 是模型面假体（能力/链数据由测试直接摆）。
type modelsFake struct {
	audit      *adminAuditFake
	realByID   map[string]*AdminRealModel
	absByID    map[string]*AdminAbstractModel
	absByName  map[string]string
	cands      map[string][]AdminCandidateItem
	caps       map[string]json.RawMessage
	excluded   map[string]bool
	deprecated []AdminDeprecationUpdate
	nextID     int
}

func newModelsFake(a *adminAuditFake) *modelsFake {
	m := &modelsFake{
		audit: a, realByID: map[string]*AdminRealModel{}, absByID: map[string]*AdminAbstractModel{},
		absByName: map[string]string{}, cands: map[string][]AdminCandidateItem{},
		caps: map[string]json.RawMessage{}, excluded: map[string]bool{},
	}
	return m
}

func (m *modelsFake) putAbs(name, status string, replacement *string, caps json.RawMessage) *AdminAbstractModel {
	m.nextID++
	id := fmt.Sprintf("am_%d", m.nextID)
	row := &AdminAbstractModel{
		ID: id, Name: name, DisplayName: name, Status: status,
		ReplacementName: replacement, CapabilityMode: "strict",
		Capabilities: caps, Strategy: json.RawMessage(`["expiry-first","balance"]`),
	}
	m.absByID[id] = row
	m.absByName[name] = id
	if caps != nil {
		m.caps[name] = caps
	}
	return row
}

func (m *modelsFake) putReal(id string) *AdminRealModel {
	row := &AdminRealModel{
		ID: id, DisplayName: "RM " + id, QualifiedName: "dsh:chan/" + id,
		UpstreamID: "up_1", UpstreamModelID: id, Status: "active", Source: "auto",
		Modalities: json.RawMessage(`["text"]`), Efforts: json.RawMessage(`["off"]`),
	}
	m.realByID[id] = row
	return row
}

func (m *modelsFake) ListRealModels(_ context.Context, _ string, _ *bool, _ string, _ string, limit, offset int) ([]AdminRealModel, int64, error) {
	var out []AdminRealModel
	for _, r := range m.realByID {
		out = append(out, *r)
	}
	return out, int64(len(out)), nil
}

func (m *modelsFake) GetRealModel(_ context.Context, id string) (*AdminRealModel, error) {
	r, ok := m.realByID[id]
	if !ok {
		return nil, ErrAdminModelNotFound
	}
	cp := *r
	return &cp, nil
}

func (m *modelsFake) UpdateRealModel(ctx context.Context, id string, patch map[string]json.RawMessage, audit AdminAuditEntry) (*AdminRealModel, string, error) {
	r, ok := m.realByID[id]
	if !ok {
		return nil, "", ErrAdminModelNotFound
	}
	// §7.1：人工改写后 source 翻 override（桥的责任，假体照做）
	r.Source = "override"
	if v, has := patch["display_name"]; has {
		_ = json.Unmarshal(v, &r.DisplayName)
	}
	cp := *r
	auditID, err := m.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (m *modelsFake) SetRealModelExcluded(ctx context.Context, id string, excluded bool, audit AdminAuditEntry) (*AdminRealModel, string, error) {
	r, ok := m.realByID[id]
	if !ok {
		return nil, "", ErrAdminModelNotFound
	}
	r.ExcludedFromCandidates = excluded
	m.excluded[id] = excluded
	cp := *r
	auditID, err := m.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (m *modelsFake) ListAbstractModels(_ context.Context, limit, offset int) ([]AdminAbstractModel, int64, error) {
	var out []AdminAbstractModel
	for _, a := range m.absByID {
		cp := *a
		cp.CandidateCount = int64(len(m.cands[a.ID]))
		out = append(out, cp)
	}
	return out, int64(len(out)), nil
}

func (m *modelsFake) GetAbstractModel(_ context.Context, id string) (*AdminAbstractModel, error) {
	a, ok := m.absByID[id]
	if !ok {
		return nil, ErrAdminModelNotFound
	}
	cp := *a
	cp.CandidateCount = int64(len(m.cands[a.ID]))
	for _, c := range m.cands[a.ID] {
		cp.AvailableCandidateCount += c.AvailableInstances
	}
	return &cp, nil
}

func (m *modelsFake) GetAbstractModelByName(_ context.Context, name string) (*AdminAbstractModel, error) {
	id, ok := m.absByName[name]
	if !ok {
		return nil, ErrAdminModelNotFound
	}
	return m.GetAbstractModel(context.Background(), id)
}

func (m *modelsFake) CreateAbstractModel(ctx context.Context, a AdminAbstractModel, cands []AdminCandidateItem, audit AdminAuditEntry) (string, string, error) {
	if _, dup := m.absByName[a.Name]; dup {
		return "", "", fmt.Errorf("%w: 对外名已存在", ErrAdminConflict)
	}
	m.nextID++
	a.ID = fmt.Sprintf("am_%d", m.nextID)
	m.absByID[a.ID] = &a
	m.absByName[a.Name] = a.ID
	m.cands[a.ID] = cands
	if a.Capabilities != nil {
		m.caps[a.Name] = a.Capabilities
	}
	auditID, err := m.audit.Append(ctx, audit)
	return a.ID, auditID, err
}

func (m *modelsFake) UpdateAbstractModel(ctx context.Context, id string, patch map[string]json.RawMessage, _ json.RawMessage, audit AdminAuditEntry) (*AdminAbstractModel, string, error) {
	a, ok := m.absByID[id]
	if !ok {
		return nil, "", ErrAdminModelNotFound
	}
	if v, has := patch["display_name"]; has {
		_ = json.Unmarshal(v, &a.DisplayName)
	}
	if v, has := patch["capability_mode"]; has {
		_ = json.Unmarshal(v, &a.CapabilityMode)
	}
	if v, has := patch["capabilities"]; has {
		a.Capabilities = v
		m.caps[a.Name] = v
	}
	if v, has := patch["strategy"]; has {
		a.Strategy = v
	}
	cp := *a
	auditID, err := m.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (m *modelsFake) SetAbstractStatus(ctx context.Context, id, status string, audit AdminAuditEntry) (string, error) {
	a, ok := m.absByID[id]
	if !ok {
		return "", ErrAdminModelNotFound
	}
	a.Status = status
	return m.audit.Append(ctx, audit)
}

func (m *modelsFake) SetDeprecation(ctx context.Context, id string, in AdminDeprecationUpdate, audit AdminAuditEntry) (string, error) {
	a, ok := m.absByID[id]
	if !ok {
		return "", ErrAdminModelNotFound
	}
	a.Status = "deprecated"
	rep := in.ReplacementName
	a.ReplacementName = &rep
	a.DeprecatedAt = in.DeprecatedAt
	a.SunsetAt = in.SunsetAt
	m.deprecated = append(m.deprecated, in)
	return m.audit.Append(ctx, audit)
}

func (m *modelsFake) ListCandidates(_ context.Context, id string) ([]AdminCandidateItem, error) {
	return m.cands[id], nil
}

func (m *modelsFake) ReplaceCandidates(ctx context.Context, id string, cands []AdminCandidateItem, audit AdminAuditEntry) ([]AdminCandidateItem, string, error) {
	old := m.cands[id]
	m.cands[id] = cands
	auditID, err := m.audit.Append(ctx, audit)
	return old, auditID, err
}

func (m *modelsFake) ReferencingGroups(_ context.Context, name string) ([]AdminGroup, error) {
	return []AdminGroup{{ID: "grp_1", Name: "默认组", IsDefault: true}}, nil
}

func (m *modelsFake) CapabilitySnapshot(_ context.Context) (map[string]json.RawMessage, error) {
	return m.caps, nil
}

// upstreamsFake / credentialsFake / verificationsFake。
type upstreamsFake struct {
	audit   *adminAuditFake
	byID    map[string]*AdminUpstream
	created []AdminUpstream
	nextID  int
}

func newUpstreamsFake(a *adminAuditFake) *upstreamsFake {
	return &upstreamsFake{audit: a, byID: map[string]*AdminUpstream{}}
}

func (f *upstreamsFake) ListUpstreams(_ context.Context, limit, offset int) ([]AdminUpstream, int64, error) {
	var out []AdminUpstream
	for _, u := range f.byID {
		out = append(out, *u)
	}
	return out, int64(len(out)), nil
}

func (f *upstreamsFake) GetUpstream(_ context.Context, id string) (*AdminUpstream, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminUpstreamNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *upstreamsFake) CreateUpstream(ctx context.Context, u AdminUpstream, audit AdminAuditEntry) (string, string, error) {
	f.nextID++
	u.ID = fmt.Sprintf("up_%d", f.nextID)
	// 桥语义：relay 行落定案值（task-12）
	if u.Kind == "relay" && u.BaseURL == "" {
		u.BaseURL = relayBaseURLPrefix + u.Name
	}
	f.created = append(f.created, u)
	cp := u
	f.byID[u.ID] = &cp
	auditID, err := f.audit.Append(ctx, audit)
	return u.ID, auditID, err
}

func (f *upstreamsFake) UpdateUpstream(ctx context.Context, id string, in AdminUpstreamUpdate, audit AdminAuditEntry) (*AdminUpstream, string, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminUpstreamNotFound
	}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if in.BaseURL != nil {
		u.BaseURL = *in.BaseURL
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	cp := *u
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *upstreamsFake) SetUpstreamSecret(ctx context.Context, id, plaintext string, audit AdminAuditEntry) (string, error) {
	u, ok := f.byID[id]
	if !ok {
		return "", ErrAdminUpstreamNotFound
	}
	if plaintext == "" {
		return "", fmt.Errorf("%w: 密钥不能为空", ErrAdminConflict)
	}
	u.SecretPresent = true
	return f.audit.Append(ctx, audit)
}

func (f *upstreamsFake) SetUpstreamKind(ctx context.Context, id, kind string, audit AdminAuditEntry) (*AdminUpstream, string, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminUpstreamNotFound
	}
	if u.Kind == "relay" && kind != "relay" && strings.HasPrefix(u.BaseURL, relayBaseURLPrefix) {
		// 改出 relay 且现值是定案值：要求先补端点（§9 不静默留 sidecar://）
		return nil, "", fmt.Errorf("%w: 现为 relay 定案值，改用 vendor/selfhosted 前先 PATCH base_url 填端点", ErrAdminConflict)
	}
	u.Kind = kind
	if kind == "relay" {
		u.BaseURL = relayBaseURLPrefix + u.Name
	}
	cp := *u
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

type credentialsFake struct {
	audit *adminAuditFake
	byID  map[string]*AdminCredential
}

func newCredentialsFake(a *adminAuditFake) *credentialsFake {
	c := &credentialsFake{audit: a, byID: map[string]*AdminCredential{}}
	c.byID["cred_1"] = &AdminCredential{
		ID: "cred_1", OwnerAccountID: "acc_9", Provider: "example-provider",
		ExternalID: "ext-1", ExternalLabel: "我的贡献", Status: "active",
		LoginEnabled: true, VerifyStatus: "verified",
		VerifyDetail: json.RawMessage(`{"L0":{"ok":true}}`), CreatedAt: 1700000000,
	}
	return c
}

func (f *credentialsFake) CountCredentials(_ context.Context, owner string) (int64, error) {
	var n int64
	for _, c := range f.byID {
		if c.OwnerAccountID == owner {
			n++
		}
	}
	return n, nil
}

func (f *credentialsFake) ListCredentials(_ context.Context, f2 AdminCredentialFilter) ([]AdminCredential, int64, error) {
	var out []AdminCredential
	for _, c := range f.byID {
		if f2.Provider != "" && c.Provider != f2.Provider {
			continue
		}
		if f2.Status != "" && c.Status != f2.Status {
			continue
		}
		out = append(out, *c)
	}
	return out, int64(len(out)), nil
}

func (f *credentialsFake) GetCredential(_ context.Context, id string) (*AdminCredential, error) {
	c, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *credentialsFake) SetCredentialStatus(ctx context.Context, id, status string, audit AdminAuditEntry) (*AdminCredential, string, error) {
	c, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminNotFound
	}
	c.Status = status
	cp := *c
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *credentialsFake) NotifyCredential(ctx context.Context, id, _ string, audit AdminAuditEntry) (string, error) {
	if _, ok := f.byID[id]; !ok {
		return "", ErrAdminNotFound
	}
	return f.audit.Append(ctx, audit)
}

type verificationsFake struct {
	audit    *adminAuditFake
	byID     map[string]*AdminVerification
	overrode map[string]string
}

func newVerificationsFake(a *adminAuditFake) *verificationsFake {
	v := &verificationsFake{audit: a, byID: map[string]*AdminVerification{}, overrode: map[string]string{}}
	v.byID["ver_1"] = &AdminVerification{
		ID: "ver_1", CredentialID: "cred_1", Provider: "example-provider",
		ClaimedModel: "some-model", Verdict: "suspect",
		Evidence: json.RawMessage(`[{"signal":"kbf","strength":"medium","result":"skipped"}]`),
	}
	return v
}

func (f *verificationsFake) ListVerifications(_ context.Context, fl AdminVerificationFilter) ([]AdminVerification, int64, error) {
	var out []AdminVerification
	for _, v := range f.byID {
		if fl.Verdict != "" && v.Verdict != fl.Verdict {
			continue
		}
		out = append(out, *v)
	}
	return out, int64(len(out)), nil
}

func (f *verificationsFake) GetVerification(_ context.Context, id string) (*AdminVerification, error) {
	v, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminNotFound
	}
	cp := *v
	return &cp, nil
}

func (f *verificationsFake) OverrideVerification(ctx context.Context, id, verdict, reason, _ string, audit AdminAuditEntry) (*AdminVerification, string, error) {
	v, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminNotFound
	}
	v.Verdict = verdict
	f.overrode[id] = reason
	cp := *v
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

// keysFake / inboxFake / settingsFake。
type keysFake struct{ counts map[string]int64 }

func (f *keysFake) CountKeys(_ context.Context, accountID string) (int64, error) {
	return f.counts[accountID], nil
}

type inboxFake struct {
	audit  *adminAuditFake
	byID   map[string]*AdminInboxItem
	readBy map[string]string
}

func newInboxFake(a *adminAuditFake) *inboxFake {
	i := &inboxFake{audit: a, byID: map[string]*AdminInboxItem{}, readBy: map[string]string{}}
	i.byID["inb_1"] = &AdminInboxItem{
		ID: "inb_1", Kind: "model_missing", Severity: "error", Title: "模型消失",
		Detail: json.RawMessage(`{"real_model_id":"rm_1"}`), CreatedAt: 1700000000,
	}
	i.byID["inb_2"] = &AdminInboxItem{
		ID: "inb_2", Kind: "account_pending", Severity: "warning", Title: "待审核",
		CreatedAt: 1700000001,
	}
	return i
}

func (f *inboxFake) List(_ context.Context, fl AdminInboxFilter) ([]AdminInboxItem, int64, error) {
	var out []AdminInboxItem
	for _, i := range f.byID {
		if fl.Kind != "" && i.Kind != fl.Kind {
			continue
		}
		if fl.Severity != "" && i.Severity != fl.Severity {
			continue
		}
		if fl.Read != nil {
			isRead := i.ReadAt != nil
			if *fl.Read != isRead {
				continue
			}
		}
		out = append(out, *i)
	}
	return out, int64(len(out)), nil
}

func (f *inboxFake) CountUnread(_ context.Context) (int64, error) {
	var n int64
	for _, i := range f.byID {
		if i.ReadAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *inboxFake) Get(_ context.Context, id string) (*AdminInboxItem, error) {
	i, ok := f.byID[id]
	if !ok {
		return nil, ErrAdminNotFound
	}
	cp := *i
	return &cp, nil
}

func (f *inboxFake) SetRead(ctx context.Context, id string, read bool, readBy string, audit AdminAuditEntry) (*AdminInboxItem, string, error) {
	i, ok := f.byID[id]
	if !ok {
		return nil, "", ErrAdminNotFound
	}
	if read {
		ts := adminEpochNow()
		i.ReadAt = &ts
		i.ReadBy = &readBy
		f.readBy[id] = readBy
	} else {
		i.ReadAt = nil
		i.ReadBy = nil
	}
	cp := *i
	auditID, err := f.audit.Append(ctx, audit)
	return &cp, auditID, err
}

func (f *inboxFake) MarkReadBatch(ctx context.Context, ids []string, readBy string, audit AdminAuditEntry) (int, string, error) {
	n := 0
	for _, id := range ids {
		if i, ok := f.byID[id]; ok && i.ReadAt == nil {
			ts := adminEpochNow()
			i.ReadAt = &ts
			i.ReadBy = &readBy
			n++
		}
	}
	auditID, err := f.audit.Append(ctx, audit)
	return n, auditID, err
}

type settingsFake struct {
	audit   *adminAuditFake
	doc     json.RawMessage
	patched map[string]json.RawMessage
}

func (f *settingsFake) GetSettings(_ context.Context) (json.RawMessage, error) {
	return f.doc, nil
}

func (f *settingsFake) PatchSettings(ctx context.Context, patch map[string]json.RawMessage, audit AdminAuditEntry) (string, error) {
	if f.patched == nil {
		f.patched = map[string]json.RawMessage{}
	}
	for k, v := range patch {
		f.patched[k] = v
	}
	return f.audit.Append(ctx, audit)
}

type ipCheckFake struct{ res *AdminIPCheckResult }

func (f *ipCheckFake) CheckIP(_ context.Context, ip string) (*AdminIPCheckResult, error) {
	if f.res != nil {
		cp := *f.res
		cp.SourceIP = ip
		return &cp, nil
	}
	return &AdminIPCheckResult{SourceIP: ip, Allowed: true, Reason: "同源回环，无需代理解析"}, nil
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// adminHarness 组装一套可登录的管理面：真 SessionManager + 真窄接口假体。
type adminHarness struct {
	server    *Server
	handler   http.Handler
	deps      *AdminDeps
	accounts  *accountsFake
	groups    *groupsFake
	models    *modelsFake
	upstreams *upstreamsFake
	creds     *credentialsFake
	verifs    *verificationsFake
	audit     *adminAuditFake
	inbox     *inboxFake
	settings  *settingsFake
	keys      *keysFake
	fsess     *sessFakeStore
	tokens    map[string]string // accountID → 会话令牌明文
}

func newAdminHarness(t *testing.T) *adminHarness {
	t.Helper()
	sess := newSessFakeStore()
	authStores := auth.Stores{Accounts: sess, Sessions: sess}
	smgr := auth.NewSessionManager(auth.SessionPolicy{TTL: 24 * time.Hour}, authStores)

	h := &adminHarness{fsess: sess, tokens: map[string]string{}}
	h.audit = &adminAuditFake{}
	h.accounts = newAccountsFake(h.audit)
	h.groups = newGroupsFake(h.audit)
	h.models = newModelsFake(h.audit)
	h.upstreams = newUpstreamsFake(h.audit)
	h.creds = newCredentialsFake(h.audit)
	h.verifs = newVerificationsFake(h.audit)
	h.inbox = newInboxFake(h.audit)
	h.keys = &keysFake{counts: map[string]int64{"acc_1": 3}}
	h.settings = &settingsFake{audit: h.audit,
		doc: json.RawMessage(`{"security":{"registration_enabled":true},"limits":{"global_daily_tokens":0}}`)}

	deps := &AdminDeps{
		Sess: smgr, Hasher: auth.NewPasswordHasher(12),
		Accounts: h.accounts, Groups: h.groups, Keys: h.keys,
		Credentials: h.creds, Models: h.models, Upstreams: h.upstreams,
		Verifications: h.verifs, Audit: h.audit, Inbox: h.inbox,
		SettingsView: h.settings, SettingsPatch: h.settings, IPCheck: &ipCheckFake{},
	}
	h.deps = deps

	s := New(Deps{Config: config.Default()})
	s.AttachAdmin(deps)
	s.SetConsoleRoutes(func(r chi.Router) { s.MountAdminRoutes(r) })
	h.server = s
	h.handler = s.Handler()
	return h
}

// issueAdminSession 给一个账户签发会话（acting 类由中间件指定，这里只建账户）。
func (h *adminHarness) issueSession(t *testing.T, username string, classes ...auth.Class) string {
	t.Helper()
	id := fmt.Sprintf("acc_%d", len(h.fsess.byID)+1)
	set := auth.NewClassSet(classes...)
	acct := &auth.Account{
		ID: id, Username: username, DisplayName: username,
		Classes: set, Status: auth.StatusActive, RPMLimit: -1, TPMLimit: -1, MaxConcurrency: -1,
	}
	h.fsess.accounts[username] = acct
	h.fsess.byID[id] = acct
	is, err := h.deps.Sess.Issue(context.Background(), acct, "127.0.0.1", "admin-test")
	if err != nil {
		t.Fatalf("签发会话失败: %v", err)
	}
	h.tokens[id] = is.Token
	return is.Token
}

// req 发一个带会话 Cookie 的请求（写方法自动带 CSRF 自定义头）。
func (h *adminHarness) req(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

// decodeData 解 {"data":...} 信封。
func decodeData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应体不是 JSON（%d）：%s", w.Code, w.Body.String())
	}
	return env
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) (code, typ string) {
	t.Helper()
	env := decodeData(t, w)
	e, _ := env["error"].(map[string]any)
	if e == nil {
		t.Fatalf("响应体缺 error 字段：%s", w.Body.String())
	}
	code, _ = e["code"].(string)
	typ, _ = e["type"].(string)
	return
}

// ---------------------------------------------------------------------------
// §2.2：权限口径与 404 一致性
// ---------------------------------------------------------------------------

// TestContractAdminNonAdminClassGets404NotFound 锁 §2.2：无 admin 类的会话
// 打管理面 → 404 + not_found（**不是 403**）。
func TestContractAdminNonAdminClassGets404NotFound(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "tenant-only", auth.ClassTenant)
	for _, path := range []string{
		"/api/admin/accounts", "/api/admin/groups", "/api/admin/credentials", "/api/admin/audit",
	} {
		w := h.req(t, http.MethodGet, path, tok, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 无 admin 类应 404，实得 %d", path, w.Code)
		}
		code, _ := errorCode(t, w)
		if code != "not_found" {
			t.Fatalf("%s 错误码应为 not_found，实得 %q", path, code)
		}
	}
}

// TestContractAdminDangerousWritesAlso404 锁 §2.2 + §2.3：危险写（删组/封禁
// 凭据/改类/改弃用）对无 admin 类也是同一个 404——API 层不因"危险"加第二道。
func TestContractAdminDangerousWritesAlso404(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "contrib-only", auth.ClassContributor)
	cases := []struct{ method, path, body string }{
		{http.MethodPatch, "/api/admin/accounts/acc_1", `{"status":"disabled"}`},
		{http.MethodPost, "/api/admin/accounts/acc_1/password", `{"generate":true}`},
		{http.MethodDelete, "/api/admin/groups/grp_2", ""},
		{http.MethodPost, "/api/admin/credentials/cred_1/ban", ""},
		{http.MethodPut, "/api/admin/abstract-models/am_1/deprecation", `{"status":"deprecated","replacement_model_id":"am_2"}`},
		{http.MethodPatch, "/api/admin/settings", `{"registration_enabled":false}`},
		{http.MethodPost, "/api/admin/verifications/ver_1/override", `{"verdict":"trusted","reason":"x"}`},
	}
	for _, c := range cases {
		w := h.req(t, c.method, c.path, tok, c.body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("危险写 %s %s 应 404（无 admin 类），实得 %d", c.method, c.path, w.Code)
		}
		if code, _ := errorCode(t, w); code != "not_found" {
			t.Fatalf("%s 码应 not_found，实得 %q", c.path, code)
		}
	}
}

// TestContractAdmin404ByteIdenticalToUnknownPath 是 §2.2 的唯一可执行副本：
// 无 admin 类的 404 与"路径真不存在"的 404 在状态码/响应头/响应体上必须
// **逐字节不可区分**，否则泄露"该路径存在"（code-review 与 lead 均点名）。
// X-Request-Id 设计上每请求随机，比对时排除（同 webui_contract_test 思路）。
func TestContractAdmin404ByteIdenticalToUnknownPath(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "tenant2", auth.ClassTenant)

	noAdmin := h.req(t, http.MethodGet, "/api/admin/accounts", tok, "")
	// 同一个 admin 前缀下的**不存在子路径**：中间件先跑 → 同一条 404 出口。
	noAdminFake := h.req(t, http.MethodGet, "/api/admin/accounts/definitely-not-here", tok, "")
	// 完全没有会话：不存在的路径也走同一条 401 出口（不区分真/假路径）。
	noTokReal := h.req(t, http.MethodGet, "/api/admin/accounts", "", "")
	noTokFake := h.req(t, http.MethodGet, "/api/admin/does-not-exist", "", "")
	// 公开面的"路径不存在"（走 server.go handleNotFound）作终极基线。
	publicMissing := h.req(t, http.MethodGet, "/api/nope/nope", "", "")

	assertSameBytes(t, noAdmin, noAdminFake, "无 admin 类：真端点 vs 假子路径")
	assertSameBytes(t, noTokReal, noTokFake, "无会话：真端点 vs 假子路径")
	// 公开面的不存在走 handleNotFound（同一条 writeError("not_found") 出口）；
	// 但进管理面前有会话判定（401），状态码与它不同是**预期**——401 只
	// 回答"你没登录"，不确认任何路径。此处断言两者的 JSON 形状同源。
	if code, typ := errorCode(t, publicMissing); code != "not_found" || typ != "not_found_error" {
		t.Fatalf("公开面不存在应 not_found/not_found_error，实得 %q/%q", code, typ)
	}
}

// perRequestHeaders 复用 webui_contract_test.go 的同名表（每请求随机头）——
// 不重新声明：同包两处声明会编译冲突，且排除集必须全测试一致。

func assertSameBytes(t *testing.T, a, b *httptest.ResponseRecorder, what string) {
	t.Helper()
	if a.Code != b.Code {
		t.Fatalf("%s：状态码不同 %d vs %d", what, a.Code, b.Code)
	}
	ha := headerMap(a.Header())
	hb := headerMap(b.Header())
	for k, v := range hb {
		if ha[k] != v {
			t.Fatalf("%s：响应头 %s 不同（%q vs %q）", what, k, ha[k], v)
		}
	}
	for k := range ha {
		if _, has := hb[k]; !has {
			t.Fatalf("%s：一侧多了响应头 %s", what, k)
		}
	}
	if a.Body.String() != b.Body.String() {
		t.Fatalf("%s：响应体不同\nA: %s\nB: %s", what, a.Body.String(), b.Body.String())
	}
}

func headerMap(h http.Header) map[string]string {
	out := map[string]string{}
	for k, vs := range h {
		if perRequestHeaders[k] {
			continue
		}
		out[k] = strings.Join(vs, ",")
	}
	return out
}

// TestContractAdminConsoleCodesUseErrorsTable 锁"控制台码的 type 出自
// errors.go 表"：ir 表不含 not_found/conflict，CodeToErrorType("not_found")
// 会静默兜底 internal_error（code-review 点名）。
func TestContractAdminConsoleCodesUseErrorsTable(t *testing.T) {
	for _, c := range []struct{ code, wantType string }{
		{"not_found", "not_found_error"},
		{"conflict", "conflict_error"},
		{"invalid_request", "invalid_request_error"},
		{"session_expired", "authentication_error"},
		{"forbidden", "permission_error"},
	} {
		if got := string(TypeForCode(c.code)); got != c.wantType {
			t.Fatalf("TypeForCode(%q)=%q，期望 %q（errors.go 控制台码表）", c.code, got, c.wantType)
		}
	}
	h := newAdminHarness(t)
	tok := h.issueSession(t, "tenant3", auth.ClassTenant)
	_, typ := errorCode(t, h.req(t, http.MethodGet, "/api/admin/accounts", tok, ""))
	if typ != "not_found_error" {
		t.Fatalf("404 的 type=%q，期望 not_found_error（若误用 ir 表会是 internal_error）", typ)
	}
}

// TestContractNoSession401AndCSRF403 锁中间件顺序：无头写请求 403（挡在
// 会话判定之前），有头无会话 401 session_expired。
func TestContractNoSession401AndCSRF403(t *testing.T) {
	h := newAdminHarness(t)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/accounts", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r) // 无 Cookie、无自定义头
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺 CSRF 头应 403，实得 %d", w.Code)
	}
	if code, _ := errorCode(t, w); code != "forbidden" {
		t.Fatalf("码应 forbidden，实得 %q", code)
	}
	w2 := h.req(t, http.MethodPost, "/api/admin/accounts", "", `{}`)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("有头无会话应 401，实得 %d", w2.Code)
	}
	if code, _ := errorCode(t, w2); code != "session_expired" {
		t.Fatalf("码应 session_expired，实得 %q", code)
	}
}

// ---------------------------------------------------------------------------
// 路由表穷举：漏挂即红
// ---------------------------------------------------------------------------

// TestContractEveryAdminRouteGuarded 枚举注册进路由表的**全部** admin 端点，
// 逐条以无 admin 类身份请求，断言 404 not_found——"新加端点忘了挂检查"
// 会在这里立刻暴露（code-review 建议）。
func TestContractEveryAdminRouteGuarded(t *testing.T) {
	h := newAdminHarness(t)
	mux := chi.NewMux()
	h.server.MountAdminRoutes(mux)

	var routes []struct{ method, pattern string }
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, struct{ method, pattern string }{method, route})
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk 失败: %v", err)
	}
	if len(routes) < 40 {
		t.Fatalf("路由穷举数量异常（%d 条）——管理面应有 40+ 端点", len(routes))
	}
	tok := h.issueSession(t, "guard-probe", auth.ClassTenant)
	for _, rt := range routes {
		path := strings.NewReplacer("{id}", "1", "{accountID}", "acc_1").Replace(rt.pattern)
		w := h.req(t, rt.method, path, tok, `{}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s 未被 admin 检查挡住：实得 %d（期望 404 not_found）", rt.method, rt.pattern, w.Code)
		}
		if code, _ := errorCode(t, w); code != "not_found" {
			t.Fatalf("%s %s 码应 not_found，实得 %q", rt.method, rt.pattern, code)
		}
	}
}

// ---------------------------------------------------------------------------
// §5 账户面
// ---------------------------------------------------------------------------

// TestContractAdminReadsAccountListWithCountsOnly 锁 §5.2：admin 可读列表，
// 行内只有 key_count/credential_count 数量、**没有 Key 列表**。
func TestContractAdminReadsAccountListWithCountsOnly(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root", auth.ClassAdmin, auth.ClassTenant)
	h.accounts.byID["acc_1"] = &AdminAccount{
		ID: "acc_1", Username: "alice", DisplayName: "alice", Status: "active",
		Classes: []string{"admin", "tenant"}, Groups: []string{"grp_1"}, CreatedAt: 1700000000,
		RPMLimit: -1, TPMLimit: -1, MaxConcurrency: -1,
	}
	h.accounts.byName["alice"] = "acc_1"
	h.keys.counts["acc_1"] = 7

	w := h.req(t, http.MethodGet, "/api/admin/accounts", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin 读列表应 200，实得 %d body=%s", w.Code, w.Body.String())
	}
	env := decodeData(t, w)
	if _, has := env["next_cursor"]; !has {
		t.Fatalf("列表信封缺 next_cursor（§3.1）：%s", w.Body.String())
	}
	rows, _ := env["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("应返回 1 行，实得 %d", len(rows))
	}
	row := rows[0].(map[string]any)
	if got := row["key_count"]; got != float64(7) {
		t.Fatalf("key_count 应为补查值 7，实得 %v", got)
	}
	body := w.Body.String()
	for _, leak := range []string{"key_hash", "prefix", "api_keys", "password_hash", "fr-", "secret_ref"} {
		if strings.Contains(body, leak) {
			t.Fatalf("列表响应含泄露字段 %q：%s", leak, body)
		}
	}
	// 权限类与状态形状
	if _, has := row["classes"].([]any); !has {
		t.Fatalf("行内缺 classes 数组")
	}
}

// TestContractAdminCreateAccountWritesAuditAndEchoesPassword 锁 §3.2 +
// §5.3：写响应带 audit_id、审计落一条 account.create（带 actor/acting_class），
// 未给密码时一次性回显生成密码且**不进审计**。
func TestContractAdminCreateAccountWritesAuditAndEchoesPassword(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "creator", auth.ClassAdmin)
	w := h.req(t, http.MethodPost, "/api/admin/accounts", tok,
		`{"username":"bob","display_name":"Bob","classes":["tenant"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("建号应 201，实得 %d body=%s", w.Code, w.Body.String())
	}
	env := decodeData(t, w)
	aid, _ := env["audit_id"].(string)
	if aid == "" {
		t.Fatalf("写响应缺 audit_id（§3.2）：%s", w.Body.String())
	}
	data := env["data"].(map[string]any)
	pw, _ := data["generated_password"].(string)
	if len(pw) < 12 {
		t.Fatalf("应一次性回显生成密码，实得 %q", data["generated_password"])
	}
	if len(h.audit.entries) == 0 {
		t.Fatalf("建号必须写审计")
	}
	e := h.audit.entries[len(h.audit.entries)-1]
	if e.Action != "account.create" {
		t.Fatalf("审计 action 应为 account.create，实得 %q", e.Action)
	}
	if e.ActingClass != "admin" {
		t.Fatalf("审计 acting_class 应为 admin，实得 %q", e.ActingClass)
	}
	if e.Result != "ok" {
		t.Fatalf("审计 result 应为 ok，实得 %q", e.Result)
	}
	// 明文密码绝不进审计（06-audit §2 禁止项）
	for _, en := range h.audit.entries {
		if strings.Contains(string(en.Detail), pw) || strings.Contains(en.Action+string(en.Detail), "password_hash") {
			t.Fatalf("审计里出现了明文密码或哈希字段：%+v", en)
		}
	}
}

// TestContractAdminCreateRejectsEmptyClassesAndBadStatus 锁 01-accounts §1
// 非空不变式与 §5.2 状态子集（参数校验 400）。
func TestContractAdminCreateRejectsEmptyClassesAndBadStatus(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root2", auth.ClassAdmin)
	w := h.req(t, http.MethodPost, "/api/admin/accounts", tok,
		`{"username":"nope","classes":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 classes 应 400，实得 %d", w.Code)
	}
	if code, _ := errorCode(t, w); code != "invalid_request" {
		t.Fatalf("码应 invalid_request，实得 %q", code)
	}
	w2 := h.req(t, http.MethodPost, "/api/admin/accounts", tok,
		`{"username":"mergedguy","classes":["tenant"],"status":"merged"}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("merged 状态（写域外）应 400，实得 %d", w2.Code)
	}
	w3 := h.req(t, http.MethodPost, "/api/admin/accounts", tok,
		`{"username":"badrate","classes":["tenant"],"rpm_limit":-2}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("rpm_limit=-2 应 400（仅 -1/0/正数），实得 %d", w3.Code)
	}
}

// TestContractApprovalRequiresGroupAndQuota 锁 §5.2/uxK：批准缺组或缺配额
// 一律 400，消息指明缺哪一项。
func TestContractApprovalRequiresGroupAndQuota(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "approver", auth.ClassAdmin)
	h.accounts.byID["acc_50"] = &AdminAccount{ID: "acc_50", Username: "newbie",
		Status: "pending", Classes: []string{"tenant"}, CreatedAt: 1700000000}
	h.accounts.byName["newbie"] = "acc_50"

	w := h.req(t, http.MethodPost, "/api/admin/accounts/pending/acc_50/approve", tok, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺组缺配额批准应 400，实得 %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "groups") {
		t.Fatalf("消息应指明缺 groups：%s", body)
	}
	w2 := h.req(t, http.MethodPost, "/api/admin/accounts/pending/acc_50/approve", tok,
		`{"groups":["grp_1"]}`)
	if w2.Code != http.StatusBadRequest || !strings.Contains(w2.Body.String(), "quota") {
		t.Fatalf("缺配额应 400 且指明 quota，实得 %d %s", w2.Code, w2.Body.String())
	}
	w3 := h.req(t, http.MethodPost, "/api/admin/accounts/pending/acc_50/approve", tok,
		`{"groups":["grp_1"],"quota":{"unit":"token","limit":200000,"period":"day"}}`)
	if w3.Code != http.StatusOK {
		t.Fatalf("组+配额齐全应 200，实得 %d %s", w3.Code, w3.Body.String())
	}
	if h.accounts.byID["acc_50"].Status != "active" {
		t.Fatalf("批准后状态应 active")
	}
	var found bool
	for _, e := range h.audit.entries {
		if e.Action == "account.approve" {
			found = true
		}
	}
	if !found {
		t.Fatalf("批准应写 account.approve 审计")
	}
}

// TestContractResetPasswordRevokesSessions 锁 §5.1：重置密码带联动吊销数，
// 且 revoke_sessions=false 时不吊销。
func TestContractResetPasswordRevokesSessions(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root3", auth.ClassAdmin)
	h.accounts.byID["acc_70"] = &AdminAccount{ID: "acc_70", Username: "pw", Status: "active",
		Classes: []string{"tenant"}}
	w := h.req(t, http.MethodPost, "/api/admin/accounts/acc_70/password", tok, `{"generate":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("重置应 200，实得 %d %s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)["data"].(map[string]any)
	if n, _ := data["sessions_revoked"].(float64); n != 2 {
		t.Fatalf("默认应吊销 2 条（假体值），实得 %v", data["sessions_revoked"])
	}
	if _, has := data["generated_password"]; !has {
		t.Fatalf("generate 分支必须回显一次性密码")
	}
	// password 与 generate 同给 → 400（歧义不猜）
	w2 := h.req(t, http.MethodPost, "/api/admin/accounts/acc_70/password", tok,
		`{"password":"aaaaaaaaaaaaaaaa","generate":true}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("同给应 400，实得 %d", w2.Code)
	}
}

// ---------------------------------------------------------------------------
// §6 组面
// ---------------------------------------------------------------------------

// TestContractDefaultGroupDeleteIs409Conflict 锁 §6：默认组不可删 → 409 conflict。
func TestContractDefaultGroupDeleteIs409Conflict(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root4", auth.ClassAdmin)
	w := h.req(t, http.MethodDelete, "/api/admin/groups/grp_1", tok, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("删默认组应 409，实得 %d %s", w.Code, w.Body.String())
	}
	code, typ := errorCode(t, w)
	if code != "conflict" || typ != "conflict_error" {
		t.Fatalf("应 conflict/conflict_error（errors.go 表），实得 %q/%q", code, typ)
	}
}

// TestContractGroupDeleteNonDefaultOk 对照组：非默认组可删（成员退回默认组
// 由桥做）并写审计。
func TestContractGroupDeleteNonDefaultOk(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root5", auth.ClassAdmin)
	h.groups.byID["grp_9"] = &AdminGroupDetail{
		AdminGroup: AdminGroup{ID: "grp_9", Name: "试点组"}, Models: []string{"smart"},
		Members: []string{"acc_2"}, Policies: json.RawMessage(`{}`),
	}
	w := h.req(t, http.MethodDelete, "/api/admin/groups/grp_9", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("删普通组应 200，实得 %d %s", w.Code, w.Body.String())
	}
	if _, has := decodeData(t, w)["audit_id"]; !has {
		t.Fatalf("删组写响应缺 audit_id")
	}
	if _, still := h.groups.byID["grp_9"]; still {
		t.Fatalf("假体里组应已删除")
	}
}

// TestContractGroupPutsRequireWholeCollection 锁 §6 整体替换语义：PUT 不带
// 数组即 400（"没给"与"清空"必须可区分）。
func TestContractGroupPutsRequireWholeCollection(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root6", auth.ClassAdmin)
	w := h.req(t, http.MethodPut, "/api/admin/groups/grp_1/models", tok, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT models 不给数组应 400，实得 %d", w.Code)
	}
	w2 := h.req(t, http.MethodPut, "/api/admin/groups/grp_1/models", tok, `{"models":[]}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("PUT models=[] 应 200（清空是合法整体替换），实得 %d %s", w2.Code, w2.Body.String())
	}
	if len(h.groups.byID["grp_1"].Models) != 0 {
		t.Fatalf("清空未生效")
	}
	w3 := h.req(t, http.MethodPut, "/api/admin/groups/grp_1/policies", tok, `{"policies":[]}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("policies 必须是 JSON 对象，数组应 400，实得 %d", w3.Code)
	}
}

// TestContractGroupMemberIDsArePrefixChecked 锁 lead 裁决①：跨资源 id 混用
// 在解码层失败（拿 acc_ 去填组 id、拿 grp_ 去填账户 id 都拒）。
func TestContractGroupMemberIDsArePrefixChecked(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root7", auth.ClassAdmin)
	w := h.req(t, http.MethodPost, "/api/admin/groups/acc_1/members", tok, `{"account_ids":["acc_2"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("组路径填 acc_ 应 400，实得 %d", w.Code)
	}
	w2 := h.req(t, http.MethodPost, "/api/admin/groups/grp_1/members", tok, `{"account_ids":["grp_2"]}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("account_ids 填 grp_ 应 400，实得 %d", w2.Code)
	}
}

// ---------------------------------------------------------------------------
// §7/§8 模型面
// ---------------------------------------------------------------------------

// TestContractRealModelPatchFlipsSourceToOverride 锁 §7.1：人工改写后
// source=override；白名单外字段（qualified_name/status）400。
func TestContractRealModelPatchFlipsSourceToOverride(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root8", auth.ClassAdmin)
	h.models.putReal("rm_1")
	w := h.req(t, http.MethodPatch, "/api/admin/real-models/rm_1", tok, `{"display_name":"新名"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH display_name 应 200，实得 %d %s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)["data"].(map[string]any)
	if data["source"] != "override" {
		t.Fatalf("人工改写后 source 应 override，实得 %v", data["source"])
	}
	for _, bad := range []string{"qualified_name", "status", "source", "upstream_id"} {
		w2 := h.req(t, http.MethodPatch, "/api/admin/real-models/rm_1", tok,
			fmt.Sprintf(`{%q:"x"}`, bad))
		if w2.Code != http.StatusBadRequest {
			t.Fatalf("不可写字段 %s 应 400，实得 %d", bad, w2.Code)
		}
	}
}

// TestContractDeprecationTargetMustBeActive 锁 §8.4 校验 1：替代目标存在且
// active——目标是 deprecated / 不存在时拒绝保存并给具体原因。
func TestContractDeprecationTargetMustBeActive(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root9", auth.ClassAdmin)
	capFull := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true,"vision":true}`)
	a := h.models.putAbs("smart", "active", nil, capFull)
	h.models.putAbs("fast", "deprecated", strPtr("smart"), capFull)

	// 目标 deprecated → 拒
	w := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"fast"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("目标 deprecated 应 400，实得 %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "deprecated") {
		t.Fatalf("原因应指明目标状态：%s", body)
	}
	// 目标不存在 → 拒
	w2 := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"ghost"}`)
	if w2.Code != http.StatusBadRequest || !strings.Contains(w2.Body.String(), "不存在") {
		t.Fatalf("目标不存在应 400+指明不存在，实得 %d %s", w2.Code, w2.Body.String())
	}
	// status 非 deprecated → 拒（弃用单向，本端点只接受 deprecated）
	w3 := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"active","replacement_model_id":"ghost"}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("status=active 走 deprecation 端点应 400，实得 %d", w3.Code)
	}
}

// TestContractDeprecationCapabilityMustBeGE 锁 §8.4 校验 2：替代目标能力
// 逐项 ≥，**未知能力字段按不满足处理**（02-abstract §4.3 的严格取向）。
func TestContractDeprecationCapabilityMustBeGE(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root10", auth.ClassAdmin)
	src := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true,"vision":true}`)
	a := h.models.putAbs("visionary", "active", nil, src)

	// 目标 context 更小 → 拒并指明不合格项
	small := json.RawMessage(`{"context_window":32000,"max_output":8192,"tool_calling":true,"vision":true}`)
	h.models.putAbs("shrink", "active", nil, small)
	w := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"shrink"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("能力不足应 400，实得 %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "context_window") {
		t.Fatalf("应指明不合格项 context_window：%s", body)
	}
	// 目标缺 vision（未知）→ 按不满足拒（不得静默放行）
	unknown := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true}`)
	h.models.putAbs("novision", "active", nil, unknown)
	w2 := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"novision"}`)
	if w2.Code != http.StatusBadRequest || !strings.Contains(w2.Body.String(), "vision") {
		t.Fatalf("目标缺 vision 应 400 并指明 vision，实得 %d %s", w2.Code, w2.Body.String())
	}
	// 目标能力齐全且更大 → 通过
	big := json.RawMessage(`{"context_window":256000,"max_output":16384,"tool_calling":true,"vision":true}`)
	b := h.models.putAbs("bigger", "active", nil, big)
	w3 := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"bigger"}`)
	if w3.Code != http.StatusOK {
		t.Fatalf("能力 ≥ 应保存成功，实得 %d %s", w3.Code, w3.Body.String())
	}
	if len(h.models.deprecated) != 1 || h.models.deprecated[0].ReplacementName != "bigger" {
		t.Fatalf("弃用配置未落库：%+v", h.models.deprecated)
	}
	_ = b
}

// TestContractDeprecationCycleRejected 锁 §8.4 校验 3：环检测拒绝并回环路径
// （visited 拓扑口径同 adminWalkChain/resolveChain，不写第二套）。
func TestContractDeprecationCycleRejected(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root11", auth.ClassAdmin)
	capFull := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true,"vision":true}`)
	// 绕库写出来的边：a（active）已指向 b——正常 API 造不出（弃用必带
	// deprecated 态），环检测兜的正是这种配置。
	a := h.models.putAbs("cycle-a", "active", nil, capFull)
	h.models.putAbs("cycle-b", "active", strPtr("cycle-a"), capFull)

	// 再让 a → b 就成环（a→b→a）
	w := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"cycle-b"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("成环应 400，实得 %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "环") {
		t.Fatalf("消息应说明成环并回路径：%s", body)
	}
	// 自环也拒
	w2 := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/deprecation", tok,
		`{"status":"deprecated","replacement_model_id":"cycle-a"}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("自环应 400，实得 %d", w2.Code)
	}
}

// TestContractChainPreviewShape 锁 §8.4 chain 预览响应形状。
func TestContractChainPreviewShape(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root12", auth.ClassAdmin)
	capFull := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true,"vision":true}`)
	capBig := json.RawMessage(`{"context_window":256000,"max_output":16384,"tool_calling":true,"vision":true}`)
	h.models.putAbs("c1", "active", nil, capBig)
	h.models.putAbs("c2", "deprecated", strPtr("c1"), capFull)

	a := h.models.absByID[h.models.absByName["c2"]]
	w := h.req(t, http.MethodGet, "/api/admin/abstract-models/"+a.ID+"/chain", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("chain 预览应 200，实得 %d %s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)["data"].(map[string]any)
	chain, _ := data["chain"].([]any)
	if len(chain) != 2 || chain[0] != "c2" || chain[1] != "c1" {
		t.Fatalf("链形状不对：%v", chain)
	}
	if data["hops"] != float64(1) || data["final_active"] != "c1" {
		t.Fatalf("hops/final_active 不对：%v / %v", data["hops"], data["final_active"])
	}
	if data["warning"] != nil {
		t.Fatalf("正常情况 warning 恒为 null（§8.4），实得 %v", data["warning"])
	}
	cc, _ := data["capability_check"].(map[string]any)
	if cc == nil || cc["ok"] != true {
		t.Fatalf("capability_check 应 ok=true：%v", data["capability_check"])
	}
	checks, _ := cc["checks"].(map[string]any)
	for _, k := range []string{"context_window", "max_output", "tool_calling", "vision"} {
		if _, has := checks[k]; !has {
			t.Fatalf("capability_check.checks 缺四项之一 %q：%v", k, checks)
		}
	}
}

// TestContractDryRunStaticVerdict 锁 §8.3：干跑回显 reachable/no_candidate
// 分桶（本轮静态实现，不重放历史流量）。
func TestContractDryRunStaticVerdict(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root13", auth.ClassAdmin)
	capFull := json.RawMessage(`{"context_window":128000,"max_output":8192,"tool_calling":true,"vision":true}`)
	a := h.models.putAbs("dryme", "active", nil, capFull)
	h.models.putReal("rm_1")
	h.models.putReal("rm_2")
	h.models.cands[a.ID] = []AdminCandidateItem{
		{RealModelID: "rm_1", AvailableInstances: 2},
		{RealModelID: "rm_2", AvailableInstances: 0},
	}
	w := h.req(t, http.MethodPost, "/api/admin/abstract-models/"+a.ID+"/dry-run", tok,
		`{"strategy":["expiry-first","balance"],"sample_size":200}`)
	if w.Code != http.StatusOK {
		t.Fatalf("干跑应 200，实得 %d %s", w.Code, w.Body.String())
	}
	data := decodeData(t, w)["data"].(map[string]any)
	if data["reachable"] != float64(1) || data["no_candidate"] != float64(1) {
		t.Fatalf("分桶不对：reachable=%v no_candidate=%v", data["reachable"], data["no_candidate"])
	}
	if _, has := data["failures"]; !has {
		t.Fatalf("缺 failures 数组（§8.3 形状）")
	}
}

// TestContractCandidateReplaceIgnoresRuntimeField 锁 §8.2：
// available_instances 是响应字段，请求里给了不得穿透到存储。
func TestContractCandidateReplaceIgnoresRuntimeField(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root14", auth.ClassAdmin)
	capFull := json.RawMessage(`{"context_window":128000}`)
	a := h.models.putAbs("candme", "active", nil, capFull)
	h.models.putReal("rm_1")
	w := h.req(t, http.MethodPut, "/api/admin/abstract-models/"+a.ID+"/candidates", tok,
		`{"candidates":[{"real_model_id":"rm_1","weight":100,"available_instances":999}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("替换候选应 200，实得 %d %s", w.Code, w.Body.String())
	}
	got := h.models.cands[a.ID]
	if len(got) != 1 || got[0].AvailableInstances != 0 {
		t.Fatalf("请求里的 available_instances 必须被忽略（归零后送桥），实得 %+v", got)
	}
}

// TestContractAbstractNameImmutable 锁 §8.1：name 创建后不可改。
func TestContractAbstractNameImmutable(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root15", auth.ClassAdmin)
	a := h.models.putAbs("rename-me", "active", nil, nil)
	w := h.req(t, http.MethodPatch, "/api/admin/abstract-models/"+a.ID, tok, `{"name":"other"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH name 应 400，实得 %d %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "name") {
		t.Fatalf("param/消息应指明 name：%s", body)
	}
}

// TestContractDeprecatedCannotReActivateViaPatch 锁 §8.1 单向性。
func TestContractDeprecatedCannotReActivateViaPatch(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root16", auth.ClassAdmin)
	a := h.models.putAbs("gone", "deprecated", strPtr("survivor"), nil)
	h.models.putAbs("survivor", "active", nil, nil)
	w := h.req(t, http.MethodPatch, "/api/admin/abstract-models/"+a.ID, tok, `{"status":"active"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("deprecated→active 应 400（弃用单向），实得 %d %s", w.Code, w.Body.String())
	}
	// disabled→active 允许（可逆止血）
	b := h.models.putAbs("paused", "disabled", nil, nil)
	w2 := h.req(t, http.MethodPatch, "/api/admin/abstract-models/"+b.ID, tok, `{"status":"active"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("disabled→active 应 200，实得 %d %s", w2.Code, w2.Body.String())
	}
}

// ---------------------------------------------------------------------------
// §9 上游与凭据
// ---------------------------------------------------------------------------

// TestContractRelayRejectsEndpointBaseURL 锁 §9：kind=relay 传了端点形态的
// 地址要**保存前明确报错**（不是忽略）；sidecar:// 定案值与空值放行。
func TestContractRelayRejectsEndpointBaseURL(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root17", auth.ClassAdmin)

	for _, bad := range []string{"https://api.example.com/v1", "http://127.0.0.1:9999", "ftp://x"} {
		w := h.req(t, http.MethodPost, "/api/admin/upstreams", tok,
			fmt.Sprintf(`{"kind":"relay","name":"p1","base_url":%q}`, bad))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("relay + %q 应 400（报错不忽略），实得 %d", bad, w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "base_url") {
			t.Fatalf("param 应指明 base_url：%s", body)
		}
	}
	if len(h.upstreams.created) != 0 {
		t.Fatalf("relay 带端点地址不得落库（保存前拒绝）")
	}
	// sidecar:// 定案值放行（一刀切拒任何字符串会拒掉 bootstrap 自己的行）
	w2 := h.req(t, http.MethodPost, "/api/admin/upstreams", tok,
		`{"kind":"relay","name":"p2","base_url":"sidecar://p2"}`)
	if w2.Code != http.StatusCreated {
		t.Fatalf("relay + sidecar:// 定案值应 201，实得 %d %s", w2.Code, w2.Body.String())
	}
	// 留空也放行，由桥补定案值
	w3 := h.req(t, http.MethodPost, "/api/admin/upstreams", tok, `{"kind":"relay","name":"p3"}`)
	if w3.Code != http.StatusCreated {
		t.Fatalf("relay 留空 base_url 应 201，实得 %d", w3.Code)
	}
	if got := h.upstreams.created[len(h.upstreams.created)-1].BaseURL; got != relayBaseURLPrefix+"p3" {
		t.Fatalf("桥应落定案值 sidecar://p3，实得 %q", got)
	}
	// vendor/selfhosted 缺 base_url → 400
	for _, kind := range []string{"vendor", "selfhosted"} {
		w4 := h.req(t, http.MethodPost, "/api/admin/upstreams", tok,
			fmt.Sprintf(`{"kind":%q,"name":"v"}`, kind))
		if w4.Code != http.StatusBadRequest {
			t.Fatalf("%s 缺 base_url 应 400，实得 %d", kind, w4.Code)
		}
	}
}

// TestContractUpstreamPatchRejectsKind 锁 §9：kind 变更不走 PATCH。
func TestContractUpstreamPatchRejectsKind(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root18", auth.ClassAdmin)
	w := h.req(t, http.MethodPost, "/api/admin/upstreams", tok,
		`{"kind":"vendor","name":"v1","base_url":"https://api.v1.com"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("建 vendor 应 201，实得 %d %s", w.Code, w.Body.String())
	}
	id := decodeData(t, w)["data"].(map[string]any)["id"].(string)
	w2 := h.req(t, http.MethodPatch, "/api/admin/upstreams/"+id, tok, `{"kind":"relay"}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("PATCH 带 kind 应 400，实得 %d %s", w2.Code, w2.Body.String())
	}
	if body := w2.Body.String(); !strings.Contains(body, "/kind") {
		t.Fatalf("消息应指向独立端点：%s", body)
	}
	w3 := h.req(t, http.MethodPost, "/api/admin/upstreams/"+id+"/kind", tok, `{"kind":"relay"}`)
	if w3.Code != http.StatusOK {
		t.Fatalf("POST /kind 应 200，实得 %d %s", w3.Code, w3.Body.String())
	}
	if h.upstreams.byID[id].BaseURL != relayBaseURLPrefix+"v1" {
		t.Fatalf("改 relay 后桥应补定案值，实得 %q", h.upstreams.byID[id].BaseURL)
	}
}

// TestContractCredentialDetailHasNoPlaintext 锁 §9 + 06-audit §2：凭据详情
// **无明文**——响应里不得出现明文/密文/引用字段。
func TestContractCredentialDetailHasNoPlaintext(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root19", auth.ClassAdmin)
	// 假体塞一个带敏感字段的行（桥若误传也应在这里暴露）
	h.creds.byID["cred_2"] = &AdminCredential{
		ID: "cred_2", OwnerAccountID: "acc_9", Provider: "p", ExternalID: "e",
		Status: "active", VerifyStatus: "unverified", CreatedAt: 1700000000,
	}
	for _, path := range []string{"/api/admin/credentials", "/api/admin/credentials/cred_1", "/api/admin/credentials/cred_2"} {
		w := h.req(t, http.MethodGet, path, tok, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实得 %d", path, w.Code)
		}
		body := w.Body.String()
		for _, leak := range []string{"secret_ref", "plaintext", "ciphertext", "secret", "password", "token", "nonce", "master_key"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Fatalf("%s 响应含敏感字段 %q：%s", path, leak, body)
			}
		}
	}
	// 元数据仍齐全（provider/external_label/verify_detail）
	w := h.req(t, http.MethodGet, "/api/admin/credentials/cred_1", tok, "")
	data := decodeData(t, w)["data"].(map[string]any)
	if data["provider"] != "example-provider" || data["external_label"] != "我的贡献" {
		t.Fatalf("凭据元数据缺失：%v", data)
	}
}

// TestContractCredentialSuspendAndBanDistinctActions 锁 §9 + §12 纪律：
// 两个端点都落 disabled，但审计 action 不同（界面/告警的差别在 action 上）。
func TestContractCredentialSuspendAndBanDistinctActions(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root20", auth.ClassAdmin)
	if w := h.req(t, http.MethodPost, "/api/admin/credentials/cred_1/suspend", tok, ""); w.Code != 200 {
		t.Fatalf("suspend 应 200，实得 %d %s", w.Code, w.Body.String())
	}
	if w := h.req(t, http.MethodPost, "/api/admin/credentials/cred_1/ban", tok, ""); w.Code != 200 {
		t.Fatalf("ban 应 200，实得 %d", w.Code)
	}
	var acts []string
	for _, e := range h.audit.entries {
		if strings.HasPrefix(e.Action, "credential.") {
			acts = append(acts, e.Action)
		}
	}
	if len(acts) != 2 || acts[0] != "credential.suspend" || acts[1] != "credential.ban" {
		t.Fatalf("审计 action 应为 suspend/ban 两条，实得 %v", acts)
	}
	if h.creds.byID["cred_1"].Status != "disabled" {
		t.Fatalf("状态应 disabled（CHECK 枚举无 banned）")
	}
}

// ---------------------------------------------------------------------------
// §10 鉴定
// ---------------------------------------------------------------------------

// TestContractOverrideRequiresReason 锁 §10：人工改判**必填理由**，缺理由
// 在任何存储写之前 400。
func TestContractOverrideRequiresReason(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root21", auth.ClassAdmin)
	w := h.req(t, http.MethodPost, "/api/admin/verifications/ver_1/override", tok,
		`{"verdict":"trusted"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 reason 应 400，实得 %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "reason") {
		t.Fatalf("param 应指明 reason：%s", body)
	}
	if len(h.verifs.overrode) != 0 {
		t.Fatalf("缺理由时不得写存储")
	}
	w2 := h.req(t, http.MethodPost, "/api/admin/verifications/ver_1/override", tok,
		`{"verdict":"trusted","reason":"人工复核证据充分"}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("带理由应 200，实得 %d %s", w2.Code, w2.Body.String())
	}
	if h.verifs.byID["ver_1"].Verdict != "trusted" {
		t.Fatalf("改判未落库")
	}
	var found bool
	for _, e := range h.audit.entries {
		if e.Action == "verification.override" {
			found = true
		}
	}
	if !found {
		t.Fatalf("改判必须写审计")
	}
}

// ---------------------------------------------------------------------------
// §11 审计与设置
// ---------------------------------------------------------------------------

// TestContractAuditQueryAndExport 锁 §11：查询/详情/导出可用，且**没有**
// DELETE /audit（append-only）。
func TestContractAuditQueryAndExport(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root22", auth.ClassAdmin)
	// 先制造几条审计
	h.req(t, http.MethodPost, "/api/admin/upstreams", tok, `{"kind":"relay","name":"ap"}`)
	h.req(t, http.MethodPost, "/api/admin/upstreams", tok, `{"kind":"relay","name":"bp"}`)

	w := h.req(t, http.MethodGet, "/api/admin/audit?action=upstream.", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("审计查询应 200，实得 %d %s", w.Code, w.Body.String())
	}
	env := decodeData(t, w)
	rows, _ := env["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("action 前缀筛选应命中 2 条，实得 %d", len(rows))
	}
	first := rows[0].(map[string]any)
	if first["acting_class"] != "admin" {
		t.Fatalf("审计应带 acting_class：%v", first)
	}
	id := first["id"].(string)
	w2 := h.req(t, http.MethodGet, "/api/admin/audit/"+id, tok, "")
	if w2.Code != http.StatusOK {
		t.Fatalf("审计详情应 200，实得 %d", w2.Code)
	}
	w3 := h.req(t, http.MethodGet, "/api/admin/audit/export?format=jsonl&action=upstream.", tok, "")
	if w3.Code != http.StatusOK || !strings.HasPrefix(w3.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("导出应 200+ndjson，实得 %d / %q", w3.Code, w3.Header().Get("Content-Type"))
	}
	if lines := strings.Count(strings.TrimSpace(w3.Body.String()), "\n") + 1; lines != 2 {
		t.Fatalf("JSONL 导出应 2 行，实得 %d：%s", lines, w3.Body.String())
	}
	// 非法 result 值 400
	if w4 := h.req(t, http.MethodGet, "/api/admin/audit?result=nope", tok, ""); w4.Code != http.StatusBadRequest {
		t.Fatalf("非法 result 应 400，实得 %d", w4.Code)
	}
	// DELETE /audit 不存在（§11 无删除端点）——405 或 404 都算"没有该能力"
	mux := chi.NewMux()
	h.server.MountAdminRoutes(mux)
	var hasDelete bool
	_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodDelete && strings.Contains(strings.ToLower(route), "audit") {
			hasDelete = true
		}
		return nil
	})
	if hasDelete {
		t.Fatalf("审计不得有 DELETE 端点（append-only）")
	}
}

// TestContractSettingsGetPatchAndIPCheck 锁 §11：设置 GET/PATCH 可用、未知键
// 400、ip-check 回显当前源 IP。
func TestContractSettingsGetPatchAndIPCheck(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root23", auth.ClassAdmin)
	w := h.req(t, http.MethodGet, "/api/admin/settings", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET settings 应 200，实得 %d", w.Code)
	}
	w2 := h.req(t, http.MethodPatch, "/api/admin/settings", tok, `{"registration_enabled":false}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("PATCH 已知键应 200，实得 %d %s", w2.Code, w2.Body.String())
	}
	if v := h.settings.patched["registration_enabled"]; string(v) != "false" {
		t.Fatalf("设置未落： %v", h.settings.patched)
	}
	w3 := h.req(t, http.MethodPatch, "/api/admin/settings", tok, `{"admin_ip_allowlist":["1.2.3.4"]}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("admin_ip_allowlist 不提供（2026-10-19 定案），应 400，实得 %d", w3.Code)
	}
	w4 := h.req(t, http.MethodPost, "/api/admin/settings/ip-check", tok, "")
	if w4.Code != http.StatusOK {
		t.Fatalf("ip-check 应 200，实得 %d", w4.Code)
	}
	if data := decodeData(t, w4)["data"].(map[string]any); data["source_ip"] == "" {
		t.Fatalf("ip-check 应回显源 IP：%v", data)
	}
}

// ---------------------------------------------------------------------------
// §12 收件箱
// ---------------------------------------------------------------------------

// TestContractInboxCountAndReadFlow 锁 §12：/count 只回未读数；标已读写
// read_by + inbox_read 审计（06-audit §3.1）；批量已读按 ids。
func TestContractInboxCountAndReadFlow(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root24", auth.ClassAdmin)
	w := h.req(t, http.MethodGet, "/api/admin/inbox/count", tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("inbox count 应 200，实得 %d", w.Code)
	}
	data := decodeData(t, w)["data"].(map[string]any)
	if data["unread"] != float64(2) {
		t.Fatalf("未读应为 2，实得 %v", data["unread"])
	}
	if _, has := data["items"]; has {
		t.Fatalf("count 端点不得拉列表（§12 读放大）")
	}
	// 读一条：read_by 记录是谁读的
	h.accounts.byID["acc_1"] = &AdminAccount{ID: "acc_1", Username: "root24", Status: "active", Classes: []string{"admin"}}
	w2 := h.req(t, http.MethodPatch, "/api/admin/inbox/inb_1", tok, `{"read":true}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("标已读应 200，实得 %d %s", w2.Code, w2.Body.String())
	}
	if h.inbox.readBy["inb_1"] == "" {
		t.Fatalf("read_by 应记录管理员账户")
	}
	var found bool
	for _, e := range h.audit.entries {
		if e.Action == "inbox_read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("标已读必须写 inbox_read 审计（06-audit §3.1）")
	}
	w3 := h.req(t, http.MethodGet, "/api/admin/inbox/count", tok, "")
	if decodeData(t, w3)["data"].(map[string]any)["unread"] != float64(1) {
		t.Fatalf("标读后未读应减 1")
	}
	// 批量
	w4 := h.req(t, http.MethodPost, "/api/admin/inbox/read", tok, `{"ids":["inb_2"]}`)
	if w4.Code != http.StatusOK {
		t.Fatalf("批量已读应 200，实得 %d %s", w4.Code, w4.Body.String())
	}
	if n := decodeData(t, w4)["data"].(map[string]any)["marked_read"]; n != float64(1) {
		t.Fatalf("批量应更新 1 条，实得 %v", n)
	}
	// 筛选参数非法值 400
	if w5 := h.req(t, http.MethodGet, "/api/admin/inbox?severity=critical", tok, ""); w5.Code != http.StatusBadRequest {
		t.Fatalf("severity=critical 不在统一词表（info/warning/error），应 400，实得 %d", w5.Code)
	}
}

// ---------------------------------------------------------------------------
// 未装配能力的处理（lead 裁决：不发明新码）
// ---------------------------------------------------------------------------

// TestContractUnwiredCapabilityIsByteIdentical404 锁 lead 裁决（2026-10-22）：
// 未上线的能力一律 404 + not_found，且与"路径真不存在"**逐字节同形**
// （501+invalid_request 的语义错配已消灭：invalid_request 暗示改请求能成，
// 而未装配改了也没用）。
func TestContractUnwiredCapabilityIsByteIdentical404(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root25", auth.ClassAdmin)
	// AdminDeps 里没接 Refresh/UpstreamTest —— 两个端点必须存在但对外 404。
	unwired := h.req(t, http.MethodPost, "/api/admin/real-models/refresh", tok, "")
	if unwired.Code != http.StatusNotFound {
		t.Fatalf("未接刷新器应 404，实得 %d", unwired.Code)
	}
	if code, _ := errorCode(t, unwired); code != "not_found" {
		t.Fatalf("码应 not_found，实得 %q", code)
	}
	// 基线：同前缀下真不存在的路径（admin 中间件的 404 出口）。
	baseline := h.req(t, http.MethodGet, "/api/admin/definitely-not-here", tok, "")
	assertSameBytes(t, unwired, baseline, "未装配能力 vs 不存在路径（§2.2 哲学延伸）")

	w2 := h.req(t, http.MethodPost, "/api/admin/upstreams/up_1/test", tok, "")
	if w2.Code != http.StatusNotFound {
		t.Fatalf("未接测试器应 404，实得 %d", w2.Code)
	}
	// 未接设置读写的两个端点同样 404（不是 405/501）
	w3 := h.req(t, http.MethodPost, "/api/admin/settings/ip-check", tok, "")
	if w3.Code != http.StatusOK && w3.Code != http.StatusNotFound {
		t.Fatalf("ip-check 已接应 200（此处假体已接）；实得 %d", w3.Code)
	}
}

// TestContractInvalidIDAndPaginationParams 锁参数校验 400 出口：坏 limit /
// cursor / 路径 id 前缀。
func TestContractInvalidIDAndPaginationParams(t *testing.T) {
	h := newAdminHarness(t)
	tok := h.issueSession(t, "root26", auth.ClassAdmin)
	cases := []struct{ path, param string }{
		{"/api/admin/accounts?limit=0", "limit"},
		{"/api/admin/accounts?limit=abc", "limit"},
		{"/api/admin/accounts?cursor=%21%40%23", "cursor"},
		{"/api/admin/groups/acc_1", "id"},     // 组路径填账户前缀
		{"/api/admin/real-models/am_1", "id"}, // 真实模型路径填抽象模型前缀
	}
	for _, c := range cases {
		w := h.req(t, http.MethodGet, c.path, tok, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实得 %d", c.path, w.Code)
		}
		env := decodeData(t, w)
		e := env["error"].(map[string]any)
		if e["code"] != "invalid_request" {
			t.Fatalf("%s 码应 invalid_request，实得 %v", c.path, e["code"])
		}
		if e["param"] != c.param {
			t.Fatalf("%s param 应为 %q，实得 %v", c.path, c.param, e["param"])
		}
	}
}
