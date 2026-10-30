#!/usr/bin/env bash
# bootstrap.sh —— YlxcServer 一次性初始化（root 执行；只在首次部署/环境重建时跑）
#
# 做完这件事，之后的日常更新全部由 GitHub Actions 完成（staging + install-update.sh），
# 不再需要从开发机直传任何文件。
#
# 用法（在服务器上、与 deploy-kit 同目录执行）：
#   bash bootstrap.sh /path/to/deploy-kit
# deploy-kit 内容（由开发机 scp 上去，见 docs/ops/02-deploy.md 形态 D）：
#   deploy-kit/freerouter.service          后端单元（仓库原样）
#   deploy-kit/freerouter-web.service      前端单元（仓库原样）
#   deploy-kit/install-update.sh           受控更新脚本（仓库原样）
#
# 环境前置：
#   · DSH 依赖根与 providers-hub 已随早前的部署包解到 /root/fr-deploy/fr-deploy/（DEV_ROOT/HUB_SRC 所指）
#     若不存在，本脚本会拒绝继续——sidecar 的 B 层依赖无法从网络自动获取
#   · node 在 nix store（NIX_NODE）；ghrunner 已存在（actions runner 服务）
set -euo pipefail

KIT="${1:?用法: bootstrap.sh /path/to/deploy-kit}"
DEV_ROOT=/root/fr-deploy/fr-deploy/usr/lib/node_modules/@deepseek-ai/dsh
HUB_SRC=/root/fr-deploy/fr-deploy/home/ylxc/.local/share/dsh/profiles/web/node_modules/dsh-providers-hub
HUB_DST=/opt/freerouter/hub/dsh-providers-hub
RUNNER_USER=ghrunner

echo "== 0. 前置自检 =="
test -f "$KIT/freerouter.service" && test -f "$KIT/freerouter-web.service" && test -f "$KIT/install-update.sh"
test -f "$DEV_ROOT/node_modules/@deepseek-ai/dsh-llm/package.json" \
  || { echo "DSH 依赖根缺失（$DEV_ROOT）；先上传解包或调整 DEV_ROOT"; exit 1; }
test -x "$NIX_NODE" || { echo "nix node 不在预期位置：$NIX_NODE"; exit 1; }
id "$RUNNER_USER" >/dev/null || { echo "runner 用户 $RUNNER_USER 不存在"; exit 1; }

echo "== 1. 服务用户与目录 =="
id freerouter &>/dev/null || useradd --system --home /var/lib/freerouter --shell /usr/sbin/nologin freerouter
install -d -m 0750 -o freerouter -g freerouter /var/lib/freerouter /etc/freerouter
install -d -m 0755 /opt/freerouter /opt/freerouter/bin /opt/freerouter/web /usr/local/bin
install -d -m 0755 -o "$RUNNER_USER" -g "$RUNNER_USER" /opt/freerouter/staging   # runner 交接区
install -d -m 0755 /usr/lib/node_modules/@deepseek-ai/dsh/node_modules

echo "== 2. DSH 依赖根（sidecar B 层依赖，一次到位）=="
if [ -f /usr/lib/node_modules/@deepseek-ai/dsh/node_modules/@deepseek-ai/dsh-llm/package.json ]; then
  echo "依赖根已在位，跳过"
else
  cp -a "$DEV_ROOT/node_modules/@deepseek-ai" /usr/lib/node_modules/@deepseek-ai/dsh/node_modules/
  cp -a "$DEV_ROOT/package.json" /usr/lib/node_modules/@deepseek-ai/dsh/package.json
  chown -R root:root /usr/lib/node_modules/@deepseek-ai
  chmod -R a+rX /usr/lib/node_modules/@deepseek-ai
  echo "依赖根安装完成"
fi

echo "== 2b. providers-hub（sidecar 只读参考，独立于 sidecar 目录以免被更新冲掉）=="
if [ -f "$HUB_DST/package.json" ]; then
  echo "hub 已在位，跳过"
else
  test -d "$HUB_SRC" || { echo "hub 源缺失：$HUB_SRC"; exit 1; }
  install -d -m 0755 /opt/freerouter/hub
  cp -a "$HUB_SRC" "$HUB_DST"
  chown -R root:root /opt/freerouter/hub
  chmod -R a+rX /opt/freerouter/hub
  echo "hub 安装完成 → $HUB_DST"
fi

echo "== 3. node 全局链接（freerouter 无权访问 /root 的 nix profile）=="
ln -sf "$NIX_NODE" /usr/local/bin/node
/usr/local/bin/node --version

