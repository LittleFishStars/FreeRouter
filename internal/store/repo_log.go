package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AuditEntry 是 audit_log 一行的形状（05-storage.md 3.8）。
//
// AccountID 用指针：登录失败、Key 试探等事件发生在身份确定之前，
// NULL 才是事实，硬塞一个 id 是伪造归因。
type AuditEntry struct {
	ID          int64  // 只读出用；写入时忽略
	TS          int64  // 0 = 由仓储统一取 UTC now
	AccountID   *int64
	ActingClass *string // 以哪个权限类身份操作（admin/tenant/contributor）
	Elevated    bool
	Action      string // 必填，如 key.create / account.disable / system.audit_pruned
	Target      *string
	IP          *string
	UA          *string
	Result      string // 必填：ok / denied / error
	Detail      map[string]any
}

func (e *AuditEntry) detailJSON() (sql.NullString, error) {
	if e.Detail == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(e.Detail)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("store: 审计 detail 序列化失败: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}


// LogRepo 是日志仓储：audit_log 的追加与查询，以及审计保留期的归档清理通道。
//
// 为什么审计没有 Update/Delete 的公开方法：表上有 append-only 触发器，
// UPDATE 永远禁止；DELETE 只存在于 PruneAudit 这一条受控通道里（8.3），
// 任何"业务代码顺手删审计"的路径都不给。
type LogRepo struct{ base }

// Append 写一条审计，返回新行 id。
//
// 为什么要求 action/result 非空：审计的价值在"谁做了什么、成没成"，
// 缺了这两项的行在追溯时等于噪音，宁可让调用方立刻报错也不入库。
func (r *LogRepo) Append(ctx context.Context, e AuditEntry) (int64, error) {
	if strings.TrimSpace(e.Action) == "" {
		return 0, errors.New("store: 审计必须有 action")
	}
	if strings.TrimSpace(e.Result) == "" {
		return 0, errors.New("store: 审计必须有 result")
	}
	ts := e.TS
	if ts == 0 {
		ts = now()
	}
	detail, err := e.detailJSON()
	if err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO audit_log(ts, account_id, acting_class, elevated, action,
		                      target, ip, ua, result, detail)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ts, e.AccountID, e.ActingClass, boolInt(e.Elevated), e.Action,
		e.Target, e.IP, e.UA, e.Result, detail)
	if err != nil {
		return 0, fmt.Errorf("store: 写审计失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: 读取审计 id 失败: %w", err)
	}
	return id, nil
}

// Appendf 是 Append 的薄封装：调用方只有"记一笔"的诉求时不必构造整个结构体。
func (r *LogRepo) Appendf(ctx context.Context, accountID *int64, action, result string, detail map[string]any) (int64, error) {
	return r.Append(ctx, AuditEntry{AccountID: accountID, Action: action, Result: result, Detail: detail})
}

// AuditFilter 是控制台审计列表的查询条件。零值 = 全量倒序。
type AuditFilter struct {
	AccountID *int64
	Action    string // 前缀匹配，如 "key." 列出全部 Key 相关操作
	FromTS    int64
	ToTS      int64
	Limit     int // <=0 取 100，硬上限 1000，防"拉全表"打爆读池
	Offset    int
}

// List 按条件倒序读审计（走读池：审计列表是报表，不占写连接）。
func (r *LogRepo) List(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, ts, account_id, acting_class, elevated, action,
		                  target, ip, ua, result, detail
		           FROM audit_log WHERE 1=1`)
	var args []any
	if f.AccountID != nil {
		q.WriteString(" AND account_id = ?")
		args = append(args, *f.AccountID)
	}
	if f.Action != "" {
		q.WriteString(" AND action LIKE ?")
		args = append(args, f.Action+"%")
	}
	if f.FromTS > 0 {
		q.WriteString(" AND ts >= ?")
		args = append(args, f.FromTS)
	}
	if f.ToTS > 0 {
		q.WriteString(" AND ts <= ?")
		args = append(args, f.ToTS)
	}
	q.WriteString(" ORDER BY id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, f.Offset)

	db := r.readDBOrSelf()
	rows, err := db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询审计失败: %w", err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var elevated int
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &e.AccountID, &e.ActingClass, &elevated,
			&e.Action, &e.Target, &e.IP, &e.UA, &e.Result, &detail); err != nil {
			return nil, fmt.Errorf("store: 读取审计行失败: %w", err)
		}
		e.Elevated = elevated != 0
		if detail.Valid {
			var m map[string]any
			if err := json.Unmarshal([]byte(detail.String), &m); err == nil {
				e.Detail = m
			}
			// detail 解析失败不丢行：审计行本身还是要展示，坏 JSON 是数据质量问题不是查询错误。
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: 遍历审计结果失败: %w", err)
	}
	return out, nil
}

// readDBOrSelf 让列表类读取走只读池；在 WithTx 内部（r.s.tx 非空）时读自己
// 所在的事务连接——否则事务里刚写的审计会"看不见"（读池是另一条连接，
// 看不到未提交的数据）。
func (r *LogRepo) readDBOrSelf() dbtx {
	if r.s != nil && r.s.tx == nil && r.s.readDB != nil {
		return r.s.readDB
	}
	return r.db
}

// ---------------------------------------------------------------- 审计归档清理

// auditDeleteTriggerSQL 与 0001_init.sql 里的定义逐字一致。
// 为什么在这里重复一份：PruneAudit 要在删除前后各执行一次"摘/装"触发器，
// 这条 SQL 与迁移文件分处两个生命周期（迁移只跑一次，摘装每次清理都跑），
// 两份内容不一致的后果是"清理后触发器保护变弱了却没人发现"。
// 契约测试里有一条专门对比这两处（TestContract 的触发器一致性检查）。
const auditDeleteTriggerSQL = `CREATE TRIGGER IF NOT EXISTS trg_audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END`

const auditDeleteTriggerDropSQL = `DROP TRIGGER IF EXISTS trg_audit_log_no_delete`

// PruneAudit 按保留期清理审计，走 05-storage.md 8.3 的受控通道：
// 导出 NDJSON 归档 → 回读校验 → 摘 DELETE 触发器 → 单事务删除 →
// 重建触发器 → 写 system.audit_pruned 审计。任一步失败即回滚并保留原数据。
//
// 为什么"导出+校验"在事务外、而删除在事务内：归档文件是数据库之外的证据
// （8.3 的关键论点：删除审计本身会被下次清理删掉，只有落盘文件消灭不了）。
// 先落文件再动数据库，失败时最坏结果是"多了一份没人引用的归档"，可接受；
// 反过来先删再导出，失败就是证据永久丢失。
//
// 为什么必须在事务里"摘→删→装"：如果三步分开，摘掉触发器到装回去之间
// 任何崩溃都会留下一个"审计可被任意删除"的库——那是比不清理严重得多的状态。
// SQLite 的 DDL 参与事务，因此三步在同一事务内原子完成。
//
// retainDays <= 0：永久保留，直接返回 (nil, nil)。
func (r *LogRepo) PruneAudit(ctx context.Context, retainDays int, archiveDir string) (*PruneResult, error) {
	if retainDays <= 0 {
		return nil, nil
	}
	if strings.TrimSpace(archiveDir) == "" {
		return nil, errors.New("store: 审计清理必须指定归档目录（没有归档就没有证据，宁可不删）")
	}
	if r.s == nil || r.s.tx != nil {
		return nil, errors.New("store: PruneAudit 必须在 Store 顶层调用（内部自行开事务）")
	}

	cutoff := now() - int64(retainDays)*86400

	// —— 第 1 步：导出目标区间到 NDJSON ——
	if err := ensureDir(archiveDir); err != nil {
		return nil, err
	}
	archivePath := filepath.Join(archiveDir,
		fmt.Sprintf("audit-%s-%d.ndjson", time.Now().UTC().Format("20060102T150405"), cutoff))
	count, firstID, lastID, err := r.exportAuditRange(ctx, cutoff, archivePath)
	if err != nil {
		return nil, err
	}

	// —— 第 2 步：回读校验：行数与首尾 id 必须与导出时一致 ——
	if err := verifyAuditArchive(archivePath, count, firstID, lastID); err != nil {
		_ = os.Remove(archivePath) // 校验不过的文件是坏证据，删掉以免误导
		return nil, err
	}

	// 没有过期行：不碰触发器，不写清理审计，返回零结果。
	if count == 0 {
		_ = os.Remove(archivePath)
		return &PruneResult{At: now(), AuditCutoff: cutoff, AuditArchive: archivePath}, nil
	}

	// —— 第 3~6 步：单事务内 摘触发器 → DELETE → 重建触发器 → 写清理审计 ——
	var deleted int64
	err = r.s.WithTx(ctx, func(tx *Repos) error {
		if _, e := tx.Logs.db.ExecContext(ctx, auditDeleteTriggerDropSQL); e != nil {
			return fmt.Errorf("store: 摘除审计删除触发器失败: %w", e)
		}
		res, e := tx.Logs.db.ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, cutoff)
		if e != nil {
			return fmt.Errorf("store: 删除过期审计失败: %w", e)
		}
		deleted, e = res.RowsAffected()
		if e != nil {
			return fmt.Errorf("store: 读取审计删除行数失败: %w", e)
		}
		// 行数不一致说明导出与删除之间数据变了（不应发生：只有新行出现，ts>=now>cutoff）。
		// 宁可回滚重来，也不接受"归档声称 N 行、实际删了 M 行"的账实不符。
		if deleted != count {
			return fmt.Errorf("store: 审计删除行数(%d)与归档行数(%d)不一致，回滚", deleted, count)
		}
		if _, e := tx.Logs.db.ExecContext(ctx, auditDeleteTriggerSQL); e != nil {
			return fmt.Errorf("store: 重建审计删除触发器失败: %w", e)
		}
		// 清理审计必须在触发器重建之后写：INSERT 不受触发器限制，但顺序上
		// 先装回保护再写行，杜绝"这行写成功后触发器还没回来就崩了"的窗口。
		if _, e := tx.Logs.Append(ctx, AuditEntry{
			Action: "system.audit_pruned",
			Result: "ok",
			Detail: map[string]any{
				"cutoff":        cutoff,
				"retain_days":   retainDays,
				"deleted_rows":  deleted,
				"archive_file":  archivePath,
				"archive_first": firstID,
				"archive_last":  lastID,
			},
		}); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		// 事务已回滚：原数据完好，触发器状态回到事务开始前。归档文件留在盘上作为
		// "试图清理过"的证据，运维可据此判断是否重试。
		return nil, err
	}

	return &PruneResult{At: now(), Audit: deleted, AuditCutoff: cutoff, AuditArchive: archivePath}, nil
}

// exportAuditRange 把 ts < cutoff 的审计按 id 升序写入 NDJSON 文件。
// 返回行数与首尾 id（空区间返回 0,0,0）。
func (r *LogRepo) exportAuditRange(ctx context.Context, cutoff int64, path string) (int64, int64, int64, error) {
	db := r.readDBOrSelf()
	rows, err := db.QueryContext(ctx,
		`SELECT id, ts, account_id, acting_class, elevated, action,
		        target, ip, ua, result, detail
		 FROM audit_log WHERE ts < ? ORDER BY id`, cutoff)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: 导出审计查询失败: %w", err)
	}
	defer rows.Close()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: 创建归档文件失败: %w", err)
	}
	w := bufio.NewWriter(f)

	var count, firstID, lastID int64
	for rows.Next() {
		var e AuditEntry
		var elevated int
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &e.AccountID, &e.ActingClass, &elevated,
			&e.Action, &e.Target, &e.IP, &e.UA, &e.Result, &detail); err != nil {
			f.Close()
			return 0, 0, 0, fmt.Errorf("store: 导出审计扫描失败: %w", err)
		}
		e.Elevated = elevated != 0
		if detail.Valid {
			e.Detail = map[string]any{"_raw": detail.String}
			var m map[string]any
			if json.Unmarshal([]byte(detail.String), &m) == nil {
				e.Detail = m
			}
		}
		line, err := json.Marshal(map[string]any{
			"id": e.ID, "ts": e.TS, "account_id": e.AccountID, "acting_class": e.ActingClass,
			"elevated": e.Elevated, "action": e.Action, "target": e.Target,
			"ip": e.IP, "ua": e.UA, "result": e.Result, "detail": e.Detail,
		})
		if err != nil {
			f.Close()
			return 0, 0, 0, fmt.Errorf("store: 归档行序列化失败: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			return 0, 0, 0, fmt.Errorf("store: 归档写入失败: %w", err)
		}
		count++
		if count == 1 {
			firstID = e.ID
		}
		lastID = e.ID
	}
	if err := rows.Err(); err != nil {
		f.Close()
		return 0, 0, 0, fmt.Errorf("store: 导出审计遍历失败: %w", err)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, 0, 0, fmt.Errorf("store: 归档刷盘失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0, 0, 0, fmt.Errorf("store: 归档 fsync 失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, 0, 0, fmt.Errorf("store: 关闭归档文件失败: %w", err)
	}
	return count, firstID, lastID, nil
}

// verifyAuditArchive 回读归档文件：逐行数行数，校验首尾 id 与导出时一致。
// 为什么必须回读而不是信任写入计数：崩溃/磁盘满/加密文件系统写入截断都发生在
// Write 返回成功之后——归档是删除的前置证据，证据本身没落稳就等于没证据。
func verifyAuditArchive(path string, wantCount, wantFirst, wantLast int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("store: 回读归档失败: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 单条审计 detail 可能较大
	var n, first, last int64
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return fmt.Errorf("store: 归档第 %d 行不是合法 JSON: %w", n+1, err)
		}
		n++
		if n == 1 {
			first = probe.ID
		}
		last = probe.ID
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("store: 回读归档扫描失败: %w", err)
	}
	if n != wantCount {
		return fmt.Errorf("store: 归档行数校验失败：导出 %d，回读 %d", wantCount, n)
	}
	if wantCount > 0 && (first != wantFirst || last != wantLast) {
		return fmt.Errorf("store: 归档首尾 id 校验失败：期望 [%d,%d]，实际 [%d,%d]",
			wantFirst, wantLast, first, last)
	}
	return nil
}
