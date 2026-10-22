#!/usr/bin/env bash
# security-checklist.sh —— 上线前安全基线逐项自检（docs/ops/01-security.md §8 的 12 项）
#
# 用法：
#   ./scripts/security-checklist.sh [--config F] [--bin F] [--base-url URL] [--json] [--selftest]
#
# 选项：
#   --config <path>    生效配置文件（默认 $FR_CONFIG_PATH；再缺则只用默认值 + FR_ 环境变量）
#   --bin <path>       freerouter 二进制（默认 bin/freerouter，其次仓库根的 freerouter）
#   --base-url <url>   指向**正在运行的实例**做运行时断言（默认 $FR_BASE_URL）
#   --json             每行一个 JSON 对象，便于 CI 消费
#   --selftest         反向验证：造一份故意违反基线的夹具，断言对应项确实翻 FAIL
#   -h | --help        帮助
#
# 退出码：0 = 没有 FAIL；1 = 至少一项 FAIL；2 = 用法错误。
#
# ── 「真的检查」的来源分类 ────────────────────────────────────────────
# 每项必须读一个真实来源，不 echo 常量：
#   CFG  = 生效配置（--print-config，它已叠加 FR_ 覆盖）
#   FILE = 文件系统事实（stat 权限、目录关系、deploy 文件内容）
#   BIN  = 二进制/源码里的实现证据（strings -a / grep）
#   HTTP = 对运行实例的真实探针（响应头、状态码）
#   RUNTIME = 外部注入的运行时证据（容器内 id/touch/CapEff，见下方可选环境变量）
# 来源拿不到时报 SKIP，不报 PASS——「查不了」绝不能伪装成「查过了」。
#
# | # | 项 | 来源 |
# |---|---|---|
# | C1 | 默认只监听回环 / 明文对外有感知且可 deny | CFG+BIN |
# | C2 | TLS 与安全响应头（CSP/nosniff/frame/referrer） | CFG+FILE+HTTP |
# | C3 | 非 root 运行 | FILE(+RUNTIME) |
# | C4 | 只读根文件系统 | FILE(+RUNTIME) |
# | C5 | cap-drop ALL | FILE(+RUNTIME) |
# | C6 | 主密钥 0600 + 与库分开存放 | FILE |
# | C7 | 数据库文件权限（组/他人位必须为空） | FILE |
# | C8 | 日志不泄露凭据（源码 + 库文件明文扫描） | BIN+FILE |
# | C9 | 限速/额度/登录强度三项定值 | CFG+BIN |
# | C10 | 入站 body 上限 ≤ 32 MiB | CFG(+HTTP) |
# | C11 | 无静默降级（aggregate 排除、capability_unsupported、C7 切换判据） | CFG+BIN |
# | C12 | 备份可恢复（真做一次 VACUUM INTO 并校验） | FILE |
# | S2/S8/S11 | §8 里依赖公网/压测/govulncheck 的三项：能测则测，否则 SKIP | HTTP/BIN |
#
# 可选运行时注入（由 smoke/deploy 侧的真实检查提供，脚本自身不起容器）：
#   FR_RUNTIME_ID=10001            容器内 uid
#   FR_CONTAINER_WRITE_TEST=1      容器内 `touch /etc/x` 的退出码（非 0 = 只读生效）
#   FR_CONTAINER_CAPEFF=0000000... /proc/1/status 的 CapEff
#
# 为什么 C7 把 0640 也判 FAIL：01-security.md §4.2 的字面要求是 0600。同机另一个
# 用户能读库 = 能拿走全部凭据密文与密码哈希；本服务只有一个运行用户，组位无合法用途，
# 所以不留软口径。现实现只保证数据目录 0750、库文件用驱动默认（实测 644），
# 因此默认部署下本项 FAIL —— 这是真实缺陷，不是脚本 bug。

set -euo pipefail

SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
REPO_ROOT="$(cd -P "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"

CONFIG_PATH="${FR_CONFIG_PATH:-}"
BIN_PATH="${FR_BINARY:-}"
BASE_URL="${FR_BASE_URL:-}"
MODE_JSON=0
SELFTEST=0

usage() { sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)
      [[ $# -ge 2 ]] || { echo "--config 需要参数" >&2; exit 2; }
      CONFIG_PATH="$2"; shift 2 ;;
    --bin)
      [[ $# -ge 2 ]] || { echo "--bin 需要参数" >&2; exit 2; }
      BIN_PATH="$2"; shift 2 ;;
    --base-url)
      [[ $# -ge 2 ]] || { echo "--base-url 需要参数" >&2; exit 2; }
      BASE_URL="$2"; shift 2 ;;
    --json) MODE_JSON=1; shift ;;
    --selftest) SELFTEST=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

# 找不到二进制不致命：CFG/BIN 类项各自 SKIP，FILE 类项照样能查。
if [[ -z "$BIN_PATH" ]]; then
  for cand in "$REPO_ROOT/bin/freerouter" "$REPO_ROOT/freerouter"; do
    [[ -x "$cand" ]] && { BIN_PATH="$cand"; break; }
  done
fi
[[ -n "$BIN_PATH" && -x "$BIN_PATH" ]] || BIN_PATH=""

# ── 输出与计数 ────────────────────────────────────────────────────────
N_PASS=0 N_FAIL=0 N_SKIP=0
RED="" GRN="" YLW="" RST=""
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; RST=$'\033[0m'
fi

record() { # record <id> <PASS|FAIL|SKIP> <标题> <证据>
  local id="$1" st="$2" title="$3" detail="$4" color=""
  case "$st" in
    PASS) N_PASS=$((N_PASS + 1)); color="$GRN" ;;
    FAIL) N_FAIL=$((N_FAIL + 1)); color="$RED" ;;
    SKIP) N_SKIP=$((N_SKIP + 1)); color="$YLW" ;;
  esac
  if [[ $MODE_JSON -eq 1 ]]; then
    python3 - "$id" "$st" "$title" "$detail" <<'PY'
