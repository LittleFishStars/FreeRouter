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
| **A. 本地直连（默认）** | 先本地跑通、单机自用 | 默认 `tls.mode=off`（明文）；需要时内置 `files` / `acme` | 低 |
| **B. Docker Compose** | 生产形态 | 同上，可选内置 | 中 |
| **C. 反代前置（可选）**（Caddy / Nginx / Traefik） | 已有反代、多站点共用 443 | 反代终止 | 中 |

形态 A 与 B 是**默认路径**：直接以 `http://<ip>:<port>` 提供服务，不需要任何前置组件。
形态 C 是**可选**的，只有部署者自己决定加反代时才用（见 §6）。
三者只影响「谁在监听、谁终止 TLS」，不影响进程拓扑——§1.1 的两个进程在三种形态下完全相同。

**为什么把「本地直连」放在第一位**：用户 2026-10-19 拍板「不用反代，默认直接通过
ip+端口访问」，且部署位置是「先本地跑」。文档的默认路径必须与这个决定一致——
否则照文档做的人会先被要求架一个反代，而反代在本项目的默认形态里**不是前置条件**。

**为什么形态 B（容器）与形态 C（反代）要分开列**：两者经常一起出现（容器 + 反代），
但它们是**独立**的选择：容器不要求反代，反代也不要求容器。
合并成一项会让「我只想用容器、不想加反代」看起来像不支持的组合。

**三种形态的共同硬要求**（来自 [01-security.md](01-security.md)）：非 root、数据目录
可写且权限 `0600`、`TRUSTED_PROXIES` 显式配置（直连形态下为空即为正确）、
非回环监听且无 TLS 时有**醒目启动警告**。

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
| `server.public_url` | string | — | 对外地址，用于生成回调与链接。默认形态填 `http://<ip>:<port>`；域名见 §2.2 待定项 |
| `server.insecure_public_listen` | enum | **`warn`** | 非回环监听 + `tls.mode=off` 时的启动策略：`warn` = 打印醒目警告后继续启动；`deny` = 拒绝启动 |
| `server.behind_proxy` | bool | `false` | **仅形态 C 需要**：声明「我在受信反代之后、由反代终止 TLS」 |
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
| `tls.mode` | enum | **`off`** | `off`（默认，明文直连）/ `files`（自备证书）/ `acme`（自动签发） |
| `tls.cert_file` | string | — | `files` 模式 |
| `tls.key_file` | string | — | `files` 模式 |
| `tls.acme_domains` | []string | — | `acme` 模式；需要真实域名（见下） |
| `tls.acme_email` | string | — | ACME 注册邮箱 |
| `tls.acme_cache_dir` | string | `/var/lib/freerouter/acme` | 证书缓存 |

**默认不启用 TLS**（`tls.mode=off`），即默认明文直连。要加密传输有两条**互斥**的路：
`tls.mode=files` / `acme`（本进程自己终止 TLS，**不需要反代**），或在前面加一层反代
由它终止 TLS（**仅形态 C**，同时设 `server.behind_proxy: true`）。
`server.insecure_public_listen` 与 `server.behind_proxy` 两个键定义在 §2.1。

> **待定**：对外域名。用户 2026-10-19 拍板「域名仍未定」（总纲第 15 章第 7 项）。
> 在域名确定前 `tls.mode=acme` 无法启用（ACME 必须针对一个真实域名签发），
> `server.public_url` 只能填 `http://<ip>:<port>` 这样的字面地址。
> `tls.mode=files` 不受影响：自签或已有证书都可以直接给路径。

**为什么 `server.insecure_public_listen` 默认是 `warn` 而不是 `deny`**：用户明确要求
默认走 IP + 端口，且部署位置是「先本地跑」。默认 `deny` 会造成「改一下监听地址
就起不来」的断层，把「先本地跑通」这一步挡在门外；`warn` 保留可启动性，
同时把「你正在明文对外」打进启动日志。

**为什么仍然必须保留 `deny` 这个取值**：明文下 Bearer Key 与请求正文全裸奔，
一次截获就是该 Key 的**全部站点额度**。默认 `warn` 是**便利性妥协**；
对确定要暴露在不可信网络的部署，管理员必须能把这条妥协**收回**。
`deny` 就是给这个场景准备的显式动作，比「记得自己去看日志」可靠。

**为什么用 `server.behind_proxy` 而不是自动识别**：反代终止 TLS 时我们确实在明文上
监听，但这与「忘了配 TLS」在配置上完全无法区分。用一个必须显式打开的开关把
「有意为之」和「疏忽」分开。它只表达意图，不参与 `X-Forwarded-For` 的判定——
那由 `security.trusted_proxies` 决定（见 §2.3）。

### 2.3 security

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `security.trusted_proxies` | []string | `[]` | CIDR 列表。**为空则忽略 `X-Forwarded-For`** |
| `security.password_min_length` | int | `12` | |
| `security.session_ttl` | duration | `12h` | |
| `security.elevation_ttl` | duration | `15m` | 提权有效期 |
| `security.login_max_failures` | int | `5` | 触发退避 |
| `security.login_lockout` | duration | `15m` | |
| `security.registration_enabled` | bool | `true` | |
| `security.registration_requires_approval` | bool | `true` | |

