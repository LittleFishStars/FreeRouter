#!/usr/bin/env bash
# fr-lib.sh —— scripts/ 下运维脚本的公共件（被 backup.sh / restore.sh / smoke.sh source）
#
# 为什么不各脚本各写一份：三份都要「读生效配置」这一件事。配置项的名字与
# FR_ 覆盖规则只应有一处定义，否则改一处漏两处，脚本会对同一个实例读出不同配置。
#
# 命名规则权威：internal/config/env.go —— `FR_` + 路径大写、`.` 换 `_`。
# 备份方式权威：docs/architecture/05-storage.md 8.1 与 internal/store/migrate.go
# 的 backupBeforeMigration（注释原话：WAL 模式下直接 cp 主库会拿到不含最新提交的、
# 内部不一致的快照；VACUUM INTO 由 SQLite 保证一致性且不需要停写）。
#
# 退出码约定（三个脚本共用）：
#   0 = 成功
#   1 = 失败
#   2 = 用法错误
#   3 = 前置条件缺失而跳过（缺 sqlite3 / 缺库文件 / 缺二进制）——按任务要求
#       「缺项报 SKIP 不崩」，用独立退出码把它和真失败分开，CI 能区分。

fr_script_dir() { cd -P "$(dirname "${BASH_SOURCE[1]:-${BASH_SOURCE[0]}}")" >/dev/null 2>&1 && pwd; }

# fr_find_bin [显式路径]：显式 > 环境变量 FR_BINARY > bin/freerouter > ./freerouter。
fr_find_bin() {
  local explicit="${1:-}"
  if [[ -n "$explicit" ]]; then [[ -x "$explicit" ]] && { printf '%s' "$explicit"; return 0; }; return 1; fi
  if [[ -n "${FR_BINARY:-}" && -x "$FR_BINARY" ]]; then printf '%s' "$FR_BINARY"; return 0; fi
  local c
  for c in "$FR_REPO_ROOT/bin/freerouter" "$FR_REPO_ROOT/freerouter"; do
    [[ -x "$c" ]] && { printf '%s' "$c"; return 0; }
  done
  return 1
}

# fr_load_cfg <二进制> <配置文件>：把生效配置读进 FR_CFG / FR_CFG_KIND。
# 二进制不可用（未构建/校验失败）时退化读 YAML 原文：备份/恢复恰恰常在「服务起不来」
# 时执行，不能因为读不了配置就罢工。
FR_CFG=""
FR_CFG_KIND="none"
fr_load_cfg() {
  local bin="${1:-}" cfgfile="${2:-}"
  FR_CFG="" FR_CFG_KIND="none"
  if [[ -n "$bin" ]]; then
    if [[ -n "$cfgfile" ]]; then
      FR_CFG="$("$bin" --print-config --config "$cfgfile" 2>/dev/null || true)"
    else
      FR_CFG="$("$bin" --print-config 2>/dev/null || true)"
    fi
    [[ -n "$FR_CFG" ]] && FR_CFG_KIND="print-config"
  fi
  if [[ -z "$FR_CFG" && -n "$cfgfile" && -f "$cfgfile" ]]; then
    FR_CFG="$(cat "$cfgfile")"; FR_CFG_KIND="raw-yaml"
  fi
}

fr_env_name() { printf 'FR_%s' "${1^^}" | tr '.-' '__'; }

# fr_cfg <点分路径>：FR_ 环境变量优先（与 config.ApplyEnv 同向），其次配置文本。
# 缩进假设 2 空格：--print-config 用 yaml.SetIndent(2)，testdata/config.yaml 与
# 文档示例同形。匹配不到返回 1，调用方自行决定 SKIP 还是报错。
fr_cfg() {
  local path="$1" evar
  evar="$(fr_env_name "$path")"
  if [[ -n "${!evar:-}" ]]; then printf '%s' "${!evar}"; return 0; fi
  [[ -n "$FR_CFG" ]] || return 1
  printf '%s\n' "$FR_CFG" | awk -v path="$path" '
    /^[[:space:]]*$/ || /^[[:space:]]*#/ { next }
    {
      match($0, /^[ ]*/); ind = RLENGTH
      level = int(ind / 2) + 1
      n = split(path, want, ".")
      key = $0; sub(/^[ ]*/, "", key); sub(/:.*/, "", key); gsub(/["'\'']/, "", key)
      if (level <= n) stack[level] = key
      if (level != n) next
      for (i = 1; i < n; i++) if (stack[i] != want[i]) next
      if (key != want[n]) next
      val = $0; sub(/^[^:]*:[ ]*/, "", val); gsub(/["'\'']/, "", val)
      sub(/[[:space:]]+#.*$/, "", val); sub(/[[:space:]]+$/, "", val)
      print val; exit
    }'
}

fr_cfg_or() { local v; v="$(fr_cfg "$1" || true)"; [[ -n "$v" ]] && printf '%s' "$v" || printf '%s' "$2"; }

# fr_abs <路径>：空值原样返回空（否则空串会被拼成仓库根，把「未配置」误报成「不存在」）。
fr_abs() {
  local p="${1:-}"
  [[ -z "$p" ]] && { printf ''; return 0; }
  [[ "$p" == "~"* ]] && p="${HOME}${p:1}"
  [[ "$p" != /* ]] && p="$FR_REPO_ROOT/$p"
  printf '%s' "$p"
}

# fr_first_line：pipefail 安全的首行提取（`cmd | head -1` 在 cmd 非零退出时会杀掉脚本）。
fr_first_line() { local l; IFS= read -r l || true; printf '%s' "$l"; }

# fr_sql_scalar <db-uri-or-path> <SQL>：单值查询，失败返回 1（不中断调用方）。
fr_sql_scalar() {
  local src="$1" sql="$2" out
  out="$(sqlite3 -cmd ".timeout ${FR_SQL_TIMEOUT_MS:-5000}" "$src" "$sql" 2>&1 | fr_first_line || true)"
  [[ "$out" == Error* ]] && { printf '%s\n' "$out" >&2; return 1; }
  printf '%s' "$out"
}

# fr_log <级别> <消息>：统一走 stderr，stdout 留给机器可读输出（备份路径等）。
fr_log() { printf '[%s] %s\n' "$1" "${*:2}" >&2; }
