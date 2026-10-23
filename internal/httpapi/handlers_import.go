package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// handlers_import.go —— 管理员 CSV 导入（docs/api/02-admin.md §5.3）。
//
//	POST /api/admin/accounts/import
//
// 格式（列顺序固定，后三列可选）：
//
//	用户名,密码[,组][,额度单位][,额度上限]
//	alice,S3cret-passphrase-2026,grp_power,token,200000
//	bob,,grp_default,request,1000
//
// 密码留空 → 服务端生成一次性随机口令并**在响应里回显**：这是唯一一次能
// 看到它们的时刻，不返回就等于这批账户全部不可登录（§5.3）。
//
// ## 行级失败策略：部分成功（本轮定案）
//
// 分册把「整单拒 vs 部分成功」列为未定案（admin_accounts.go 的历史 TODO）。
// 这里选**部分成功**，理由有两条：
//   - 审计形状已经预设了它：06-audit 要求批次事件记「行数、成功/失败数」，
//     整单拒的话失败数只会是 0 或「全失败」，那个字段就没有意义；
//   - 行动性：一行用户名撞了就要管理员改完重贴整份名单，而导入的常见场景
//     恰恰是「名单里有人已经注册过」。逐行报出原因，管理员能只处理那几行。
//
// 代价是导入不是原子的——但「半套账户存在」在这里不是坏状态：每个成功的
// 账户都是完整可用的（账户+权限类+组+额度在同一事务里），失败的行根本没动。
// 真正需要原子的是**单个账户内部**，那由桥保证。

// ImportRow 是一行解析结果（handler 解析，桥落库）。
type ImportRow struct {
	Username    string
	DisplayName string
	// Password 是 CSV 里的原文（空 = 需要生成一次性口令）。
	Password string
	// PasswordHash 由 handler 算好（与单体创建同一口径：桥不碰口令策略，
	// 只做字段搬运）。生成与否由 handler 决定并回显明文。
	PasswordHash string
	// Group 是组 id（grp_ 形态）；空 → 默认组。
	Group string
	// QuotaUnit / QuotaLimit 成对出现；limit=0 是「全拒」，缺省是不写行。
	QuotaUnit  string
	QuotaLimit *int64
}

// ImportStore 是导入面的窄接口（桥实现；逐行落库 + 批次审计）。
type ImportStore interface {
	// ImportAccount 建一个导入账户（active + tenant 类 + 组 + 额度），
	// 与它的创建审计同事务。口令哈希已在 row 里备好，桥不生成、不校验强度。
	ImportAccount(ctx context.Context, row ImportRow, audit AdminAuditEntry) (id string, err error)
	// ImportBatchAudit 记一条批次事件（06-audit：操作者、行数、成功/失败数）。
	ImportBatchAudit(ctx context.Context, audit AdminAuditEntry) (string, error)
}

// parseImportCSV 解析 CSV 文本为行数组。
//
// 不做「整份语法错就 400」：语法错的行按行报错更有帮助（第 3 行少个逗号
// 不该让前两行白写）。但**表头**要跳过——`用户名,密码` 这种首行是常见贴法，
// 当成数据会建出一个叫「用户名」的账户。
func parseImportCSV(text string) ([]ImportRow, []string) {
	var rows []ImportRow
	var problems []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := strings.Split(line, ",")
		for j := range cols {
			cols[j] = strings.TrimSpace(cols[j])
		}
		if i == 0 && isHeaderRow(cols) {
			continue
		}
		if cols[0] == "" {
			problems = append(problems, "第 "+strconv.Itoa(i+1)+" 行：用户名为空")
			continue
		}
		if len(cols) > 5 {
			problems = append(problems, "第 "+strconv.Itoa(i+1)+" 行：列数超过 5（用户名,密码[,组][,额度单位][,额度上限]）")
			continue
		}
		row := ImportRow{Username: cols[0], DisplayName: cols[0]}
		if len(cols) > 1 {
			row.Password = cols[1]
		}
		if len(cols) > 2 {
			row.Group = cols[2]
		}
		if len(cols) > 3 && cols[3] != "" {
			row.QuotaUnit = cols[3]
		}
		if len(cols) > 4 && cols[4] != "" {
			n, err := strconv.ParseInt(cols[4], 10, 64)
			if err != nil {
				problems = append(problems, "第 "+strconv.Itoa(i+1)+" 行：额度上限不是整数（"+cols[4]+"）")
				continue
			}
			row.QuotaLimit = &n
		}
		if row.QuotaUnit == "" && row.QuotaLimit != nil {
			problems = append(problems, "第 "+strconv.Itoa(i+1)+" 行：给了额度上限但缺额度单位")
			continue
		}
		if row.QuotaUnit != "" && !validQuotaUnit(row.QuotaUnit) {
			problems = append(problems, "第 "+strconv.Itoa(i+1)+" 行：额度单位须为 request/token/credit（"+row.QuotaUnit+"）")
			continue
		}
		rows = append(rows, row)
	}
	return rows, problems
}

