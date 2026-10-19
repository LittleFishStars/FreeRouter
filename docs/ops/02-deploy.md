> 分册：运维 · 依赖：[01-security.md](01-security.md)、[../architecture/04-sidecar.md](../architecture/04-sidecar.md) · 对应总纲：[PLAN.md](../PLAN.md) 第 7.1、9、12 章

# 部署与配置

本册写清楚：FreeRouter 怎么装、怎么配、怎么备、怎么升、出问题怎么查。

---

## 1. 部署形态

### 1.1 进程拓扑

无论哪种形态，进程拓扑是固定的**两个进程**：

```
┌─────────────────────────────────────────────┐
│  freerouter（Go，主进程）                    │
│  ├─ HTTP/HTTPS 监听（入站 + 控制台 + 管理面） │
│  ├─ SQLite（WAL）                            │
│  └─ sidecar 客户端 ──┐                       │
└──────────────────────┼───────────────────────┘
                       │ Unix socket
┌──────────────────────▼───────────────────────┐
│  freerouter-sidecar（Node，子进程）           │
│  加载 dsh-providers-hub 的 auth/adapter 模块  │
└──────────────────────────────────────────────┘
                       │ HTTP 127.0.0.1:8326
┌──────────────────────▼───────────────────────┐
│  DSH（外部，已有）dsh-providers-hub 网关      │
└──────────────────────────────────────────────┘
```

**为什么 sidecar 必须是独立进程而不是同进程内的库**：`dsh-providers-hub` 的
模块会与 DSH 进程内的模块状态耦合（实测直接 `import()` 在有 DSH 运行时存在冲突）。
子进程隔离让两边的模块状态互不干扰，也让 sidecar 崩了不会带走主进程。

**为什么走 Unix socket 而不是 TCP**：sidecar 只服务本机主进程，Unix socket
没有端口占用问题、没有网络暴露面、权限靠文件系统控制。

### 1.2 三种部署形态

| 形态 | 适用 | TLS | 复杂度 |
|---|---|---|---|
| **A. 裸机 + systemd** | 单机自用、开发 | 内置 ACME 或反代 | 低 |
| **B. Docker Compose** | 推荐的生产形态 | 内置 ACME 或反代 | 中 |
| **C. 反代前置**（Caddy / Nginx / Traefik） | 已有反代、多站点共用 443 | 反代终止 | 中 |

**三种形态的共同硬要求**（来自 [01-security.md](01-security.md)）：
非 root、TLS 强制、`TRUSTED_PROXIES` 显式配置、数据目录可写且权限 `0600`。

### 1.3 与 DSH 的部署关系

**关键约束**：`dsh-providers-hub` 的网关**只绑 `127.0.0.1` 并显式拒绝 `0.0.0.0`**。
所以 FreeRouter 必须与 DSH 在**同一网络命名空间**内：

| 情况 | 做法 |
|---|---|
| 裸机 | 直接可用（都在宿主） |
| Docker | **必须 `network_mode: host`**，否则容器内 `127.0.0.1:8326` 是容器自己 |
| 两台机器 | **不支持**——上游不接受跨机访问，这是它的设计而非配置遗漏 |

**为什么不用 `host.docker.internal`**：桥接网络下容器访问宿主需经网关地址，
但上游网关只监听回环接口，网关地址连不上。`network_mode: host` 是唯一可行解。

---

## 2. 配置项全表

配置文件默认 `/etc/freerouter/config.yaml`，**任何项都可用环境变量覆盖**
（前缀 `FR_`，层级用 `_` 分隔，如 `FR_SERVER_LISTEN`）。

### 2.1 server

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `server.listen` | string | `127.0.0.1:8080` | 监听地址。**初始化完成前只允许回环** |
| `server.public_url` | string | — | 对外地址，用于生成回调与链接 |
| `server.read_header_timeout` | duration | `10s` | 防慢头攻击 |
| `server.idle_timeout` | duration | `120s` | **必须 ≤ 上游的 120s** |
| `server.max_body_bytes` | int | `33554432` | 入站 32 MiB，小于上游 300 MiB |
| `server.shutdown_grace` | duration | `30s` | 优雅关闭 drain 窗口 |

**为什么 `idle_timeout` 要 ≤ 上游的 120s**：上游 120 秒后发 FIN。
如果我们的连接池认为连接还活着并复用，就会拿到 `Connection reset`。
把本地空闲上限压到上游之下，由我们先主动关，避免踩上游的 FIN。

### 2.2 tls

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `tls.mode` | enum | `off` | `off` / `files` / `acme` |
| `tls.cert_file` | string | — | `files` 模式 |
| `tls.key_file` | string | — | `files` 模式 |
| `tls.acme_domains` | []string | — | `acme` 模式 |
| `tls.acme_email` | string | — | ACME 注册邮箱 |
| `tls.acme_cache_dir` | string | `/var/lib/freerouter/acme` | 证书缓存 |

