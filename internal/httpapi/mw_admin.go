// mw_admin.go —— 管理面（/api/admin/*）的中间件、窄接口契约与共享工具（task-20，M7）。
//
// 唯一事实源：docs/api/02-admin.md §2/§3，docs/security/06-audit.md §3。
//
// 权限口径（02-admin §2.2，不得漂）：管理端点**只过一层**——会话账户的
// classes 含 admin；不满足统一返回 **404 not_found** 而不是 403。
// 理由写在分册里：403 等于向无权限者确认"这个路径存在"，管理面的路由
// 结构本身就是信息。一次登录即该账户全部权限类的全部权限，不存在
// "已登录但还差一步"的中间态，因此没有第二道判定的位置。
//
// 404 的一致性本身就是安全属性（code-review 2026-10-22）：无 admin 类
// 的 404 与"路径真不存在"的 404 必须**字节级不可区分**——两者都走
// errors.go 的 writeError("not_found")，形状/头部同源；admin_test.go 里
// 有这条断言，它是 §2.2 的唯一可执行副本。
//
// 危险写（§2.3）：删/禁账户、改组、改抽象模型、改弃用、封禁凭据、人工
// 改判、危险设置——**只是界面二次确认**，API 层不做额外标记、不要求
// 额外请求头。所以本文件里没有任何 "dangerous" 分支。
//
// 审计与业务写**同事务**（06-audit §5 的延伸纪律，code-review 点名）：
// 每个写方法收 AdminAuditEntry 入参、回 auditID——桥在 store.WithTx 里
// 先业务后审计一并提交；handler 单独 Append 会留下"改成了但没记上"的
// 崩溃窗口。例外只有导入批次汇总（多条事务后的统计行，允许单独 Append）。
//
// 写域纪律：本文件（及 admin_*.go）不碰 server.go——挂载经 AttachAdmin +
// MountAdminRoutes 由装配层（internal/app）调用，模式与 v1Registry/AttachV1
// 相同（指针键 registry，测试并行互不污染）。

package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
)

// ---------------------------------------------------------------------------
// 依赖注入面（Lead 在 internal/app 装配）
// ---------------------------------------------------------------------------

// AdminDeps 是管理面全部依赖的注入包。
//
// 为什么各 Store 字段都是窄接口而不是具体仓储：沿用 auth 的 storeiface
// 纪律——httpapi 只声明"管理面需要什么能力"，SQL/表结构属于 storage 的
// 领域；两处各写一份必然漂移。装配层把 store 仓储钉到这些接口上。
//
// 允许为 nil 的可选字段（Refresh/UpstreamTest/SettingsView/SettingsPatch/
// IPCheck）：与 auth.Stores 同理——nil 表达"这条路径未装配"，用到时统一
// 返回 404 + not_found（lead 裁决：未上线的功能不确认其存在，与 §2.2 同一
// 哲学），而不是 panic；强制实现空方法只会诱导出假成功的假体。
type AdminDeps struct {
	// Sess 是会话校验器（与控制台复用同一个 SessionManager——同一套
	// 会话存储与 TTL；admin 检查发生在 Validate 之后）。
	Sess *auth.SessionManager
	// Hasher 用于账户创建与重置密码（argon2id，参数包内定案）。
	Hasher *auth.PasswordHasher

	Accounts      AdminAccountsStore
	Groups        AdminGroupsStore
	Keys          AdminKeyCountStore
	Credentials   AdminCredentialStore
	Models        AdminModelStore
	Upstreams     AdminUpstreamStore
	Verifications AdminVerificationStore
	Audit         AdminAuditStore
	Inbox         AdminInboxStore

	// Refresh 是真实模型目录刷新触发器（POST /real-models/refresh）。
	Refresh AdminModelRefresher
	// UpstreamTest 是上游连通性测试（POST /upstreams/{id}/test）。
	UpstreamTest AdminUpstreamTester
	// SettingsView 提供 GET /settings 的分组视图。
	SettingsView AdminSettingsReader
	// SettingsPatch 应用 PATCH /settings（业务写与审计写同事务在桥内）。
	SettingsPatch AdminSettingsPatcher
	// IPCheck 自检当前源 IP 的配置归属（POST /settings/ip-check）。
	IPCheck AdminIPChecker
}

// AttachAdmin 把管理面依赖挂到 Server（装配层调用一次；测试注入假体走
// 同一入口）。
func (s *Server) AttachAdmin(deps *AdminDeps) {
	adminRegistry[s] = deps
}

// adminRegistry 是 Server 实例 → 管理面依赖的注册表（与 v1Registry 同型：
// Server 结构在 server.go 属不可改域，指针键避免全局单例互相覆盖）。
var adminRegistry = map[*Server]*AdminDeps{}

func (s *Server) adminDeps() *AdminDeps { return adminRegistry[s] }

// AdminMiddleware 导出管理面中间件闭包（装配层要把 admin 检查复用到
// 自定义路由组时用它拿闭包）。
func (s *Server) AdminMiddleware() func(http.Handler) http.Handler {
	return s.mwAdmin()
}

// MountAdminRoutes 在给定路由器上注册 /api/admin/* 全套端点。
//
// 为什么单独导出一个 Mount 而不是塞进 SetConsoleRoutes 回调体：管理面
// 端点数量是控制台的十倍，混在一个回调里装配 diff 不可审。Lead 在
// MountConsoleRoutes 末尾加一行 `a.server.MountAdminRoutes(r)` 即完成挂载。
// 路由组整体挂 mwAdmin——"漏挂一个端点"是这类面最常见的失效模式，
// 整组挂杜绝逐个登记；admin_test.go 用路由表穷举兜底。
func (s *Server) MountAdminRoutes(r chi.Router) {
	deps := s.adminDeps()
	r.Route("/api/admin", func(ar chi.Router) {
		ar.Use(s.mwAdmin())
		s.mountAdminAccounts(ar, deps)
		s.mountAdminGroups(ar, deps)
		s.mountAdminModels(ar, deps)
		s.mountAdminOps(ar, deps)
	})
}

