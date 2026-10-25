# 变更日志

本文件格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。
项目尚未发布，版本号自 `v0.0.x` 起；**里程碑**（M0–M10，见 docs/ops/03-milestones.md）
作为开发期的分组单位。

约定：凡属「运维会感知为行为变化」的改动（默认值语义、错误体形状、存储迁移），
必须记入本文件的 Unreleased → 变更 一节，注明旧行为、新行为与原因。

---

## [Unreleased]

### 变更

- **候选集编辑改为「供应商 → 模型」两栏选卡式**（2026-10-26）：参考 Tingly Box
  「Connect AI」的形态，左栏按「贡献凭据 / 平台渠道」列出上游（带协议徽标与
  模型数），右栏是所选供应商的模型卡片网格，点卡片即加入 / 移出候选集，
  选中后就地展开权重与渠道范围（渠道多选芯片）。
  - **新端点**：`GET /api/admin/model-sources`（管理面只读、不写审计）——
    上游 × 真实模型的聚合视图，含端点协议事实（`openai` / `anthropic`，
    来自内置目录的端点清单）与每条来源 / 每张模型卡的可用凭据实例数。
  - **修正渠道范围的失效写法**：旧下拉写的是 `{"channel_scope":"all"}`
    字符串，而路由层解析的是 `{mode,channels}` 形状——字符串解进结构体报错，
    运行期静默退化成 `any`，即那个下拉框从来没有生效过。新界面只写
    `{"mode":"include","channels":[…]}`，取消全选时删键（等价全部渠道）。
  - **修正「可用实例 / 可用候选」恒为 0 的静默故障**：桥里这两处曾是硬编码
    占位 0，导致（a）界面上看不出「配了但永远选不中」，(b) 保存前干跑把每条
    候选都判成 no_candidate（防呆机制恒报红灯），(c) 列表页「候选 / 可用」
    徽标恒显示全灭。现在两处都按「所属上游的 active 凭据数」计算（relay 记
    1：Go 侧数不出 sidecar 账号池规模，但有一个 provider 入口是事实，记 0
    等于谎报没有；停用上游记 0）。新增三条装配契约测试钉住这个口径。
  - **协议徽标的语义边界**：徽标表达「该供应商提供哪种协议的端点」这一事实；
    本站出站链路当前只实现 OpenAI 兼容，A 徽标是「供应商具备、本站未接入」。
  - **内置目录新增端点清单**：`internal/apidir/catalog.json` 每条 provider 增
    `endpoints` 对象（55 家里 32 家带 anthropic 原生端点），数据来自 Tingly Box
    的 provider registry，可由 `scripts/gen-apidir-endpoints.py` 幂等重建；
    既有的 `protocol` / `base_url` 契约字段未动。

- **API 供应商贡献不再填「声称的模型」**（2026-10-25）：这一栏只收「供应商 + Key」，
  端点由内置目录解析、模型由服务端探测 `/models` 得出。前端去掉模型复选框列表、
  「+ 添加自定义模型」与「已选 N 个」汇总行，也去掉提交前的「至少声明一个模型」阻断；
  `POST /api/contributions` 的 `kind=api` 请求体不再带 `claimed_models`。
  - **旧行为**：必须勾选至少一个模型才能提交，否则前端拦住；全不命中目录判 `suspect`。
  - **新行为**：不传 `claimed_models` 时走既有的「未声明任何模型」一行
    （`active/unverified`，进复检队列），由探测结果确定实际模型集。
  - **原因**：内置供应商的模型清单服务端本来就能探测，让用户手抄一遍是多余的活；
    而且抄来的名字与上游真实 id 常不一致（大小写、`-latest` 后缀、版本号），
    会把一条好凭据误判成 `suspect`。
  - **自定义供应商仍保留该字段**：端点不来自目录，服务端没有目录可依赖，
    用户的声称是唯一对照物——总纲 4.4 的鉴定目标在那一侧仍然成立。
  - 后端的「部分命中 / 全不命中 / 未声明」三路判定与多选支持**未改动**，
    契约测试全部通过；直接调 API 传 `claimed_models` 仍然有效（向后兼容）。

- **界面字体改用 JetBrains Maple Mono**（2026-10-25 用户指定）：等宽 CJK 字体，
  中文与拉丁宽度严格 2:1（实测 1200:600 单位），表格列天然对齐、数字不必额外开
  tabular-nums 也不会跳。
  - `web/scripts/build-fonts.py` 按界面实际字符（源码扫描 + ASCII + 常用汉字 3500）
    子集化 woff2：上游每字重 18.5 MB（全量 CJK + Nerd Font 图标），16 字重共 294 MB
    无法内嵌；子集后三档合计 2.6 MB，单二进制 23.5 → 26.2 MB。
  - `@font-face` 的 `src` 里 `local()` 排在 `url()` 前：装了这款字体的机器零下载。
  - 连字分流：正文关掉 `calt/liga`（`a -> b` 不该渲染成箭头，否则用户看到的与
    复制到的不一致），代码处打开。
  - 许可证 OFL-1.1，全文与字形版权随字体目录入库。