> **硬约束**：`server.listen` 绑非回环地址且 `tls.mode=off` 时**拒绝启动**。
> 若确实在反代之后（由反代终止 TLS），必须显式设 `server.behind_proxy: true`
> 表示「我知道自己在明文后面」。

**为什么用显式开关而不是猜**：反代终止 TLS 时我们确实在明文上监听，
但这与「忘了配 TLS」在配置上无法区分。用一个必须显式打开的开关把
「有意为之」和「疏忽」分开，避免一次疏忽就把明文挂到公网。

### 2.3 security

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `security.trusted_proxies` | []string | `[]` | CIDR 列表。**为空则忽略 `X-Forwarded-For`** |
| `security.admin_ip_allowlist` | []string | `[]` | 空 = 不限制 |
| `security.password_min_length` | int | `12` | |
| `security.session_ttl` | duration | `12h` | |
| `security.elevation_ttl` | duration | `15m` | 提权有效期 |
| `security.login_max_failures` | int | `5` | 触发退避 |
| `security.login_lockout` | duration | `15m` | |
| `security.registration_enabled` | bool | `true` | |
| `security.registration_requires_approval` | bool | `true` | |

### 2.4 limits

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `limits.default_daily_tokens` | int | `0` | **0 = 不可用**，显式授权才给 |
| `limits.default_monthly_tokens` | int | `0` | 同上 |
| `limits.default_rpm` | int | `60` | |
| `limits.default_tpm` | int | `0` | 0 = 不限制 |
| `limits.default_max_concurrency` | int | `2` | |
| `limits.default_keys_per_account` | int | `10` | |
| `limits.global_daily_tokens` | int | `0` | 全局熔断阈值，0 = 不熔断 |
| `limits.global_rpm` | int | `0` | |

**为什么默认额度是 0 而不是某个正整数**：任何非零默认值都会在没有人工
授权的情况下产生消耗。0 让「新建 Key 不能用」成为默认行为，
把授权动作显式化——这是 §1.2「默认拒绝」的具体落地。

### 2.5 upstream

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `upstream.dsh_gateway.enabled` | bool | `true` | |
| `upstream.dsh_gateway.base_url` | string | `http://127.0.0.1:8326` | **不接受非回环地址** |
| `upstream.dsh_gateway.api_key_file` | string | `$DSH_HOME/openai-gateway/api-key` | 自举用 |
| `upstream.dsh_gateway.timeout` | duration | `300s` | 长回答需要 |
| `upstream.dsh_gateway.connect_timeout` | duration | `5s` | |
| `upstream.dsh_gateway.max_retries` | int | `2` | 幂等请求（未吐内容）才重试 |
| `upstream.dsh_gateway.model_refresh_interval` | duration | `10m` | 目录刷新 |
| `upstream.dsh_gateway.aggregate_excluded` | bool | `true` | 排除 `aggregate/*` 作候选 |

**为什么 `base_url` 只接受回环**：上游网关的设计假定自己只服务本机
（它显式拒绝 `0.0.0.0`）。允许配成外网地址只会制造「配了但连不上」的困惑，
不如在配置校验阶段直接拒绝并说明原因。

### 2.6 sidecar

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `sidecar.enabled` | bool | `true` | 关闭则不能贡献供应商账号 |
| `sidecar.node_path` | string | `node` | Node 可执行文件 |
| `sidecar.entry` | string | `/opt/freerouter/sidecar/index.mjs` | sidecar 入口 |
| `sidecar.socket` | string | `/run/freerouter/sidecar.sock` | |
| `sidecar.hub_path` | string | — | `dsh-providers-hub` 安装路径 |
| `sidecar.startup_timeout` | duration | `20s` | 超时则标记「贡献功能不可用」 |

**为什么 sidecar 起不来不阻塞主进程**：贡献功能是增量能力，
核心的调用与管控不依赖它。让它失败即整站不可用是过度耦合。

### 2.7 store

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `store.path` | string | `/var/lib/freerouter/freerouter.db` | |
| `store.master_key_file` | string | — | `0600`，**与库分开存放** |
| `store.busy_timeout` | duration | `5s` | |
| `store.backup_dir` | string | `/var/lib/freerouter/backups` | |
| `store.audit_retain_days` | int | `180` | |
| `store.request_log_retain_days` | int | `30` | |

---

## 3. 环境变量

常用覆盖项（完整列表见 `freerouter config --print`）：