// mwAdmin 是管理面中间件：CSRF 自定义头 → 会话校验 → admin 类检查。
//
// 三道各自的失败语义（顺序即成本顺序，最便宜的判定先跑）：
//   - 缺自定义头（非幂等方法）→ 403 forbidden：请求本身不合法；
//   - 会话 Cookie 缺失/失效 → 401 session_expired：与控制台面同码；
//   - 会话有效但 classes 不含 admin → **404 not_found**（§2.2，不是 403）。
//
// admin 检查为什么放在这里而不是每个 handler 第一行：一层检查就该只
// 有一个执行点；散在 handler 里等于给"漏挂检查"制造一百个机会。
func (s *Server) mwAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deps := s.adminDeps()
			if deps == nil || deps.Sess == nil {
				// 整个管理面未接线（lead 忘了 AttachAdmin）：按 §2.2 哲学
				// 对外统一 404（不确认管理面存在），真因只进服务器日志。
				s.log.Error("admin surface used without AttachAdmin")
				writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
				return
			}
			// CSRF 第二道（与 mwSession 同一判据：SameSite=Lax + 自定义头）。
			if needsCSRFCheck(r.Method) && !hasCustomHeader(r) {
				writeError(w, http.StatusForbidden, "forbidden",
					"缺少自定义请求头（跨站请求不接受）", nil)
				return
			}
			c, err := r.Cookie(SessionCookieName)
			if err != nil || c.Value == "" {
				writeError(w, http.StatusUnauthorized, "session_expired",
					"会话不存在或已过期，请重新登录", nil)
				return
			}
			// actingClass 传 "admin"：Validate 会确认账户真的持有该类，
			// 主体快照里的 ActingClass 即审计的 acting_class 字段来源。
			p, verr := deps.Sess.Validate(r.Context(), c.Value, string(auth.ClassAdmin))
			if verr != nil {
				var ae *auth.Error
				if asAuthError(verr, &ae) && ae.Code == auth.CodeInternal {
					writeError(w, http.StatusInternalServerError,
						string(auth.CodeInternal), ae.Msg, nil)
					return
				}
				// 账户不持有 admin 类 → Validate 按 CodeForbidden 拒绝。
				// 管理面对外统一 404：与"路径不存在"的 404 同源同形状
				// （同一个 writeError 调用点、同一个码），不确认路径存在（§2.2）。
				if asAuthError(verr, &ae) && ae.Code == auth.CodeForbidden {
					writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
					return
				}
				writeError(w, http.StatusUnauthorized, "session_expired",
					"会话不存在或已过期，请重新登录", nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(
				r.Context(), ctxKeySessionPrincipal, p)))
		})
	}
}

// ---------------------------------------------------------------------------
// 管理面 DTO（对外 JSON 形状的承载体；字段名即契约，见分册各节）
// ---------------------------------------------------------------------------

// AdminQuota 是账户级额度（02-admin §5.2 的单个对象；used 只读）。
type AdminQuota struct {
	Unit   string `json:"unit"`   // request / token / credit
	Limit  int64  `json:"limit"`  // 0 = 零额度全拒（与"没有该行=不限"互斥）
	Used   int64  `json:"used"`   // 只读：由结算写入
	Period string `json:"period"` // day / month
}

// AdminAccount 是账户行（管理面形状）。
//
// 为什么 ID 是 string：对外形状是稳定不透明的 id 字符串（auth 侧同样用
// string，桥在 internal/app 做 int64↔string 翻译，parseID 已是唯一点）。
// 结构里**没有 password_hash 字段**——让"把哈希传到列表路径"成为类型层
// 不可能（与 store 的 Account/AccountAuth 分形同理）。
// KeyCount/CredentialCount 只有数量没有列表（§5.2：这个端点不具备
// "一次拿到某人的全部资源"的能力）。
type AdminAccount struct {
	ID             string      `json:"id"`
	DisplayName    string      `json:"display_name"`
	Username       string      `json:"username"`
	Classes        []string    `json:"classes"`
	Status         string      `json:"status"`
	Groups         []string    `json:"groups"`
	CreatedAt      int64       `json:"-"` // 渲染层转 RFC3339
	LastLoginAt    *int64      `json:"-"`
	Note           string      `json:"note,omitempty"`
	RPMLimit       int64       `json:"rpm_limit"`       // -1 不限 / 0 全拒
	TPMLimit       int64       `json:"tpm_limit"`       // 同上
	MaxConcurrency int64       `json:"max_concurrency"` // 同上
	Quota          *AdminQuota `json:"quota,omitempty"` // 合计行；无行=不限则缺省

	KeyCount        int64 `json:"key_count"`
	CredentialCount int64 `json:"credential_count"`
}

// AdminAccountOut 是账户的对外渲染形状（时间 RFC3339 化）。
type AdminAccountOut struct {
	AdminAccount
	CreatedAt   any `json:"created_at"`
	LastLoginAt any `json:"last_login_at"`
	// GeneratedPassword 只在创建/重置密码且本轮生成时出现（一次性回显，
	// §5.3：不返回就等于这批账户全部不可登录）；omitempty 保证常规读
	// 响应里没有这个键。
	GeneratedPassword string `json:"generated_password,omitempty"`
}

func renderAdminAccount(a *AdminAccount) AdminAccountOut {
	return AdminAccountOut{
		AdminAccount: *a,
		CreatedAt:    adminJSONTime(&a.CreatedAt),
		LastLoginAt:  adminJSONTime(a.LastLoginAt),
	}
}

// AdminAccountUpdate 是 PATCH 的局部更新载荷。
//
// 为什么区分 nil 与零值：§3.1 定 PATCH 是局部更新，未提供的字段保持
// 原值——*int64 的 nil（没给）与 0（设成全拒）语义完全相反，压成一个
// 值就会把"设成 0"读成"没改"。
// Classes 用 *[]string：给 [] 与不给必须可区分（空数组是"清空权限类"，
// 违反非空不变式要 400；不给是不动）。
type AdminAccountUpdate struct {
	DisplayName    *string     `json:"display_name,omitempty"`
	Classes        *[]string   `json:"classes,omitempty"`
	Status         *string     `json:"status,omitempty"`
	Groups         *[]string   `json:"groups,omitempty"`
	Note           *string     `json:"note,omitempty"`
	Quota          *AdminQuota `json:"quota,omitempty"`
	RPMLimit       *int64      `json:"rpm_limit,omitempty"`
	TPMLimit       *int64      `json:"tpm_limit,omitempty"`
	MaxConcurrency *int64      `json:"max_concurrency,omitempty"`
}

// AdminAccountFilter 是列表筛选（§3.1：显式参数，不做过滤器语言）。
type AdminAccountFilter struct {
	Class   string
	Status  string
	GroupID string
	Limit   int // 缺省 50、硬上限 500（adminLimit 已夹好）
	Offset  int // cursor 的翻译点（cursor = base64(offset)）
}

// ---------------------------------------------------------------------------
// 窄接口（装配层的注入契约——Lead 照着在 internal/app 桥到 store 仓储）
// ---------------------------------------------------------------------------

// 审计入参约定：写方法收的 AdminAuditEntry 由 handler 填基础字段
// （AccountID/ActingClass/Action/Target/Result/IP/UA/Detail 基线），
// 桥在**同一事务**里把旧值→新值差异补进 Detail 后落 audit_log，并把
// 最终行 id 作为 auditID 回传（§3.2 的 audit_id 写进响应）。
// Result 传 "ok"：业务失败时桥不写审计直接返回错误。

// AdminAccountsStore 是账户面读写（02-admin §5）。
//
// 为什么没有 DELETE：§5.1 定"没有删除账户端点"——requests/audit_log
// 引用 account_id，物理删除让历史悬空；禁用/合并是状态迁移。
// ResetPassword 收哈希而不是明文：hashing 走 deps.Hasher（协议层持有），
// "存储只拿哈希"这条约束由接口形状保证。
type AdminAccountsStore interface {
	// ListAccounts 按筛选返回 (行, 总数)。行内 Quota/KeyCount/CredentialCount
	// 由实现决定是否填充（handler 会补查 Keys/Credentials 计数，实现留零值
	// 即可）——两处都填会让桥的错误被掩盖，选一条并保持。
	ListAccounts(ctx context.Context, f AdminAccountFilter) ([]AdminAccount, int64, error)
	// GetAccount 按 id 取行；不存在返回 ErrAdminAccountNotFound。
	GetAccount(ctx context.Context, id string) (*AdminAccount, error)
	// CreateAccount 建账户 + 权限类 + 审计（单事务）。passwordHash 已是
	// argon2id 自描述串。用户名冲突返回 ErrAdminConflict。
	CreateAccount(ctx context.Context, in AdminAccount, passwordHash string, audit AdminAuditEntry) (id string, auditID string, err error)
	// UpdateAccount 局部更新（nil 字段不动），业务写与审计写同事务。
	UpdateAccount(ctx context.Context, id string, in AdminAccountUpdate, audit AdminAuditEntry) (*AdminAccount, string, error)
	// ResetPassword 写新哈希；revokeSessions=true 时同一事务吊销全部会话
	// （改密吊销会话是 02-authn 的联动定案；联动数量进审计 Detail）。
	ResetPassword(ctx context.Context, id, passwordHash string, revokeSessions bool, audit AdminAuditEntry) (sessionsRevoked int, auditID string, err error)
	// RevokeSessions 吊销该账户全部会话（与审计同事务），返回吊销数。
	RevokeSessions(ctx context.Context, id string, audit AdminAuditEntry) (int, string, error)
}