echo "== 4. 主密钥（幂等：已有则保留）=="
if [ ! -f /etc/freerouter/master.key ]; then
  head -c 32 /dev/urandom > /etc/freerouter/master.key
  chown freerouter:freerouter /etc/freerouter/master.key
  chmod 0600 /etc/freerouter/master.key
  echo "已生成新主密钥"
else
  echo "主密钥已存在，保留"
fi
chown freerouter:freerouter /etc/freerouter
chmod 0750 /etc/freerouter

echo "== 5. 生产配置（幂等：已有则保留，避免覆盖运维手工改动）=="
if [ -f /etc/freerouter/config.yaml ]; then
  echo "/etc/freerouter/config.yaml 已存在，保留"
else
  install -m 0640 -o root -g freerouter /dev/null /etc/freerouter/config.yaml
  cat > /etc/freerouter/config.yaml <<'YAML'
# FreeRouter 生产配置（YlxcServer / Debian 13，形态 D：前后端分离 + systemd）
# 键语义见 docs/ops/02-deploy.md 第 2 节；环境变量 FR_* 可逐项覆盖。
server:
  # 后端只监听回环：外部唯一入口是 freerouter-web（0.0.0.0:8081）。
  listen: "127.0.0.1:8080"
  # 对外地址随前端边缘服务走（8081）。
  public_url: "http://172.24.0.1:8081"
  # 回环监听不会触发明文告警；此项仅当 listen 改成非回环时才有意义。
  insecure_public_listen: "warn"
  behind_proxy: false
  read_header_timeout: "10s"
  idle_timeout: "120s"
  max_body_bytes: 33554432
  shutdown_grace: "30s"
  auto_open_browser: false

tls:
  mode: "off"

security:
  trusted_proxies: []
  password_min_length: 12
  session_ttl: "24h"
  login_max_failures: 5
  login_lockout: "15m"
  registration_enabled: true
  registration_requires_approval: true

limits:
  default_daily_tokens: 0
  default_monthly_tokens: 0
  default_rpm: 60
  default_tpm: -1
  default_max_concurrency: 2
  default_keys_per_account: 10
  global_daily_tokens: 0
  global_rpm: 0
  global_concurrency: 200
  queue_timeout: "5s"

upstream:
  relay:
    enabled: true
    timeout: "300s"
    connect_timeout: "5s"
    max_retries: 2
    model_refresh_interval: "10m"
    aggregate_excluded: true
    allowlist: []

sidecar:
  enabled: true
  # 服务用户读不到 /root/.nix-profile，走 /usr/local/bin 的全局链接。
  node_path: "/usr/local/bin/node"
  entry: "/opt/freerouter/sidecar/index.mjs"
  ipc: "stdio"
  hub_path: "/opt/freerouter/hub/dsh-providers-hub"
  deps_root: "/usr/lib/node_modules/@deepseek-ai/dsh/node_modules"
  startup_timeout: "20s"

store:
  path: "/var/lib/freerouter/freerouter.db"
  master_key_file: "/etc/freerouter/master.key"
  busy_timeout: "5s"
  backup_dir: "/var/lib/freerouter/backups"
  audit_retain_days: 0
  request_log_retain_days: 30

log:
  level: "info"
  format: "json"
YAML
  echo "已写入生产配置"
fi

echo "== 6. systemd 单元与受控更新脚本 =="
install -D -m 0644 "$KIT/freerouter.service" /etc/systemd/system/freerouter.service
install -D -m 0644 "$KIT/freerouter-web.service" /etc/systemd/system/freerouter-web.service
install -m 0755 "$KIT/install-update.sh" /opt/freerouter/bin/install-update.sh
chown root:root /opt/freerouter/bin/install-update.sh

echo "== 7. sudo 白名单（Actions deploy job 的唯一 root 入口）=="
cat > /etc/sudoers.d/freerouter-deploy <<'SUDOERS'
# FreeRouter 自动部署：ghrunner 只能以 root 跑这一条受控脚本。
# 脚本 root 属主、无参数、路径写死——runner 侧不能注入任何命令。
ghrunner ALL=(root) NOPASSWD: /opt/freerouter/bin/install-update.sh
SUDOERS
chmod 0440 /etc/sudoers.d/freerouter-deploy
visudo -c

echo "== 8. 启动 =="
systemctl daemon-reload
systemctl enable --now freerouter freerouter-web
sleep 3
systemctl --no-pager is-active freerouter freerouter-web

echo "== bootstrap 完成 =="
echo "对外入口: http://172.24.0.1:8081 （控制台 + API 同源）"
echo "后续更新: git push → GitHub Actions 自动部署；日常运维不再需要手工传文件"
