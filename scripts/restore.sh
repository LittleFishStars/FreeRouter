#!/usr/bin/env bash
# restore.sh —— 从 backup.sh 的快照恢复主库：**先校验备份可用，再动现场**。
#
# 用法：
#   ./scripts/restore.sh <备份文件> [--config F] [--db PATH] [--dry-run] [--yes] [--keep-old]
#   ./scripts/restore.sh --latest [--config F] [--dry-run] [--yes]
#
# 选项：
#   <备份文件>        要恢复的快照路径（backup.sh 产出的 *.sqlite）
#   --latest          取 store.backup_dir 里最新的一份，省去手抄时间戳
#   --config <path>   配置文件（用于定位 store.path / store.backup_dir / master_key_file）
#   --db <path>       显式指定目标库路径（默认取 store.path）
#   --dry-run         **只做校验不落地**：验备份可开、迁移齐、关键表在、与主密钥的匹配性
#   --yes             跳过交互确认（无人值守用；--dry-run 下不需要）
#   --keep-old        把被替换的旧库存到 <目标库>.pre-restore-<时间戳> 而不是删除
#   -h | --help       帮助
#
# 退出码：0 成功 / 1 失败 / 2 用法错误 / 3 前置条件缺失而跳过
#
# ── 为什么校验必须在替换之前，且校验失败就绝不落地 ──────────────────
# 恢复是全站最高危的运维动作：它把「唯一的事实」换成一个可能不可用的文件。
# 如果先替换再发现备份坏了，现场已经没了——那才是真正的灾难。因此本脚本的顺序是
# 死的：校验 → 校验 → 再校验 → 才碰目标。--dry-run 就是这个前缀的独立可执行形态，
# 演练（05-storage.md 8.1「未演练过的备份不算备份」）应该只用 --dry-run 跑。
#
# ── 校验的三重断言（任务书要求「备份库可开 + schema_migrations 完整」）──
#   ① 可开：sqlite3 能以只读 URI 打开 + PRAGMA integrity_check = ok
#      （PRAGMA user_version 一并读出：它是 SQLite 自己的 schema 版本槽位，
#       本项目不用它管迁移——迁移账本在 schema_migrations 表，见 migrate.go——
#       但读出来作诊断信息，异常值提示这个文件可能不是 freerouter 的库）
#   ② 迁移完整：schema_migrations 有行、且**每个 version 唯一**（05-storage.md 5.2
#      的编号连续纪律），并与当前二进制的迁移集对照——备份比二进制旧是可以的
#      （启动时会补迁移），备份比二进制新则必须拒绝（那是要回退版本的场景，
#      02-deploy.md §8：「迁移只前进不回退」，硬恢复会撞 CHECK/列缺失）
#   ③ 关键表齐：账户/Key/凭据密文/审计/模型目录/settings/会话，缺一张都不算可恢复
#
# ── 主密钥匹配性检查（为什么放在 restore 而不是只看文件在不在）───────
# 05-storage.md 8.2：库 + 主密钥缺一不可解出凭据。恢复后最常见的隐性故障是
# 「服务起来了、readyz 绿、但每次用凭据都报解密失败」——因为恢复时用了一把**新的**
# 主密钥。本脚本在落地前核对 master_key_file 存在且 0600、32 字节，并把结论打进输出；
# 真正的「能解密」验证需要 --verify-store（lead 在 cmd/freerouter/main.go 侧提供），
# 本脚本在落地后自动尝试调用它，不可用时给出等价的手工验证命令。
# 主密钥**永不入库、绝不打印内容**——这里只统计它的权限与长度。

set -euo pipefail

SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
FR_REPO_ROOT="$(cd -P "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"
# shellcheck source=./fr-lib.sh
source "$SCRIPT_DIR/fr-lib.sh"

