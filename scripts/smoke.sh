#!/usr/bin/env bash
# smoke.sh —— 端到端冒烟：起临时实例 → 探活 → 初始化 → 登录 → 模型目录 → 优雅退出。
#
# 用法：
#   ./scripts/smoke.sh [--bin F] [--config F] [--port N] [--keep] [--docker] [--timeout S]
#
# 选项：
#   --bin <path>     freerouter 二进制（默认 bin/freerouter，其次 ./freerouter）
#   --config <path>  以这份配置为底，再用 FR_ 覆盖到临时目录/临时端口
#   --port <N>       临时实例端口（默认 0 = 内核随机分配，从启动日志回读实际端口）
#   --keep           保留临时目录（默认成功即删；失败总是保留供排查）
#   --docker         用 deploy/docker/compose.yaml 起容器形态做同一套断言
#                    （本地没有 docker/镜像时报 SKIP 退出码 3，不装依赖）
#   --timeout <S>    每个探测步骤的超时秒数，默认 20
#   -h | --help      帮助
#
# 退出码：
#   0 = 全部断言通过
#   1 = 至少一个断言失败（输出里每个 FAIL 都带原因）
#   2 = 用法错误
#   3 = 前置条件缺失（无二进制/无 docker）——SKIP 而不是失败
#
# ── 覆盖的断言（每条都是对**真实行为**的断言，不是输出里找字符串）────────
#   S1 /healthz 与 /readyz 分离且都 200（进程活着 + 自举完成）
#   S2 /api/version 200 且 JSON 有 name/version 字段
#   S3 安全响应头四件套（CSP/nosniff/frame/referrer）真实出现在响应里
#   S4 初始化向导：空库时 setup_required=true → 建 admin → 二次 init 409
#   S5 登录拿 HttpOnly+SameSite 会话 cookie，GET /api/auth/session 用它过鉴权
#   S6 无 Key 打 /v1/models → 401 invalid_api_key（Key 面的失败必须长这样）
#   S7 登出后会话立即失效（再访问 session → 401）
#   S8 SIGTERM 优雅退出：退出码 0 且日志含「已关闭」
#   S9 凭据明文零落库：对临时库跑 M8 验收同一条 strings 扫描
#   S10 数据库文件权限 0600（容器/systemd 形态下 umask 0077 的直接断言）
#   S11 sidecar 存活且应答 health 帧（若二进制以 sidecar.enabled=true 启动）
#
# ── 为什么临时实例必须「一个 bash 调用内闭环」──────────────────────
# 本脚本要在 CI/沙箱（bwrap --die-with-parent）里可用：那种环境里后台进程
# 活不过当前 shell。所以脚本内不依赖「先起服务再另一个命令探测」的两段式，
# 起服务、探测、杀服务全部在本进程的子进程树内完成，退出前必然清理。
#
# ── 临时实例的隔离 ────────────────────────────────────────────────
# 数据/密钥全部落在 mktemp -d 的临时目录；端口用 :0（内核分配）避免与
# 常驻实例冲突；sidecar 默认关（冒烟不依赖 providers-hub，见 S11 的说明）。

set -euo pipefail

SCRIPT_DIR="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
FR_REPO_ROOT="$(cd -P "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"
# shellcheck source=./fr-lib.sh
source "$SCRIPT_DIR/fr-lib.sh"