import json, sys
print(json.dumps({"id": sys.argv[1], "status": sys.argv[2],
                  "title": sys.argv[3], "detail": sys.argv[4]}, ensure_ascii=False))
PY
  else
    printf '%s%-4s%s %-3s %s\n' "$color" "$st" "$RST" "$id" "$title"
    printf '       %s\n' "$detail"
  fi
}

# ── 配置读取 ──────────────────────────────────────────────────────────
# 配置文件在校验失败时 --print-config 不产出内容；此时退化读 YAML 原文。
# 不这样处理的话「配置写错时清单全盘失明」，而那正是最需要跑清单的时刻。
CFG_SRC=""
CFG_SRC_KIND="none"
CFG_FILE_TEXT=""
[[ -n "$CONFIG_PATH" && -f "$CONFIG_PATH" ]] && CFG_FILE_TEXT="$(cat "$CONFIG_PATH")"

if [[ -n "$BIN_PATH" ]]; then
  if [[ -n "$CONFIG_PATH" ]]; then
    CFG_SRC="$("$BIN_PATH" --print-config --config "$CONFIG_PATH" 2>/dev/null || true)"
  else
    CFG_SRC="$("$BIN_PATH" --print-config 2>/dev/null || true)"
  fi
  if [[ -n "$CFG_SRC" ]]; then
    CFG_SRC_KIND="print-config"
  elif [[ -n "$CFG_FILE_TEXT" ]]; then
    CFG_SRC="$CFG_FILE_TEXT"; CFG_SRC_KIND="raw-yaml-fallback"
  fi
elif [[ -n "$CFG_FILE_TEXT" ]]; then
  CFG_SRC="$CFG_FILE_TEXT"; CFG_SRC_KIND="raw-yaml"
fi

env_name_for() { printf 'FR_%s' "${1^^}" | tr '.-' '__'; }

