# syntax=docker/dockerfile:1
# FreeRouter —— 多阶段构建：前端 → Go 主二进制 → 运行阶段（非 root）
#
# 构建（上下文必须是仓库根，因为要访问 web/ internal/ cmd/ sidecar/）：
#   docker build -f deploy/docker/Dockerfile -t freerouter:local .
#
# ── 为什么是三个阶段 ────────────────────────────────────────────────
# 1) webbuild：Vite 产物必须落到 internal/webui/dist 才能被 go:embed 嵌进二进制
#    （web/vite.config.ts 的 outDir 就是为这条约束写的，跨目录 embed 会被 go 拒绝）。
#    所以前端构建必须**先于** go build，且产物要能被 go 阶段看见 → 用 COPY --from。
# 2) gobuild：主二进制。带 -tags webui_dist 才是「单二进制含界面」的交付形态；
#    不带该 tag 时 internal/webui 走 placeholder.go，界面是一页提示（双形态见 M9）。
# 3) runtime：只有它进最终镜像。构建工具链（go、pnpm、gcc）一概不带——
#    镜像里少一个可执行工具就少一个被拿下后的落脚点。
#
# ── 为什么运行阶段用 node 而不是 scratch/alpine ──────────────────────
# 进程拓扑是固定的两个进程（02-deploy.md §1.1）：主进程要拉起 sidecar 这个 **Node 子进程**。
# 没有 node 的二进制镜像里，sidecar.enabled=true 会永远失败（虽然不阻塞主进程，
# 但贡献/relay 能力全废）。所以 node 是运行期的真实依赖，不是构建依赖。
# 用 slim 变体：node:22-alpine 约 55 MB，比完整 alpine+手动装 node 更小且自带 CA。
# Node ≥ 22.19 是 providers-hub 的 engines 要求（sidecar/README.md）。
#
# ── 主密钥绝不进镜像（硬红线）──────────────────────────────────────
# 本文件没有任何 COPY/ADD 涉及 master.key，也没有 ARG/ENV 接收密钥内容：
# 镜像会被推到 registry、被复制到开发机，把密钥打进层里等于把它公开。
# 运行期由 compose 以只读 bind mount 或 secrets 注入路径（见 compose.yaml）。
# scripts/security-checklist.sh 的 C6 会对本文件做反向 grep 断言。
#
# ── providers-hub 不在镜像里 ────────────────────────────────────────
# sidecar 需要 `import()` providers-hub 的 lib/*-adapter.js，那是**宿主上的代码**
# 且依赖 DSH 登录态（02-deploy.md §1.3：两台机器的形态不支持）。打副本进镜像会让
# 版本与宿主漂移，且把登录态问题变成镜像问题。做法是只读挂载宿主目录，见 compose。

# ── 阶段 1：前端产物 ────────────────────────────────────────────────
FROM node:22-alpine AS webbuild
WORKDIR /src/web

# 先只拷依赖清单：源码改动不该让 pnpm install 这一层重来（构建缓存的主要收益点）。
# pnpm-workspace.yaml 也必须拷：pnpm 11+ 的构建脚本白名单（allowBuilds: esbuild）
# 只认这个文件，缺它则 esbuild postinstall 被拒、vite 拿不到平台二进制。
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
# pnpm 版本必须**写死**而不是 `corepack prepare --activate`（无版本时 corepack 会
# 去读 package.json 的 packageManager 字段，而 web/package.json 里没有这个字段，
# 结果是拿到 corepack 内置的最新版）：lockfileVersion 9.0 允许 pnpm 9~12 都读，
# 但小版本间的解析差异会让镜像里的 node_modules 与宿主的不可复现，
# 而这个差异只在构建阶段表现为“能过”，到运行阶段才表现为行为不一致。
# 与开发机对齐的代价是一次手工修改，换回的是可复现构建。
RUN corepack enable && corepack prepare pnpm@12.15.0 --activate && \
    # XDG_RUNTIME_DIR 必须显式给：pnpm 在容器里拿不到它时会静默退到一个不可写路径后失败。
    XDG_RUNTIME_DIR=/tmp pnpm install --frozen-lockfile --store-dir /tmp/.pnpm-store

COPY web/ ./
# 产物写到 ../internal/webui/dist（vite.config.ts 的 outDir）：先建出目录，
# 否则空目录不存在时 vite 会自己创建，而属主是构建用户、阶段 2 拷贝时一切正常，
# 所以这里不建也不会失败——但显式建出来能让「改 outDir 就断」这件事在构建期暴露。
RUN mkdir -p /src/internal/webui/dist && XDG_RUNTIME_DIR=/tmp pnpm build

# ── 阶段 2：Go 主二进制 ────────────────────────────────────────
FROM golang:1.27-alpine AS gobuild
WORKDIR /src

# GOPROXY 用构建参数传入：CI/内网环境的 Go 模块代理与构建机本机 go env 不一定一致
# （实测：容器内直连 proxy.golang.org 在部分网络下不可达，而宿主走 goproxy.cn）。
# ARG 不给默认值：未传时 ${GOPROXY:+...} 展开为空，容器继承官方镜像的 GOPROXY，
# 传了才在**这条 RUN 的环境**里生效——不进镜像层元数据，也不影响后续指令。
ARG GOPROXY
COPY go.mod go.sum ./
# --mount=type=cache,target=/go/pkg/mod：模块缓存在多次构建间复用，不进镜像层。
# 为什么挂缓存而不是 COPY 宿主模块目录：构建上下文会因此多出几百 MB 且依赖宿主
# 目录布局；cache mount 是 BuildKit 的正解，二次构建直接命中。
RUN --mount=type=cache,target=/go/pkg/mod \
    env ${GOPROXY:+GOPROXY=$GOPROXY} go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=webbuild /src/internal/webui/dist ./internal/webui/dist

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
# go build 也挂同一份模块缓存 + GOCACHE：改一行代码的重构构建从分钟级降到秒级。
# GOPROXY 同样只在传了时生效（离线二次构建时模块已在缓存里，代理不可达也不影响）。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOPROXY=${GOPROXY:-off} go build -trimpath \
      -tags webui_dist \
      -ldflags "-X github.com/LittleFishStars/FreeRouter/internal/version.Version=${VERSION} \
                -X github.com/LittleFishStars/FreeRouter/internal/version.Commit=${COMMIT} \
                -X github.com/LittleFishStars/FreeRouter/internal/version.BuildTime=${BUILD_TIME}" \
      -o /out/freerouter ./cmd/freerouter