// ErrAdminAccountNotFound 是账户面未命中哨兵（→ 404 not_found）。
var ErrAdminAccountNotFound = errors.New("admin-api: account not found")

// ErrAdminConflict 是通用冲突哨兵（唯一约束/状态冲突 → 409 conflict）。
var ErrAdminConflict = errors.New("admin-api: conflict")

// ErrAdminDefaultGroupProtected 默认组保护（03-groups §5：删组不删人的
// 落点必须存在）。桥用 errors.Is 包装 store 的同名哨兵。
var ErrAdminDefaultGroupProtected = errors.New("admin-api: default group protected")

// ErrAdminNotWired 是桥可选用的哨兵：能力存在但未接线 → 404 not_found
// （lead 裁决 2026-10-22：未上线一律不确认存在，与真 404 逐字节同形）。
// 注：03-errors §4 没有 501 行，501+invalid_request 的语义错配已由裁决消灭。
var ErrAdminNotWired = errors.New("admin-api: capability not wired")

// AdminKeyCountStore 是 Key 数量读（列表只给数量——§5.2）。
type AdminKeyCountStore interface {
	CountKeys(ctx context.Context, accountID string) (int64, error)
}

// ---------------------------------------------------------------------------
// 组面（§6）
// ---------------------------------------------------------------------------

// AdminGroup 是组行。
type AdminGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsDefault   bool   `json:"is_default"`
	MemberCount int64  `json:"member_count"`
	CreatedAt   int64  `json:"-"`
}

// AdminGroupOut 是组的对外渲染形状。
type AdminGroupOut struct {
	AdminGroup
	CreatedAt any `json:"created_at"`
}

func renderAdminGroup(g *AdminGroup) AdminGroupOut {
	return AdminGroupOut{AdminGroup: *g, CreatedAt: adminJSONTime(&g.CreatedAt)}
}

// AdminGroupDetail 是组详情：成员、模型授权、策略（§6.1 字段形状）。
type AdminGroupDetail struct {
	AdminGroup
	Members []string `json:"members"`
	// Models 是抽象模型名（契约例子给的是 "smart"/"fast" 这类对外名）。
	Models []string `json:"models"`
	// Policies 是组策略文档原文（§6.1 结构：allow_create_key/max_keys/
	// account_quotas/model_quota_caps/pool…）。存储侧怎么摊平由桥决定，
	// API 层原样进出——策略键语义是组域知识（auth.GroupPolicy），
	// 在 httpapi 再摊一次就是两处口径必然漂移。
	Policies json.RawMessage `json:"policies"`
}

// AdminGroupsStore 是组读写面（§6）。
//
// 为什么模型授权与策略用整文档替换（Replace*）：§6 定"PUT 整体替换"
// ——合并语义对整个集合求值，局部增删会让生效值依赖难以复盘的历史。
// 替换是**全量**语义：空集合 = 清空。
type AdminGroupsStore interface {
	ListGroups(ctx context.Context, limit, offset int) ([]AdminGroup, int64, error)
	GetGroup(ctx context.Context, id string) (*AdminGroupDetail, error)
	// CreateGroup 建组；name 冲突返回 ErrAdminConflict。
	// models/policies 可空（原样传桥：桥负责把抽象模型名解析成 id）。
	CreateGroup(ctx context.Context, g AdminGroup, models []string, policies json.RawMessage, audit AdminAuditEntry) (id string, auditID string, err error)
	// UpdateGroup 改基本信息（name/description/is_default）。is_default
	// 置 true 时若已有别的默认组，桥返回 ErrAdminConflict（部分唯一索引）。
	UpdateGroup(ctx context.Context, id string, name, description *string, setDefault *bool, audit AdminAuditEntry) (*AdminGroup, string, error)
	// DeleteGroup 删组，成员退回默认组；退回成员数由桥补进审计 Detail。
	// 默认组返回 ErrAdminDefaultGroupProtected。
	DeleteGroup(ctx context.Context, id string, audit AdminAuditEntry) (string, error)
	// DuplicateGroup 复制组（含模型授权与策略文档）。新组**不**继承
	// is_default（复制出的永远是普通组）。
	DuplicateGroup(ctx context.Context, id, newName string, audit AdminAuditEntry) (*AdminGroup, string, error)

	ListGroupMembers(ctx context.Context, groupID string) ([]string, error)
	AddGroupMembers(ctx context.Context, groupID string, accountIDs []string, audit AdminAuditEntry) (string, error)
	RemoveGroupMember(ctx context.Context, groupID, accountID string, audit AdminAuditEntry) (string, error)
	// ReplaceGroupModels 替换模型授权集合（抽象模型名）；旧集合并进审计
	// Detail（06-audit §3 改策略记旧值/新值）。
	ReplaceGroupModels(ctx context.Context, groupID string, modelNames []string, audit AdminAuditEntry) (old []string, auditID string, err error)
	// ReplaceGroupPolicies 替换策略文档；旧文档原文进审计 Detail。
	ReplaceGroupPolicies(ctx context.Context, groupID string, doc json.RawMessage, audit AdminAuditEntry) (old json.RawMessage, auditID string, err error)
}

// ---------------------------------------------------------------------------
// 模型面（§7 真实模型 / §8 抽象模型）
// ---------------------------------------------------------------------------

// AdminRealModel 是真实模型行（§7.1）。
//
// QualifiedName 只读（§7：命名由上游目录决定，改写会让 missing 巡检
// 失去判据）。Modalities/Efforts 用 RawMessage 原样进出：它们是 JSON
// 数组但元素集合随上游演化，API 层不摊成 []string——摊平即过滤，
// 过滤即静默丢信息。
type AdminRealModel struct {
	ID                     string          `json:"id"`
	DisplayName            string          `json:"display_name"`
	QualifiedName          string          `json:"qualified_name"`
	UpstreamID             string          `json:"upstream_id"`
	UpstreamModelID        string          `json:"upstream_model_id"`
	GroupName              *string         `json:"group_name"`
	Channel                *string         `json:"channel,omitempty"`
	ContextWindow          *int64          `json:"context_window"`
	Modalities             json.RawMessage `json:"modalities"`
	Efforts                json.RawMessage `json:"efforts"`
	Status                 string          `json:"status"` // active/missing/disabled
	Hidden                 bool            `json:"hidden"`
	SortOrder              int64           `json:"sort_order"`
	Source                 string          `json:"source"` // auto / override
	CreditMultiplier       json.RawMessage `json:"credit_multiplier,omitempty"`
	LastSeenAt             *int64          `json:"-"`
	MissingSince           *int64          `json:"-"`
	ExcludedFromCandidates bool            `json:"excluded_from_candidates"`
}