**管理面没有 IP 白名单配置项，这是有意为之**：用户 2026-10-19 拍板「管理面不限来源 IP」，
因此**不提供** `admin_ip_allowlist` 这类功能，而不是「提供但默认空」。理由与补偿手段见
[01-security.md](01-security.md#3-账户与认证)：管理面的真正门槛是**类级提权**
（进管理面当场再验、15 分钟有效、危险操作每次重验）+ 强密码 + 登录退避锁定；
按来源 IP 限制只会误锁从移动网络 / CGNAT 登录的管理员，却挡不住已拿到凭据的攻击者。

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
# 默认形态：只监听回环，不启 TLS（先本地跑）
FR_SERVER_LISTEN=127.0.0.1:8080
FR_STORE_MASTER_KEY_FILE=/etc/freerouter/master.key
FR_UPSTREAM_DSH_GATEWAY_BASE_URL=http://127.0.0.1:8326
FR_SIDECAR_HUB_PATH=/opt/dsh/profiles/web/node_modules/dsh-providers-hub
FR_DSH_HOME=$HOME/.local/share/dsh

# 要暴露到公网时（显式动作，不自动探测）：换成具体地址。
# 默认 warn：明文仍可启动，但启动日志会打印醒目警告；
# 改成 deny 则非回环 + 无 TLS 时直接拒绝启动。
FR_SERVER_LISTEN=0.0.0.0:8080
FR_SERVER_PUBLIC_URL=http://203.0.113.10:8080
FR_SERVER_INSECURE_PUBLIC_LISTEN=warn

# 传输加密：内置 TLS 与反代二选一（不要同时做）
# 内置 TLS（默认 off，不依赖反代）：
# FR_TLS_MODE=files
# FR_TLS_CERT_FILE=/etc/freerouter/tls/fullchain.pem
# FR_TLS_KEY_FILE=/etc/freerouter/tls/privkey.pem
# 或 FR_TLS_MODE=acme（需要真实域名，域名待定，见 §2.2）：
# FR_TLS_ACME_DOMAINS=fr.example.com
# FR_TLS_ACME_EMAIL=ops@example.com

# 只有形态 C（自己加反代）才需要下面两行：
# FR_SERVER_BEHIND_PROXY=true
# FR_SECURITY_TRUSTED_PROXIES=172.16.0.0/12,10.0.0.0/8
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
3. 创建完成 → 写入 `settings.initialized = true` → 按 `server.listen` 监听。
   **默认仍留在 `127.0.0.1:8080`**：切到公网地址是一次**显式配置改动**
   （改 `server.listen`，或用 `FR_SERVER_LISTEN` 覆盖后重启），**不做自动探测**。
4. 若 `upstream.dsh_gateway.api_key_file` 可读 → 自举上游凭据并拉模型目录。
5. 若 sidecar 配置就绪 → 启动子进程；失败只记警告，贡献页显示不可用。

**为什么切到公网是显式动作而不是自动探测**：自动探测（例如「发现公网网卡就绑它」）
会让「我把服务暴露到了公网」变成一件管理员**没有做过的决定**。
监听地址本身就是安全边界，它必须由人写下。默认留在回环的另一个含义是：
初始化完成 ≠ 要上线——部署者可以先把本地流程走完，再决定何时暴露。

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
      - $HOME/.local/share/dsh:/dsh:ro
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

## 6. 反代前置（形态 C，可选）

**本节只在你自己决定加反代时才需要读。** 默认形态是直连 IP + 端口，不需要任何反代
（见 §1.2 形态 A / B）。选择形态 C 的典型理由是：已经有反代在跑、多站共用 443、
或需要反代侧的证书自动续期与访问日志。

选择了形态 C 就必须同时配 `security.trusted_proxies`（见 §2.3），否则 IP 限速
拿不到真实 IP。

Caddy 片段（`fr.example.com` 是占位域名，真实域名见 §2.2 的待定项）：

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

**选形态 C 就必须同时做**：把反代地址段写进 `security.trusted_proxies`，
否则 IP 限速拿不到真实 IP。

**为什么 `flush_interval -1`**：反代默认会缓冲响应以做压缩/改写，
这会让 SSE 变成「攒一批再发」，客户端的逐字输出直接失效。
这是中转站接反代最常踩的坑。

**为什么反代是可选的而不是前置条件**：反代提供的能力（TLS 终止、证书续期、
统一入口）在本项目里都能由进程自身或部署者按需承担；把它设成前置条件会把
「先本地跑通」这件事挡在门外，也让一个单机自用部署凭空多一个组件与一个故障点。
可选的代价是「明文直连成为默认形态」——这一点已经用启动警告与
`server.insecure_public_listen` 开关显式化（见 §2.2），不是被悄悄忽略了。

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
| 启动日志有「明文对外」警告 | 绑了非回环地址 + `tls.mode=off`（默认行为） | 要消除：配内置 TLS（`files` / `acme`）或加反代；要把明文变成硬错误：`server.insecure_public_listen=deny` |
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