# ── 阶段 3：运行 ───────────────────────────────────────────────────
FROM node:22-alpine AS runtime

# 显式建用户/组并固定 uid/gid=10001：
# 为什么固定数字而不是用户名——compose/systemd 里都写 `user: "10001:10001"`，
# 数字属主让「容器里建的目录」与「宿主 bind mount 的目录」属主判定只依赖一个约定，
# 换镜像基础层也不会漂（alpine 的 node 用户 uid 就是会变）。
RUN addgroup -g 10001 -S freerouter && \
    adduser  -u 10001 -S -G freerouter -h /var/lib/freerouter freerouter

WORKDIR /opt/freerouter

# 二进制 + sidecar 入口（sidecar 是**代码**不是依赖包，随镜像走；hub 才走挂载）。
COPY --from=gobuild /out/freerouter /usr/local/bin/freerouter
COPY --from=gobuild /out/freerouter /opt/freerouter/freerouter
COPY sidecar/ /opt/freerouter/sidecar/

# 数据/配置目录：属主给服务用户，权限 0750（同机他人不可进）。
# 为什么不在镜像里 chmod 更宽：数据目录里的库文件本身就该 0600（01-security §4.2），
# 目录 0750 + 文件 0600 已经够服务用户读写，宽一分就多一分泄露面。
RUN mkdir -p /var/lib/freerouter/backups /etc/freerouter && \
    chown -R freerouter:freerouter /var/lib/freerouter && \
    chmod 0750 /var/lib/freerouter && \
    chown root:root /etc/freerouter && chmod 0755 /etc/freerouter && \
    chmod 0755 /usr/local/bin/freerouter

# 这些是**镜像内默认**，运行期一律可被 FR_ 环境变量覆盖（config.ApplyEnv 的规则）。
# 默认值与 internal/config/config.go 的 Default() 逐项一致——不一致就是文档/实现分叉。
# 为什么这里没有 FR_STORE_MASTER_KEY_FILE：主密钥路径取决于挂载方式（bind mount 或
# secrets），写死进镜像会让「忘了挂载」变成「用镜像内残留的路径」这种更难排的故障；
# 缺这个环境变量时 store.Open 会拒绝启动并给出明确的运维提示（刻意行为）。
ENV FR_STORE_PATH=/var/lib/freerouter/freerouter.db \
    FR_STORE_BACKUP_DIR=/var/lib/freerouter/backups \
    FR_SIDECAR_ENTRY=/opt/freerouter/sidecar/index.mjs \
    FR_SIDECAR_NODE_PATH=node \
    FR_SERVER_LISTEN=127.0.0.1:8080 \
    FR_LOG_FORMAT=json

# 只声明端口供人阅读：**host 网络下 ports 映射不生效**（02-deploy.md §5 的理由），
# 实际监听由 FR_SERVER_LISTEN 决定，默认回环。
EXPOSE 8080

# 不用 root 跑。USER 用数字，与 compose 的 user: 一致，避免运行时再解析用户名。
USER 10001:10001

# 健康检查用 /readyz 而不是 /healthz 的理由（01-security §7）：
# /healthz 只答「进程活着」，上游全挂时它照样绿；/readyz 才反映自举状态。
# 但**编排系统重启解决不了上游问题**，所以这里探 readyz 只做可观测标记；
# 容器的重启交给 compose 的 restart: unless-stopped（进程退出语义）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null 2>&1 || exit 1

# 入口脚本做两件事（为什么不直接 ENTRYPOINT 二进制）：
#   1. umask 077 —— 实测（M10）：umask 022 下启动库文件是 644（同机他人可读凭据密文），
#      077 下是 0600。Docker 没有 UMask= 这个选项，compose 也给不了；
#      这里是容器形态下让 01-security §4.2 成立的唯一位置。
#   2. 配置文件存在才传 --config —— config.Load 对不存在的路径直接报错，
#      而镜像内配置是可选形态（容器里用环境变量配置是常见形态，02-deploy §1.2）。
#      硬编码 --config 会让「没挂配置文件」变成启动失败，那不是安全收益而是
#      把「可选配置」变成「必填项」的误伤。
RUN printf '#!/bin/sh\n# FreeRouter 容器入口：umask 077 + 可选 --config（理由见 Dockerfile 同名注释）\numask 077\nif [ -f /etc/freerouter/config.yaml ]; then\n  exec /usr/local/bin/freerouter --config /etc/freerouter/config.yaml "$@"\nfi\nexec /usr/local/bin/freerouter "$@"\n' \
      > /usr/local/bin/freerouter-entrypoint && chmod 0755 /usr/local/bin/freerouter-entrypoint

ENTRYPOINT ["/usr/local/bin/freerouter-entrypoint"]
# 附加参数（compose 的 command:）追加在 --config 之后。
CMD []