// AdminRealModelOut 是真实模型的对外渲染形状。
type AdminRealModelOut struct {
	AdminRealModel
	LastSeenAt   any `json:"last_seen_at"`
	MissingSince any `json:"missing_since"`
}

func renderAdminRealModel(m *AdminRealModel) AdminRealModelOut {
	return AdminRealModelOut{
		AdminRealModel: *m,
		LastSeenAt:     adminJSONTime(m.LastSeenAt),
		MissingSince:   adminJSONTime(m.MissingSince),
	}
}

// AdminAbstractModel 是抽象模型行（§8.1）。
//
// Strategy 与 Capabilities 保持 JSON 原文：存储侧 strategy 是 TEXT 列
// （数组还是逗号分隔由桥定），API 层进出都是 JSON 数组；能力声明同理。
// Name 创建后不可改（§8.1 定案，PATCH 载荷里出现 name 由
// DisallowUnknownFields 直接 400）。
type AdminAbstractModel struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name"`
	CapabilityMode  string          `json:"capability_mode"` // strict / filter
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Strategy        json.RawMessage `json:"strategy"`
	Status          string          `json:"status"` // active/deprecated/disabled
	ReplacementName *string         `json:"replacement"`
	DeprecatedAt    *int64          `json:"-"`
	SunsetAt        *int64          `json:"-"`
	MigrationDoc    *string         `json:"migration_doc,omitempty"`
	Note            *string         `json:"note,omitempty"`

	// 计数是响应侧字段（§8.1：都不可写；available_candidate_count 是
	// "声明式候选 → 可用凭据实例"的展开结果）。
	CandidateCount          int64 `json:"candidate_count"`
	AvailableCandidateCount int64 `json:"available_candidate_count"`
}

// AdminAbstractModelOut 是抽象模型的对外渲染形状。
type AdminAbstractModelOut struct {
	AdminAbstractModel
	DeprecatedAt any `json:"deprecated_at"`
	SunsetAt     any `json:"sunset_at"`
	Chain        any `json:"chain"` // §8.1 例子里的恒 null 位（链走 /chain）
}

func renderAdminAbstractModel(m *AdminAbstractModel) AdminAbstractModelOut {
	return AdminAbstractModelOut{
		AdminAbstractModel: *m,
		DeprecatedAt:       adminJSONTime(m.DeprecatedAt),
		SunsetAt:           adminJSONTime(m.SunsetAt),
		Chain:              nil,
	}
}

// AdminCandidateItem 是抽象模型的一个候选（§8.2）。
type AdminCandidateItem struct {
	RealModelID string          `json:"real_model_id"`
	Constraint  json.RawMessage `json:"constraint,omitempty"`
	Weight      int64           `json:"weight"`
	// AvailableInstances 是运行时展开的可用凭据实例数——**响应字段，
	// 请求里给了也忽略**（§8.2：它由运行时状态决定，不可写）。
	AvailableInstances int64 `json:"available_instances"`
}

// AdminDeprecationUpdate 是 PUT /abstract-models/{id}/deprecation 的载荷。
//
// ReplacementName 对应契约字段 replacement_model_id：§8.4 例子给的值是
// "am_smart"（对外名形态），API 层按"先当名字查、查不到再当 id 查"解析。
type AdminDeprecationUpdate struct {
	Status          string `json:"status"` // 本轮只接受 "deprecated"（弃用单向）
	ReplacementName string `json:"replacement_model_id"`
	DeprecatedAt    *int64 `json:"deprecated_at,omitempty"`
	SunsetAt        *int64 `json:"sunset_at,omitempty"`
	MigrationDoc    string `json:"migration_doc,omitempty"`
	Note            string `json:"note,omitempty"`
}

// AdminModelStore 是模型目录读写（§7/§8）。
//
// 为什么弃用三条硬校验在 handler 而不是桥：校验用的能力逐项 ≥ 与链
// 游走逻辑同 dry-run/chain 预览共享一份实现——放在 httpapi 一处，
// chain 端点与保存端点必然同判据；桥只保证落库原子（+审计同事务）。
type AdminModelStore interface {
	// --- 真实模型 ---
	ListRealModels(ctx context.Context, status string, hidden *bool, source, upstreamID string, limit, offset int) ([]AdminRealModel, int64, error)
	GetRealModel(ctx context.Context, id string) (*AdminRealModel, error)
	// UpdateRealModel 局部改（载荷为请求认识的键：display_name/group_name/
	// context_window/modalities/efforts/hidden/sort_order/credit_multiplier）。
	// 保存后行上 source 必须变 override（§7.1 定案——由桥保证，handler 审计）。
	UpdateRealModel(ctx context.Context, id string, patch map[string]json.RawMessage, audit AdminAuditEntry) (*AdminRealModel, string, error)
	// SetRealModelExcluded 排除/恢复候选资格（POST exclude 的两态）。
	SetRealModelExcluded(ctx context.Context, id string, excluded bool, audit AdminAuditEntry) (*AdminRealModel, string, error)

	// --- 抽象模型 ---
	ListAbstractModels(ctx context.Context, limit, offset int) ([]AdminAbstractModel, int64, error)
	GetAbstractModel(ctx context.Context, id string) (*AdminAbstractModel, error)
	// GetAbstractModelByName 按对外名取行（链游走与弃用校验用；不存在
	// 返回 ErrAdminModelNotFound）。
	GetAbstractModelByName(ctx context.Context, name string) (*AdminAbstractModel, error)
	// CreateAbstractModel：name 冲突返回 ErrAdminConflict。
	CreateAbstractModel(ctx context.Context, m AdminAbstractModel, candidates []AdminCandidateItem, audit AdminAuditEntry) (id string, auditID string, err error)
	// UpdateAbstractModel 局部改（name/status 不在载荷里——name 创建后
	// 不可改；status 走 SetAbstractStatus）。freshDryRun 非 nil 时桥把它
	// 落行（§8.3 干跑新鲜度；存储落点由桥定，本轮桥可忽略并回报）。
	UpdateAbstractModel(ctx context.Context, id string, patch map[string]json.RawMessage, freshDryRun json.RawMessage, audit AdminAuditEntry) (*AdminAbstractModel, string, error)
	// SetAbstractStatus 启用/禁用（deprecated→active 的不可逆由 handler
	// 判——弃用单向，§8.1；桥纯落）。
	SetAbstractStatus(ctx context.Context, id, status string, audit AdminAuditEntry) (string, error)
	// SetDeprecation 保存弃用配置（API 层已完成三条硬校验，桥只管落库：
	// status/deprecated_at/sunset_at/migration_doc/note + 替代链边）。
	SetDeprecation(ctx context.Context, id string, in AdminDeprecationUpdate, audit AdminAuditEntry) (string, error)

	// --- 候选集 ---
	ListCandidates(ctx context.Context, abstractModelID string) ([]AdminCandidateItem, error)
	// ReplaceCandidates 整表替换（PUT 全量语义）；旧集合进审计 Detail。
	ReplaceCandidates(ctx context.Context, abstractModelID string, cands []AdminCandidateItem, audit AdminAuditEntry) (old []AdminCandidateItem, auditID string, err error)

	// ReferencingGroups 返回把该抽象模型（按名）授权进了集合的组。
	ReferencingGroups(ctx context.Context, name string) ([]AdminGroup, error)

	// CapabilitySnapshot 返回"模型名 → 能力声明文档"的全量快照，供
	// chain 预览与弃用校验（比较逻辑在 httpapi 的 adminCapGE，桥只给数据）。
	CapabilitySnapshot(ctx context.Context) (map[string]json.RawMessage, error)
}