# cfg_get <点分路径>：FR_ 环境变量优先（与 config.ApplyEnv 的覆盖方向一致），
# 其次生效配置文本。缩进假设 2 空格（print-config 用 SetIndent(2)，
# testdata/config.yaml 与文档示例同形）；不匹配返回空，调用方报 SKIP。
cfg_get() {
  local path="$1" evar
  evar="$(env_name_for "$path")"
  if [[ -n "${!evar:-}" ]]; then printf '%s' "${!evar}"; return 0; fi
  [[ -n "$CFG_SRC" ]] || return 1
  printf '%s\n' "$CFG_SRC" | awk -v path="$path" '
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

# bin_or_src_has <固定串>：优先查二进制（部署态可能只有二进制），否则查源码。
bin_or_src_has() {
  local needle="$1"
  if [[ -n "$BIN_PATH" ]] && command -v strings >/dev/null 2>&1; then
    strings -a "$BIN_PATH" 2>/dev/null | grep -qF -- "$needle" && return 0
    [[ -d "$REPO_ROOT/internal" ]] || return 1
  fi
  if [[ -d "$REPO_ROOT/internal" ]]; then
    grep -rqF -- "$needle" "$REPO_ROOT/internal" "$REPO_ROOT/cmd" 2>/dev/null && return 0
    return 1
  fi
  return 1
}

have_http() { [[ -n "$BASE_URL" ]] && command -v curl >/dev/null 2>&1; }

# abs_path <路径>：展开 ~ 并转绝对（不要求存在）。
# 空入参必须原样返回空：否则 `db="$(abs_path "")"` 会把仓库根当成库路径，
# 把「未配置 store.path」误报成「库不存在」——SKIP 的原因就写错了。
abs_path() {
  local p="${1:-}"
  [[ -z "$p" ]] && { printf ''; return 0; }
  [[ "$p" == "~"* ]] && p="${HOME}${p:1}"
  [[ "$p" != /* ]] && p="$REPO_ROOT/$p"
  printf '%s' "$p"
}

# first_line：pipefail 安全的首行提取。
# 为什么不用 `cmd | head -1`：pipefail 下上游非零退出（典型：sqlite3 报「no such table」
# 后退出 1）会让命令替换失败，而 set -e 直接杀掉脚本——被检项反而从输出里消失，
# 「检查到问题」变成「脚本崩溃」。selftest 里 C12 整条 MISSING 就是这个原因。
first_line() { local l; IFS= read -r l || true; printf '%s' "$l"; }

# ── C1 默认只监听回环 / 明文对外有感知且可收紧 ───────────────────────
c1() {
  local listen tls insecure
  listen="$(cfg_get server.listen || true)"
  tls="$(cfg_get tls.mode || true)"
  insecure="$(cfg_get server.insecure_public_listen || true)"
  if [[ -z "$listen" ]]; then
    record C1 SKIP "默认只监听回环/明文感知" "来源=CFG 读不到 server.listen（无二进制且无配置文件）"
    return
  fi
  local host="${listen%%:*}" loopback=0
  case "$host" in 127.*|localhost|\[::1\]|::1) loopback=1 ;; esac
  [[ "$host" == "$listen" ]] && loopback=0

  # deny 是否真的拒绝启动：同样的覆盖组合跑 --check-config，非 0 才算收紧有效。
  local deny_ok="无二进制未测"
  if [[ -n "$BIN_PATH" ]]; then
    if FR_SERVER_LISTEN="0.0.0.0:8080" FR_TLS_MODE="off" FR_SERVER_INSECURE_PUBLIC_LISTEN="deny" \
       "$BIN_PATH" ${CONFIG_PATH:+--config "$CONFIG_PATH"} --check-config >/dev/null 2>&1; then
      deny_ok="实测 deny 竟然放行（FAIL）"
    else
      deny_ok="实测 deny 拒绝启动"
    fi
  fi
  local warn_ok="未验证"
  bin_or_src_has "是非回环地址且未启用 TLS" && warn_ok="警告文本存在"

  local src="来源=${CFG_SRC_KIND}"
  if [[ $loopback -eq 1 ]]; then
    record C1 PASS "默认只监听回环/明文感知" "$src listen=$listen 为回环；$src BIN: $warn_ok；$deny_ok"
  elif [[ "${tls,,}" == "files" ]]; then
    record C1 PASS "默认只监听回环/明文感知" "$src 非回环 $listen 但 tls.mode=files，链路已加密"
  elif [[ "${insecure,,}" == "deny" ]]; then
    record C1 PASS "默认只监听回环/明文感知" "$src 非回环 + 明文已把策略收紧为 deny：$deny_ok"
  else
    record C1 FAIL "默认只监听回环/明文感知" "$src listen=$listen 非回环且 tls.mode=${tls:-off}，策略=${insecure:-warn}：正在明文对外。这是 §2 记录在案的默认妥协，上线前须显式确认网络可信，否则配 tls.mode=files 或 insecure_public_listen=deny（$deny_ok；$warn_ok）"
  fi
}

# ── C2 TLS 与安全响应头 ───────────────────────────────────────────────
c2() {
  local tls cert key bad=0
  local -a notes=()
  tls="$(cfg_get tls.mode || true)"
  cert="$(cfg_get tls.cert_file || true)"
  key="$(cfg_get tls.key_file || true)"

  if [[ "${tls,,}" == "files" ]]; then
    for f in "$cert" "$key"; do
      [[ -n "$f" ]] || continue
      f="$(abs_path "$f")"
      if [[ ! -r "$f" ]]; then notes+=("$f 不可读"); bad=1; continue; fi
      if [[ "$f" == "$(abs_path "$key")" ]]; then
        local p; p="$(stat -c '%a' "$f" 2>/dev/null || echo unknown)"
        if [[ "$p" =~ ^[0-7][0-7]0$ ]]; then notes+=("TLS 私钥权限 $p 合规")
        else notes+=("TLS 私钥权限 $p 含读位"); bad=1; fi
      fi
    done
  fi

  local -a want=("X-Content-Type-Options: nosniff" "X-Frame-Options: DENY" "Content-Security-Policy:" "Referrer-Policy:")
  if have_http; then
    local hdrs got=0
    hdrs="$(curl -sS -m 5 -D - -o /dev/null "$BASE_URL/healthz" 2>/dev/null || true)"
    local h
    for h in "${want[@]}"; do
      if grep -qiF -- "$h" <<<"$hdrs"; then got=$((got + 1)); else notes+=("运行实例缺响应头 $h"); bad=1; fi
    done
    [[ $bad -eq 0 ]] && notes=("来源=HTTP 实测 $((got))/${#want[@]} 个安全头齐备")
  else
    local all=1 h
    for h in "X-Content-Type-Options" "X-Frame-Options" "Content-Security-Policy" "Referrer-Policy"; do
      bin_or_src_has "$h" || { all=0; notes+=("代码里找不到 $h 的设置"); bad=1; }
    done
    [[ $all -eq 1 ]] && notes=("来源=BIN 四个安全头在中间件里都有设置（未提供 --base-url，未实测响应）")
  fi

  if [[ ${#notes[@]} -eq 0 ]]; then notes=("无 TLS 配置、无运行实例：仅确认默认明文形态（tls.mode=${tls:-off}）"); fi
  [[ $bad -eq 0 ]] && record C2 PASS "TLS 与安全响应头" "${notes[*]}" || record C2 FAIL "TLS 与安全响应头" "${notes[*]}"
}

# ── C3 非 root ────────────────────────────────────────────────────────
c3() {
  local df="$REPO_ROOT/deploy/docker/Dockerfile" unit="$REPO_ROOT/deploy/systemd/freerouter.service"
  local -a notes=(); local bad=0

  if [[ -n "${FR_RUNTIME_ID:-}" ]]; then
    if [[ "$FR_RUNTIME_ID" == "0" ]]; then notes+=("来源=RUNTIME 容器内 uid=0（root 运行）"); bad=1
    else notes+=("来源=RUNTIME uid=$FR_RUNTIME_ID 非 root"); fi
  fi
  if [[ -f "$df" ]]; then
    local u; u="$(grep -E '^[[:space:]]*USER[[:space:]]' "$df" | tail -1 | awk '{print $2}' || true)"
    if [[ -z "$u" ]]; then notes+=("Dockerfile 没有 USER 指令"); bad=1
    elif [[ "$u" == "0" || "$u" == "root" ]]; then notes+=("Dockerfile USER=$u 是 root"); bad=1
    else notes+=("来源=FILE Dockerfile USER=$u"); fi
  fi
  if [[ -f "$unit" ]]; then
    local uu; uu="$(grep -E '^[[:space:]]*User=' "$unit" | head -1 | cut -d= -f2- | tr -d ' ' || true)"
    if [[ -z "$uu" ]]; then notes+=("systemd 单元没有 User=（会以 root 运行）"); bad=1
    elif [[ "$uu" == "root" ]]; then notes+=("systemd User=root"); bad=1
    else notes+=("来源=FILE systemd User=$uu"); fi
  fi
  if [[ ${#notes[@]} -eq 0 ]]; then
    record C3 SKIP "非 root 运行" "没有 deploy/docker/Dockerfile、deploy/systemd 单元，也没注入 FR_RUNTIME_ID"
    return
  fi
  [[ $bad -eq 0 ]] && record C3 PASS "非 root 运行" "${notes[*]}" || record C3 FAIL "非 root 运行" "${notes[*]}"
}

# ── C4 只读根文件系统 ─────────────────────────────────────────────────
c4() {
  local cp="$REPO_ROOT/deploy/docker/compose.yaml"
  local -a notes=(); local bad=0

  if [[ -f "$cp" ]]; then
    if grep -qE '^[[:space:]]*read_only:[[:space:]]*true' "$cp"; then
      notes+=("来源=FILE compose read_only: true")
    else notes+=("compose 没有 read_only: true"); bad=1; fi
    if grep -qE '^[[:space:]]*-[[:space:]]*(/tmp|tmp:)' "$cp" || grep -qE '^[[:space:]]*tmpfs:' "$cp"; then
      notes+=("compose 给了可写 tmpfs /tmp")
    else notes+=("read_only 下没有 /tmp tmpfs：进程无处写临时文件"); bad=1; fi
  fi
  if [[ -n "${FR_CONTAINER_WRITE_TEST:-}" ]]; then
    if [[ "$FR_CONTAINER_WRITE_TEST" == "0" ]]; then
      notes+=("来源=RUNTIME 容器内写 /etc 竟然成功（read_only 未生效）"); bad=1
    else notes+=("来源=RUNTIME 容器内写 /etc 失败（read_only 生效）"); fi
  fi
  if [[ ${#notes[@]} -eq 0 ]]; then
    record C4 SKIP "只读根文件系统" "没有 deploy/docker/compose.yaml，也未注入 FR_CONTAINER_WRITE_TEST"
    return
  fi
  [[ $bad -eq 0 ]] && record C4 PASS "只读根文件系统" "${notes[*]}" || record C4 FAIL "只读根文件系统" "${notes[*]}"
}

# ── C5 capability 全丢 ────────────────────────────────────────────────
c5() {
  local cp="$REPO_ROOT/deploy/docker/compose.yaml" unit="$REPO_ROOT/deploy/systemd/freerouter.service"
  local -a notes=(); local bad=0

  if [[ -f "$cp" ]]; then
    if awk '/cap_drop:/{f=1; next} f && /^[[:space:]]*-[[:space:]]*ALL/{print "y"; exit} f && /^[^[:space:]]/{exit}' "$cp" | grep -q y ||
       grep -qE 'cap_drop:[[:space:]]*\[[[:space:]]*ALL' "$cp"; then
      notes+=("来源=FILE compose cap_drop: ALL")
    else notes+=("compose 没有 cap_drop: [ALL]"); bad=1; fi
  fi
  if [[ -f "$unit" ]]; then
    if grep -qE '^[[:space:]]*AmbientCapabilities=' "$unit"; then
      notes+=("systemd 声明了 AmbientCapabilities（提权面被打开）"); bad=1
    else notes+=("来源=FILE systemd 未声明 AmbientCapabilities"); fi
    if grep -qE '^[[:space:]]*NoNewPrivileges=true' "$unit"; then notes+=("NoNewPrivileges=true")
    else notes+=("systemd 缺 NoNewPrivileges=true"); bad=1; fi
  fi
  if [[ -n "${FR_CONTAINER_CAPEFF:-}" ]]; then
    if [[ "$FR_CONTAINER_CAPEFF" =~ ^0+$ ]]; then notes+=("来源=RUNTIME CapEff=$FR_CONTAINER_CAPEFF（能力全空）")
    else notes+=("来源=RUNTIME CapEff=$FR_CONTAINER_CAPEFF 非零"); bad=1; fi
  fi
  if [[ ${#notes[@]} -eq 0 ]]; then
    record C5 SKIP "cap-drop" "没有 compose/systemd 可查，也未注入 FR_CONTAINER_CAPEFF"
    return
  fi
  [[ $bad -eq 0 ]] && record C5 PASS "capability 全丢" "${notes[*]}" || record C5 FAIL "capability 全丢" "${notes[*]}"
}

# ── C6 主密钥 0600 且与库分开存放 ─────────────────────────────────────
c6() {
  local key db
  key="$(cfg_get store.master_key_file || true)"
  db="$(cfg_get store.path || true)"
  [[ -n "$db" ]] && db="$(abs_path "$db")"
  if [[ -z "$key" ]]; then
    record C6 SKIP "主密钥 0600 且与库分开" "来源=CFG store.master_key_file 未配置（上游凭据无法加解密，见 02-deploy §2.7）"
    return
  fi
  key="$(abs_path "$key")"
  if [[ ! -e "$key" ]]; then
    record C6 FAIL "主密钥 0600 且与库分开" "来源=FILE 配置指向 $key 但文件不存在"
    return
  fi
  local -a notes=(); local bad=0
  local perm size
  perm="$(stat -c '%a' "$key" 2>/dev/null || echo unknown)"
  size="$(stat -c '%s' "$key" 2>/dev/null || echo 0)"
  if [[ "$perm" == "600" ]]; then notes+=("权限 0600")
  else notes+=("权限 $perm（必须恰好 0600；修复：chmod 600 $key）"); bad=1; fi
  if [[ "$size" == "32" ]]; then notes+=("长度 32 字节（AES-256）")
  else notes+=("长度 $size 字节（internal/store/masterkey.go 要求精确 32）"); bad=1; fi
  if [[ -n "$db" ]]; then
    local kdir ddir
    kdir="$(cd -P "$(dirname "$key")" >/dev/null 2>&1 && pwd || echo "$(dirname "$key")")"
    ddir="$(cd -P "$(dirname "$db")" >/dev/null 2>&1 && pwd || echo "$(dirname "$db")")"
    if [[ "$kdir" == "$ddir" || "$kdir" == "$ddir"/* ]]; then
      notes+=("主密钥位于库目录下（$kdir ⊂ $ddir），违反 05-storage 8.2 分开存放"); bad=1
    else notes+=("与库分处两地（key∈$kdir，db∈$ddir）"); fi
  fi
  [[ $bad -eq 0 ]] && record C6 PASS "主密钥 0600 且与库分开" "来源=FILE ${notes[*]}" || record C6 FAIL "主密钥 0600 且与库分开" "来源=FILE ${notes[*]}"
}

# ── C7 数据库文件权限 ─────────────────────────────────────────────────
c7() {
  local db; db="$(abs_path "$(cfg_get store.path || true)")"
  [[ -n "$db" ]] || db="$REPO_ROOT/data/freerouter.db"
  if [[ ! -e "$db" ]]; then
    record C7 SKIP "数据库文件权限" "来源=FILE 库文件 $db 尚不存在（首次启动前）"
    return
  fi
  local -a notes=(); local bad=0 f p
  for f in "$db" "$db-wal" "$db-shm"; do
    [[ -e "$f" ]] || continue
    p="$(stat -c '%a' "$f" 2>/dev/null || echo unknown)"
    notes+=("$(basename "$f")=$p")
    # 口径：其他位与组位必须全 0（01-security §4.2 字面 0600）。
    if [[ "$p" =~ ^[0-7][0-7][1-7]$ || "$p" =~ ^[0-7][1-7][0-7]$ ]]; then bad=1; fi
  done
  if [[ $bad -eq 0 ]]; then record C7 PASS "数据库文件权限" "来源=FILE ${notes[*]}"
  else record C7 FAIL "数据库文件权限" "来源=FILE ${notes[*]} —— 同机其他用户可读库 = 能拿走全部凭据密文。临时修复：chmod 600 ${db}*；根治需 internal/store 打开后补 chmod（已记入 M10 缺陷条目）"; fi
}

# ── C8 日志不泄露凭据 ─────────────────────────────────────────────────
c8() {
  local src="$REPO_ROOT/internal"
  if [[ ! -d "$src" ]]; then
    record C8 SKIP "日志不泄露凭据" "没有源码可静态扫描，也没有 --base-url 做运行验证"
    return
  fi
  local -a notes=(); local bad=0
  local hits
  hits="$(grep -rnE 'log\.(Info|Warn|Debug|Error)\([^)]*Authorization' "$src" --include='*.go' 2>/dev/null | grep -v '_test.go' || true)"
  if [[ -n "$hits" ]]; then notes+=("源码把 Authorization 写进日志：$(head -c 200 <<<"$hits")"); bad=1
  else notes+=("源码无「凭据头进日志」语句（Authorization 只进 ParseBearer 比对）"); fi

  if grep -rnE 'log\.(Info|Warn|Debug|Error)\([^)]*\.Header' "$src" --include='*.go' 2>/dev/null | grep -v '_test.go' | grep -q .; then
    notes+=("源码把整个 r.Header 打进日志"); bad=1
  fi

  local db; db="$(abs_path "$(cfg_get store.path || true)")"
  if [[ -n "$db" && -f "$db" ]] && command -v strings >/dev/null 2>&1; then
    local leaked
    leaked="$(strings -a "$db" 2>/dev/null | grep -icE 'sk-[A-Za-z0-9]{12,}|Bearer [A-Za-z0-9._-]{12,}' || true)"
    if [[ "${leaked:-0}" != "0" ]]; then notes+=("库文件出现凭据明文样式 $leaked 处"); bad=1
    else notes+=("库文件 strings 扫描零命中凭据明文（M8 验收同一条命令）"); fi
  else
    notes+=("库文件不存在，未做明文扫描")
  fi
  [[ $bad -eq 0 ]] && record C8 PASS "日志不泄露凭据" "来源=BIN+FILE ${notes[*]}" || record C8 FAIL "日志不泄露凭据" "来源=BIN+FILE ${notes[*]}"
}

# ── C9 限速/额度/登录强度三项定值 ─────────────────────────────────────
c9() {
  local daily monthly rpm tpm conc minlen ttl maxf lock
  daily="$(cfg_get limits.default_daily_tokens || true)"
  monthly="$(cfg_get limits.default_monthly_tokens || true)"
  rpm="$(cfg_get limits.default_rpm || true)"
  tpm="$(cfg_get limits.default_tpm || true)"
  conc="$(cfg_get limits.default_max_concurrency || true)"
  minlen="$(cfg_get security.password_min_length || true)"
  ttl="$(cfg_get security.session_ttl || true)"
  maxf="$(cfg_get security.login_max_failures || true)"
  lock="$(cfg_get security.login_lockout || true)"
  if [[ -z "$daily" || -z "$rpm" ]]; then
    record C9 SKIP "限速与默认额度护栏" "来源=${CFG_SRC_KIND} 读不到 limits.* 生效值"
    return
  fi
  local -a notes=(); local bad=0
  if [[ "$daily" == "0" && "$monthly" == "0" ]]; then notes+=("新建账户默认额度=0（默认拒绝）")
  else notes+=("默认额度 daily=$daily monthly=$monthly 非 0：新建 Key 可直接消耗上游额度（§8 第 3 项）"); bad=1; fi
  if [[ "$rpm" =~ ^[0-9]+$ && "$rpm" -gt 0 ]]; then notes+=("default_rpm=$rpm")
  else notes+=("default_rpm=$rpm（应为正整数；-1 不限 / 0 全拒都不该当出厂值）"); bad=1; fi
  notes+=("default_tpm=${tpm:-?} default_max_concurrency=${conc:-?}（哨兵 -1=不限 0=全拒）")
  if [[ "$minlen" =~ ^[0-9]+$ && "$minlen" -ge 12 ]]; then notes+=("密码≥$minlen")
  else notes+=("password_min_length=${minlen:-空} < 12"); bad=1; fi
  if [[ "$maxf" =~ ^[0-9]+$ && "$maxf" -ge 1 ]]; then notes+=("失败 ${maxf} 次触发退避")
  else notes+=("login_max_failures=${maxf:-空} 非法（0 让退避永不触发）"); bad=1; fi
  [[ -n "$lock" ]] && notes+=("锁定窗口 $lock") || { notes+=("login_lockout 未设"); bad=1; }
  [[ -n "$ttl" ]] && notes+=("会话 $ttl") || { notes+=("session_ttl 未设（取消类级提权后它是唯一时效边界）"); bad=1; }
  if bin_or_src_has 'm=65536,t=3,p=2' || bin_or_src_has 'ArgonMemory'; then
    notes+=("argon2id 参数是代码定值")
  else notes+=("找不到 argon2id 参数定值证据"); bad=1; fi
  [[ $bad -eq 0 ]] && record C9 PASS "限速与默认额度护栏" "来源=${CFG_SRC_KIND}+BIN ${notes[*]}" || record C9 FAIL "限速与默认额度护栏" "来源=${CFG_SRC_KIND}+BIN ${notes[*]}"
}

# ── C10 入站 body 上限 ────────────────────────────────────────────────
c10() {
  local mb; mb="$(cfg_get server.max_body_bytes || true)"
  if [[ -z "$mb" ]]; then record C10 SKIP "入站 body 上限" "来源=${CFG_SRC_KIND} 读不到 server.max_body_bytes"; return; fi
  if [[ "$mb" =~ ^[0-9]+$ ]] && [[ "$mb" -gt 0 && "$mb" -le 33554432 ]]; then
    local extra=""
    if have_http && [[ $mb -le 67108864 ]]; then
      local tf code
      tf="$(mktemp)"
      head -c $((mb + 4096)) < /dev/zero | tr '\0' 'a' > "$tf"
      code="$(curl -sS -m 20 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data-binary "@$tf" "$BASE_URL/v1/chat/completions" 2>/dev/null || echo 000)"
      rm -f "$tf"
      case "$code" in
        413) extra="；实测超限返回 413" ;;
        000) extra="；实测未连通" ;;
        *) extra="；实测返回 $code（期望 413）" ;;
      esac
    fi
    record C10 PASS "入站 body 上限" "来源=${CFG_SRC_KIND} max_body_bytes=$mb（≤32 MiB，小于上游 300 MiB）$extra"
  else
    record C10 FAIL "入站 body 上限" "来源=${CFG_SRC_KIND} max_body_bytes=$mb：0/负数等于不设限，超过 32 MiB 违反 01-security §2"
  fi
}

# ── C11 无静默降级 ────────────────────────────────────────────────────
c11() {
  local agg; agg="$(cfg_get upstream.relay.aggregate_excluded || true)"
  local -a notes=(); local bad=0
  if [[ "$agg" == "true" ]]; then notes+=("aggregate_excluded=true（聚合渠道不进候选）")
  else notes+=("aggregate_excluded=${agg:-未设}：aggregate/* 会进候选池"); bad=1; fi
  if bin_or_src_has "capability_unsupported"; then notes+=("capability_unsupported 出口存在")
  else notes+=("找不到 capability_unsupported（C6 要求能力缺失显式报错而非换个模型答）"); bad=1; fi
  if bin_or_src_has "VisibleWritten"; then notes+=("C7 切换判据消费 VisibleWritten")
  else notes+=("找不到 VisibleWritten（C7：已吐可见内容不得换候选）"); bad=1; fi
  if bin_or_src_has "no_candidate"; then notes+=("no_candidate 明确错误存在")
  else notes+=("找不到 no_candidate（无候选必须是明确错误）"); bad=1; fi
  [[ $bad -eq 0 ]] && record C11 PASS "无静默降级" "来源=${CFG_SRC_KIND}+BIN ${notes[*]}" || record C11 FAIL "无静默降级" "来源=${CFG_SRC_KIND}+BIN ${notes[*]}"
}

# ── C12 备份可恢复（真做一次 VACUUM INTO 并校验）──────────────────────
c12() {
  if ! command -v sqlite3 >/dev/null 2>&1; then
    record C12 SKIP "备份可恢复" "本机没有 sqlite3，无法验证（缺项按 SKIP 不崩）"
    return
  fi
  local db; db="$(abs_path "$(cfg_get store.path || true)")"
  if [[ -z "$db" || ! -f "$db" ]]; then
    record C12 SKIP "备份可恢复" "来源=FILE 库文件 ${db:-（未配置）} 不存在，无法做恢复级验证"
    return
  fi
  local tmpd snap ic sm missing="" t
  tmpd="$(mktemp -d)"
  snap="$tmpd/verify.sqlite"
  # trap 用单引号会延迟到 RETURN 时才展开 $tmpd，而它是 local，函数返回时已出作用域（set -u 下报「tmpd: 未绑定的变量」并丢掉清理）。用双引号立即展开路径。
  trap "rm -rf '$tmpd'" RETURN
  # mode=ro：自检绝不打写锁；busy_timeout 让并发写时快速失败而不是挂住。
  if ! sqlite3 -cmd ".timeout 3000" "file:$db?mode=ro" "VACUUM INTO '$snap'" >/dev/null 2>&1; then
    record C12 FAIL "备份可恢复" "VACUUM INTO 失败（写锁未释放或库损坏）：$db"; return
  fi
  ic="$(sqlite3 "$snap" 'PRAGMA integrity_check;' 2>&1 | first_line || true)"
  sm="$(sqlite3 "$snap" 'SELECT count(*) FROM schema_migrations;' 2>&1 | first_line || true)"
  for t in accounts api_keys upstream_secrets audit_log real_models settings sessions; do
    sqlite3 "$snap" "SELECT 1 FROM $t LIMIT 0;" >/dev/null 2>&1 || missing="$missing $t"
  done
  local -a notes=("来源=FILE VACUUM INTO 成功（只读连接，无需停写）")
  local bad=0
  if [[ "$ic" == "ok" ]]; then notes+=("integrity_check=ok"); else notes+=("integrity_check=$ic"); bad=1; fi
  if [[ "$sm" =~ ^[0-9]+$ && "$sm" -ge 1 ]]; then notes+=("schema_migrations 有 $sm 条"); else notes+=("schema_migrations 异常：$sm"); bad=1; fi
  if [[ -z "$missing" ]]; then notes+=("7 张关键表齐备"); else notes+=("缺表：$missing"); bad=1; fi
  [[ $bad -eq 0 ]] && record C12 PASS "备份可恢复" "${notes[*]}" || record C12 FAIL "备份可恢复" "${notes[*]}"
}

# ── S 段：§8 里依赖公网/压测/工具的三项（查不了就 SKIP，绝不虚报）─────
s_xff_runtime() {
  if ! have_http; then
    record S2 SKIP "伪造 XFF 不能绕限速" "没有 --base-url，无法实测（§8 第 2 项要真实请求）"
    return
  fi
  local tp; tp="$(cfg_get security.trusted_proxies || true)"
  if [[ -n "$tp" && "$tp" != "[]" ]]; then
    record S2 SKIP "伪造 XFF 不能绕限速" "来源=${CFG_SRC_KIND} trusted_proxies 已配置（$tp），是否放行伪造取决于网段，需人工核对"
    return
  fi
  local c1 c2
  c1="$(curl -sS -m 5 -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 8.8.8.8' "$BASE_URL/v1/models" 2>/dev/null || echo 000)"
  c2="$(curl -sS -m 5 -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 9.9.9.9' "$BASE_URL/v1/models" 2>/dev/null || echo 000)"
  if [[ "$c1" == "$c2" && "$c1" != "000" ]]; then
    record S2 PASS "伪造 XFF 不能绕限速" "来源=HTTP 未配 trusted_proxies，两个假 IP 响应码一致（$c1=$c2）→ 按同一真实 IP 计数"
  else
    record S2 FAIL "伪造 XFF 不能绕限速" "来源=HTTP 假 IP 得到不同待遇（$c1 vs $c2）"
  fi
}

s_retry_after() {
  if bin_or_src_has "Retry-After"; then
    record S8 SKIP "429 带 Retry-After" "来源=BIN 代码存在 Retry-After 下发；实测要把 default_rpm 打满，属压测动作（§8 第 8 项）"
  else
    record S8 FAIL "429 带 Retry-After" "来源=BIN 找不到 Retry-After 下发证据"
  fi
}

s_govulncheck() {
  if ! command -v govulncheck >/dev/null 2>&1; then
    record S11 SKIP "govulncheck 无高危" "本机未安装 govulncheck（按纪律不装新依赖，§8 第 11 项留给 CI）"
    return
  fi
  if (cd "$REPO_ROOT" && GOMODCACHE="${GOMODCACHE:-$REPO_ROOT/.cache/gomod}" govulncheck ./... >/dev/null 2>&1); then
    record S11 PASS "govulncheck 无高危" "来源=RUNTIME 无漏洞报告"
  else
    record S11 FAIL "govulncheck 无高危" "来源=RUNTIME govulncheck 有发现"
  fi
}

run_all() {
  c1; c2; c3; c4; c5; c6; c7; c8; c9; c10; c11; c12
  s_xff_runtime; s_retry_after; s_govulncheck
}

# selftest —— 反向验证（code-review 要求）：造一份故意违反基线的夹具，
# 断言 C1/C6/C7/C9/C10/C11/C12 全部翻 FAIL。没有它就无法区分「检查有效」与「恒 PASS」。
selftest() {
  local root
  root="$(mktemp -d)"
  trap 'rm -rf "$root"' RETURN
  mkdir -p "$root/etc" "$root/data"
  head -c 32 /dev/urandom >"$root/etc/master.key"
  chmod 644 "$root/etc/master.key"          # 破坏①：密钥权限过宽
  cat >"$root/config.yaml" <<EOF
server:
  listen: "0.0.0.0:8080"                     # 破坏②：非回环且明文
  insecure_public_listen: "warn"             # 且策略未收紧
  max_body_bytes: 1073741824                 # 破坏③：1 GiB 超入站上限
tls:
  mode: "off"
security:
  password_min_length: 6                     # 破坏④：低于 12
  session_ttl: 24h
  login_max_failures: 5
  login_lockout: 15m
limits:
  default_daily_tokens: 50000                # 破坏⑤：新建账户默认就有额度
  default_monthly_tokens: 0
  default_rpm: 60
  default_tpm: -1
  default_max_concurrency: 2
upstream:
  relay:
    aggregate_excluded: false                # 破坏⑥：聚合渠道进候选
store:
  path: "$root/data/freerouter.db"
  master_key_file: "$root/etc/master.key"
EOF
  sqlite3 "$root/data/freerouter.db" "CREATE TABLE t(x);" 2>/dev/null || \
    printf 'not a sqlite file\n' >"$root/data/freerouter.db"   # 破坏⑦：库无 schema_migrations

  local jsonfile rc=0
  jsonfile="$root/checklist.json"
  # 自检必须清掉 FR_ 覆盖：cfg_get 里环境变量优先级最高，若继承了外层会话的 FR_ 值，
  # 自检读到的就是「夹具之外的配置」，那样的 PASS/FAIL 都不作数。
  # 同时不传 --bin：让 BIN 类证据走源码 grep（REPO_ROOT 仍在），避免外层无二进制时
  # 传空串导致参数形状变化。
  env -u FR_SERVER_LISTEN -u FR_TLS_MODE -u FR_SERVER_INSECURE_PUBLIC_LISTEN \
      -u FR_STORE_PATH -u FR_STORE_MASTER_KEY_FILE -u FR_LIMITS_DEFAULT_RPM \
      -u FR_LIMITS_DEFAULT_DAILY_TOKENS -u FR_LIMITS_DEFAULT_MONTHLY_TOKENS \
      -u FR_SECURITY_PASSWORD_MIN_LENGTH -u FR_UPSTREAM_RELAY_AGGREGATE_EXCLUDED \
      -u FR_BASE_URL -u FR_CONFIG_PATH -u FR_BINARY \
      NO_COLOR=1 "$0" --config "$root/config.yaml" --json >"$jsonfile" 2>"$root/err.log" || rc=$?

  [[ -s "$jsonfile" ]] || { echo "selftest: 未产出结果（rc=$rc）"; sed -n '1,20p' "$root/err.log" >&2; return 1; }
  if [[ $rc -eq 0 ]]; then
    echo "selftest: 夹具本应触发 FAIL，但脚本退出 0 —— 检查存在恒 PASS 风险。"
    return 1
  fi
  # 踩过的坑：原先写成 `printf "$out" | python3 - <<'PY'`，heredoc 会**覆盖**管道成为
  # stdin，脚本里的 sys.stdin 于是是空的，所有项报 MISSING——看着像「检查失效」，
  # 其实是自检自己的 bug。结论：自检的判定数据一律走**文件参数**，不抢 stdin。
  python3 - "$jsonfile" C1 C6 C7 C9 C10 C11 C12 <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2:]
status = {}
with open(path, encoding="utf-8") as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            o = json.loads(line)
        except json.JSONDecodeError:
            continue
        status[o["id"]] = (o["status"], o["detail"])
bad = []
for k in want:
    st = status.get(k, ("MISSING", ""))
    print(f"  {k}: {st[0]}  {st[1][:100]}")
    if st[0] != "FAIL":
        bad.append(k)
if bad:
    print(f"未翻 FAIL：{', '.join(bad)}")
sys.exit(1 if bad else 0)
PY
}

if [[ $MODE_JSON -eq 0 && $SELFTEST -eq 0 ]]; then
  printf '%s\n' "FreeRouter 安全基线自检（docs/ops/01-security.md §8）"
  printf '%s\n' "  仓库=$REPO_ROOT"
  printf '%s\n' "  二进制=${BIN_PATH:-（未找到：BIN/CFG 类项将 SKIP）}"
  printf '%s\n' "  配置=${CONFIG_PATH:-（无配置文件：默认值 + FR_ 环境变量）} 解析来源=${CFG_SRC_KIND}"
  printf '%s\n' "  运行实例=${BASE_URL:-（未提供 --base-url：HTTP 类项将 SKIP）}"
  printf '%s\n' "----------------------------------------------------------------"
fi

if [[ $SELFTEST -eq 1 ]]; then selftest; exit $?; fi

run_all

if [[ $MODE_JSON -eq 0 ]]; then
  printf '%s\n' "----------------------------------------------------------------"
  printf '合计：PASS=%d FAIL=%d SKIP=%d\n' "$N_PASS" "$N_FAIL" "$N_SKIP"
  [[ $N_FAIL -eq 0 ]] || printf '%s\n' "存在 FAIL：按 §8 口径「任一项不通过就不上线」。"
fi
[[ $N_FAIL -eq 0 ]] || exit 1
exit 0