usage() { sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

BIN=""
CONFIG_PATH="${FR_CONFIG_PATH:-}"
PORT=0
KEEP=0
DOCKER_MODE=0
STEP_TIMEOUT=20

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin)     [[ $# -ge 2 ]] || { echo "--bin 需要参数" >&2; exit 2; }; BIN="$2"; shift 2 ;;
    --config)  [[ $# -ge 2 ]] || { echo "--config 需要参数" >&2; exit 2; }; CONFIG_PATH="$2"; shift 2 ;;
    --port)    [[ $# -ge 2 ]] || { echo "--port 需要参数" >&2; exit 2; }; PORT="$2"; shift 2 ;;
    --keep)    KEEP=1; shift ;;
    --docker)  DOCKER_MODE=1; shift ;;
    --timeout) [[ $# -ge 2 ]] || { echo "--timeout 需要参数" >&2; exit 2; }; STEP_TIMEOUT="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ "$PORT" =~ ^[0-9]+$ ]] || { echo "--port 必须是数字" >&2; exit 2; }
[[ "$STEP_TIMEOUT" =~ ^[0-9]+$ ]] || { echo "--timeout 必须是数字" >&2; exit 2; }

N_PASS=0 N_FAIL=0 N_SKIP=0
RED="" GRN="" YLW="" RST=""
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; RST=$'\033[0m'
fi
ok()   { N_PASS=$((N_PASS+1)); printf '%sPASS%s %-4s %s\n' "$GRN" "$RST" "$1" "$2"; }
bad()  { N_FAIL=$((N_FAIL+1)); printf '%sFAIL%s %-4s %s\n' "$RED"  "$RST" "$1" "$2"; printf '       %s\n' "${3:-}" >&2; }
skip() { N_SKIP=$((N_SKIP+1)); printf '%sSKIP%s %-4s %s\n' "$YLW"  "$RST" "$1" "$2"; }

TMP=""
cleanup() {
  if [[ -n "${SMOKE_PID:-}" ]]; then kill -TERM "$SMOKE_PID" 2>/dev/null || true; fi
  if [[ -n "${SMOKE_CTR:-}" ]]; then timeout 10 docker rm -f "$SMOKE_CTR" >/dev/null 2>&1 || true; fi
  if [[ -n "$TMP" && -d "$TMP" && $KEEP -eq 0 && $N_FAIL -eq 0 ]]; then rm -rf "$TMP"; fi
}
trap cleanup EXIT

# ── 容器模式：用同一套断言打真实容器（lead ④ 要求的可判据形态）───────
if [[ $DOCKER_MODE -eq 1 ]]; then
  command -v docker >/dev/null 2>&1 || { skip "D0" "本机没有 docker，容器冒烟跳过（不装依赖）"; exit 3; }
  if ! timeout 15 docker info >/dev/null 2>&1; then
    skip "D0" "docker daemon 未运行，容器冒烟跳过"; exit 3
  fi
  IMAGE="${FR_SMOKE_IMAGE:-freerouter:local}"
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    fr_log INFO "本地没有 $IMAGE，尝试构建（deploy/docker/Dockerfile）"
    if ! docker build -f "$FR_REPO_ROOT/deploy/docker/Dockerfile" -t "$IMAGE" "$FR_REPO_ROOT" >&2; then
      skip "D0" "镜像 $IMAGE 不存在且构建失败（或网络不可用）"; exit 3
    fi
  fi
  TMP="$(mktemp -d "${FR_SMOKE_TMP:-/tmp}/fr-smoke.XXXXXX")"
  mkdir -p "$TMP/data" "$TMP/etc"
  head -c 32 /dev/urandom >"$TMP/etc/master.key"; chmod 600 "$TMP/etc/master.key"
  chmod 755 "$TMP"  # 目录要能被容器内 uid 10001 进
  chmod 700 "$TMP/etc"; chmod 700 "$TMP/data"; chown -R 10001:10001 "$TMP/data" "$TMP/etc" 2>/dev/null || true
  CNAME="fr-smoke-$$"
  # host 网络 + 回环监听：与 compose.yaml 的默认形态一致。
  if ! timeout 40 docker run -d --name "$CNAME" \
      --network host --user "10001:10001" --read-only --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --tmpfs /tmp:size=64m,mode=1777 \
      -v "$TMP/data":/var/lib/freerouter \
      -v "$TMP/etc/master.key":/run/secrets/master.key:ro \
      -e FR_SERVER_LISTEN=127.0.0.1:0 \
      -e FR_STORE_PATH=/var/lib/freerouter/freerouter.db \
      -e FR_STORE_MASTER_KEY_FILE=/run/secrets/master.key \
      -e FR_SIDECAR_ENABLED=false \
      "$IMAGE" >/dev/null 2>&1; then
    bad "D0" "容器起不来" "docker logs $CNAME"
    SMOKE_CTR="$CNAME"
    exit 1
  fi
  SMOKE_CTR="$CNAME"
  BASE="http://127.0.0.1:8080"
  # 回读实际端口（host 网络 + :0 时端口在容器日志里）。
  for i in $(seq 1 $((STEP_TIMEOUT * 4))); do
    PL=$(docker logs "$CNAME" 2>&1 | grep -oE 'listen=127\.0\.0\.1:[0-9]+' | tail -1 | grep -oE '[0-9]+$' || true)
    [[ -n "$PL" ]] && break
    sleep 0.5
  done
  [[ -n "$PL" ]] && BASE="http://127.0.0.1:$PL"

  # —— 容器特有的运行时断言（不是「配置里写了就算」）——
  UID_C=$(docker exec "$CNAME" id -u 2>/dev/null || echo "?")
  [[ "$UID_C" == "0" ]] && bad "D1" "容器内 uid=0（root 运行）" || ok "D1" "容器内 uid=$UID_C 非 root"
  CAPEFF=$(docker exec "$CNAME" sh -c 'grep CapEff /proc/self/status' 2>/dev/null | awk '{print $2}' || echo "?")
  if [[ "$CAPEFF" =~ ^0+$ ]]; then ok "D2" "容器内 CapEff=$CAPEFF（能力全空）"; else bad "D2" "CapEff=$CAPEFF 非零"; fi
  docker exec "$CNAME" sh -c 'touch /etc/x 2>/dev/null' && bad "D3" "容器内可写 /etc（read_only 未生效）" || ok "D3" "容器内写 /etc 失败（read_only 生效）"

  . "$SCRIPT_DIR/smoke-lib.sh"   # 共享断言函数（见文件头说明）
  run_http_assertions "$BASE" "$TMP" "$STEP_TIMEOUT"

  docker exec "$CNAME" sh -c 'stat -c %a /var/lib/freerouter/freerouter.db' >/dev/null 2>&1
  DBPERM=$(docker exec "$CNAME" sh -c 'stat -c %a /var/lib/freerouter/freerouter.db 2>/dev/null' || echo "?")
  [[ "$DBPERM" == "600" || "$DBPERM" == "640" ]] && ok "D4" "容器内库权限 $DBPERM" || bad "D4" "容器内库权限 $DBPERM（期望 600）"

  docker rm -f "$CNAME" >/dev/null 2>&1; SMOKE_CTR=""
  printf '\n合计：PASS=%d FAIL=%d SKIP=%d\n' "$N_PASS" "$N_FAIL" "$N_SKIP"
  [[ $N_FAIL -eq 0 ]] || exit 1
  exit 0
fi

# ── 裸进程模式 ───────────────────────────────────────────────────────
BIN="$(fr_find_bin "$BIN" || true)"
[[ -n "$BIN" ]] || { skip "P0" "找不到 freerouter 二进制（先 make build）"; exit 3; }

TMP="$(mktemp -d "${FR_SMOKE_TMP:-/tmp}/fr-smoke.XXXXXX")"
mkdir -p "$TMP/data" "$TMP/etc"
head -c 32 /dev/urandom >"$TMP/etc/master.key"; chmod 600 "$TMP/etc/master.key"

# 临时实例的 FR_ 覆盖：全指向临时目录、临时端口、关 sidecar（冒烟不依赖 providers-hub）。
export FR_SERVER_LISTEN="127.0.0.1:$PORT" \
       FR_STORE_PATH="$TMP/data/freerouter.db" \
       FR_STORE_MASTER_KEY_FILE="$TMP/etc/master.key" \
       FR_STORE_BACKUP_DIR="$TMP/data/backups" \
       FR_SIDECAR_ENABLED=false \
       FR_LOG_FORMAT=text \
       FR_LOG_LEVEL=info

# umask 077：与 systemd 单元 / 容器 entrypoint 的部署形态对齐——
# 冒烟测的就是上线形态的行为（库文件 0600），裸 shell 默认 022 测不出这条。
umask 077

"$BIN" ${CONFIG_PATH:+--config "$CONFIG_PATH"} >"$TMP/server.log" 2>&1 &
SMOKE_PID=$!

# 等监听行出现并回读端口（:0 时端口由内核分配）。
BASE=""
for i in $(seq 1 $((STEP_TIMEOUT * 4))); do
  L=$(grep -oE 'listen=127\.0\.0\.1:[0-9]+' "$TMP/server.log" 2>/dev/null | tail -1 || true)
  if [[ -n "$L" ]]; then
    PORT_ACTUAL="${L##*:}"
    BASE="http://127.0.0.1:$PORT_ACTUAL"
    # 端口可探通才继续（监听行出现与 accept 就绪之间有一个窗口）。
    curl -s -m 2 -o /dev/null "$BASE/healthz" 2>/dev/null && break
  fi
  kill -0 "$SMOKE_PID" 2>/dev/null || { break; }
  sleep 0.5
done
if [[ -z "$BASE" ]]; then
  bad "P0" "实例未在 ${STEP_TIMEOUT}s 内开始监听" "日志尾部：$(tail -5 "$TMP/server.log" 2>/dev/null | tr '\n' ' ')"
  exit 1
fi
ok "P0" "实例已监听 $BASE（pid=$SMOKE_PID，临时目录 $TMP）"

. "$SCRIPT_DIR/smoke-lib.sh"
run_http_assertions "$BASE" "$TMP" "$STEP_TIMEOUT"

# S8：优雅退出（SIGTERM → 退出码 0 → 日志含「已关闭」）。
kill -TERM "$SMOKE_PID" 2>/dev/null || true
RC=0
wait "$SMOKE_PID" 2>/dev/null || RC=$?
SMOKE_PID=""
if [[ $RC -eq 0 ]] && grep -q "已关闭" "$TMP/server.log"; then
  ok "S8" "SIGTERM 优雅退出（rc=0，日志含「已关闭」）"
else
  bad "S8" "优雅退出异常（rc=$RC）" "$(tail -3 "$TMP/server.log" | tr '\n' ' ')"
fi

printf '\n合计：PASS=%d FAIL=%d SKIP=%d\n' "$N_PASS" "$N_FAIL" "$N_SKIP"
if [[ $N_FAIL -gt 0 ]]; then
  printf '日志与数据保留在：%s（复现：--keep 或看 server.log）\n' "$TMP"
  exit 1
fi
[[ $KEEP -eq 1 ]] && printf '临时目录保留：%s\n' "$TMP"
exit 0