// AdminModelRefresher 是目录刷新触发（POST /real-models/refresh）。
//
// 为什么是窄触发器而不是把 upstream.refresh 搬进 httpapi：刷新归
// upstream 域（sidecar models.list + upsert），管理面只负责"人点了一下"。
type AdminModelRefresher interface {
	RefreshModels(ctx context.Context) (map[string]any, error)
}

// ---------------------------------------------------------------------------
// 上游与凭据（§9）
// ---------------------------------------------------------------------------

// AdminUpstream 是上游行。
type AdminUpstream struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"` // relay / vendor / selfhosted
	Name           string  `json:"name"`
	BaseURL        string  `json:"base_url"`
	Enabled        bool    `json:"enabled"`
	OwnerAccountID *string `json:"owner_account_id,omitempty"`
	LastSeenAt     *int64  `json:"-"`
	// SecretPresent 是"这条上游在 upstream_secrets 里有密文行"的布尔；
	// **永不返回密文或引用串本身**（06-audit §2 禁止项）。
	SecretPresent bool `json:"secret_present"`
}

// AdminUpstreamOut 是上游的对外渲染形状。
type AdminUpstreamOut struct {
	AdminUpstream
	LastSeenAt any `json:"last_seen_at"`
}

func renderAdminUpstream(u *AdminUpstream) AdminUpstreamOut {
	return AdminUpstreamOut{AdminUpstream: *u, LastSeenAt: adminJSONTime(u.LastSeenAt)}
}

