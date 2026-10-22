// repo_inbox.go —— admin_inbox 待处理事项仓储（task-19）。
//
// 表已在 0001 建好（05-storage.md 3.9），本文件补仓储方法。
//
// 这张表的定位（docs/security/06-audit.md §3.1、docs/api/02-admin.md §12）：
// 后台任务把"需要管理员动手的事"写进来，控制台拉取与标记。它与审计表同类——
// **只追加，不提供删除接口**（02-admin §12「没有任何端点能删除 admin_inbox 记录」：
// 它是运维事件的留痕）。
//
// 三个形状上的刻意选择：
//   - detail_json 存原文、读取时原样返回字符串：解析是消费方的事。收件箱的
//     detail 形状随 kind 变化（model_change 有 real_model_id、registration 有
//     账户与组、pool_alert 有 unit/period），存储层解析它等于把消费方的版本
//     绑进 schema。
//   - related_id 是裸 id 不带表名：kind 已经隐含目标表，再加一层后缀就是
//     两个必须保持一致的字段（02-admin §12 明确否掉了带后缀的方案）。
//   - severity 排序按 error > warning > info：待处理事项页的全部价值在于
//     "有限注意力该先放哪"（01-catalog §151）。一律 info 会让真故障埋在
//     几十条"新模型上线"里。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 值域哨兵错误：kind/severity 都没有 DB CHECK（0001 不可改），值域由 Go 层
// 保证，因此必须**可判定**——调用方（admin-api）要据此回 400 而不是 500，
// 用 errors.Is 而不是匹配错误字符串。
var (
	ErrInvalidInboxKind     = errors.New("store: 非法 inbox kind")
	ErrInvalidInboxSeverity = errors.New("store: 非法 inbox severity")
)

// InboxKind 是待办事项的种类。
//
// 词表（lead 裁决 2）：以 06-audit §3.1 的 10 个为**唯一权威**——它是
// "新事件必须先归类否则告警规则覆盖不到"的守门表，语义最完整。02-admin 的
// 5 个（registration/model_change/failure/pool_alert/breaker_alert）是它的
// 子集且命名更糊（model_change 分不清新增/消失/属性变，而三者处置动作不同）；
// 别名由文档层统一映射，**代码里只出现规范名**。
//
// 无 DB CHECK（0001 不能改），因此值域由 Go 层保证：Append 必须拒非枚举值。
type InboxKind string

const (
	InboxAccountPending     InboxKind = "account_pending"     // 新注册待审核
	InboxModelAdded         InboxKind = "model_added"         // 目录新增上游模型
	InboxModelMissing       InboxKind = "model_missing"       // 上游模型消失
	InboxModelChanged       InboxKind = "model_changed"       // 上下文/模态/efforts 等属性变化
	InboxChannelFailing     InboxKind = "channel_failing"     // 某渠道连续失败（故障）
	InboxCredentialInvalid  InboxKind = "credential_invalid"  // 贡献凭据失效
	InboxVerificationFailed InboxKind = "verification_failed" // 鉴定 suspect / 复检回归
	InboxPoolExhausted      InboxKind = "pool_exhausted"      // 组共享池耗尽
	InboxGlobalBudget       InboxKind = "global_budget"       // 全站预算熔断（等级最高）
	InboxKeyAnomaly         InboxKind = "key_anomaly"         // 单账户单位时间用量突增
)

func (k InboxKind) valid() bool {
	switch k {
	case InboxAccountPending, InboxModelAdded, InboxModelMissing, InboxModelChanged,
		InboxChannelFailing, InboxCredentialInvalid, InboxVerificationFailed,
		InboxPoolExhausted, InboxGlobalBudget, InboxKeyAnomaly:
		return true
	}
	return false
}

// InboxSeverity 是严重级。词表（lead 裁决 1）= info | warning | error，秩递增。
//
// 取这三个是因为 02-admin 是对外 API 契约（改它属 breaking）且用的正是它；
// DDL 注释的 critical、catalog/upstream 的 warn 全部向它对齐（文档侧统一）。
type InboxSeverity string

const (
	SeverityInfo    InboxSeverity = "info"
	SeverityWarning InboxSeverity = "warning"
	SeverityError   InboxSeverity = "error"
)

func (s InboxSeverity) valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityError:
		return true
	}
	return false
}