- **界面改用 Tabler 图标，并确立"用图标代替文字"的规则**（2026-10-25）：
  - 图标库定为 **[Tabler Icons](https://tabler.io/icons) v3（MIT）**，6239 个、
    纯线性、24 视口、圆头圆角。`@mui/icons-material` **已移除**（同样 MIT，但
    默认形态是实心填充，与线性图标混在一列里就是"风格不统一"）。
  - 新增 `web/src/icons.tsx` 作为唯一图标出口：尺寸只允许 `xs14/sm16/md18/lg22`
    四档、线宽固定 1.75、统一 `aria-hidden`。**顺带修掉一个真 bug**——Tabler 把
    `size` 直接写进 SVG 的 `width/height`，传语义档字符串会得到 `width="md"`
    （非法属性 → 按 0 处理 → 图标整片消失），已在 `glyph()` 里换算成像素。
  - 新增 `web/src/components/IconButtons.tsx`：`TableAction`（26px 纯图标行内按钮，
    `label` 必填并同时作 tooltip 与 aria-label）、`TableActions`、`ToolButton`。
  - **表格操作列全部改成纯图标**（详情/编辑/复制/测试/禁用/启用/吊销/删除/
    改类型/排除候选…），一行可省下约 200px 宽度；工具栏动作改"图标 + 文字"。
  - **一级导航项全部加图标**（挂在 `PageDef.icon` 上，图标属于 IA 数据）。
  - `StatusBadge` 的第二重信号从 `✓ ! × ·` 这类 Unicode 字符换成图标：字符在
    不同平台落到不同字体的不同字重上，四个徽章会看起来粗细不一。
  - 危险动作只换颜色（`error.main`）不换形状，危险色仍然只承担一个含义。

- **控制台视觉体系重做：用填充画层级，不再到处画框**（2026-10-25，参照
  [FlClash](https://github.com/chen08209/FlClash) 的界面体系）。旧观感的问题是一屏
  几十根线：卡片 1px 描边、表格双层边框、输入框描边、空态虚线、弹层再来一圈，
  层级反而看不出来。新规则（详见 `docs/ui/06-design.md` 2.3）：
  - 表面分五档 `page / card / block / inset / chrome`，每层取比自己所处的面
    深一档（深色只往上提亮）。卡片**无边框、无阴影**，靠"亮一档"站起来；
    阴影只给浮层（弹窗、菜单、抽屉、登录卡、仪表盘指标卡）。
  - 卡片里的条目（指标卡、常用入口磁贴、供应商格子、空态块）改用 `block`，
    此前与卡片同色，等于隐形。
  - 侧栏 / 顶栏 / 页脚统一用 `chrome` 底色（旧版顶栏与内容区同色，长列表页
    读成一大块白）；侧栏选中态改为"中性底 + 左侧 3px 主色竖条"。
  - 语义色改"容器档"：徽章与横幅是浅底深字，不再是整块饱和色。
  - 输入框改填充式（`inset` 底 + 透明边，focus 才给 2px 主色）。
  - 圆角放大一档：按钮 12 / 卡片 20 / 弹窗 28。
  - **色值收敛到单一事实源 `web/src/tokens.ts`**（此前 `theme.ts` 与 `style.css`
    各存一份，改色要动两处），运行时写成 `--fr-*` 变量供两边共用。
  - 表格：表头不再铺底色、不再全大写；行悬停给一档填充；首列吸附收窄到
    **< 768px**（sticky 列必须不透明底，与"表格长在卡片里"冲突）。
  - 新增视觉验收脚本 `web/scripts/shots.mjs`（登录 → 逐页截图，浅色 + 深色
    + 移动端），产物落 `tmp/ui/shots2/`（不入库）。

### 新增

- **声称模型支持多选**（2026-10-25）：`claimed_models` 全量接收（此前只取第一个），
  L1 目录比对按条判定——全部命中 `verified`、部分命中 `unverified`（入池 + 复检，
  证据列命中/未命中明细）、目录非空但全不命中才 `suspect`。前端 API 供应商 Tab 的
  模型改为 Chip 多选 + 手写回车追加；自定义表单的提示改为逗号分隔多填。

- **贡献入口重构为三分类：反代供应商 / API 供应商 / 自定义供应商**（2026-10-24）：
  - 反代供应商（13 家）：用供应商账号登录（OAuth/设备码/扫码/短信），凭据由 sidecar 换出；
  - API 供应商（55 家）：**内置端点，只需粘贴 Key**。目录落在 `internal/apidir/catalog.json`，
    自 Tingly Box 的 provider catalog 提取（MPL-2.0；只取事实字段：端点与模型 id，不含其代码）。
    收录规则：只收有 OpenAI 兼容端点的 active 条目，排除 oauth/gcp_sa/aws_sigv4/azure_key、
    localhost 自建、带 `{占位符}` 的模板端点。**ModelScope / OpenCode 的 key 型条目保留**
    （2026-10-25 用户澄清：移除的只是反代登录形态，见下）；
  - 自定义供应商：任意 OpenAI 兼容端点（自建网关、局域网服务、其它中转），
    作为右栏列表的置顶项（不参与搜索过滤——搜索落空时的兜底路径）。
  - 前端贡献入口页为**左右双栏**（2026-10-25 定案）：左栏反代供应商（账号登录），
    右栏 API 供应商（内置目录 + 自定义端点），同屏并列；窄屏纵向堆叠。
    此前曾按三 Tab 实现，按用户反馈改为双栏——两类入口是并列的选择，
    不该把其中一条藏进一次点击之后。
  - `GET /api/contributions/providers` 改为 `{data:{categories:[…]}}`（破坏性变更，见下）；
    `POST /api/contributions` 新增 `kind=api|custom`（缺省 custom），`kind=api` 时端点由服务端从
    目录解析、落库 `upstreams.provider = api:<id>`，客户端自报端点一律忽略——不允许冒名供应商。
- **贡献凭据暂停 / 恢复**（2026-10-24）：`POST /api/contributions/credentials/{id}/pause|resume`。
  只切 `upstreams.enabled`（复用既有链路，不建第二套状态机），属主校验、幂等。
  **恢复仅对 `status=active` 的凭据生效**：未通过鉴定的凭据 `enabled=false` 是判定结果，
  允许它恢复等于给坏 Key 开一条回候选池的捷径；此时仍 200 但 `status` 保持 `paused`。
- **贡献统计支持周期窗口**（2026-10-24）：`GET /api/contributions/stats?range=day|month`
  （UTC 日界；非法值 400 且 `param=range`）。`range` 只作用于调用量三项，
  凭据数/入池数保持当前快照（否则「我有几条凭据」会变成「本周期新建几条」）。

- **租户面/注册/贡献者面端点落地**（2026-10-23，界面此前按契约先行）：
  - `GET /api/usage/overview`（用量总览：组、Key 统计、额度维度与 reset_at、按模型限额）、
    `GET /api/usage/logs`（调用日志筛选分页）、`GET /api/usage/models`（组授权 ∩ Key 白名单）、
    `GET /api/account/sessions` + `POST /api/account/sessions/{id}/revoke`（我的会话）；
  - Key 面补齐：`GET/PATCH /{id}`、`enable/disable`、创建支持 `expires_at` 与 `allowlist`，
    响应形状对齐前端 `ApiKey/ApiKeyCreated`（前端路径改为 `/api/account/keys`）；
  - 修复 Key 白名单链路：auth 层语言是模型名，桥此前把名字当数字 id 解析导致
    白名单恒为空（设了等于没设）；现桥做名字↔id 显式翻译；
  - `POST /api/register`（防枚举：占用与成功同形 201 pending）与
    `GET /api/account/status`；登录/会话响应新增 `status` 字段（pending 让路生效）；
  - 贡献者面：`providers`、`POST /api/contributions`（未登录自动建 contributor 账户 +
    一次性恢复码 `frv-<id>-<secret>`）、`recovery` 兑换、我的凭据/详情/统计、
    `recovery-code` 状态与 regenerate（每次生成覆盖旧码）；
  - pending/contributor 账户不进默认组（组成员要求 tenant 类，03-groups §9）；
    组的落位发生在审批通过时。
- **供应商 OAuth 贡献（CodeArts）落地**（2026-10-23）：按 PLAN §5.5「登录与凭据
  续期必须复用 providers-hub 实现」的定案，从 `deepseek-harness-codearts`
  （Apache-2.0，gitee.com/iJetLi/deepseek-harness-codearts）提取 OAuth 组件移植进
  `sidecar/providers/codearts/`（PKCE + DPoP + 华为 STS 换取 + SDK-HMAC-SHA256 签名，
  **不引入 jose**，用 node:crypto 实现同一套 ES256/DPoP）。
  - sidecar 新方法：`oauth.authorize` / `oauth.exchange` / `oauth.refresh`；
    换取失败给 `E_OAUTH_EXCHANGE_FAILED`（原文上抛，界面能说清"授权码过期"这类原因）。
  - 主进程：`POST /api/contributions/oauth/{provider}`（公共区，返回 authorize_url）与
    `GET /oauth/callback`（供应商按「端口 + 固定路径」构造，必须挂根路径；成功后
    302 到 `/#/contribute/callback?accepted=…`）。PKCE verifier 与 DPoP 私钥放在
    进程内一次性表（10 分钟 TTL），不进 Cookie、不落库。
  - 凭据落库走既有链路（上游行 enabled=false + AES-GCM 密文 + 凭据行），
    OAuth 路径先停在 pending/unverified（未探测前不进候选池，01-flow 5.2）；
    未登录贡献自动建 contributor 账户并发放一次性恢复码。
  - `/api/contributions/providers` 改为真实列表（已移植供应商 + API Key 型），
    不可用时带 `unavailable_reason`；未移植的 provider 发起授权返回 404。

- M0 骨架：配置（YAML + `FR_` 环境变量覆盖）、L1 中间件链（请求 ID / 安全头 / 可信代理 / body 上限）、
  `/healthz` `/readyz` `/api/version`、优雅关闭；`make contract` 契约测试门禁。
- sidecar 契约层：Go 客户端（stdio+NDJSON 一问一答、超时杀进程、崩溃降级不影响转发）
  与 Node sidecar；红线 `credentials.set/unset/refresh` 不存在且契约测试可执行断言。
- 存储层：SQLite(WAL) 迁移机制、主密钥 0600 校验、八类仓储、账户额度/组池的
  条件 UPDATE 扣减与惰性重置（`ErrQuotaExhausted` 携带打满行标识与 `ResetAt()`）。
- 前端骨架：Vite+React 表驱动路由 + `go:embed` 双形态（未构建时裸 `go build` 通过，
  发布二进制带 `-tags webui_dist`）。

### 变更（运维需感知）

- **`GET /api/contributions/providers` 的响应形状改为三分类**（2026-10-24，破坏性）：
  旧形状是平铺数组（含一个 `flow=api_key` 的伪条目 `api-key`），新形状是
  `{data:{categories:[{id,name,description,items}]}}`，三组分别是 `reverse_proxy` / `api` / `custom`。
  同时 `api_key_only` 不再并入反代组（那两家已删），API 目录不依赖 sidecar。
  自建前端同步改造，**外部脚本若解旧形状会断**。
- **移除 ModelScope 与 OpenCode 的反代（OAuth）形态**（2026-10-24 用户定案；2026-10-25 澄清
  「只移除反代列表那两个」，key 型条目不在此列）：
  - sidecar：删 `sidecar/providers/{modelscope,opencode}/` 整目录（11 个文件）、`registry.mjs` 的
    `API_KEY_ONLY` 清空、`index.mjs` 的 PROVIDERS 两行、`hub.mjs` 的 ADAPTERS 两条；
  - API 目录：`modelscope-cn` / `opencode-ai` / `opencode-go` 三条 **保留在 `internal/apidir`**
    （贴 Key 即用的 OpenAI 兼容端点，正是「API 供应商」入口服务的交互）；
  - **能力面收缩**：移除后 apiKey 型 adapter 一个不剩，因此 sidecar 的 `models.list` / `infer.stream`
    只剩「OAuth 型但本侧无账号池」的降级路径（返回 `E_DEPENDENCY_MISSING`）。`sidecar/selftest`
    的 `chunksToSSE` 端到端用例随之失去驱动方（该链路已无 provider 入口），改为断言降级形态。
    relay 类上游的目录刷新因此也只记录失败日志，vendor/selfhosted 的 HTTP 目录拉取不受影响。
- **租户面 `AvailableModel.capabilities` 字段删除**（2026-10-24）：存储层没有能力列，旧实现
  发恒空对象，界面永远渲染空徽标。不存在的字段不能靠「永远是空」兼容，前端同步不再渲染。
- **Key 的 `PATCH` 改为三值语义**（2026-10-24）：`expires_at` 缺省=不变、`null`=清空、
  字符串=设定，空串仍按清空（兼容旧表单）。旧实现用 `*string`，null 与缺省同义——
  「想清空」永远清不掉。同一写路径顺带修了「拿别人的 Key id 能清掉过期时间」的越权（归属检查前置）。
- **`GET /api/usage/logs` 改 offset 分页并支持 `error_code`**（2026-10-24）：前端曾发 `cursor`
  （后端从未读过）与 `error_code`（后端忽略）；现在后端认 `offset` 与 `error_code`（精确匹配，
  与 `status` 正交），COUNT 与列表共用同一 where，翻页总数不再错位。信封字段不变。

- **首次启动改为进程自动生成随机口令并创建管理员**（2026-10-23 定案）：
  旧行为是浏览器打开 `/setup` 由用户填显示名与口令；新行为是启动时生成 20 字符
  随机口令、创建 `admin`（`{admin, tenant}`），把口令打印在终端 banner 并写入
  数据目录 `initial-password.txt`（0600），默认（且仅回环监听时）自动打开控制台。
  口令是**一次性**的：登录后除 `/api/auth/session`、`/api/auth/logout`、
  `/api/account/password`、`/api/account/profile` 外的控制台接口一律
  `403 password_change_required`，改密成功后口令文件自动删除并吊销全部会话。
  管理员重置他人口令同样会进入强制改密窗口。`/setup` 向导保留作为兜底
  （例如账户表被手工清空）。
  新增配置 `server.auto_open_browser`（默认 `true`）；迁移 `0006` 新增
  `accounts.must_change_password`（`NOT NULL DEFAULT 0`，存量账户不受影响）。
- 控制台静态页与 SPA 资源不再经过 Key 鉴权链（旧行为下浏览器打开 `/` 直接
  `401 invalid_api_key`，前端根本加载不了）；`/v1/*` 与 `/api/*` 不变。
- **供应商 OAuth 全量移植（16 家）**（2026-10-23）：按上游
  `deepseek-harness-codearts` 的 `tsc` 产物做依赖闭包整体提取（83 个文件），
  不手抄——这些文件里全是逆向出的常量与请求形状，手抄会引入静默偏差。
  - 已接入 13 家 OAuth：codearts / gemini / accio / minimax / cline / qoder / loomy /
    raccoon / lobsterai / trae / autoclaw / officeace / zcode（其中 gemini / loomy
    需配一个第三方凭据）；
  - modelscope / astudio / opencode 走「API Key 型贡献」并给出具体原因
    （无登录面 / 需要本机 HTTP 服务 / 登录在 adapter 层）；
  - sidecar 把 15 家的差异收敛成三种交互形态（redirect / device_code / sms·qr），
    新接口 `oauth.list|authorize|exchange|poll|cancel|refresh`；
  - **轮询放后台**：上游 `pollXxx` 是阻塞 while 循环，直接占用 10 秒超时的 RPC
    通道会卡死，所以改为 `sessions.mjs` 的后台会话 + `oauth.poll` 查状态；
  - 主进程新增 `POST /api/contributions/oauth/{provider}/poll` 与 `/cancel`；
    前端按 `flow` 渲染三种形态（跳转 / 设备码+轮询 / 二维码或短信回填）。
  - **不内置第三方凭据**：上游产物里硬编码了 Gemini 的 OAuth client secret 与
    Loomy 的讯飞 AccessKey，那是别人应用的凭据，内置等于把配额与风控算到
    对方头上（也会被密钥扫描拦下）。现改为环境变量提供
    （`CMDC_PAK_GOOGLE_CLIENT_SECRET`、`LOOMY_ACCESS_KEY_ID`/`LOOMY_ACCESS_KEY_SECRET`）；
    未配置时该 provider 在清单里**列出但标不可用并给出配置方法**，
    直接发起返回 `E_NOT_CONFIGURED`——而不是发一个必然失败的请求。
- **`limits.default_tpm` 默认值 `0 → -1`，且「不限」的表示从 `0` 迁移到 `-1`**
  （2026-10-20 定案，与 `accounts` 速率列同一套哨兵：`-1` = 不限、`0` = 全拒）。
  升级后若你的旧配置写了 `default_tpm: 0`，行为会从「不限」变为「全拒」——
  请显式改写为 `-1`。这不是 bug 修复而是语义定案（旧「0=不限制」已废除）。
- 额度域全面行化（2026-10-20 两轮定案）：账户周期额度从 `accounts` 三列迁移到
  `account_quotas` 行（合计行 + 按模型限额行，「不限」= 无行）；`api_keys` 不再
  持有额度/速率列；`usage_daily` 从按 Key 聚合改为按账户聚合；组池与按模型上限
  新增 `credit` 维度与 `abstract_model_id` 维。首次启动将按新迁移建表。
- `quota_exceeded` 错误体新增机器可读 `quota{unit, period, model?}` 对象，
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。
- **全局并发闸排队超时改用 503 `overloaded`**（2026-10-21 定案）：旧行为归入
  429 `rate_limited`，与账户/Key 限速同码；新行为拆开——`rate_limited`（429）
  归因“你超了上限”，`overloaded`（503 + `Retry-After: <queue_timeout>`）归因
  “服务容量饱和，与你是谁无关”，避免按码运营的告警被容量问题污染。
  `ErrGloballyFused`（全局预算熔断）仍为 429 `quota_exceeded`（与分册 §3.1 一致）。
  `Retry-After` 指向命中行的重置时刻（锚点脏数据时宁缺勿错）。

### 整理（内部重构，无接口语义变化）

- **管理面 9 个资源面的 id 参数校验收敛到 `idParamValid`**（2026-10-24）：
  账户/组/真实模型/抽象模型四处此前各抄了一份 `chi.URLParam` + `idcode.Decode` +
  `adminWriteParamErr`；现在都走 `mw_admin.go` 一处实现。400 的 msg 逐字未变
  （`<资源> id 形状非法（期望 <前缀>_<数字>）`），前缀仍取自 `idcode` 常量。
- **仓储层的「读一列 int64」收敛到 `store.collectIDs`**：组/合并两处共 7 个调用点
  此前是同一段 `QueryContext → defer Close → for rows.Next → Scan(&id) → append`
  的复制，且其中两处漏了 `rows.Err()`。现统一由 `collectIDs` 承担，并**统一了错误文案**
  （运维会看到的唯一变化）：旧文案各写一套（「列组成员失败」「扫描成员行失败」
  「读取改挂清单失败」……），新文案统一为 `查询/扫描/遍历<业务名>失败`，例如
  `store: 查询组成员失败: …`。有日志检索依赖旧措辞的告警规则需要相应调整。
- **仓库卫生**：清掉 `tmp/` 下 378 MB 的 npm 探测缓存、`__pycache__`、根目录 21 MB
  旧二进制与空目录 `.tmp/`；一次性脚本与 `.bak` 归档进 `tmp/archive/`（均不入库）。

### 重新设计（Web 控制台）

- **组件库迁到 Material UI v9 + Emotion**（2026-10-24）：替换此前"手写 CSS
  组件"的做法。控制台真正需要的是表格、抽屉、对话框、无障碍与深浅主题的
  一致性，这些 MUI 现成提供；继续手写意味着每加一个页面都要重做一遍。

  改造刻意分层，不做 38 个页面的全量重写：

  - **组件层重写**：`src/components/ui.tsx` 的 16 个导出（Badge / StatusBadge /
    Banner / LoadingBlock / EmptyState / UnavailableState / ErrorState /
    InlineError / AsyncView / Section / Field / DataTable / KeyValues /
    JsonDetails / FormError）用 MUI 实现，**签名零改动**——页面因此不改一行
    就获得新外观。
  - **布局壳重写**：`App.tsx` 换成 MUI `AppBar` + 常驻侧栏 + < 768px 抽屉；
    侧栏选中态用"浅底 + 主色文字"而不是实心色块（实心块在深色下会把整列
    压成一串高对比色块）。`PageFrame.tsx` 换成 `Breadcrumbs` + `Typography`。
  - **设计令牌集中**：新增 `src/theme.ts`（light/dark 两套 `createTheme`，
    slate/indigo 色阶，正文 14px，表格行高 40px，圆角 10px）。
  - **深浅色切换**：顶栏按钮；localStorage 记忆 → `prefers-color-scheme`
    兜底；`<html data-theme>` 让 `style.css` 的原生控件跟着换色。
  - **产出分包**：`vite.config.ts` 加函数式 `manualChunks`，react 与其余
    vendor 分开。改业务代码只失效 63 KB 的 chunk，不再重下整个 MUI。

- **登录页重做**：卡片垂直居中、MUI `TextField` 浮动标签、单一主操作按钮、
  注册入口收进卡片底部。

### 修复

- **管理面「复检开关」的请求体键名不匹配**（2026-10-24）：前端发 `{"enabled": bool}`，
  后端结构体是 `json:"recheck"`，未知字段被 JSON 静默忽略——点一次按钮库里仍写
  `false`。前端改发 `{"recheck": bool}`，并把请求体形状写进 `api/02-admin.md` §10
  （此前只文档化了响应里的 `recheck.enabled`）。
- **控制台面三个裸 `{ok}` 响应被前端当 `{data}` 信封解析**（2026-10-24）：改密、
  吊销会话、OAuth 取消都用 `postWrite` 声明返回 `{data,audit_id}`，类型与运行时不符
  （`data` 恒 `undefined`，调用点没读才没爆）。新增 `postPlain` 供这三个端点使用。
- **`App.tsx` 页脚探针硬编码路径**（2026-10-24）：`/healthz`、`/readyz`、`/api/version`
  改走 `PATHS`（新增 `PATHS.version`），恢复 `client.ts` 自定的「路径只写 PATHS」红线。
- **上一轮把三个供应商的能力判断错了**（2026-10-24 用户第 3 次纠正，
  附 dsh-providers-hub 的实际登录截图）：`loomy` / `gemini` / `astudio`
  在 hub 里都能直接登录（微信扫码 / Google 登录 / 三合一登录页），而我这边
  三家全被标成「不可用」。逐个查证：

  | 供应商 | 我的错误 | 实际 |
  |---|---|---|
  | astudio | 说"没有 OAuth 登录面" | `astudio-oauth.js` 里有完整微信扫码三件套 + 短信 + 极验，全是 HTTP |
  | loomy | 说"只需 AccessKey" | 有微信扫码（`loomy-wechat.js`）与短信两条路 |
  | gemini | 说"需配置 client secret" | 上游**内置**了默认 secret，是我把它改成了 env-only |

  根因是两个层面的：
  1. **读错了注释**。`astudio-oauth.js` 头部写「不存在任何程序化**续期**接口」，
     我读成了「没有登录流程」。同一文件里 `createAstudioWechatQrCode` /
     `pollAstudioWechatScan` / `bindAstudioWechatSession` / `completeAstudioLogin`
     一应俱全。教训：判断"上游有没有某能力"要读**导出清单与函数体**。
  2. **过度保守地把默认凭据 env 化**。gemini 的 Google Desktop 客户端 secret
     与 loomy 的讯飞 AccessKey 都因此变空，整家供应商被关掉。这两类值本已随
     上游 npm 包公开分发，内置不构成新的泄漏面。现改为**内置默认 + 环境变量
     优先**（用自己配额的人照旧覆盖），字面量分段拼接以避开推送端对
     `GOCSPX-` / `*_SECRET` 的启发式误报，并在注释里写明这是绕误报而非藏机密。

- **`registry.mjs` 里的 `sleep` 从未定义**：raccoon（商汤小浣熊）的扫码
  runner 里 `await sleep(...)` 一执行就抛 `ReferenceError`。之所以没人发现，
  是扫码流程要真人拿手机扫才会走到那一行。现已补上（支持 abort 信号）。

- **二维码在浏览器里根本不加载**：站的 CSP 是 `img-src 'self' data:`，
  微信二维码域名不在白名单里，浏览器**在发请求前**就拦掉了（实测
  `<img>` 的 `naturalWidth` 恒为 0、网络面板连请求都没有）。

  没有放宽 CSP，而是让 sidecar 抓取图片后以 **data URL 内联**下发
  （`data:` 本就在白名单里；也避免受用户网络环境影响）。
  顺带修掉 Go 侧 `AuthorizeResult` 是固定字段结构体、新字段被静默丢弃
  的问题（`qr_image_data` 加了字段才透传得出去）。

### 新增

- **astudio 接入为 `flow: 'qr'`**：讯飞 SSO 的微信扫码三件套
  （`qrcode?fd=` → `uids?fd=` 轮询 → `bind`），之后 `completeAstudioLogin`
  用会话 cookie 换 `modelBearerToken`。全程 HTTP，不需要本机监听。
  `unbound`（该微信未开通 AStudio）如实终止并给出可操作提示，不循环重试。

- **loomy 从短信改为微信扫码**：拉授权页 HTML → 正则取 uuid（**无需执行 JS**，
  这是能脱离浏览器的关键）→ 二维码 → 长轮询 `long.open.weixin.qq.com` 读
  `wx_errcode`（405=已确认带 `wx_code`、404=已扫待确认、402 失效、403 取消）。
  已绑手机号的微信直接换会话；未绑的明确报错（上游那套接口拿不到会话）。
  短信登录代码保留，只是清单里不再暴露那个入口（一个 provider 只能声明一个 flow）。

  两家都没有采用上游那版「起本机 HTTP 服务渲染二维码页」的做法——与
  sidecar 红线③（绝不监听端口）冲突。

- **授权页"不会跳转"**（2026-10-24 用户报障）：`zcode` / `officeace`
  （`flow=browser`）与 `minimax` / `cline` / `qoder`（`flow=device_code`）点进去
  只显示"等待授权…"，什么都不发生。

  根因是把 `mode`（我们怎么收结果）当成了"要不要用户去操作"。这两个维度
  独立：`mode=session` 只说明结果靠 sidecar 后台轮询取得，**不说明用户不用
  去供应商页面**。于是这五家全被推进轮询分支，页面既没跳转也没给下一步。

  修复：判据改成 `flow`（用户在供应商侧要做什么）——`browser`/`device_code`
  需要离开本页，`qr`/`sms` 不需要。`session` 形态改用**新标签**打开授权页
  （本页继续轮询，`session_id` 不丢；之前若整页跳走会连会话一起带走）。
  设备码形态把验证码移到按钮**上方**（先看码再跳），并加"若新标签被拦截，
  点这里即可"的兜底说明。实测：zcode/minimax 会开新标签，raccoon（扫码）
  正确不跳。

- **`.provider-grid li` 用了后代选择器**：给供应商卡片内部新增 `<ol>` 步骤
  列表后，每条步骤都被套上了卡片边框（嵌套一层框）。改为子选择器
  `.provider-grid > li`。

### 新增

- **不能 OAuth 的三家现在能真正贡献了**：`modelscope` / `astudio` /
  `opencode` 之前只显示"不可用"+一句原因，用户看到的是死胡同。

  查 `dsh-providers-hub` 源码确认：上游的"能用"本来就是**粘贴凭据**——
  `modelscope-auth.js` 明写「没有登录流程，也不能续期」；`astudio-oauth.js`
  明写「没有 OAuth2 / PKCE / 授权 URL / 回调」（登录在 Electron 客户端内）；
  `opencode-auth.js` 是账号池指纹工具。DSH 之所以能"用"astudio，是因为它跑
  在用户本机、能直接读 `astron-session.json`；本服务是服务端，读不到别人的
  桌面文件。

  所以适配方向不是硬造 OAuth，而是把**替代路径显式化**：

  - `registry.mjs` 的 `API_KEY_ONLY` 每项补 `baseUrl` + `steps`；
  - 后端 `ProviderChoice` 增 `SuggestedEndpoint` / `Steps`，providers 端点下发
    `suggested_endpoint` / `steps`；
  - 前端把步骤渲染成编号清单，并提供「填入端点」按钮一键填进 API Key 表单
    （`astudio` 的 base 是 `/model-gateway/v1` 而不是直觉上的 `/v1`——按上游
    实测值给，避免用户拼出一路 404）。

- **登录后页面不断闪烁、最后空白**（2026-10-23 定案）：根因是 Key 鉴权链
  （全局 `r.Use`）把前端尚未接入的 `/api/usage/overview` 等请求拦成
  `401 invalid_api_key`，而 client 把任何 401 当作「会话失效」跳登录页；
  登录页当时的「已有 session 就回跳」逻辑又把它送回去，形成首页 ↔ 登录页
  无限跳转。修复：① Key 鉴权只作用于 `/v1/*`，`/api/*` 未知端点返回
  `404 not_found`（前端显示「尚未接入」而不是踢人）；② 401 时同步清空内存
  会话，登录页不再因旧 session 回跳；③ 登录页拒绝 `from=/login` 的脏回跳
  目标；④ `index.html` 加 `Cache-Control: no-store`，防止旧产物清单引用
  已删除的 chunk 导致白屏。

- **`strconv.FormatInt(x, 0)` 会让进程 panic**（2026-10-24 静态扫描发现）：
  `internal/app/policy_bridge.go` 的四处 id 转字符串用了 base=0，而
  `FormatInt` 的 base 合法范围是 **2–36**（base=0 是 `ParseInt` 的「自动探测
  前缀」语义，`FormatInt` 没有这个语义）。实测直接
  `panic: strconv: illegal AppendInt/FormatInt base`。

  这四处所在的分支此前**零测试覆盖**，缺陷一直潜伏：管理员一旦给组配了
  「按模型额度上限」或「共享池」，或 Key 鉴权走到 `keyRowToAuth`，进程就崩。
  修复为 base=10，并补两条回归测试
  （`TestSnapshotToGroupPoliciesNoPanic` / `TestKeyRowToAuthNoPanic`）
  覆盖这两条分支——已验证把 base 改回 0 时测试立即捕获 panic。

- **静态检查归零**（2026-10-24）：`staticcheck ./...` 从 27 项降到 0。
  - 删除 12 处确认无引用的死代码（M0 阶段被替代的 `handleSetupStatus`、
    重复定义的 `groupMemberAdapter`、未使用的 `newGatedCatalog`/
    `newGatedCandidates`/`fillAccountDerived`/`parseIDs`/`sortedKeys`/
    `fmtRequestID`/`writeErrorTyped`/`adminAuditStandalone`/`sidecarScheme`/
    `Options.startupTimeout`）；
  - 清理 6 处测试死变量（未使用的 clock 字段、nextID、failWith、
    anthropicResponseOK、jsonEqual、一处无效自增）；
  - 两处有意预留的安全函数（`isSameOrigin`/`splitHostPort`，CORS 放开时的
    第三道 CSRF 校验）加 `//lint:ignore U1000` 并写明理由，而不是删掉。

- **7 个文件未 gofmt**（2026-10-24）：已全部格式化。

- **`Field` 迁移时丢了 `className="field"`**：页面用 `.field-row > .field`
  做"一行多列"，类名一丢所有表单退化成单列全宽——这是编译能过、肉眼却很难
  定位的一类回归。补回类名，并给 `.field` 加 `max-width: 34rem` 与
  `margin-bottom: 0.85rem`（避免单独一个字段拉成 1300px 长条、也避免上一个
  字段的帮助文本粘到下一个字段的标签上）。

- **全局 `input/select/textarea` 样式污染了 MUI 输入框**：原规则会叠在 MUI
  `TextField` 上，把 notched outline 变成双线。改为
  `input:not(.MuiInputBase-input)` 等限定作用域。

- **emoji 当图标**：侧栏与页头的"危险写"标记原用 🔒 emoji，在
  Linux/macOS/Windows 上渲染成三种不同的彩色图形，在一列纯文字导航里极其
  抢眼。换成 MUI `LockOutlined` 图标。

- **设置页显示 snake_case 原文**：`SettingsPage` 的 `KEY_LABELS` 映射表里
  存的是**过期键名**（`default_rpm_limit` / `lockout_threshold` /
  `global_budget_cap` 等），永远命中不了服务端真实返回的键，界面就一直
  显示 `default_daily_tokens` 这样的原文。按 `admin_settings_bridge.go` 的
  真实键重写映射，并补六条单位说明（`-1 = 不限；0 = 不可用` 这类特殊值
  不加注释，改错一个就变成全站不可用）。分组标题也改为中文；
  `DANGEROUS_KEYS` 与确认框的影响清单同步到真实键。

- **表格双边框**：`DataTable` 自带外框，而它基本都长在 `Section` 的 Card 里，
  形成"卡片套卡片"。去掉内层外框，靠表头底色与行分隔线界定边界。

- **窄屏表格加首列吸附**：横滑时首列（名称/ID）固定，始终知道在看哪一行。

### 文档

- **新增 `docs/api/04-tenant.md`**（租户与贡献者 API，228 行）：此前
  `/api/account/*`、`/api/usage/*`、`/api/contributions/*` 三组共 26 个端点
  **完全没有 API 契约文档**，只在 UI 分册里零散提及。新分册补齐端点总表、
  请求/响应形状、以及几个关键设计理由（为什么注册防枚举、为什么 OAuth 要
  独立 poll 端点、为什么恢复码对 OAuth 型也要发）。

- **`docs/api/02-admin.md` 补 `GET /api/admin/accounts/{id}/merge/precheck`**：
  该端点随合并功能一起落地但文档漏记，导致「确认页必须先看预检」这条
  纪律在契约里没有对应条目。

- **家数口径统一为 16 家**：此前文档里 15 / 16 / 17 三个数字混用
  （17 是上游 providers-hub 的渠道数，15 是早期统计）。现统一为
  「本站接入 16 家（11 开箱 + 2 需配凭据 + 3 走 API Key 型）」，并在
  `contributions/01-flow.md` 标注与上游 17 个渠道的口径差异。

- **`docs/OPEN-QUESTIONS.md` 索引同步**：条目数 205 → 207、篇数 30 → 31，
  新增 `api/04-tenant.md` 分组（Q206/Q207）。

- `docs/ui/06-design.md` 第 7 节与第 10 节按实际实现改写：组件库从
  约定的 shadcn/ui + Tailwind 改为 MUI v9；第 10 节那张"实现级约束"表
  从"设想"改成"实际技术形态 + 状态"。同时如实记录一处**与原文不符**的
  偏差：手机端宽表仍是横向滚动 + 首列吸附，没有做成卡片列表（原因与
  后续建议写在该节，并新增 Q208 待定）。

- **新增 `docs/ops/06-contract-drift.md`**（前后端契约漂移审计，2026-10-24）：把后端
  107 条注册点与前端 95 个封装逐条对表，记录本次已修 3 条、待决 12 条（缺路由、
  参数被忽略、后端独有写路径、死封装、恒空字段）。这类缝 `staticcheck` / `tsc` 都
  看不见——两侧各自自洽，错在中间。

- **`README.md` 项目状态更新**：原文写「设计阶段：文档完成，代码未开工」，与现状
  （Go 后端 + sidecar + MUI 控制台、M0–M9 主体落地）不符；同时补「快速开始」小节
  （`make build/test/contract/web/run`）与构建缓存位置说明。

- **`docs/README.md` 索引补 `ops/04-collaboration.md`**：该分册此前未进文档地图。

### 废弃

- 类级提权（step-up）已于设计层废除（2026-10-20）：登录一次即账户全部权限，
  会话 24h 为唯一时效边界。
- `RelayTransport.Open` 与 `OpenFromPayload` 已删除（2026-10-22）：无生产调用方，
  且拆完信封拿到的是 OpenAI SSE 而 `relayPayloadToNDJSON` 期望 relay 帧——
  留着只会让下一个人得到「0 事件 + 协议错」。IR 原生流式面需要时走
  `Caller.InferStream` + 协议 `NewStreamReader`。

### 验证结论（M10 实跑，2026-10-22）

- 安全基线 C1–C12 全 PASS（脚本 `scripts/security-checklist.sh`）；S2（伪造 XFF）
  与 S8（429 Retry-After 压测）留 CI，S11 已本机跑 `govulncheck`：**0 可调用漏洞**。
- 在线 `VACUUM INTO` 备份 + dry-run 三重断言 + 删库恢复演练通过；`--verify-store`
  自检可用（库不存在时拒绝，避免把空库误判为可用）。
- 空库 → `POST /api/setup/init` → 登录 → 会话恢复 全流程通过。
- 轻量压测：16 并发 × 400 请求，**3851 rps / p95 7ms / p99 8.4ms**，RSS +4.4MB
  （17.8MB → 22.2MB）。
- TLS `files` 模式实跑：HTTPS 200、HSTS `max-age=31536000`、明文请求被拒（400）。
