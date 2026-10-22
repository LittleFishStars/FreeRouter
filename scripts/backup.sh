#!/usr/bin/env bash
# backup.sh —— 用 sqlite3 `VACUUM INTO` 做在线一致快照，并校验备份本身。
#
# 用法：
#   ./scripts/backup.sh [--config F] [--out DIR] [--db PATH] [--keep N] [--quiet]
#
# 选项：
#   --config <path>  配置文件（同 freerouter --config；缺省只用默认值 + FR_ 覆盖）
#   --out <dir>      备份目录（默认取 store.backup_dir）
#   --db <path>      显式指定主库路径（默认取 store.path）
#   --keep <N>       滚动保留份数，默认 7（05-storage.md 8.1「7 份滚动」）
#   --quiet          stdout 只输出备份文件路径，便于脚本消费
#   -h | --help      帮助
#
# 退出码：0 成功 / 1 失败 / 2 用法错误 / 3 前置条件缺失而跳过
#
# ── 为什么必须是 VACUUM INTO 而不是 cp（硬要求）──────────────────────
# 与 internal/store/migrate.go 的 backupBeforeMigration 同一口径，注释原话：
# 「WAL 模式下数据可能还在 -wal 里，直接 cp 主库文件会拿到一个不含最新提交的、
# 内部不一致的快照。VACUUM INTO 由 SQLite 保证一致性，且不需要停写。」
# 实测印证（M10）：服务运行中主库文件只有 4 KiB，而 -wal 已经 320 KiB——
# 此刻 cp 得到的副本几乎是空库，且恢复后 SQLite 因找不到配套 -wal 而静默丢数据。
#
# ── 为什么备份里绝不包含主密钥（lead 2026-10-22 裁定）────────────────
# 05-storage.md 8.2 要求两者分开存放；internal/store/masterkey.go 的原话是
# 「落到同一个库里等于把锁和钥匙放进同一个抽屉」，而它论证过「警告会被忽略，
# 所以选择拒绝启动而不是警告后继续」。备份会被复制到异地/对象存储/开发机，
# 泄露面远大于运行环境——同一套逻辑推到备份上，结论只能是**默认不打包**。
# 因此本脚本不提供 `--include-master-key` 这类开关：开关一旦存在就会有人顺手加上。
# 主密钥的备份走独立介质、独立权限，见文末提示与 docs/ops/02-deploy.md §12.4。
#
# ── 为什么备份也要被校验 ────────────────────────────────────────────
# 05-storage.md 8.1：「备份也要被验证，否则它只是一个文件」。本脚本对快照跑
# PRAGMA integrity_check + schema_migrations 计数 + 关键表存在性三重断言，
# 任一不过就删掉半成品并以失败退出（留下一个不可用的备份比没有备份更危险）。

set -euo pipefail

SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
FR_REPO_ROOT="$(cd -P "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"
# shellcheck source=./fr-lib.sh
source "$SCRIPT_DIR/fr-lib.sh"