// severityRankCase 是 SQL 侧的严重级秩表达式（lead 裁决 1 的排序键），
// %s 填严重级列名。秩：error=3 > warning=2 > 其余=1。
//
// 为什么值域已在 Go 层硬校验、SQL 仍用 ELSE 1 而不是报错：历史库里可能有
// 裁决前写入的旧拼写；ELSE 1 让它们沉到末尾（最不紧急），而不是让整个查询失败。
// 新写入则由 Append 拦住，不会再产生非法值。
const severityRankCase = `CASE %s WHEN 'error' THEN 3 WHEN 'warning' THEN 2 ELSE 1 END`

// severityRank 是 severityRankCase 的 Go 镜像（与 SQL 同形：error=3、warning=2、
// 其余=1）。契约测试断言二者对同一组值一致，防两份映射漂移。
func severityRank(s InboxSeverity) int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	}
	return 1
}

// InboxItem 是一条待办事项。
type InboxItem struct {
	ID         int64
	Kind       InboxKind
	Severity   InboxSeverity
	Title      string
	DetailJSON string // 原文；无内容时为空串
	RelatedID  *int64
	CreatedAt  int64
	ReadAt     *int64
	ReadBy     *int64
}

// Read 报告该条是否已读。
func (i InboxItem) Read() bool { return i.ReadAt != nil }

// InboxRepo 是待处理事项仓储。
type InboxRepo struct{ base }