// AdminUpstreamUpdate 是 PATCH /upstreams/{id} 的载荷（**不含 kind**——
// kind 变更走独立端点 §9；载荷里出现 kind 会被 DisallowUnknownFields 拒）。
type AdminUpstreamUpdate struct {
	Name    *string `json:"name,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

// AdminUpstreamStore 是上游读写。
//
// 为什么 kind 与 base_url 是方法层强制的两条通道：§9 定 kind 变更单独
// 成端点并标危险——它改变"这条上游怎么被调用"；并进 UpdateUpstream 会
// 让 PATCH 一条 SQL 就换掉调用路径。
type AdminUpstreamStore interface {
	ListUpstreams(ctx context.Context, limit, offset int) ([]AdminUpstream, int64, error)
	GetUpstream(ctx context.Context, id string) (*AdminUpstream, error)
	// CreateUpstream：kind/base_url 组合校验在 handler 完成（relay 禁
	// 端点形态的 base_url、vendor/selfhosted 必填——§9 表），桥落库 +
	// (kind,name) 唯一，冲突返回 ErrAdminConflict。
	CreateUpstream(ctx context.Context, u AdminUpstream, audit AdminAuditEntry) (id string, auditID string, err error)
	UpdateUpstream(ctx context.Context, id string, in AdminUpstreamUpdate, audit AdminAuditEntry) (*AdminUpstream, string, error)
	// SetUpstreamKind 变更调用形态；改 relay 时桥负责把 base_url 改写为
	// 定案值 sidecar://<name>（0001 NOT NULL + task-12 定案），改出 relay
	// 时若现值是定案值则清空并由桥报错要求先补端点（不静默留个 sidecar://）。
	SetUpstreamKind(ctx context.Context, id, kind string, audit AdminAuditEntry) (*AdminUpstream, string, error)
	// SetUpstreamSecret 写入/替换该上游的密钥材料（AES-GCM 加密入库）。
	// 为什么不在 CreateUpstream 里顺带写：改名/改端点与改密钥是两类操作，
	// 混在一个方法里会让「只改名字」也触发密钥重写；且创建时通常还没有 id。
	SetUpstreamSecret(ctx context.Context, id, plaintext string, audit AdminAuditEntry) (auditID string, err error)
}

// AdminUpstreamTester 是连通性测试（装配 upstream 层的探测能力）。
type AdminUpstreamTester interface {
	TestUpstream(ctx context.Context, id string) (map[string]any, error)
}

// AdminCredential 是凭据行（管理端形状）。
//
// **没有 secret_ref / plaintext / key 任何字段的对位**：§9 定"没有任何
// 端点返回凭据明文"；引用串虽不含明文，泄露引用形态等于泄露本地安全
// 存储的结构（06-audit §2 禁止项精神）。ExternalID 保留：它是去重判据
// （UNIQUE(provider, external_id)），管理员处置冲突需要它。
type AdminCredential struct {
	ID             string          `json:"id"`
	OwnerAccountID string          `json:"owner_account_id"`
	Provider       string          `json:"provider"`
	ExternalID     string          `json:"external_id"`
	ExternalLabel  string          `json:"external_label,omitempty"`
	Status         string          `json:"status"` // pending/active/invalid/expired/disabled
	LoginEnabled   bool            `json:"login_enabled"`
	VerifyStatus   string          `json:"verify_status"`
	VerifyDetail   json.RawMessage `json:"verify_detail,omitempty"`
	CreatedAt      int64           `json:"-"`
	LastUsedAt     *int64          `json:"-"`
}

// AdminCredentialOut 是凭据的对外渲染形状。
type AdminCredentialOut struct {
	AdminCredential
	CreatedAt  any `json:"created_at"`
	LastUsedAt any `json:"last_used_at"`
}

func renderAdminCredential(c *AdminCredential) AdminCredentialOut {
	return AdminCredentialOut{
		AdminCredential: *c,
		CreatedAt:       adminJSONTime(&c.CreatedAt),
		LastUsedAt:      adminJSONTime(c.LastUsedAt),
	}
}

// AdminCredentialFilter 是凭据列表筛选（owner/provider/verify_status/status）。
type AdminCredentialFilter struct {
	OwnerAccountID string
	Provider       string
	VerifyStatus   string
	Status         string
	Limit          int
	Offset         int
}

// AdminCredentialStore 是凭据管理面读写。
//
// 为什么 suspend/ban 都落 "disabled"：upstream_credentials.status 的
// CHECK 枚举没有 "banned"（0001_init）——封禁的运维语义（不再使用、
// 可逆、保留 owner 归因）与 disabled 一致；两端的区别在**审计 action**
// （credential.suspend / credential.ban），界面与告警看得到差别。
// 值域定案已列入回报（若 task-19 扩枚举，桥改映射即可，API 形状不动）。
type AdminCredentialStore interface {
	// CountCredentials 给账户列表的 credential_count（§5.2 只给数量）。
	CountCredentials(ctx context.Context, ownerAccountID string) (int64, error)
	ListCredentials(ctx context.Context, f AdminCredentialFilter) ([]AdminCredential, int64, error)
	GetCredential(ctx context.Context, id string) (*AdminCredential, error)
	// SetCredentialStatus 改状态；桥必须校验值域（pending/active/invalid/
	// expired/disabled 之一），旧值进审计 Detail。
	SetCredentialStatus(ctx context.Context, id, status string, audit AdminAuditEntry) (*AdminCredential, string, error)
	// NotifyCredential 给贡献者发站内通知（经桥落到贡献者的消息通道；
	// MVP 至少写审计——通知文本可能含用户内容，桥只存摘要/长度由实现
	// 定案（06-audit §4 脱敏线）；已列入回报）。
	NotifyCredential(ctx context.Context, id, message string, audit AdminAuditEntry) (string, error)
}

// ---------------------------------------------------------------------------
// 鉴定（§10）
// ---------------------------------------------------------------------------

// AdminVerification 是鉴定队列条目。
//
// 为什么贴着凭据行而不是 §10.1 全量文档：鉴定的落库位置是
// upstream_credentials.verify_status/verify_detail（storage task-15 定案），
// stage/stage_results/recheck 列不存在。API 层不发明存储没有的东西——
// Source/Stage/StageResults/Recheck 留 omitempty 可选位，等存储补齐后
// 由桥填；当前实现给核心字段即可满足队列与改判。
type AdminVerification struct {
	ID           string          `json:"id"`
	CredentialID string          `json:"credential_id"`
	Provider     string          `json:"provider"`
	ClaimedModel string          `json:"claimed_model,omitempty"`
	Source       string          `json:"source,omitempty"`
	Stage        string          `json:"stage,omitempty"`
	Verdict      string          `json:"verdict"` // verified/suspect/unverified/trusted
	Evidence     json.RawMessage `json:"evidence,omitempty"`
	Override     json.RawMessage `json:"override,omitempty"`
	StageResults json.RawMessage `json:"stage_results,omitempty"`
	Recheck      json.RawMessage `json:"recheck,omitempty"`
}

// AdminVerificationFilter 是队列筛选（verdict/provider/source）。
type AdminVerificationFilter struct {
	Verdict  string
	Provider string
	Source   string
	Limit    int
	Offset   int
}

// AdminVerificationStore 是鉴定管理面。
type AdminVerificationStore interface {
	ListVerifications(ctx context.Context, f AdminVerificationFilter) ([]AdminVerification, int64, error)
	GetVerification(ctx context.Context, id string) (*AdminVerification, error)
	// OverrideVerification 人工改判：四态之一 + 必填理由 + 操作人落
	// override 记录（含时间）；桥把结论写 verify_status 并回改判后的
	// 条目，新旧状态进审计 Detail（06-audit §3 鉴定结论变化）。
	OverrideVerification(ctx context.Context, id, verdict, reason, operatorAccountID string, audit AdminAuditEntry) (*AdminVerification, string, error)
}

// ---------------------------------------------------------------------------
// 审计（§11）
// ---------------------------------------------------------------------------

// AdminAuditEntry 是审计行（管理面形状；detail 原文进出）。
type AdminAuditEntry struct {
	ID          string          `json:"id"`
	TS          int64           `json:"-"`
	AccountID   string          `json:"account_id,omitempty"` // 空 = 身份未确定时的事件
	ActingClass string          `json:"acting_class,omitempty"`
	Action      string          `json:"action"`
	Target      *string         `json:"target"`
	IP          string          `json:"ip,omitempty"`
	UA          string          `json:"ua,omitempty"`
	Result      string          `json:"result"` // ok / denied / error
	Detail      json.RawMessage `json:"detail,omitempty"`
}

// AdminAuditOut 是审计行的对外渲染形状。
type AdminAuditOut struct {
	AdminAuditEntry
	TS any `json:"ts"`
}

func renderAdminAudit(e *AdminAuditEntry) AdminAuditOut {
	return AdminAuditOut{AdminAuditEntry: *e, TS: adminJSONTime(&e.TS)}
}

// AdminAuditFilter 是审计查询（from/to/account_id/acting_class/action/result）。
type AdminAuditFilter struct {
	FromTS      int64
	ToTS        int64
	AccountID   string
	ActingClass string
	Action      string // 前缀匹配（"group." 列全部组操作）
	Result      string
	Limit       int
	Offset      int
}

// AdminAuditStore 是审计读 + 独立追加（批次汇总用）。
//
// 为什么没有 Update/Delete 方法：06-audit §1 append-only 是绝对约束，
// 表上触发器物理禁止 UPDATE/DELETE——接口不声明，业务代码就碰不到。
type AdminAuditStore interface {
	Append(ctx context.Context, e AdminAuditEntry) (string, error)
	List(ctx context.Context, f AdminAuditFilter) ([]AdminAuditEntry, int64, error)
	Get(ctx context.Context, id string) (*AdminAuditEntry, error)
	// Export 按筛选导出；format ∈ {csv, jsonl}（§13 的格式待定项：
	// 先两种都支持、默认 jsonl，文档定稿后收敛——已列入回报）。
	Export(ctx context.Context, f AdminAuditFilter, format string) (rows []byte, filename string, err error)
}

// ---------------------------------------------------------------------------
// 设置（§11）
// ---------------------------------------------------------------------------

// AdminSettingsReader 提供 GET /settings 的分组视图（原文 JSON 文档：
// {"<分组>":{"<键>":<值>}}）。设置的结构化映射属于 config 域，httpapi
// 不复制一张会漂移的镜像。
type AdminSettingsReader interface {
	GetSettings(ctx context.Context) (json.RawMessage, error)
}

// AdminSettingsPatcher 应用 PATCH /settings（载荷是扁平键值对象；业务写
// 与审计写同事务在桥内，逐键旧值→新值进 Detail——06-audit §3 配置变更）。
// 未知键由 handler 先拦 400（认识的键集合来自 GetSettings 文档的键并集，
// 见 admin_ops.go），桥的拒绝是第二道。
type AdminSettingsPatcher interface {
	PatchSettings(ctx context.Context, patch map[string]json.RawMessage, audit AdminAuditEntry) (auditID string, err error)
}

// AdminIPCheckResult 是 ip-check 的自检结果。
type AdminIPCheckResult struct {
	SourceIP       string   `json:"source_ip"`
	Allowed        bool     `json:"allowed"`
	Reason         string   `json:"reason"`
	BehindProxy    bool     `json:"behind_proxy"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

// AdminIPChecker 用当前源 IP 自检（§11：自检与保存解耦，前端可先查再提）。
//
// 注：admin_ip_allowlist 不提供（2026-10-19 定案）。这里的 allowed 判据
// 是 trusted_proxies 配置形状 + behind_proxy 开关下"该源 IP 会不会被
// 正确解析"，不是允许列表。
type AdminIPChecker interface {
	CheckIP(ctx context.Context, sourceIP string) (*AdminIPCheckResult, error)
}

// ---------------------------------------------------------------------------
// 收件箱（§12）
// ---------------------------------------------------------------------------

// AdminInboxItem 是收件箱条目。
//
// ID 用 string：契约 §12 例子是 "inb_01J8..." 形态、库里是 INTEGER——
// 对外统一 string 让桥自由编码（已请 lead 裁决前按对外字符串处理）。
type AdminInboxItem struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Severity  string          `json:"severity"` // error/warning/info，默认降序
	Title     string          `json:"title"`
	Detail    json.RawMessage `json:"detail_json,omitempty"`
	RelatedID *string         `json:"related_id"`
	CreatedAt int64           `json:"-"`
	ReadAt    *int64          `json:"-"`
	ReadBy    *string         `json:"read_by"`
}

