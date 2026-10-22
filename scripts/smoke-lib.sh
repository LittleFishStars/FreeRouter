#!/usr/bin/env bash
# smoke-lib.sh —— smoke.sh 的 HTTP 断言函数库（被两种模式共享：裸进程 / 容器）
#
# 为什么单独一个文件：容器模式与裸进程模式要跑**同一套** HTTP 断言
# （S1–S7/S9/S10），两处复制必然漂移——漂移的后果是「裸进程过了、容器没测」，
# 恰好把容器形态特有的问题漏掉。断言集中在这里，模式差异只留在 smoke.sh。
#
# 依赖：ok / bad / skip 计数函数与 TMP 变量由 smoke.sh 定义并导出，
# 这里只提供 run_http_assertions <base-url> <tmpdir> <step-timeout-s>。

run_http_assertions() {
  local BASE="$1" TMPDIR_ARG="$2" STO="$3"
  local CK="$TMPDIR_ARG/cookie.txt"

  # S1 探活分离：/healthz（进程）与 /readyz（自举）都必须 200。
  local h r
  h=$(curl -s -m "$STO" -o /dev/null -w '%{http_code}' "$BASE/healthz" 2>/dev/null || echo 000)
  r=$(curl -s -m "$STO" -o /dev/null -w '%{http_code}' "$BASE/readyz" 2>/dev/null || echo 000)
  if [[ "$h" == "200" && "$r" == "200" ]]; then
    ok "S1" "/healthz 与 /readyz 均 200（探活分离）"
  else
    bad "S1" "探活异常 healthz=$h readyz=$r"
  fi

  # S2 版本面：JSON 里要有 name 与 version 字段（jq 存在时做字段断言，否则做文本断言）。
  local v
  v=$(curl -s -m "$STO" "$BASE/api/version" 2>/dev/null || true)
  if command -v jq >/dev/null 2>&1; then
    if jq -e '.name == "freerouter" and (.version | length > 0)' <<<"$v" >/dev/null 2>&1; then
      ok "S2" "/api/version 返回 name/version（$(jq -r .version <<<"$v")）"
    else
      bad "S2" "/api/version 字段缺失" "$v"
    fi
  elif grep -q '"name"' <<<"$v" && grep -q '"version"' <<<"$v"; then
    ok "S2" "/api/version 返回 name/version（无 jq，文本断言）"
  else
    bad "S2" "/api/version 字段缺失" "$v"
  fi

  # S3 安全响应头四件套真实出现在响应里（中间件真的在跑，不是「代码里有」）。
  local HDRS
  HDRS=$(curl -s -m "$STO" -D - -o /dev/null "$BASE/healthz" 2>/dev/null || true)
  local -a WANT=("X-Content-Type-Options: nosniff" "X-Frame-Options: DENY" "Content-Security-Policy:" "Referrer-Policy:")
  local MISS=0 hh
  for hh in "${WANT[@]}"; do grep -qiF -- "$hh" <<<"$HDRS" || MISS=1; done
  if [[ $MISS -eq 0 ]]; then ok "S3" "四个安全响应头齐备"
  else bad "S3" "安全响应头缺失" "$HDRS"; fi

  # S4 初始化向导：空库 → setup_required=true；建 admin；二次 init 必须 409。
  local st
  st=$(curl -s -m "$STO" "$BASE/api/setup/status" 2>/dev/null || true)
  if grep -q '"setup_required":true' <<<"$st"; then
    ok "S4a" "空库 setup_required=true"
  else
    bad "S4a" "空库状态异常" "$st"
  fi
  local i1
  i1=$(curl -s -m "$((STO + 10))" -X POST -H 'Content-Type: application/json' \
       -d '{"username":"smokeadmin","password":"Smoke-Password-123","display_name":"Smoke Admin"}' \
       -o /dev/null -w '%{http_code}' "$BASE/api/setup/init" 2>/dev/null || echo 000)
  if [[ "$i1" == "201" ]]; then ok "S4b" "初始化建 admin 成功（201）"
  else bad "S4b" "初始化返回 $i1（期望 201）"; fi
  local i2
  i2=$(curl -s -m "$STO" -X POST -H 'Content-Type: application/json' \
       -d '{"username":"smokeadmin2","password":"Smoke-Password-123"}' \
       -o /dev/null -w '%{http_code}' "$BASE/api/setup/init" 2>/dev/null || echo 000)
  if [[ "$i2" == "409" ]]; then ok "S4c" "二次 init 409（向导窗口正确关闭）"
  else bad "S4c" "二次 init 返回 $i2（期望 409）"; fi

  # S5 登录 → 会话 cookie 属性 → /api/auth/session 过鉴权。
  # X-Requested-With：写方法要过 CSRF 自定义头检查（mwSession 的口径）。
  local login_code
  login_code=$(curl -s -m "$((STO + 10))" -c "$CK" -X POST -H 'Content-Type: application/json' \
       -H 'X-Requested-With: smoke' \
       -d '{"username":"smokeadmin","password":"Smoke-Password-123"}' \
       -o /dev/null -w '%{http_code}' "$BASE/api/auth/login" 2>/dev/null || echo 000)
  if [[ "$login_code" == "200" ]]; then ok "S5a" "登录成功（200）"
  else bad "S5a" "登录返回 $login_code"; fi
  local SETCK
  SETCK=$(curl -s -m "$STO" -D - -o /dev/null -X POST -H 'Content-Type: application/json' \
       -H 'X-Requested-With: smoke' \
       -d '{"username":"smokeadmin","password":"Smoke-Password-123"}' \
       "$BASE/api/auth/login" 2>/dev/null | grep -i '^set-cookie' || true)
  if grep -qi 'HttpOnly' <<<"$SETCK" && grep -qi 'SameSite=Lax' <<<"$SETCK"; then
    ok "S5b" "会话 cookie HttpOnly + SameSite=Lax"
  else
    bad "S5b" "cookie 属性异常" "$SETCK"
  fi
  local sess
  sess=$(curl -s -m "$STO" -b "$CK" -o /dev/null -w '%{http_code}' "$BASE/api/auth/session" 2>/dev/null || echo 000)
  if [[ "$sess" == "200" ]]; then ok "S5c" "会话 cookie 通过 /api/auth/session 鉴权"
  else bad "S5c" "session 返回 $sess（期望 200）"; fi

  # S6 Key 面：无凭据打 /v1/models 必须 401 invalid_api_key（单出口错误码）。
  local m
  m=$(curl -s -m "$STO" "$BASE/v1/models" 2>/dev/null || true)
  local mc
  mc=$(curl -s -m "$STO" -o /dev/null -w '%{http_code}' "$BASE/v1/models" 2>/dev/null || echo 000)
  if [[ "$mc" == "401" ]] && grep -q 'invalid_api_key' <<<"$m"; then
    ok "S6" "无 Key 访问 /v1/models → 401 invalid_api_key"
  else
    bad "S6" "Key 面行为异常（$mc）" "$m"
  fi

  # S7 登出即失效：登出（幂等 200）后再访问 session 必须 401。
  local lo
  lo=$(curl -s -m "$STO" -b "$CK" -c "$CK" -X POST -H 'X-Requested-With: smoke' \
       -o /dev/null -w '%{http_code}' "$BASE/api/auth/logout" 2>/dev/null || echo 000)
  local sess2
  sess2=$(curl -s -m "$STO" -b "$CK" -o /dev/null -w '%{http_code}' "$BASE/api/auth/session" 2>/dev/null || echo 000)
  if [[ "$lo" == "200" && "$sess2" == "401" ]]; then
    ok "S7" "登出后会话立即失效（logout=$lo, session=$sess2）"
  else
    bad "S7" "登出语义异常（logout=$lo, session=$sess2）"
  fi

  # S9 凭据明文零落库（M8 验收同一条命令）：对**本实例刚写出的库**扫描。
  # 库路径取 FR_STORE_PATH（容器模式下宿主路径在 $TMPDIR_ARG/data 下）。
  local db="${FR_STORE_PATH:-$TMPDIR_ARG/data/freerouter.db}"
  # 容器模式里 FR_STORE_PATH 是容器内路径；宿主侧对应 $TMPDIR_ARG/data。
  [[ -f "$db" ]] || db="$TMPDIR_ARG/data/freerouter.db"
  if [[ -f "$db" ]] && command -v strings >/dev/null 2>&1; then
    local leaked
    leaked=$(strings -a "$db" 2>/dev/null | grep -cE 'sk-[A-Za-z0-9]{12,}|Bearer [A-Za-z0-9._-]{12,}' || true)
    if [[ "$leaked" == "0" ]]; then ok "S9" "库文件 strings 扫描零命中凭据明文"
    else bad "S9" "库文件出现 $leaked 处凭据明文样式"; fi
  else
    skip "S9" "库文件不可达（$db）或无 strings"
  fi

  # S10 数据库文件权限 0600：umask 077（systemd/entrypoint 同款）下应自动成立。
  if [[ -f "$db" ]]; then
    local p
    p=$(stat -c '%a' "$db" 2>/dev/null || echo '?')
    if [[ "$p" == "600" ]]; then ok "S10" "库文件权限 0600（umask 077 形态生效）"
    else bad "S10" "库文件权限 $p（期望 600；裸进程模式依赖脚本内 umask 077）"; fi
  else
    skip "S10" "库文件不存在"
  fi

  # S11 sidecar：本冒烟默认关（FR_SIDECAR_ENABLED=false——providers-hub 是宿主依赖，
  # CI 里没有）。这里只断言「关掉时不拖垮主进程」：readyz 已经在 S1 里 200 过了，
  # 所以 S11 的存在意义是把「sidecar 缺失 ≠ 不健康」这条口径显式写出来。
  skip "S11" "sidecar 断言：冒烟形态 sidecar.enabled=false（04-sidecar 的「失败不阻塞」已在 S1 覆盖）"
}