// Append 投递一条待办事项，返回行 id。
//
// 未读期幂等（01-catalog §3.5「同一个 (kind, related_id) 在未读期间不重复写」）：
// 同一关联对象的同一变动第二次到达时，只把 detail_json 更新为最新值、不新建行
// （裸 INSERT 会让每轮刷新灌一条，一天上百条，待办页失去意义）。幂等窗口由
// 0003 的部分唯一索引 ux_admin_inbox_open 在库层面保证，本函数用对应的
// ON CONFLICT 目标命中它；已读之后索引不再覆盖，自然可以重开新行。
//
// DO UPDATE 只改 detail_json 与 severity，且 severity **只升不降**（lead 裁决 4）：
// 一轮刷新里同一变动可能先报 error 后报 info，降级会让真故障从待办页顶部滑走；
// 而 title 不覆盖是因为它携带的是首次发现时的描述（时间语义已定在未读窗口）。
//
// detailJSON 原样存：调用方给什么就存什么，存储层不解析、不校验、不改写。
// 为什么不做"必须是合法 JSON"的校验：02-admin 的入站示例里 detail_json 是个
// 对象，但表结构注释只说"JSON"；把校验做在存储层会让"先建条目后补细节"的
// 工作流无法落地，而这类补写是运维的常见动作。
func (r *InboxRepo) Append(ctx context.Context, kind InboxKind, severity InboxSeverity, title, detailJSON string, relatedID *int64) (int64, error) {
	// 值域硬校验（lead 裁决 1/2）：kind/severity 都没有 DB CHECK，而拼错的
	// severity 会静默排到末尾（CASE ... ELSE 1）——"看起来能用、实际不可用"
	// 的那类 bug 只能在这里拦。错误可判定：ErrInvalidInboxKind / ErrInvalidInboxSeverity。
	if !kind.valid() {
		return 0, fmt.Errorf("%w: %q", ErrInvalidInboxKind, kind)
	}
	if !severity.valid() {
		return 0, fmt.Errorf("%w: %q", ErrInvalidInboxSeverity, severity)
	}
	if strings.TrimSpace(title) == "" {
		return 0, errors.New("store: inbox title 不得为空（列表里没有标题的待办无从处理）")
	}
	var detail sql.NullString
	if detailJSON != "" {
		detail = sql.NullString{String: detailJSON, Valid: true}
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_inbox(kind, severity, title, detail_json, related_id, created_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(kind, related_id) WHERE read_at IS NULL AND related_id IS NOT NULL
		 DO UPDATE SET
		     detail_json = excluded.detail_json,
		     -- severity 只升不降：新秩 >= 旧秩才覆盖（秩表达式与 List 的排序键同形）。
		     severity = CASE WHEN `+fmt.Sprintf(severityRankCase, "excluded.severity")+` >=
		                           `+fmt.Sprintf(severityRankCase, "admin_inbox.severity")+`
		                      THEN excluded.severity
		                      ELSE admin_inbox.severity END`,
		string(kind), string(severity), title, detail, relatedID, now()); err != nil {
		return 0, fmt.Errorf("store: 投递待办事项失败: %w", err)
	}
	// 回读 id 而不是用 LastInsertId：命中部分唯一索引走 DO UPDATE 时，
	// LastInsertId 不会指向被更新的那一行（它只在真插入时有意义），
	// 拿它返回会让调用方拿着一个错 id 去标已读。回读与 ON CONFLICT 同一
	// 目标条件，拿到的就是当前未读的那一条。
	var id int64
	q := `SELECT id FROM admin_inbox WHERE kind = ? AND read_at IS NULL`
	args := []any{string(kind)}
	if relatedID != nil {
		q += ` AND related_id = ?`
		args = append(args, *relatedID)
	} else {
		q += ` AND related_id IS NULL`
	}
	if err := r.db.QueryRowContext(ctx, q+` ORDER BY id DESC LIMIT 1`, args...).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("store: 回读待办事项 id 失败: %w", err)
	}
	return id, nil
}

// InboxFilter 是列表筛选条件。零值 = 全部。
type InboxFilter struct {
	// Kind 为空 = 不按种类过滤。
	Kind InboxKind
	// Severity 为空 = 不按严重级过滤。
	Severity InboxSeverity
	// UnreadOnly = true 时只列未读（控制台默认视图：待处理事项页只看没处理过的）。
	UnreadOnly bool
	// ReadOnly = true 时只列已读。与 UnreadOnly 互斥（同时为 true 无意义）。
	ReadOnly bool
	// FromTS / ToTS 按创建时间闭区间过滤（Unix 秒）。
	FromTS int64
	ToTS   int64
	// Limit <=0 取 50，硬上限 500——列表端点会被控制台每分钟轮询，
	// 一次拉全表是读放大。
	Limit int
	// Offset 分页偏移。
	Offset int
}

// List 按筛选条件列待办事项。
//
// 排序：severity 降序 → created_at 降序 → id 降序。
// 为什么同严重级还要按时间倒序：同一严重级里"最新的更可能还没处理"，
// 而 id 兜底保证全序稳定（否则同秒投递的两条顺序会随机）。
func (r *InboxRepo) List(ctx context.Context, f InboxFilter) ([]InboxItem, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, kind, severity, title, detail_json, related_id,
	                      created_at, read_at, read_by
	                FROM admin_inbox WHERE 1=1`)
	var args []any
	if f.Kind != "" {
		q.WriteString(" AND kind = ?")
		args = append(args, string(f.Kind))
	}
	if f.Severity != "" {
		q.WriteString(" AND severity = ?")
		args = append(args, string(f.Severity))
	}
	if f.UnreadOnly {
		q.WriteString(" AND read_at IS NULL")
	}
	if f.ReadOnly {
		q.WriteString(" AND read_at IS NOT NULL")
	}
	if f.FromTS > 0 {
		q.WriteString(" AND created_at >= ?")
		args = append(args, f.FromTS)
	}
	if f.ToTS > 0 {
		q.WriteString(" AND created_at <= ?")
		args = append(args, f.ToTS)
	}
	// 排序键：severity 秩降序 → created_at 降序 → id 降序。
	// 秩表达式**只有一处定义**（severityRankCase），Append 的"只升不降"与这里
	// 共用它——两份秩表各自内联是漂移温床，漂移的后果是"Go 认为 error 最高、
	// 库排出来 info 在前"这种没人看得出来的错位。
	// 次级决胜键是必须的：无决胜键时同秩行的顺序由查询计划决定，会随索引与
	// 统计漂移，"默认按 severity 降序"就成了空话；id 兜底保证全序。
	//
	// 取舍（刻意）：不为 severity 加索引。现有 ix_admin_inbox_unread(read_at,
	// created_at) 与 ix_admin_inbox_kind(kind, created_at) 都不含 severity，
	// 按秩排会 filesort——但 inbox 是"每轮刷新都写"的表，为读排序加索引换来的
	// 写放大不值；MVP 阶段未读行是极少数，排序代价可接受。
	q.WriteString(` ORDER BY ` + fmt.Sprintf(severityRankCase, "severity") + ` DESC,
	                  created_at DESC, id DESC
	                  LIMIT ? OFFSET ?`)
	args = append(args, limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 列待办事项失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []InboxItem
	for rows.Next() {
		it, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历待办事项失败: %w", err)
	}
	return out, nil
}

// Get 按 id 读一条待办事项（含 detail_json 原文）。未命中返回 ErrNotFound。
func (r *InboxRepo) Get(ctx context.Context, id int64) (*InboxItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, kind, severity, title, detail_json, related_id, created_at, read_at, read_by
		   FROM admin_inbox WHERE id = ?`, id)
	it, err := scanInbox(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return it, nil
}

// MarkRead 标记一条为已读（read_at + read_by）。幂等：已读的再标一次不报错，
// 只把 read_by 更新为最新标记人。
//
// 为什么幂等而不是报"已经是已读"：控制台的双击、轮询与 PATCH 重试都会造成
// 重复提交，把正常操作变成错误只会催生更多绕过代码。
func (r *InboxRepo) MarkRead(ctx context.Context, id, readBy int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_inbox SET read_at = ?, read_by = ? WHERE id = ?`,
		now(), readBy, id)
	if err != nil {
		return fmt.Errorf("store: 标记待办已读失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkUnread 撤回已读标记（read_at/read_by 清空）。幂等。
//
// 为什么要能撤回：管理员误点、或"这条还没处理完"是常态——已读不是终态，
// 没有撤回就等于把"我看过了"与"我处理完了"混成一个不可逆的位。
func (r *InboxRepo) MarkUnread(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_inbox SET read_at = NULL, read_by = NULL WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: 撤回已读失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkReadBatch 批量标记已读，返回实际标记行数。
//
// 为什么用一条 IN 语句而不是循环单条：控制台可能一次勾选几百条
// （02-admin §12 的批量端点带 id 数组），逐条写是几百个事务。
// 单事务单语句是这批量操作的最短路径。
//
// 空 id 列表直接返回 0：调用方"没有要标记的"是正常输入，不是错误。
func (r *InboxRepo) MarkReadBatch(ctx context.Context, ids []int64, readBy int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > 1000 {
		return 0, fmt.Errorf("store: 批量标记条数过多（%d）", len(ids))
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	args = append(args, now(), readBy)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE admin_inbox SET read_at = ?, read_by = ? WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("store: 批量标记已读失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: 读取批量标记行数失败: %w", err)
	}
	return n, nil
}

// CountUnread 返回未读条数。导航角标专用（02-admin §12 的 /inbox/count）：
// 它只需要一个整数，用列表端点算意味着每分钟拉一次含 detail_json 的全表。
func (r *InboxRepo) CountUnread(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM admin_inbox WHERE read_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: 统计未读待办失败: %w", err)
	}
	return n, nil
}

// CountUnreadByKind 按种类统计未读条数（控制台按类分组的角标）。
func (r *InboxRepo) CountUnreadByKind(ctx context.Context) (map[InboxKind]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT kind, COUNT(1) FROM admin_inbox WHERE read_at IS NULL GROUP BY kind`)
	if err != nil {
		return nil, fmt.Errorf("store: 按类统计未读待办失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[InboxKind]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, fmt.Errorf("store: 扫描未读分组失败: %w", err)
		}
		out[InboxKind(k)] = n
	}
	return out, rows.Err()
}

// scanInbox 扫描一行待办事项（*sql.Row 与 *sql.Rows 共用）。
func scanInbox(sc interface{ Scan(dest ...any) error }) (*InboxItem, error) {
	var it InboxItem
	var kind, severity string
	var detail sql.NullString
	if err := sc.Scan(&it.ID, &kind, &severity, &it.Title, &detail,
		&it.RelatedID, &it.CreatedAt, &it.ReadAt, &it.ReadBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: 扫描待办事项失败: %w", err)
	}
	it.Kind, it.Severity = InboxKind(kind), InboxSeverity(severity)
	it.DetailJSON = detail.String
	return &it, nil
}

// 编译期断言：severity 权重与 DDL 的 CHECK 枚举必须同步。
// 0001 的 admin_inbox.severity 没有 CHECK（见 migrations/0001_init.sql），
// 因此值域由应用层保证——这正是上面 Append 校验存在的原因。
var _ = severityRank(SeverityInfo)