// AdminInboxOut 是收件箱的对外渲染形状。
type AdminInboxOut struct {
	AdminInboxItem
	CreatedAt any `json:"created_at"`
	ReadAt    any `json:"read_at"`
}

func renderAdminInbox(i *AdminInboxItem) AdminInboxOut {
	return AdminInboxOut{
		AdminInboxItem: *i,
		CreatedAt:      adminJSONTime(&i.CreatedAt),
		ReadAt:         adminJSONTime(i.ReadAt),
	}
}

// AdminInboxFilter 是收件箱筛选（kind/severity/read/时间）。
type AdminInboxFilter struct {
	Kind     string
	Severity string
	Read     *bool
	FromTS   int64
	ToTS     int64
	Limit    int
	Offset   int
}

// AdminInboxStore 是收件箱读写（**没有任何删除能力**——§12：它是运维
// 留痕，与审计同类；清理策略是 §13 待定项）。
//
// 为什么已读状态变更由桥连带写审计：06-audit §3.1 定案——"已读状态写
// admin_inbox 表，但'某人标为已读'这个动作写一条审计事件（inbox_read）"，
// 两者必须同事务，否则出现"标了没记"与"记了没标"。
type AdminInboxStore interface {
	// List 返回条目；实现方默认按 severity 降序（error>warning>info，§12）。
	List(ctx context.Context, f AdminInboxFilter) ([]AdminInboxItem, int64, error)
	// CountUnread 只数未读（/inbox/count 导航角标专用，命中索引）。
	CountUnread(ctx context.Context) (int64, error)
	Get(ctx context.Context, id string) (*AdminInboxItem, error)
	// SetRead 标记已读/未读（与 inbox_read 审计同事务）；readBy 是操作
	// 管理员账户 id（§12：read_by 记录是谁读的）。幂等：重复标已读不报错。
	SetRead(ctx context.Context, id string, read bool, readBy string, audit AdminAuditEntry) (*AdminInboxItem, string, error)
	// MarkReadBatch 按 id 集合批量标记（一条批次审计覆盖全部行，同事务）。
	MarkReadBatch(ctx context.Context, ids []string, readBy string, audit AdminAuditEntry) (updated int, auditID string, err error)
}

// ---------------------------------------------------------------------------
// 管理面共享工具
// ---------------------------------------------------------------------------

// adminWriteOK 写响应 {"data":..., "audit_id":...}（§3.2：每个写响应
// 带 audit_id，界面据此回指审计行）。auditID 为空时省略字段——桥写审计
// 失败不该让一次已成功的变更对外装作失败，但回指就没了（取舍已列入回报）。
func adminWriteOK(w http.ResponseWriter, status int, data any, auditID string) {
	payload := map[string]any{"data": data}
	if auditID != "" {
		payload["audit_id"] = auditID
	}
	writeJSON(w, status, payload)
}

// adminBaseAudit 构造审计行的基础字段（actor/act/action/target/result）。
func adminBaseAudit(p *auth.Principal, r *http.Request, action, target, result string) AdminAuditEntry {
	e := AdminAuditEntry{
		Action: action,
		Result: result,
		IP:     ClientIPFromContext(r.Context()),
		UA:     r.UserAgent(),
	}
	if p != nil {
		e.AccountID = p.AccountID
		e.ActingClass = string(p.ActingClass)
	}
	if target != "" {
		e.Target = &target
	}
	return e
}

// adminAuditStandalone 独立追加一条审计（仅用于批次汇总这类"跨多条
// 事务的统计行"——单条业务写一律走 store 方法的事务内审计）。
func (d *AdminDeps) adminAuditStandalone(ctx context.Context, p *auth.Principal, r *http.Request, action, target, result string, detail map[string]any) string {
	if d == nil || d.Audit == nil {
		return ""
	}
	e := adminBaseAudit(p, r, action, target, result)
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = b
		}
	}
	id, err := d.Audit.Append(ctx, e)
	if err != nil {
		return ""
	}
	return id
}

// mapAdminError 把窄接口错误翻译成管理面错误体。
//
// 为什么集中一处：四十个 handler 各写一遍 errors.Is 链必然漏支；
// 未识别的错误 = 内部故障（500），绝不下意识 400——把存储故障说成
// 用户参数错，会同时误导用户和掩盖事故。type 一律出自 errors.go 的
// errorTypes 表（控制台码域）——**不走 ir.CodeToErrorType**：那张表是
// 入站协议面的映射，不含 not_found/conflict，套上去会把 404 的 type
// 静默兜底成 internal_error（code-review 点名，admin_test 锁住）。
func mapAdminError(w http.ResponseWriter, err error, resource string) {
	switch {
	case errors.Is(err, ErrAdminAccountNotFound),
		errors.Is(err, ErrAdminGroupNotFound),
		errors.Is(err, ErrAdminModelNotFound),
		errors.Is(err, ErrAdminUpstreamNotFound),
		errors.Is(err, ErrAdminNotFound):
		writeError(w, http.StatusNotFound, "not_found", resource+"不存在", nil)
	case errors.Is(err, ErrAdminDefaultGroupProtected):
		writeError(w, http.StatusConflict, "conflict", "默认组不可删除/不可撤销默认（删组不删人需要它作退路）", nil)
	case errors.Is(err, ErrAdminConflict):
		writeError(w, http.StatusConflict, "conflict", "与现有资源冲突（唯一约束或状态不允许）", nil)
	case errors.Is(err, ErrAdminNotWired):
		// lead 裁决（2026-10-22）：未上线能力一律 404 + not_found——与 §2.2
		// 同一哲学（不确认其存在），文案与 handleNotFound/mwAdmin 逐字节同形。
		writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "管理面存储操作失败", nil)
	}
}

// 各资源面未命中哨兵（桥用 errors.Is 包装仓储错误）。
var (
	ErrAdminGroupNotFound    = errors.New("admin-api: group not found")
	ErrAdminModelNotFound    = errors.New("admin-api: model not found")
	ErrAdminUpstreamNotFound = errors.New("admin-api: upstream not found")
	ErrAdminNotFound         = errors.New("admin-api: not found")
)

// adminDecodeBoth 一次读 body 同时得到结构体与原始字段集（PATCH 需要
// "未给的字段不动"（结构体 nil）与"给了契约外字段报错"同时成立——
// DisallowUnknownFields 让 id/created_at/key_count 这类只读字段出现在
// 写载荷时自动 400，不需要逐端点手写禁键表）。
//
// 为什么不用 decodeJSONBody（8KB）：组策略文档、能力声明、干跑样本集
// 都可能超 8KB；管理面 body 上限 1 MiB——比链上 mwBodyLimit（32 MiB）
// 小两个数量级，管理请求没有理由更大。
func adminDecodeBoth[T any](w http.ResponseWriter, r *http.Request, dst *T) (map[string]json.RawMessage, bool) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		writeError(w, http.StatusBadRequest, "invalid_request", "请求体读取失败或超过 1 MiB", nil)
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "请求体不能为空", nil)
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象", nil)
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "请求体字段不符："+err.Error(), nil)
		return nil, false
	}
	return fields, true
}

