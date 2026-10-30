#!/usr/bin/env bash
# install-update.sh —— FreeRouter 受控更新脚本（GitHub Actions deploy job 调用）
#
# 权限模型：ghrunner 对本脚本只有「执行权」（sudoers 单条白名单），
# 脚本本身 root 属主、无参数、所有路径写死——runner 侧只能影响
# /opt/freerouter/staging 里的内容，安装什么由这份脚本说了算。
#
# 布局约定（与 .github/workflows/deploy.yml 的 artifact 结构一致）：
#   /opt/freerouter/staging/bin/freerouter            后端二进制
#   /opt/freerouter/staging/bin/freerouter-web        前端边缘服务二进制
#   /opt/freerouter/staging/sidecar/                  sidecar 入口 + providers
#   /opt/freerouter/staging/internal/webui/dist/      vite 控制台产物
set -euo pipefail

STAGING=/opt/freerouter/staging

echo "[install] 校验 staging 内容"
for f in "$STAGING/bin/freerouter" "$STAGING/bin/freerouter-web" "$STAGING/sidecar/index.mjs" "$STAGING/internal/webui/dist/index.html"; do
  test -f "$f" || { echo "[install] 缺件：$f"; exit 1; }
done

echo "[install] 安装二进制"
install -m 0755 "$STAGING/bin/freerouter" /usr/local/bin/freerouter
install -m 0755 "$STAGING/bin/freerouter-web" /usr/local/bin/freerouter-web

echo "[install] 同步 sidecar 与控制台产物"
rsync -a --delete "$STAGING/sidecar/" /opt/freerouter/sidecar/
rsync -a --delete "$STAGING/internal/webui/dist/" /opt/freerouter/web/dist/
chown -R root:root /opt/freerouter/sidecar /opt/freerouter/web
chmod -R a+rX /opt/freerouter/sidecar /opt/freerouter/web

echo "[install] 重启服务"
systemctl restart freerouter freerouter-web
sleep 2

echo "[install] 冒烟"
systemctl is-active --quiet freerouter || { echo "[install] freerouter 未激活"; journalctl -u freerouter -n 20 --no-pager; exit 1; }
systemctl is-active --quiet freerouter-web || { echo "[install] freerouter-web 未激活"; journalctl -u freerouter-web -n 20 --no-pager; exit 1; }
TITLE=$(curl -sf http://127.0.0.1:8081/ | grep -oE '<title>[^<]*</title>' || true)
echo "[install] 首页标题：${TITLE:-<空>}"
case "${TITLE:-}" in
  *FreeRouter*) echo "[install] OK：控制台已就绪" ;;
  *) echo "[install] 标题异常（应为「FreeRouter 控制台」）"; exit 1 ;;
esac

echo "[install] 完成：$(date -u +%FT%TZ)"