```bash
FR_SERVER_LISTEN=0.0.0.0:443
FR_SERVER_PUBLIC_URL=https://fr.example.com
FR_TLS_MODE=acme
FR_TLS_ACME_DOMAINS=fr.example.com
FR_TLS_ACME_EMAIL=ops@example.com
FR_SECURITY_TRUSTED_PROXIES=172.16.0.0/12,10.0.0.0/8
FR_STORE_MASTER_KEY_FILE=/run/secrets/freerouter-master.key
FR_UPSTREAM_DSH_GATEWAY_BASE_URL=http://127.0.0.1:8326
FR_SIDECAR_HUB_PATH=/opt/dsh/profiles/web/node_modules/dsh-providers-hub
FR_DSH_HOME=/home/ylxc/.local/share/dsh
```

**主密钥生成**（首次）：

```bash
mkdir -p /etc/freerouter
head -c 32 /dev/urandom | base64 > /etc/freerouter/master.key
chmod 600 /etc/freerouter/master.key
```

**为什么主密钥要单独一份文件而不是写进 config.yaml**：config.yaml 常被
放进版本库、备份、容器编排文件里。主密钥与密文分处两地，
使「拿到配置」不等于「能解密凭据」。

---

## 4. 初始化流程

1. 首次启动：库中无账户 → **只监听 `127.0.0.1`**，日志打印一次性引导地址。
2. 浏览器打开 `http://127.0.0.1:8080/setup`，创建管理员账户
   （显示名 + 密码，无默认密码，密码 ≥12 字符）。
3. 创建完成 → 写入 `settings.initialized = true` → 按配置监听公网地址。
4. 若 `upstream.dsh_gateway.api_key_file` 可读 → 自举上游凭据并拉模型目录。
5. 若 sidecar 配置就绪 → 启动子进程；失败只记警告，贡献页显示不可用。

**为什么初始化前只监听回环**：公网暴露的首次运行窗口是全站最脆弱的时刻
（无账户、无密码、无审计）。先回环、建号、再开公网，把这个窗口关在本地。

**为什么管理员创建后不自动跟一个「默认租户账户」**：账户模型里权限类是集合，
管理员的账户在创建时就是 `{admin, tenant}`（见
[../security/01-accounts.md](../security/01-accounts.md)），不需要额外创建第二个账户。

---

## 5. Docker Compose 参考

```yaml
# deploy/docker-compose.yml
services:
  freerouter:
    image: ghcr.io/littlefishstars/freerouter:latest
    # 必须 host 网络：上游网关只监听 127.0.0.1
    network_mode: host
    restart: unless-stopped
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    user: "10001:10001"
    volumes:
      - /etc/freerouter/config.yaml:/etc/freerouter/config.yaml:ro
      - freerouter-data:/var/lib/freerouter
      - freerouter-run:/run/freerouter
      # 上游 key 与 DSH 数据（只读）
      - /home/ylxc/.local/share/dsh:/dsh:ro
      # sidecar 代码与 providers-hub
      - /opt/freerouter/sidecar:/opt/freerouter/sidecar:ro
    environment:
      FR_DSH_HOME: /dsh
      FR_UPSTREAM_DSH_GATEWAY_API_KEY_FILE: /dsh/openai-gateway/api-key
      FR_SIDECAR_HUB_PATH: /opt/freerouter/sidecar/node_modules/dsh-providers-hub
      FR_STORE_MASTER_KEY_FILE: /run/secrets/master.key
    secrets:
      - master.key
    tmpfs:
      - /tmp:size=64M

secrets:
  master.key:
    file: /etc/freerouter/master.key

volumes:
  freerouter-data:
  freerouter-run:
```

**为什么 `read_only: true` + 单独的 `freerouter-run` 卷**：
sidecar 的 Unix socket 与 PID 文件需要写权限，但根文件系统不需要。
把「必须可写」的范围压到最小，容器被拿下时能改的东西就少。

**为什么不用 `ports:`**：`network_mode: host` 下端口直接由应用监听，
写 `ports:` 会被忽略并可能造成误解。

---

## 6. 反代前置（形态 C）

Caddy 片段：

```
fr.example.com {
    encode zstd gzip
    # 流式响应必须关闭缓冲
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
        header_up X-Real-IP {remote_host}
    }
}
```

**必须同时做**：把反代地址段写进 `security.trusted_proxies`，
否则 IP 限速拿不到真实 IP。

**为什么 `flush_interval -1`**：反代默认会缓冲响应以做压缩/改写，
这会让 SSE 变成「攒一批再发」，客户端的逐字输出直接失效。
这是中转站接反代最常踩的坑。

---

## 7. 备份与恢复

### 7.1 备份

```bash
# 一致性快照（SQLite 官方方式，无需停服）
sqlite3 /var/lib/freerouter/freerouter.db \
  "VACUUM INTO '/var/lib/freerouter/backups/fr-$(date +%F-%H%M).db'"
```