usage() { sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

CONFIG_PATH="${FR_CONFIG_PATH:-}"
OUT_DIR=""
DB_EXPLICIT=""
KEEP=7
QUIET=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) [[ $# -ge 2 ]] || { echo "--config 需要参数" >&2; exit 2; }; CONFIG_PATH="$2"; shift 2 ;;
    --out)    [[ $# -ge 2 ]] || { echo "--out 需要参数" >&2; exit 2; };    OUT_DIR="$2";    shift 2 ;;
    --db)     [[ $# -ge 2 ]] || { echo "--db 需要参数" >&2; exit 2; };     DB_EXPLICIT="$2"; shift 2 ;;
    --keep)   [[ $# -ge 2 ]] || { echo "--keep 需要参数" >&2; exit 2; };   KEEP="$2";       shift 2 ;;
    --quiet)  QUIET=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ "$KEEP" =~ ^[0-9]+$ ]] || { echo "--keep 必须是非负整数（0 = 不清理旧备份）" >&2; exit 2; }

# ── 前置条件：缺项 SKIP，不崩 ────────────────────────────────────────
if ! command -v sqlite3 >/dev/null 2>&1; then
  fr_log SKIP "本机没有 sqlite3，无法做一致快照（VACUUM INTO 是硬要求，cp 不算备份）"
  exit 3
fi

BIN="$(fr_find_bin '' || true)"
fr_load_cfg "$BIN" "$CONFIG_PATH"
fr_log INFO "配置来源=${FR_CFG_KIND:-none} 二进制=${BIN:-未找到}"

DB="$(fr_abs "${DB_EXPLICIT:-$(fr_cfg_or store.path '')}")"
if [[ -z "$DB" ]]; then
  fr_log SKIP "store.path 未配置且未给 --db，不知道要备份哪个库"
  exit 3
fi
if [[ ! -f "$DB" ]]; then
  fr_log SKIP "库文件不存在：$DB（首次启动前没有东西可备份）"
  exit 3
fi

OUT_DIR="$(fr_abs "${OUT_DIR:-$(fr_cfg_or store.backup_dir 'data/backups')}")"
[[ -n "$OUT_DIR" ]] || OUT_DIR="$FR_REPO_ROOT/data/backups"
mkdir -p "$OUT_DIR"
chmod 700 "$OUT_DIR" 2>/dev/null || true  # 备份目录不给同机他人读；失败不致命（只影响建议权限）

# ── 快照名：与 migrate.go 的 <base>-<UTC 时间戳>-<用途>.sqlite 同形，天然可排序 ──
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BASE="$(basename "${DB%.*}")"
SNAP="$OUT_DIR/$BASE-$STAMP-snapshot.sqlite"
if [[ -e "$SNAP" ]]; then  # VACUUM INTO 拒绝已存在的目标（实测：output file already exists）
  i=1
  while [[ -e "$OUT_DIR/$BASE-$STAMP-snapshot-$i.sqlite" ]]; do i=$((i + 1)); done
  SNAP="$OUT_DIR/$BASE-$STAMP-snapshot-$i.sqlite"
fi

# ── 备份 ─────────────────────────────────────────────────────────────
# mode=ro 的理由：自检/备份都不该给主库打写锁。实测确认——服务正在写时，
# 只读连接上的 VACUUM INTO 仍能拿到含未 checkpoint WAL 内容的完整快照
# （账户行确实在副本里），并发写只会让它报 database is locked 而不是损坏数据。
# .timeout 让锁冲突变成「等一下再失败」而不是立刻失败。
fr_log INFO "开始快照：$DB → $SNAP"
if ! sqlite3 -cmd ".timeout 10000" "file:$DB?mode=ro" "VACUUM INTO '$SNAP'"; then
  # 只读连接失败的兜底：某些文件系统/权限组合下 URI 只读形态不可用，退回普通连接。
  # 仍然是 VACUUM INTO，不是 cp——一致性由 SQLite 保证这条不变。
  fr_log WARN "只读连接快照失败，改用普通连接重试（仍为 VACUUM INTO）"
  if ! sqlite3 -cmd ".timeout 10000" "$DB" "VACUUM INTO '$SNAP'"; then
    rm -f "$SNAP"
    fr_log FAIL "VACUUM INTO 失败：库被长期写锁或已损坏（$DB）"
    exit 1
  fi
fi
chmod 600 "$SNAP" 2>/dev/null || fr_log WARN "备份文件权限未能收紧到 0600：$(stat -c '%a' "$SNAP" 2>/dev/null || echo 未知)"

# ── 校验：备份也要被验证，否则它只是一个文件（05-storage.md 8.1）──────
IC="$(fr_sql_scalar "$SNAP" 'PRAGMA integrity_check;' || echo '')"
if [[ "$IC" != "ok" ]]; then
  rm -f "$SNAP"
  fr_log FAIL "快照完整性校验未通过（integrity_check=${IC:-无输出}），已删除半成品"
  exit 1
fi
SM="$(fr_sql_scalar "$SNAP" 'SELECT count(*) FROM schema_migrations;' || echo '')"
if [[ ! "$SM" =~ ^[0-9]+$ || "$SM" -lt 1 ]]; then
  rm -f "$SNAP"
  fr_log FAIL "快照里 schema_migrations 缺失或为空（${SM:-查询失败}）：这不是一个迁移完成过的库"
  exit 1
fi
MISSING=""
for t in accounts api_keys upstream_secrets audit_log real_models settings sessions; do
  sqlite3 "$SNAP" "SELECT 1 FROM $t LIMIT 0;" >/dev/null 2>&1 || MISSING="$MISSING $t"
done
if [[ -n "$MISSING" ]]; then
  rm -f "$SNAP"
  fr_log FAIL "快照缺少关键表：$MISSING"
  exit 1
fi
ROWS="$(fr_sql_scalar "$SNAP" "SELECT (SELECT count(*) FROM accounts) || '/' || (SELECT count(*) FROM audit_log);" || echo '?/?')"
SIZE="$(stat -c '%s' "$SNAP" 2>/dev/null || echo 0)"
fr_log INFO "校验通过：integrity=ok 迁移=$SM 条 accounts/audit=$ROWS 大小=${SIZE}B"

# ── 滚动保留（05-storage.md 8.1：7 份）───────────────────────────────
if [[ "$KEEP" -gt 0 ]]; then
  mapfile -t OLD < <(ls -1t "$OUT_DIR"/*.sqlite 2>/dev/null | tail -n "+$((KEEP + 1))" || true)
  if [[ ${#OLD[@]} -gt 0 ]]; then
    for f in "${OLD[@]}"; do
      [[ -e "$f" ]] || continue
      rm -f "$f" && fr_log INFO "滚动删除旧备份：$(basename "$f")"
    done
  fi
fi

if [[ $QUIET -eq 1 ]]; then printf '%s\n' "$SNAP"; else
  printf '备份完成：%s\n' "$SNAP"
  cat >&2 <<'EOF'

⚠ 主密钥（store.master_key_file）**不在**这个备份里，这是刻意的设计而非疏漏：
  库 + 主密钥同处一包 = 任何拿到备份的人拿到全部上游凭据（05-storage.md 8.2）。
  这份快照单独恢复后，upstream_secrets 里的密文一律解不开，必须让贡献者重新提交。

  主密钥请走独立介质、独立权限，例如（在**另一台机器/另一块盘**上执行）：
    scp -p <host>:/etc/freerouter/master.key <离线密钥库>/master.key.$(date +%F)
  并确认它是 0600、且与上面的备份目录不同路径。恢复流程见 scripts/restore.sh。
EOF
fi
exit 0