// adminListEnvelope 输出 {"data":[...],"next_cursor":null}（§3.1）。
//
// next_cursor 编码是 base64(offset)。为什么 offset 而不是 id 游标：
// 管理面要支持跳页与总数展示，单机 SQLite 的 offset 扫描成本可忽略；
// id 游标会把翻页正确性绑到实现的排序稳定性上。total 是额外字段
// （契约只承诺 data/next_cursor——多给不减，界面分页需要它）。
func adminListEnvelope[T any](w http.ResponseWriter, rows []T, total int64, limit, offset int) {
	if rows == nil {
		rows = []T{}
	}
	next := ""
	if int64(offset+len(rows)) < total {
		next = adminEncodeCursor(offset + limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":        rows,
		"next_cursor": adminNilable(next),
		"total":       total,
	})
}

func adminEncodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// adminDecodeCursor 解析 cursor → offset；非法值由调用方转 400。
func adminDecodeCursor(cur string) (int, error) {
	if cur == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return 0, errors.New("cursor 编码非法")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, errors.New("cursor 值非法")
	}
	return n, nil
}

// adminLimit 解析分页参数：缺省 50、硬上限 500（防"拉全表"——store 侧
// 审计 List 的 1000 硬上限同纪律，这里更严因为管理端一页 50 条已够人看）。
func adminLimit(q string) (int, error) {
	if q == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return 0, errors.New("limit 必须是正整数")
	}
	if n > 500 {
		n = 500
	}
	return n, nil
}

func adminNilable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// adminParseTime 解析 from/to 查询参数：接受 RFC3339 或 epoch 秒。
func adminParseTime(param, v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是 RFC3339 或 epoch 秒", param)
	}
	return t.Unix(), nil
}

// adminJSONTime 把 epoch 秒渲染成契约的 RFC3339 字符串（nil → JSON null）。
func adminJSONTime(sec *int64) any {
	if sec == nil {
		return nil
	}
	return time.Unix(*sec, 0).UTC().Format(time.RFC3339)
}

// adminGenPassword 生成一次性密码（重置 generate 分支用）。20 字节
// base64url ≈ 33 字符 / 160 bit 熵——管理面生成的密码不需要人抄写，
// 所以取熵不取消歧字符集。
func adminGenPassword() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// adminValidClassSet 校验权限类集合（闭集合 + 非空不变式，01-accounts §1）。
func adminValidClassSet(classes []string) error {
	if len(classes) == 0 {
		return errors.New("classes 不能为空（账户至少有一个权限类）")
	}
	seen := map[string]bool{}
	for _, c := range classes {
		if !auth.Class(c).Valid() {
			return fmt.Errorf("非法权限类 %q（admin/tenant/contributor）", c)
		}
		if seen[c] {
			return fmt.Errorf("权限类 %q 重复", c)
		}
		seen[c] = true
	}
	return nil
}

// adminValidAccountStatus 校验管理面可写的账户状态子集。
//
// 为什么只允许 active/disabled/rejected：pending 由注册流产生、merged
// 由合并流产生，手动设成这两个会让账户脱离产生它的联动逻辑（merged
// 不带 merge 行 = 追溯链断裂）。§5.2 的状态列因此是子集。
func adminValidAccountStatus(st string) error {
	switch st {
	case "active", "disabled", "rejected":
		return nil
	}
	return fmt.Errorf("非法账户状态 %q（可写：active/disabled/rejected）", st)
}

// adminValidRateLimit 校验速率哨兵：仅允许 -1、0 或正数（01-accounts §1）。
func adminValidRateLimit(field string, v int64) error {
	if v < -1 {
		return fmt.Errorf("%s 仅允许 -1（不限）、0（全拒）或正数，给了 %d", field, v)
	}
	return nil
}

// adminValidQuota 校验额度对象的单位/周期枚举与下限（02-admin §5.2；
// limit=0 合法=零额度全拒，"不限"是**没有 quota 对象**）。
func adminValidQuota(q *AdminQuota) error {
	if q == nil {
		return nil
	}
	switch q.Unit {
	case "request", "token", "credit":
	default:
		return fmt.Errorf("quota.unit 非法 %q（request/token/credit）", q.Unit)
	}
	switch q.Period {
	case "day", "month":
	default:
		return fmt.Errorf("quota.period 非法 %q（day/month）", q.Period)
	}
	if q.Limit < 0 {
		return errors.New("quota.limit 不能为负（不限 = 不提供 quota 对象）")
	}
	return nil
}

// adminWriteParamErr 是"参数校验 400"的统一出口（03-errors §4：param
// 填出错字段名）。
func adminWriteParamErr(w http.ResponseWriter, param, msg string) {
	writeErrorParam(w, http.StatusBadRequest, "invalid_request", param, msg)
}

// adminFail 是"校验错误自动落 param"的小工具：错误串以 "字段名:" 开头
// 时把字段名填进 param，否则落 "body"。
func adminFail(w http.ResponseWriter, fallbackParam string, err error) {
	msg := err.Error()
	if i := strings.Index(msg, ":"); i > 0 && i < 24 {
		head := msg[:i]
		if strings.HasPrefix(head, "quota.") {
			adminWriteParamErr(w, "quota", msg)
			return
		}
		if !strings.ContainsAny(head, " （）/") && !strings.Contains(head, "非法") {
			adminWriteParamErr(w, head, msg)
			return
		}
	}
	adminWriteParamErr(w, fallbackParam, msg)
}

// adminPrincipal 取当前管理面主体（mwAdmin 保证非 nil；为 nil 即装配
// 错误——handler 返回 500 而不是 panic）。
func adminPrincipal(r *http.Request) *auth.Principal {
	return SessionPrincipalFromContext(r.Context())
}

// adminActor 取主体（nil 时写 500 并返回 false）——handler 的统一前置。
func adminActor(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p := adminPrincipal(r)
	if p == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "管理面主体缺失（中间件未生效）", nil)
		return nil, false
	}
	return p, true
}

// adminCursorAndLimit 解析 ?limit=&cursor=（两错的 400 出口一致）。
func adminCursorAndLimit(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	var err error
	if limit, err = adminLimit(r.URL.Query().Get("limit")); err != nil {
		adminWriteParamErr(w, "limit", err.Error())
		return 0, 0, false
	}
	if offset, err = adminDecodeCursor(r.URL.Query().Get("cursor")); err != nil {
		adminWriteParamErr(w, "cursor", err.Error())
		return 0, 0, false
	}
	return limit, offset, true
}

// adminEpochNow 取 UTC 秒。管理面本轮用真实时钟：弃用/退役时间戳来自
// 请求体，测试断言的是"落库值=给定值"，不需要推进时钟。
func adminEpochNow() int64 { return time.Now().UTC().Unix() }

// need 检查某条能力是否装配，缺失统一写 404 not_found（lead 裁决：未上线
// 的功能不确认存在，与真 404 逐字节同形——name 只进服务器日志供排障）。
// 为什么每个 handler 都要判：AdminDeps 的字段允许 nil（未装配≠程序错误），
// 但用到时才炸就把 nil 解引用变成 panic——中间件挡不住这个。
func (d *AdminDeps) need(w http.ResponseWriter, name string, ok bool) bool {
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
		return false
	}
	return true
}