usage() { sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

BACKUP=""
WANT_LATEST=0
CONFIG_PATH="${FR_CONFIG_PATH:-}"
DB_EXPLICIT=""
DRY_RUN=0
ASSUME_YES=0
KEEP_OLD=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --latest)  WANT_LATEST=1; shift ;;
    --config)  [[ $# -ge 2 ]] || { echo "--config 需要参数" >&2; exit 2; }; CONFIG_PATH="$2"; shift 2 ;;
    --db)      [[ $# -ge 2 ]] || { echo "--db 需要参数" >&2; exit 2; };    DB_EXPLICIT="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --yes)     ASSUME_YES=1; shift ;;
    --keep-old) KEEP_OLD=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
    *)
      # 位置参数只接受一个备份文件：接受多个会让「恢复到哪一个」变成猜测。
      [[ -z "$BACKUP" ]] || { echo "只能指定一个备份文件（已给出 $BACKUP 与 $1）" >&2; exit 2; }
      BACKUP="$1"; shift ;;
  esac
done

if [[ $WANT_LATEST -eq 1 && -n "$BACKUP" ]]; then
  echo "--latest 与显式备份路径不能同时给" >&2; exit 2
fi

command -v sqlite3 >/dev/null 2>&1 || { fr_log SKIP "本机没有 sqlite3，无法校验与恢复"; exit 3; }

BIN="$(fr_find_bin '' || true)"
fr_load_cfg "$BIN" "$CONFIG_PATH"
fr_log INFO "配置来源=${FR_CFG_KIND:-none} 二进制=${BIN:-未找到}"

BACKUP_DIR="$(fr_abs "${FR_BACKUP_DIR_OVERRIDE:-$(fr_cfg_or store.backup_dir 'data/backups')}")"
[[ -n "$BACKUP_DIR" ]] || BACKUP_DIR="$FR_REPO_ROOT/data/backups"

if [[ $WANT_LATEST -eq 1 ]]; then
  # 按名字排序取最新：文件名是 UTC 时间戳，字典序 = 时间序（与 backup.sh 命名同形）。
  # 为什么不用 ls -t：mtime 会被 rsync -t / cp -p 从异地备份包改写，
  # 而名字里的时间戳是备份生成时刻的事实，不会被搬运操作污染。
  BACKUP="$(find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.sqlite' -printf '%f\n' 2>/dev/null | sort | tail -1 || true)"
  [[ -n "$BACKUP" ]] && BACKUP="$BACKUP_DIR/$BACKUP"
  [[ -n "$BACKUP" ]] || { fr_log FAIL "备份目录 $BACKUP_DIR 里没有 *.sqlite"; exit 1; }
  fr_log INFO "取最新备份：$(basename "$BACKUP")"
fi
[[ -n "$BACKUP" ]] || { echo "必须给出备份文件，或用 --latest" >&2; usage >&2; exit 2; }
[[ -f "$BACKUP" ]] || { fr_log FAIL "备份文件不存在：$BACKUP"; exit 1; }

DB="$(fr_abs "${DB_EXPLICIT:-$(fr_cfg_or store.path '')}")"
[[ -n "$DB" ]] || { fr_log FAIL "store.path 未配置且未给 --db，不知道要恢复到哪"; exit 1; }

# ── 断言①：备份可开 ──────────────────────────────────────────────────
# 只读 URI 打开：校验阶段绝不能有写权限，否则「校验」本身会改动待检文件。
if ! sqlite3 "file:$BACKUP?mode=ro" "SELECT 1;" >/dev/null 2>&1; then
  fr_log FAIL "备份无法以只读打开（不是 SQLite 文件、被截断、或权限不足）：$BACKUP"
  exit 1
fi
IC="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'PRAGMA integrity_check;' || echo '')"
if [[ "$IC" != "ok" ]]; then
  fr_log FAIL "备份完整性校验未通过：integrity_check=${IC:-无输出}"
  exit 1
fi
UV="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'PRAGMA user_version;' || echo '?')"
fr_log INFO "断言①通过：可开 + integrity=ok（user_version=$UV，仅作诊断，本项目迁移账本在 schema_migrations）"

# ── 断言②：schema_migrations 完整 ────────────────────────────────────
MV="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'SELECT count(*) FROM schema_migrations;' || echo '')"
if [[ ! "$MV" =~ ^[0-9]+$ || "$MV" -lt 1 ]]; then
  fr_log FAIL "断言②未通过：schema_migrations 缺失或为空（${MV:-查询失败}）——这不是迁移完成过的库"
  exit 1
fi
DUP="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'SELECT count(*) FROM (SELECT version FROM schema_migrations GROUP BY version HAVING count(*) > 1);' || echo '?')"
if [[ "$DUP" != "0" ]]; then
  fr_log FAIL "断言②未通过：schema_migrations 里有 $DUP 个重复 version（迁移账本被破坏，恢复后启动校验会拒绝）"
  exit 1
fi
BK_MAX_RAW="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'SELECT max(version) FROM schema_migrations;' || echo '')"
# 非数字必须先归 0：`(( ))` 拿到 "Parse error..." 这类文本会当场炸掉整个脚本
# （set -e 下算术展开的语法错误是直接退出，而不是返回非零）。
[[ "$BK_MAX_RAW" =~ ^[0-9]+$ ]] || BK_MAX_RAW=0
BK_MAX="$BK_MAX_RAW"
BK_LIST="$(sqlite3 "file:$BACKUP?mode=ro" 'SELECT version FROM schema_migrations ORDER BY version;' 2>/dev/null | tr '\n' ',' || true)"
fr_log INFO "断言②通过：$MV 条迁移（max=$BK_MAX）[$BK_LIST]"

# 备份 vs 当前二进制：只拒绝「备份比二进制新」这一方向。
if [[ -n "$BIN" ]]; then
  # 二进制的迁移集大小从 --print-config 拿不到，用源码目录为准的稳妥做法：
  # 数 embed 的迁移文件（与 loadMigrations 的编号规则同口径：NNNN_*.sql，排除 0000）。
  MIG_DIR="$FR_REPO_ROOT/internal/store/migrations"
  if [[ -d "$MIG_DIR" ]]; then
    BIN_MAX=0
    for f in "$MIG_DIR"/*.sql; do
      [[ -e "$f" ]] || continue
      v="$(basename "$f" | grep -oE '^[0-9]{4}' || true)"
      [[ -n "$v" ]] && (( 10#$v > BIN_MAX )) && BIN_MAX=$((10#$v))
    done
    if (( BK_MAX > BIN_MAX )); then
      fr_log FAIL "备份的迁移版本（$BK_MAX）比当前二进制（$BIN_MAX）更新：迁移只前进不回退（02-deploy §8），恢复它会撞上缺失的列/约束。请先升级二进制再恢复。"
      exit 1
    fi
    (( BK_MAX == BIN_MAX )) && fr_log INFO "备份与二进制迁移版本一致（$BK_MAX）" || \
      fr_log INFO "备份落后于二进制（$BK_MAX < $BIN_MAX）：恢复后首次启动会自动补迁移（补之前二进制会自己做一次 pre-migrate 快照）"
  fi
fi

# ── 断言③：关键表齐 ──────────────────────────────────────────────────
MISSING=""
for t in accounts api_keys upstream_secrets audit_log real_models settings sessions; do
  sqlite3 "file:$BACKUP?mode=ro" "SELECT 1 FROM $t LIMIT 0;" >/dev/null 2>&1 || MISSING="$MISSING $t"
done
if [[ -n "$MISSING" ]]; then
  fr_log FAIL "断言③未通过：备份缺少关键表 $MISSING"
  exit 1
fi
STATS="$(fr_sql_scalar "file:$BACKUP?mode=ro" "SELECT 'accounts=' || (SELECT count(*) FROM accounts) || ' keys=' || (SELECT count(*) FROM api_keys) || ' secrets=' || (SELECT count(*) FROM upstream_secrets) || ' audit=' || (SELECT count(*) FROM audit_log);" || echo '?')"
fr_log INFO "断言③通过：$STATS"

# ── 主密钥核对（只统计，绝不打印内容）────────────────────────────────
KEY="$(fr_abs "$(fr_cfg_or store.master_key_file '')")"
KEY_VERDICT="未配置"
if [[ -n "$KEY" && -f "$KEY" ]]; then
  KP="$(stat -c '%a' "$KEY" 2>/dev/null || echo '?')"
  KSZ="$(stat -c '%s' "$KEY" 2>/dev/null || echo '?')"
  if [[ "$KP" != "600" || "$KSZ" != "32" ]]; then
    fr_log WARN "主密钥 $KEY 权限=$KP 长度=$KSZ（要求 0600 + 32 字节）：服务会以「权限过宽」拒绝启动，见 internal/store/masterkey.go"
  else
    KEY_VERDICT="存在（0600/32B）"
  fi
elif [[ -n "$KEY" ]]; then
  fr_log WARN "主密钥文件不存在：$KEY —— 恢复后所有 upstream_secrets 都解不开，必须让贡献者重新提交（05-storage.md 8.2）"
else
  fr_log WARN "store.master_key_file 未配置：凭据功能不可用"
fi

# 密文行数 vs 主密钥可用性联动：有密文却没密钥，是「恢复成功但功能残废」的典型形态。
NSEC="$(fr_sql_scalar "file:$BACKUP?mode=ro" 'SELECT count(*) FROM upstream_secrets;' || echo '0')"
if [[ "${NSEC:-0}" != "0" && -z "$KEY" ]]; then
  fr_log WARN "备份里有 $NSEC 条凭据密文，但主密钥未配置：恢复后这些凭据一律不可解"
fi

if [[ $DRY_RUN -eq 1 ]]; then
  cat >&2 - <<EOF

── dry-run 结束（未改动任何文件）──
  备份：$BACKUP
  目标库：$DB（当前 $([[ -f "$DB" ]] && echo 存在 || echo 不存在)）
  主密钥：${KEY:-未配置}（$KEY_VERDICT）
  三重断言：可开 + 迁移 $MV 条 + 关键表齐 → 全部通过
EOF
  printf 'DRY-RUN OK：%s 可用于恢复到 %s\n' "$BACKUP" "$DB"
  exit 0
fi

# ── 落地 ─────────────────────────────────────────────────────────────
if [[ -f "$DB" && $ASSUME_YES -ne 1 ]]; then
  fr_log WARN "目标库已存在且将被替换：$DB"
  if [[ -t 0 ]]; then
    read -r -p "确认恢复？输入 yes 继续：" ans
    [[ "$ans" == "yes" ]] || { fr_log INFO "已取消（未改动任何文件）"; exit 1; }
  else
    fr_log FAIL "非交互环境下的覆盖恢复必须带 --yes（防止误把生产库换掉）"
    exit 2
  fi
fi

# （下面紧跟本项的完整理由与判据实现）
# 服务在跑就拒绝：恢复期间实例仍在写 WAL，替换主库文件会得到「新主库 + 旧 -wal」
# 的拼接体——SQLite 会把恢复进来的库当成自己的旧页而报损坏或静默回滚。
#
# 为什么不用 `pgrep -f freerouter`：-f 匹配**整条命令行**，实测会误报——沙箱/CI 里
# 任何命令行提到过这个路径或字（包括本脚本自己的调用链、bwrap 包裹行）都会被判成
# 「服务在跑」而把恢复块住。改用两道各自可靠、互为补位的判据：
#   ① pgrep -x <进程名>：只比 comm（名字截断 15 字节，freerouter 在内），不会误伤；
#   ② 文件句柄：lsof/fuser 直接回答「有没有进程开着这个库」，比猜名字准确。
fr_proc_name="freerouter"
[[ -n "$BIN" ]] && fr_proc_name="$(basename "$BIN")"
RUNNING=""
if command -v pgrep >/dev/null 2>&1 && pgrep -x "$fr_proc_name" >/dev/null 2>&1; then
  RUNNING="pgrep -x $fr_proc_name 命中"
fi
if [[ -z "$RUNNING" && -f "$DB" ]]; then
  if command -v lsof >/dev/null 2>&1 && lsof -- "$DB" >/dev/null 2>&1; then
    RUNNING="lsof 报告有进程持有 $DB 的文件句柄"
  elif command -v fuser >/dev/null 2>&1 && fuser "$DB" >/dev/null 2>&1; then
    RUNNING="fuser 报告有进程持有 $DB 的文件句柄"
  fi
fi
if [[ -n "$RUNNING" && "${FR_RESTORE_FORCE:-0}" != "1" ]]; then
  fr_log FAIL "实例仍在运行或持有库文件（$RUNNING）：先 systemctl stop freerouter（或 docker compose stop）再恢复"
  fr_log FAIL "确认要强制恢复可设 FR_RESTORE_FORCE=1（需承担新主库 + 旧 -wal 拼接的风险，不建议）"
  exit 1
fi
[[ -n "$RUNNING" ]] && fr_log WARN "FR_RESTORE_FORCE=1：忽略「$RUNNING」继续恢复"

TS="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -f "$DB" ]]; then
  if [[ $KEEP_OLD -eq 1 ]]; then
    OLD="$DB.pre-restore-$TS"
    mv "$DB" "$OLD" && fr_log INFO "旧库保留为 $OLD"
    # -wal/-shm 属于旧库，留着会让 SQLite 把它们跟新库拼一起（见上）。必须清掉。
    rm -f "$DB-wal" "$DB-shm"
    printf '%s\n' "$OLD" >&2
  else
    # 不保留旧库时仍然做一次安全网：WAL 模式下的 cp 不一致，所以用 VACUUM INTO。
    SAFETY="$DB.pre-restore-$TS.rejected"
    if sqlite3 -cmd ".timeout 5000" "$DB" "VACUUM INTO '$SAFETY'" >/dev/null 2>&1; then
      chmod 600 "$SAFETY" 2>/dev/null || true
      mv "$SAFETY" "$DB.pre-restore-$TS" && fr_log INFO "旧库已用 VACUUM INTO 存为 $DB.pre-restore-$TS（回滚可用）"
    else
      fr_log WARN "旧库无法生成回滚快照（可能已损坏），仍将执行恢复"
      rm -f "$SAFETY"
    fi
    rm -f "$DB" "$DB-wal" "$DB-shm"
  fi
else
  mkdir -p "$(dirname "$DB")"
  chmod 750 "$(dirname "$DB")" 2>/dev/null || true
  rm -f "$DB-wal" "$DB-shm"  # 清掉孤儿 WAL：没有主库的 -wal 会让 SQLite 打开时拒绝或困惑
fi

# 恢复方式：VACUUM INTO 到新路径而不是 mv/cp 备份文件。
# 为什么：① 顺带再压缩一次并去掉备份里的空闲页；② 目标不存在时 SQLite 自己建，
# 权限与属主由本进程 umask 决定，我们随后统一 chmod 600；③ 备份原文件保持不动，
# 一次失败恢复不该把唯一的备份也消耗掉。
if ! sqlite3 "file:$BACKUP?mode=ro" "VACUUM INTO '$DB'"; then
  fr_log FAIL "恢复写入失败：$BACKUP → $DB（磁盘/权限问题？旧库快照已保留，可按上面的 .pre-restore 路径手工回滚）"
  exit 1
fi
chmod 600 "$DB" || fr_log WARN "库文件未能 chmod 600（当前 $(stat -c '%a' "$DB" 2>/dev/null || echo '?')）：01-security §4.2 要求 0600"

# 恢复后自检：新库必须能只读打开且完整性通过——写路径成功不代表读路径成功。
RIC="$(fr_sql_scalar "file:$DB?mode=ro" 'PRAGMA integrity_check;' || echo '')"
RSV="$(fr_sql_scalar "file:$DB?mode=ro" 'SELECT count(*) FROM schema_migrations;' || echo '')"
if [[ "$RIC" != "ok" ]]; then
  fr_log FAIL "恢复后的库完整性校验失败（integrity_check=${RIC:-无输出}）：请立即用 .pre-restore 快照回滚"
  exit 1
fi
fr_log INFO "恢复后自检：integrity=ok 迁移=$RSV 条"

# 尝试调用 --verify-store（lead 在 main.go 侧提供）：库可开 + 主密钥可解密测试凭据。
VERIFY_HINT=""
if [[ -n "$BIN" ]] && "$BIN" --help 2>&1 | grep -q -- '--verify-store'; then
  if "$BIN" ${CONFIG_PATH:+--config "$CONFIG_PATH"} --verify-store >&2; then
    fr_log INFO "--verify-store 通过：库可打开且主密钥能解密凭据"
  else
    fr_log FAIL "--verify-store 未通过：多半是主密钥与库不匹配（见 02-deploy §7.2 第 5 步）"
    exit 1
  fi
else
  VERIFY_HINT="（当前二进制还没有 --verify-store；可先启动一次服务，看日志有无「凭据解密失败」）"
fi

cat >&2 - <<EOF

── 恢复完成 ──
  新库：$DB（$(stat -c '%a' "$DB" 2>/dev/null || echo ?)）
  主密钥：${KEY:-未配置}（$KEY_VERDICT）
  启动前请确认：store.path 指向这里、服务用户对该目录可写、属主为服务用户
  $VERIFY_HINT
  建议紧接着跑：./scripts/security-checklist.sh ${CONFIG_PATH:+--config "$CONFIG_PATH"}
EOF
printf 'RESTORED：%s\n' "$DB"
exit 0