// isHeaderRow 判定表头：首列是「用户名」或 username（大小写不敏感）。
func isHeaderRow(cols []string) bool {
	if len(cols) == 0 {
		return false
	}
	first := strings.ToLower(cols[0])
	return first == "用户名" || first == "username" || first == "user"
}

func validQuotaUnit(u string) bool {
	return u == "request" || u == "token" || u == "credit"
}

// HandleAdminAccountsImport POST /api/admin/accounts/import。
//
// 请求体 {"csv": "..."}（前端 importAccountsCsv 的形状）。
// 响应 {"data":{"imported":N,"generated":[{username,password}],"failed":[...]}}。
func (s *Server) HandleAdminAccountsImport(deps *AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || !deps.need(w, "账户存储", deps.Accounts != nil) {
			return
		}
		p, ok := adminActor(w, r)
		if !ok {
			return
		}
		var body struct {
			CSV string `json:"csv"`
		}
		if _, ok := adminDecodeBoth(w, r, &body); !ok {
			return
		}
		rows, problems := parseImportCSV(body.CSV)
		if len(rows) == 0 && len(problems) == 0 {
			adminWriteParamErr(w, "csv", "没有可导入的行")
			return
		}
		importer, ok := deps.Accounts.(ImportStore)
		if !ok {
			// 桥没实现导入面：与「未接线」同口径报 404，而不是假装成功。
			writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
			return
		}

		type failedRow struct {
			Username string `json:"username"`
			Error    string `json:"error"`
		}
		var (
			imported  int
			generated = []map[string]string{}
			failed    = []failedRow{}
		)
		for _, prob := range problems {
			failed = append(failed, failedRow{Error: prob})
		}
		for _, row := range rows {
			// 口令：留空则生成一次性随机值（§5.3 定案：拒绝会让整个导入失败，
			// 而生成一次性口令只是多一步「把结果交给管理员」）。
			plain := row.Password
			gen := ""
			if strings.TrimSpace(plain) == "" {
				pw, perr := adminGenPassword()
				if perr != nil {
					failed = append(failed, failedRow{Username: row.Username, Error: "生成口令失败：" + perr.Error()})
					continue
				}
				plain, gen = pw, pw
			}
			hash, herr := deps.Hasher.Hash(plain)
			if herr != nil {
				failed = append(failed, failedRow{Username: row.Username, Error: "口令哈希失败：" + herr.Error()})
				continue
			}
			row.PasswordHash = hash
			audit := adminBaseAudit(p, r, "account.import", "account:"+row.Username, "ok")
			id, ierr := importer.ImportAccount(r.Context(), row, audit)
			if ierr != nil {
				// 冲突/未找到给可读文案；其余保留原样（那是真故障，原文更有用）。
				//
				// 判据用 errors.Is 而不是 asAuthError：桥的 mapStoreErr 把唯一约束
				// 包成 fmt.Errorf("%w: %v", ErrAdminConflict, err)，它不是 *auth.Error，
				// 类型断言匹配不上（实测就是这样把 SQLite 原文漏给了用户）。
				msg := ierr.Error()
				switch {
				case errors.Is(ierr, ErrAdminConflict):
					msg = "用户名已被占用"
				case errors.Is(ierr, ErrAdminAccountNotFound):
					msg = "目标不存在（组 id 无效？）"
				}
				failed = append(failed, failedRow{Username: row.Username, Error: msg})
				continue
			}
			imported++
			if gen != "" {
				generated = append(generated, map[string]string{"username": row.Username, "password": gen})
			}
			_ = id
		}

		// 批次事件：06-audit 要求记「行数、成功/失败数」——它是「谁在什么时候
		// 批量造了 30 个账户」的唯一聚合线索（逐行事件回答不了「一共多少」）。
		batch := adminBaseAudit(p, r, "account.import_batch", "accounts", "ok")
		if b, err := json.Marshal(map[string]any{
			"rows":   len(rows) + len(problems),
			"ok":     imported,
			"failed": len(failed),
		}); err == nil {
			batch.Detail = b
		}
		batchID, err := importer.ImportBatchAudit(r.Context(), batch)
		if err != nil {
			// 批次审计写失败不推翻已完成的导入：账户是真的建好了，
			// 报 500 会让管理员重试并造出重复账户。只记服务器日志。
			s.log.Error("admin import: 批次审计写入失败（导入本身已完成）", "err", err)
			batchID = ""
		}
		adminWriteOK(w, http.StatusOK, map[string]any{
			"imported":  imported,
			"generated": generated,
			"failed":    failed,
		}, batchID)
	}
}
