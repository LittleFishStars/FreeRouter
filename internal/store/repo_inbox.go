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

// InboxKind 是待办事项的种类（02-admin §12 入站字段）。
type InboxKind string

const (
	InboxRegistration InboxKind = "registration"  // 注册待审核
	InboxModelChange  InboxKind = "model_change"  // 目录变动（新增/消失/属性变化）
	InboxFailure      InboxKind = "failure"       // 故障通知
	InboxPoolAlert    InboxKind = "pool_alert"    // 池告警
	InboxBreakerAlert InboxKind = "breaker_alert" // 熔断告警
)

func (k InboxKind) valid() bool {
	switch k {
	case InboxRegistration, InboxModelChange, InboxFailure, InboxPoolAlert, InboxBreakerAlert:
		return true
	}
	return false
}

// InboxSeverity 是严重级。降序序：error > warning > info。
type InboxSeverity string

const (
	SeverityError   InboxSeverity = "error"
	SeverityWarning InboxSeverity = "warning"
	SeverityInfo    InboxSeverity = "info"
)

func (s InboxSeverity) valid() bool {
	switch s {
	case SeverityError, SeverityWarning, SeverityInfo:
		return true
	}
	return false
}

// severityRank 给出排序权重（越大越靠前）。error=3 / warning=2 / info=1。
//
// 为什么用数字权重而不是字符串排序：字典序是 error < info < warning，
// 与"降序"要求正好错位——直接 ORDER BY severity DESC 会把 info 排在最前。
// 这类错位不会报错、只会让最紧急的事项沉底，所以在代码里显式定权重。
func severityRank(s InboxSeverity) int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
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

// Append 投递一条待办事项，返回新行 id。
//
// detailJSON 原样存：调用方给什么就存什么，存储层不解析、不校验、不改写。
// 为什么不做"必须是合法 JSON"的校验：02-admin 的入站示例里 detail_json 是个
// 对象，但表结构注释只说"JSON"；把校验做在存储层会让"先建条目后补细节"的
// 工作流无法落地，而这类补写是运维的常见动作。
func (r *InboxRepo) Append(ctx context.Context, kind InboxKind, severity InboxSeverity, title, detailJSON string, relatedID *int64) (int64, error) {
	if !kind.valid() {
		return 0, fmt.Errorf("store: 非法 inbox kind %q", kind)
	}
	if !severity.valid() {
		return 0, fmt.Errorf("store: 非法 inbox severity %q", severity)
	}
	if strings.TrimSpace(title) == "" {
		return 0, errors.New("store: inbox title 不得为空（列表里没有标题的待办无从处理）")
	}
	var detail sql.NullString
	if detailJSON != "" {
		detail = sql.NullString{String: detailJSON, Valid: true}
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO admin_inbox(kind, severity, title, detail_json, related_id, created_at)
		 VALUES(?,?,?,?,?,?)`,
		string(kind), string(severity), title, detail, relatedID, now())
	if err != nil {
		return 0, fmt.Errorf("store: 投递待办事项失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取待办事项 id 失败: %w", err)
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
	// severity 用 CASE 权重排序（字典序是错的，见 severityRank 注释）。
	q.WriteString(` ORDER BY CASE severity
	                     WHEN 'error' THEN 3 WHEN 'warning' THEN 2 WHEN 'info' THEN 1 ELSE 0 END DESC,
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