要备份的三样：

| 对象 | 位置 | 丢了怎样 |
|---|---|---|
| 数据库 | `store.path` | **灾难**——账户、Key、凭据密文全没 |
| 主密钥 | `store.master_key_file` | **灾难**——库在但凭据解不开 |
| 配置 | `config.yaml` | 需重配 |

**为什么用 `VACUUM INTO` 而不是 `cp`**：WAL 模式下直接复制 `.db` 文件会丢掉
尚未 checkpoint 的 WAL 内容，得到一个陈旧或损坏的副本。
`VACUUM INTO` 由 SQLite 自身保证一致性，且可在线执行。

**为什么主密钥要跟数据库**一起但**分开存放**：两者缺一不可恢复，
但放同一个备份包里就等于「一次泄露全泄露」。建议数据库进常规备份，
主密钥走单独的密钥管理通道（或离线保管）。

### 7.2 恢复

1. 停服。
2. 放回数据库文件，权限 `0600`，属主为服务用户。
3. 放回主密钥文件，权限 `0600`。
4. 启动 → 校验：库可打开、主密钥能解密一条测试凭据。
5. 若主密钥丢失：库可打开但凭据不可解，**所有贡献凭据必须重新贡献**，
   需在控制台提示贡献者。

---

## 8. 升级流程

```bash
# 1. 备份
sqlite3 .../freerouter.db "VACUUM INTO '.../pre-upgrade.db'"

# 2. 停服
systemctl stop freerouter          # 或 docker compose down

# 3. 换二进制/镜像

# 4. 启动（迁移自动执行）
systemctl start freerouter

# 5. 验证
curl -s localhost:8080/readyz
# 跑契约测试（锁 8326 的 4 个端点）
go test ./internal/upstream/dshgateway/... -run TestContract
```

**迁移策略**：启动时自动执行，**迁移前自动备份一次**，迁移失败则回滚到
上一版本并保留库不变。迁移只前进不回退（回退版本需手工恢复备份）。

**为什么升级后必跑契约测试**：`dsh-providers-hub` 升级可能改变
`/v1/models` 条目字段或 SSE 帧形状。契约测试把「上游变了」这件事
在升级窗口就暴露出来，而不是等用户报错。

---

## 9. 故障排查

| 现象 | 可能原因 | 检查 |
|---|---|---|
| 启动失败「明文对外」 | 绑了公网地址 + `tls.mode=off` | 设 TLS 或 `server.behind_proxy: true` |
| `/readyz` 红但 `/healthz` 绿 | DSH 没跑 / 8326 不通 | `curl 127.0.0.1:8326/v1/models` 带 Bearer |
| 请求随机失败、日志见 `http=000` | 上游偶发连接失败 | 看重试次数；检查连接池空闲上限 |
| 流式输出变成一次性吐出 | 反代缓冲 | 反代加 `flush_interval -1` |
| 429 但用户说没用多少 | 多租户共用 IP 且未配 `trusted_proxies` | 检查真实 IP 解析 |
| 贡献页「功能不可用」 | sidecar 起不来 | `sidecar.startup_timeout` 内看子进程日志 |
| 凭据解密失败 | 主密钥换了 | 确认 `master_key_file` 与加密时一致 |
| `Connection reset` 偶发 | 复用了上游已 FIN 的连接 | 确认 `idle_timeout ≤ 120s` |

---

## 10. 资源估算

| 规模 | 并发 | 内存 | CPU | 磁盘（30 天日志） |
|---|---|---|---|---|
| 自用 | ≤5 | 256 MiB | 1 核 | 200 MiB |
| 小组 | ≤50 | 512 MiB | 2 核 | 1 GiB |
| 多租户 | ≤200 | 1 GiB | 4 核 | 5 GiB |

**为什么内存与并发不是线性**：主要内存消耗是**流式响应的缓冲**与**请求体**，
两者都由 `max_body_bytes` 与并发数共同决定；而 token 的计数与路由是轻量的。
把 `max_body_bytes` 调大会显著抬高内存估算。

> **待定**：是否内置 Prometheus `/metrics` 端点（总纲未列）。
> 若部署环境已有监控体系，建议加；否则先只做日志与 `/readyz`。

---

## 11. 相关分册

| 主题 | 详见 |
|---|---|
| 安全基线（必须先满足） | [01-security.md](01-security.md) |
| 里程碑与验收命令 | [03-milestones.md](03-milestones.md) |
| sidecar 的 IPC 契约 | [../architecture/04-sidecar.md](../architecture/04-sidecar.md) |
| 上游适配与重试 | [../architecture/03-upstream.md](../architecture/03-upstream.md) |
| 数据模型与迁移 | [../architecture/05-storage.md](../architecture/05-storage.md) |