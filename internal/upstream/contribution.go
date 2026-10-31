// contribution.go —— 贡献流程接凭据（M8，docs/contributions/01-flow.md 5 节、
// 02-credentials.md 5/6 节、models/05-verification.md 5 节）。
//
// 一次提交的完整链路（全部在本文件内编排，存储与探测都是窄接口）：
//
//	提交（provider + base_url + key + 声称模型）
//	  → L0 形式校验（本地，秒级；廉价闸门）
//	  → key 指纹 = external_id（纯 Key 型没有外部身份，用它做幂等与撞身份键）
//	  → 落库：upstreams 行 + upstream_secrets（AES-GCM 密文）+ upstream_credentials(pending)
//	  → 探测：GET {base_url}/models（余额端点未接，见 credits.query 的说明）
//	  → 判定四态 → 入池（active）或拒绝（disabled，保留行与证据）
//	  → 贡献者可一键撤销（status=disabled + 销毁密文）
//
// 三条不能破的边界：
//  1. **明文只在内存里**：密文经 ContributionStore.PutSecret 落库（AES-GCM 在 store 侧），
//     本文件不保留明文的第二份；错误信息一律脱敏（redactText）；
//  2. **无人工审核**：判定只由这里的两段自动判定给出（01-flow 5.1）；
//  3. **status 与 verify_status 正交**（02-credentials 5.1）：status 回答「能不能被调用」，
//     verify_status 回答「声称是否相符」。混用会让可恢复的问题变成不可恢复的删除。
package upstream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// —— 错误 ——

// ErrContributionRejected 表示判定为「拒绝入池」（不是程序错误）。
//
// 为什么用哨兵错误而不是只靠 Outcome：调用方（控制台/API）需要把「业务拒绝」与
// 「系统故障」分开——前者给贡献者可执行的原因（01-flow 7 节），后者要重试。
var ErrContributionRejected = errors.New("upstream: 贡献被拒绝入池")

// ErrContributionForbidden 越权撤销（不是本人也不是管理员）。
var ErrContributionForbidden = errors.New("upstream: 无权撤销该凭据")

// —— 窄接口（写域铁律：功能包定义接口面，装配层钉实现） ——

// ContributionStore 是贡献流程对存储的最小写面。
//
// 为什么是这些方法而不是 *store.Store：一次提交只碰三张表（upstreams / upstream_secrets /
// upstream_credentials），把这五个动作列出来，装配层换实现（内存 fake、未来分库）时
// 不需要重读一遍 store 的全部 API。
//
// 幂等读（ListCredentials）也在这里：撞 UNIQUE(provider, external_id) 时要区分
// 「同一账户重复提交」（幂等接受）与「另一个账户提交同一身份」（拒绝），
// 而 store 侧没有 by-external-id 的读，用 owner+provider 列再筛是现有的能力。
type ContributionStore interface {
	UpsertUpstream(ctx context.Context, kind, name, baseURL string, ownerAccountID *string, enabled bool) (string, error)
	PutSecret(ctx context.Context, upstreamID string, plaintext []byte) (string, error)
	DeleteSecret(ctx context.Context, secretID string) error
	CreateCredential(ctx context.Context, ownerAccountID string, provider, externalID, label, secretRef string) (string, error)
	SetCredentialStatus(ctx context.Context, id string, status store.CredentialStatus) error
	MarkVerified(ctx context.Context, credentialID string, status store.VerifyStatus, evidence string) error
	// SeedCatalog 把「自动获取」探到的模型 id 清单落成 real_models 行
	// （source=auto、status=active 的最小行）。与刷新器共用 UpsertRealModel
	// 的幂等 upsert；实现见 ModelRepo.SeedCatalog。
	SeedCatalog(ctx context.Context, upstreamID string, models []string) (int, error)
	LookupCredential(ctx context.Context, id string) (*store.Credential, error)
	ListCredentials(ctx context.Context, f store.CredentialFilter) ([]store.Credential, error)
	// ListUpstreams / UpdateUpstream 是撤销的必要读面：
	// **候选池的入口是 upstreams.enabled + real_models 可见性**，不是凭据 status
	// （realModelCandidates 走 ListVisibleRealModels + ListUpstreams）。
	// 所以「撤销立即生效」必须把上游行一起停掉，只改 credential.status 是假的撤销。
	ListUpstreams(ctx context.Context) ([]store.Upstream, error)
	UpdateUpstream(ctx context.Context, id string, name, baseURL string, enabled bool) error
}

// ProbeInput 是一次探测的输入。APIKey 只在本进程内存里流转。
type ProbeInput struct {
	Provider string
	BaseURL  string
	APIKey   string
	// ClaimedModels 是贡献者声称的模型（可多个、可空）。多选是 2026-10-25 定案：
	// 一把 key 往往同时提供好几个模型，只让声明一个会让贡献者隐去其余事实。
	ClaimedModels []string
}

// ProbeOutput 是一次探测的结果。Detail 必须是**可给贡献者看**的脱敏文本。
type ProbeOutput struct {
	// Reachable 是否连上了端点（连接层）。
	Reachable bool
	// AuthOK 端点是否接受这把 key（401/403 → false）。
	AuthOK bool
	// Models 目录里的模型 id（拿不到目录时为空）。
	Models []string
	// UsedModels 声称模型在目录中的命中（宁可空也不猜）。
	UsedModels []string
	// Detail 脱敏后的证据（进 verify_detail 与给贡献者的原因）。
	Detail string
	// Latency 探测耗时（排障用，不进证据文本）。
	Latency time.Duration
}

// CredentialProbe 探测一次「这把 key 能不能调、声称的模型在不在」。
//
// 为什么接口而不是直接发 HTTP：判定要与**怎么探测**解耦——测试要能构造
// 「401 / 目录缺模型 / 无目录 / 连不上」四种结果而不开端口；将来接 sidecar 的
// models.list（hub adapter）时也只是换一个实现。
type CredentialProbe interface {
	Probe(ctx context.Context, in ProbeInput) (ProbeOutput, error)
}

// —— 选项与构造 ——

// ContributionOptions 是贡献服务的可调参数。
type ContributionOptions struct {
	// FailThreshold 是「连续失败自动禁用」的阈值（≤0 取 5）。
	//
	// 为什么是 5：单次失败可能只是上游抖动（一次 502 不代表凭据坏了），而 5 次连续
	// 失败已经足以排除「偶发」——再放行下去就是把用户的请求持续打到一条坏通道上。
	FailThreshold int
	// Now 可注入的时间源（测试用）。
	Now func() time.Time
}

// ContributionService 编排提交 / 撤销 / 失败计数。
type ContributionService struct {
	store  ContributionStore
	probe  CredentialProbe
	opt    ContributionOptions
	mu     sync.Mutex
	fails  map[string]int // credentialID → 连续失败次数（仅进程内；重启即清零）
	lastAt map[string]time.Time
}

// NewContributionService 构造服务。probe 为 nil 时用 HTTPProbe（默认实现）。
func NewContributionService(st ContributionStore, probe CredentialProbe, opt ContributionOptions) *ContributionService {
	if probe == nil {
		probe = HTTPProbe{Client: &http.Client{Timeout: 15 * time.Second}}
	}
	if opt.FailThreshold <= 0 {
		opt.FailThreshold = 5
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &ContributionService{
		store: st, probe: probe, opt: opt,
		fails:  make(map[string]int),
		lastAt: make(map[string]time.Time),
	}
}

// —— 提交 ——

// Submission 是一次贡献提交。
//
// OwnerAccountID 必填：**匿名贡献也先建账户**（contributor 类是零权限容器，
// 见 01-flow 2.2）——账户创建属于 auth 包，本服务只负责「有账户之后的凭据链路」。
type Submission struct {
	OwnerAccountID string
	Provider       string
	// BaseURL 是 OpenAI 兼容端点根（如 https://api.example.com/v1）。
	BaseURL string
	// APIKey 是明文；只在本次调用内存活。
	APIKey string
	// ClaimedModels 是贡献者声称该 key 提供的模型（可多个、可空：空则不判定「相符」，
	// 只做可达性与鉴权，判定落在 unverified）。判定口径见 judgeProbe：
	// 全部命中 verified、部分命中 unverified（复检）、全不命中且目录非空才 suspect。
	ClaimedModels []string
	// Label 是给贡献者认领用的可读标签（如 "我的魔搭 key"）。
	Label string
	// Kind 是上游类型：vendor（公网端点）或 selfhosted（自建/本机）。
	// 空则按 BaseURL 判定（回环 → selfhosted，其余 → vendor）。
	Kind string
	// PreProbe 携带 Discover 那一次探测的结果，让 Submit 不再打第二次网络。
	// 它是**同包构造**的可选输入：外部（桥）拿到 *PreProbe 后原样传回来，
	// 不传（nil）时行为与改造前完全一致。
	PreProbe *PreProbe
}

// PreProbe 装一次探测的两个返回值（Go 没有"可选的多返回值"，用结构体装）。
//
// 为什么要把它做成可跨层传递的值而不是让桥自己缓存：缓存要键、要过期、
// 要并发保护，而这里的一次性传递根本不需要——探测结果的生命周期就是这一次
// 提交。做成导出结构体后，桥可以把它当不透明令牌穿过 httpapi 那道接缝。
type PreProbe struct {
	Out ProbeOutput
	Err error
}

// Outcome 是一次提交的结果（可以直接回给控制台）。
type Outcome struct {
	CredentialID string
	Status       store.CredentialStatus
	VerifyStatus store.VerifyStatus
	Evidence     string
	// InPool 是否进了候选池（status == active）。
	InPool bool
	// Reused 表示命中「同一账户重复提交同一身份」的幂等路径（没有新建行）。
	Reused bool
	// DiscoveredModels 是本次提交落进 real_models 的模型 id 清单（仅
	// 未声明模型 + 探到目录 + 凭据入池时填充）。「自动获取」把探测到的目录
	// 直接落库，前端据此回显给贡献者确认；同时凭据的模型卡立即有数据，
	// 不必等下一轮定时刷新（默认 10 分钟）。
	DiscoveredModels []string
	// SeededModels 是实际落库的条数（与 DiscoveredModels 的差 = 落库失败被
	// 记进 Evidence 等下轮刷新补齐的部分；0 且非空清单 = 全部落库成功）。
	SeededModels int
}

// Discover 是「自定义供应商未声明模型」的提交前闸门：探一次端点，
// 把能自动获取到的模型目录回给调用方；拿不到就返回 needClaimed=true，
// 由调用方在**落任何库之前**退回让用户手工声明。
//
// 为什么闸门必须在建账户之前：贡献链路会先建 contributor 账户并发恢复码
// （handler 侧），若把退回留在 Submit 内部，每次"拿不到目录"的尝试都会留下
// 一个无凭据的孤儿账户，而用户重试会再留一个。
//
// 为什么只有「可达 + 鉴权过 + 目录为空」才要求补声明：填模型能救的只有
// "列不出目录"这一种。不可达与鉴权失败填了也还是失败，那种情形必须走正常
// 链路落库留痕（02-credentials 7 节：拒绝也保留记录，它是申诉与惯犯识别的
// 证据），不能被一个"请先填模型"的提示挡在门外。
//
// 探测本身跑不了（probeErr）按"拿不到目录"处理：此时无法证明端点可用，
// 让贡献者显式声明比替他猜更安全。
func (s *ContributionService) Discover(ctx context.Context, provider, baseURL, apiKey string) (models []string, needClaimed bool, reason string, pre *PreProbe, err error) {
	if s.probe == nil {
		return nil, false, "", nil, errors.New("upstream: 探测能力未装配")
	}
	out, probeErr := s.probe.Probe(ctx, ProbeInput{Provider: provider, BaseURL: baseURL, APIKey: apiKey})
	pre = &PreProbe{Out: out, Err: probeErr}
	if probeErr != nil {
		return nil, true, "探测无法执行：" + redactText(probeErr.Error()), pre, nil
	}
	if !out.Reachable || !out.AuthOK {
		// 连不上 / 鉴权不过：填模型也救不了，交给正常链路落库留痕。
		return nil, false, "", pre, nil
	}
	if len(out.Models) == 0 {
		return nil, true, "端点可达且鉴权通过，但没有列出任何模型", pre, nil
	}
	return out.Models, false, "", pre, nil
}

// Submit 走完 L0 → 落库 → 探测 → 判定 的完整链路。
//
// 返回 (Outcome, nil) 表示「流程走完」（可能是入池，也可能是拒绝——看 Outcome.InPool）；
// 只有**程序/存储故障**才返回 error。这个区分让调用方能对两种失败给出不同的 UI：
// 拒绝要给可执行原因，故障要提示重试。
func (s *ContributionService) Submit(ctx context.Context, sub Submission) (Outcome, error) {
	if err := validateSubmission(sub); err != nil {
		return Outcome{}, err
	}
	kind := sub.Kind
	if kind == "" {
		kind = defaultKindFor(sub.BaseURL)
	}
	// L0 的远端地址校验复用 Target 的出站边界（§9.3）：vendor 必须 https，回环 http 放行。
	if err := ValidateTarget(Target{Kind: Kind(kind), BaseURL: sub.BaseURL}); err != nil {
		return Outcome{}, fmt.Errorf("upstream: 端点地址不合法：%w", err)
	}

	fingerprint := KeyFingerprint(sub.APIKey)
	// 幂等：同一账户 + 同一 provider + 同一 key 指纹 = 同一条贡献。
	// 为什么用指纹而不是「查 key 明文」：明文不进库，等价判定只能靠稳定派生值。
	if existing, ok, err := s.findOwned(ctx, sub.OwnerAccountID, sub.Provider, fingerprint); err != nil {
		return Outcome{}, err
	} else if ok {
		verify := existing.VerifyStatus
		if verify == "" {
			verify = store.VerifyUnverified
		}
		evidence := decodeEvidence(existing.VerifyDetail)
		return Outcome{
			CredentialID: existing.ID,
			Status:       store.CredentialStatus(existing.Status),
			VerifyStatus: verify,
			Evidence:     evidence,
			InPool:       existing.Status == string(store.CredActive),
			Reused:       true,
		}, nil
	}

	// discovered 只在自动获取路径上非空（见函数末尾的 Outcome 装配）。
	var discovered []string

	// 落库：upstreams 行（承载 base_url 与归属）+ 密文 + 凭据行。
	// 顺序为什么是 upstreams → secret → credential：credential.secret_ref 指向上一步的
	// secret id，而 secret 又必须有 upstreams 行（upstream_secrets.upstream_id 有外键）。
	upstreamName := contributeUpstreamName(sub.Provider, fingerprint)
	baseURL := strings.TrimRight(sub.BaseURL, "/")
	// enabled=false 先建：**未通过判定的凭据绝不能可调用**（01-flow 5.2「落库与可调用分开」）。
	// 判定入池后再把这一行打开——这是「入池」的唯一实现手段（见接口注释）。
	upstreamID, err := s.store.UpsertUpstream(ctx, kind, upstreamName, baseURL, &sub.OwnerAccountID, false)
	if err != nil {
		return Outcome{}, fmt.Errorf("upstream: 建贡献上游行失败：%w", err)
	}
	secretID, err := s.store.PutSecret(ctx, upstreamID, []byte(sub.APIKey))
	if err != nil {
		return Outcome{}, fmt.Errorf("upstream: 加密写入凭据失败：%w", err)
	}
	// secretRef 用密文行 id 的字符串形态（Assembly 的 secretSource 按它解密；
	// 02-credentials 6.4 的「引用形式」待定项在这里先钉成 id，换成字符串前缀时只改这两处）。
	secretRef := fmt.Sprintf("%s", secretID)

	credID, err := s.store.CreateCredential(ctx, sub.OwnerAccountID, sub.Provider, fingerprint,
		sanitizeLabel(sub.Label, sub.APIKey), secretRef)
	if err != nil {
		// 建行失败要把刚写的密文收回去，避免留下「无主的密钥材料」。
		// 删不掉也不阻塞主错误（密文行没有凭据指向，取不到 upstream 归属）。
		_ = s.store.DeleteSecret(context.WithoutCancel(ctx), secretID)
		return Outcome{}, fmt.Errorf("upstream: 建凭据行失败：%w", err)
	}

	// 探测：网络/鉴权/目录。HTTPProbe 的错误只表达「探测本身跑不了」（如非法 URL），
	// 连不上是 ProbeOutput.Reachable=false 而不是 error —— 它是判定输入，不是故障。
	//
	// 若调用方（自定义供应商的自动获取闸门）已经探过就复用那一次：同一次提交里
	// 同一把 key 与同一个端点，再打一次网络等于把用户等待的提交延迟翻倍。
	var out ProbeOutput
	var probeErr error
	// 只在同样未声明模型时复用：闸门那次探测的 ProbeInput 不含 ClaimedModels，
	// 拿它回答"声明过"的判定会算错 UsedModels。
	if sub.PreProbe != nil && len(sub.ClaimedModels) == 0 {
		out, probeErr = sub.PreProbe.Out, sub.PreProbe.Err
	} else {
		out, probeErr = s.probe.Probe(ctx, ProbeInput{
			Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey, ClaimedModels: sub.ClaimedModels,
		})
	}
	status, verify, evidence := judgeProbe(sub.ClaimedModels, out, probeErr)
	// 未声明模型时把探测到的目录回给前端（自动获取的产物，供贡献者确认）。
	// 判定口径不变：仍按未声明走 L0 放行 + 复检队列，由后续探测确定实际模型集。
	if len(sub.ClaimedModels) == 0 {
		discovered = append(discovered, out.Models...)
	}

	// 写回判定。两步分开写（status 与 verify_status 不同列），失败要能各自归因。
	if err := s.store.SetCredentialStatus(ctx, credID, status); err != nil {
		return Outcome{}, fmt.Errorf("upstream: 写凭据状态失败：%w", err)
	}
	if err := s.store.MarkVerified(ctx, credID, verify, evidence); err != nil {
		return Outcome{}, fmt.Errorf("upstream: 写鉴定结论失败：%w", err)
	}
	res := Outcome{
		CredentialID: credID, Status: status, VerifyStatus: verify,
		Evidence: evidence, InPool: status == store.CredActive,
		DiscoveredModels: discovered,
	}
	if !res.InPool {
		// 拒绝不是 error：凭据行保留（02-credentials 7 节「失败了也保留记录」——
		// 它是申诉、复核与惯犯识别的证据），上游行保持 enabled=false（不可调用）。
		return res, nil
	}
	// 入池：打开上游行。这一步失败必须报错——否则会出现「凭据说 active 但实际不可调用」
	// 的假状态，而调用方已经收到了成功应答。
	if err := s.store.UpdateUpstream(ctx, upstreamID, upstreamName, baseURL, true); err != nil {
		return Outcome{}, fmt.Errorf("upstream: 入池（启用上游行）失败：%w", err)
	}

	// 自动获取的目录就地落库（仅未声明模型 + 探到目录 + 入池）。为什么在
	// 这里而不是只在 Discover：目录就是这次探测拿到的，扔掉它、让凭据的
	// 模型卡等 10 分钟一轮的定时刷新，是白白让「提交成功」与「模型可见」
	// 之间出现一段空窗。落库走 UpsertRealModel——与刷新器同一条幂等仓储
	// 方法（real_models 的写域仍是它，贡献链路只是提前触发一次同形写入），
	// 之后的定时刷新会把它当作普通 auto 行继续维护。
	//
	// 失败只记证据字段不回滚：模型行的缺失是**可自愈的**（下轮刷新补上），
	// 而凭据已经入池成功——为一件可自愈的事回滚整笔贡献是自伤。
	if len(discovered) > 0 {
		if n, derr := s.store.SeedCatalog(ctx, upstreamID, discovered); derr != nil {
			res.Evidence += "\n" + evidenceJSON("L0", "模型目录落库失败（等待下一轮刷新补齐）："+redactText(derr.Error()))
		} else {
			res.SeededModels = n
		}
	}
	return res, nil
}

// contributeUpstreamName 是贡献上游行的名字（确定性，撤销时靠它反查 upstream id）。
// 为什么把指纹前缀编进名字：upstream_credentials 的读面不含 upstream id，
// 而 store 没有 by-credential 的 upstream 查询——确定性命名的反查是现有能力内最稳的做法。
func contributeUpstreamName(provider, fingerprint string) string {
	short := fingerprint
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s-contrib-%s", provider, short)
}

// ContributeUpstreamName 是 contributeUpstreamName 的导出面。
//
// 为什么导出：账户自助的「我的贡献」页要按凭据反查它的可调用状态（停止贡献与否），
// 反查依据与撤销是同一份命名事实——第二份实现早晚漂移（撤销后页面却显示
// 「贡献中」就是那种漂移的表现）。展示面只读不写，逻辑本体仍住在撤销链路里。
func ContributeUpstreamName(provider, fingerprint string) string {
	return contributeUpstreamName(provider, fingerprint)
}

// findOwned 在同一账户的凭据里找 external_id 命中项（幂等判定的读面）。
func (s *ContributionService) findOwned(ctx context.Context, owner string, provider, externalID string) (*store.Credential, bool, error) {
	rows, err := s.store.ListCredentials(ctx, store.CredentialFilter{
		OwnerAccountID: &owner, Provider: provider, Limit: 500,
	})
	if err != nil {
		return nil, false, fmt.Errorf("upstream: 查已有贡献失败：%w", err)
	}
	for i := range rows {
		if rows[i].ExternalID == externalID {
			return &rows[i], true, nil
		}
	}
	return nil, false, nil
}

// validateSubmission 是 L0 的本地部分（不花钱、不发请求）。
//
// 「廉价闸门」的意义（05-verification 4.2）：它必须便宜到「全部贡献必经」，
// 所以只挡结构性垃圾——空字段、明显不是 URL、明显不是 key、标签里含 key。
func validateSubmission(sub Submission) error {
	if sub.OwnerAccountID == "" {
		return errors.New("upstream: 贡献必须归属某个账户（匿名贡献也先建 contributor 账户）")
	}
	if strings.TrimSpace(sub.Provider) == "" {
		return errors.New("upstream: provider 不能为空")
	}
	if strings.TrimSpace(sub.BaseURL) == "" {
		return errors.New("upstream: base_url 不能为空")
	}
	u, err := url.Parse(sub.BaseURL)
	if err != nil || u.Host == "" {
		return errors.New("upstream: base_url 不是合法 URL")
	}
	key := strings.TrimSpace(sub.APIKey)
	if key == "" {
		return errors.New("upstream: key 不能为空")
	}
	if len(key) < 8 {
		return errors.New("upstream: key 太短（<8 字节），不可能是有效凭据")
	}
	// 标签不得包含 key 本身（02-credentials 3.2：用户会把 key 粘进标签）。
	if sub.Label != "" && strings.Contains(sub.Label, key) {
		return errors.New("upstream: 标签不得包含明文 key")
	}
	if len(sub.Label) > 200 {
		return errors.New("upstream: 标签过长（>200 字节）")
	}
	return nil
}

// defaultKindFor 按地址推断上游类型：回环/局域网自建 → selfhosted，其余 → vendor。
// 为什么允许 http 只给 selfhosted：validate.go 的出站边界就是这样定的
// （公网明文 http 会把凭据暴露在链路上）。
func defaultKindFor(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return string(KindVendor)
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") {
		return string(KindSelfHosted)
	}
	return string(KindVendor)
}

// sanitizeLabel 兜底清掉标签里的密钥形态（用户可能粘贴 sk-xxx）。
func sanitizeLabel(label, key string) string {
	l := strings.TrimSpace(label)
	if l == "" {
		l = "贡献凭据"
	}
	if key != "" {
		l = strings.ReplaceAll(l, key, "***")
	}
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}

// KeyFingerprint 是纯 Key 型的 external_id（稳定、不可逆、等值判定用）。
//
// 为什么不用明文或简单哈希前缀：撞 UNIQUE(provider, external_id) 是**跨账户**的
// 撞身份判定（02-credentials 4.1），拼一个可被枚举的前缀会让「我猜你用的 key」成为
// 攻击面。sha256 前 16 字节足够区分且不可逆。
func KeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "key_" + hex.EncodeToString(sum[:16])
}

// —— 判定 ——

// judgeProbe 把探测结果映射成 (status, verify_status, evidence)。
//
// 判定表（05-verification 5.1 + 02-credentials 5.2 的库内枚举；2026-10-25 起声称可多选）：
//
//	连不上                 → disabled + unverified（L0 不通过：端点都不通，谈不上「相符」）
//	401/403                → disabled + unverified（凭据无效；给可执行原因）
//	未声明模型             → active   + unverified（不判定「相符」，进复检队列）
//	声称全部命中           → active   + verified
//	声称部分命中（≥1 <N）  → active   + unverified（放行进复检，证据列命中/未命中明细）
//	有目录但声称全不命中   → disabled + suspect  （冒充/填错；给证据，不含探针细节）
//	无目录（404/非 JSON）  → active   + unverified （冷门模型默认放行，进复检队列）
//
// 为什么「部分命中」是 unverified 而不是 verified：命中一半说明 key 是真的、但
// 声称清单掺了水——给它盖章 verified 等于替贡献者背书了全部声称。也不是 suspect：
// suspect 的语义是「冒充的最强信号」，而「三个里中了一个」恰恰说明不是冒充。
// 落在 unverified（入池 + 复检队列）是两个误判代价里较小的一个。
//
// 为什么「无目录」放行而不是拒绝：05-verification 5.2 的整段论证——「库里没有」不等于
// 「可疑」，误杀成本高于误放，且有运行时健康与定时复检兜底。
func judgeProbe(claimed []string, out ProbeOutput, probeErr error) (store.CredentialStatus, store.VerifyStatus, string) {
	if probeErr != nil {
		return store.CredDisabled, store.VerifyUnverified,
			evidenceJSON("L0", "探测无法执行："+redactText(probeErr.Error()))
	}
	if !out.Reachable {
		return store.CredDisabled, store.VerifyUnverified,
			evidenceJSON("L0", "端点不可达："+redactText(firstNonEmpty(out.Detail, "连接失败")))
	}
	if !out.AuthOK {
		return store.CredDisabled, store.VerifyUnverified,
			evidenceJSON("L0", "上游拒绝鉴权（401/403）："+redactText(firstNonEmpty(out.Detail, "鉴权失败")))
	}
	// 归一：去空、去重，保持声明顺序（证据文本按它列明细）。
	seen := make(map[string]struct{}, len(claimed))
	claims := make([]string, 0, len(claimed))
	for _, c := range claimed {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		claims = append(claims, c)
	}
	if len(claims) == 0 {
		return store.CredActive, store.VerifyUnverified,
			evidenceJSON("L0", "可达且鉴权通过；未声明模型，记为未鉴定并进复检队列")
	}
	hit := make([]string, 0, len(claims))
	hitSet := make(map[string]struct{}, len(out.UsedModels))
	for _, m := range out.UsedModels {
		hitSet[m] = struct{}{}
	}
	missed := make([]string, 0, len(claims))
	for _, c := range claims {
		if _, ok := hitSet[c]; ok {
			hit = append(hit, c)
		} else {
			missed = append(missed, c)
		}
	}
	switch {
	case len(missed) == 0:
		return store.CredActive, store.VerifyVerified,
			evidenceJSON("L1", fmt.Sprintf("声称的 %d 个模型全部在目录中命中：%s", len(claims), strings.Join(claims, "、")))
	case len(hit) > 0:
		return store.CredActive, store.VerifyUnverified,
			evidenceJSON("L1", fmt.Sprintf("声称的 %d 个模型命中 %d 个（%s）；未命中：%s。已放行并进入复检队列",
				len(claims), len(hit), strings.Join(hit, "、"), strings.Join(missed, "、")))
	case len(out.Models) == 0:
		return store.CredActive, store.VerifyUnverified,
			evidenceJSON("L1", "端点未提供模型目录；声称的 "+strings.Join(claims, "、")+" 暂无法比对，已放行并进入复检队列")
	}
	// 有目录但声称全不命中：这是「冒充」的最强信号（05-verification 3 节的强信号之一）。
	// 证据只说「不在目录」与目录条数，**不列我们的探针细节**（5.3 的脱敏要求）。
	return store.CredDisabled, store.VerifySuspect,
		evidenceJSON("L1", fmt.Sprintf("声称的 %d 个模型均不在上游目录（目录共 %d 条）", len(claims), len(out.Models)))
}

// evidenceJSON 生成 verify_detail 的 JSON（见 0001_init.sql 的列注释：证据会演化，
// 用 JSON 才能不迁移地补信号）。
//
// detail 在这里**统一脱敏**，而不是指望每个调用方记得包 redactText：
// 证据会进库、会回显给贡献者、会被管理端看到——它是泄露面最大的一处，
// 而「以后新增的失败分支忘了脱敏」是必然会发生的。脱敏幂等，重复调用无副作用。
func evidenceJSON(stage, detail string) string {
	detail = redactText(detail)
	b, err := json.Marshal(map[string]any{"stage": stage, "detail": detail})
	if err != nil {
		return `{"stage":"` + stage + `","detail":"证据序列化失败"}`
	}
	return string(b)
}

// decodeEvidence 从 verify_detail JSON 里取出 detail（幂等路径回给贡献者的原因）。
func decodeEvidence(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var v struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "" // 坏 JSON 不当证据外发（可能是旧格式）
	}
	return v.Detail
}

// —— 撤销 ——

// RevokeActor 是撤销的发起人。
type RevokeActor struct {
	// AccountID 是操作者账户 UUID；IsAdmin=true 时不受归属限制。
	AccountID string
	IsAdmin   bool
}

// Revoke 撤销一条贡献凭据：status=disabled + 销毁密文。
//
// 为什么撤销要**删密文**而不是只改状态：
//   - 贡献者对「收回自己的账号」的承诺是可验证的（01-flow 7 节/02-credentials 5.3）；
//     只改状态意味着密钥材料还在库里，一旦状态被误改回 active 就能再次被调用；
//   - 留行不留钥：`requests.resolved_credential_id` 的归因链靠**行**，不靠密文。
//
// 为什么 status 用 disabled 而不是文档建议的 revoked：库里的 CHECK 枚举
// （pending/active/invalid/expired/disabled）没有 revoked（02-credentials 5.2 与 DDL
// 的已知张力，已在 repo_verification.go 记录）。disabled 是「不可调用且可留痕」的唯一落点。
func (s *ContributionService) Revoke(ctx context.Context, credentialID string, actor RevokeActor) error {
	cred, err := s.store.LookupCredential(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("upstream: 撤销前读凭据失败：%w", err)
	}
	if !actor.IsAdmin && actor.AccountID != cred.OwnerAccountID {
		return ErrContributionForbidden
	}

	// 撤销的三步都与「立即生效 + 不可再用」有关，缺一不可：
	//   1. 上游行 enabled=false —— 唯一能把凭据移出候选池的开关；
	//   2. 覆盖密文为随机墓碑 —— 密钥材料不可恢复（等价于销毁，且不需要 store 的删行接口）；
	//   3. credential.status=disabled —— 留痕：为什么这条不可用了（行不删，归因链要它）。
	up, found, err := s.findContributeUpstream(ctx, cred.Provider, cred.ExternalID)
	if err != nil {
		return err
	}
	if found {
		if err := s.store.UpdateUpstream(ctx, up.ID, up.Name, up.BaseURL, false); err != nil {
			return fmt.Errorf("upstream: 撤销（停用上游行）失败：%w", err)
		}
		// 随机墓碑：写入后原密文被替换，明文不再可解（GCM 认证会失败 → 调用方拿到 ErrSecretDecrypt，
		// 与「密文坏了」同一处置：告警 + 重新提交）。
		tomb := make([]byte, 32)
		if _, err := rand.Read(tomb); err != nil {
			return fmt.Errorf("upstream: 撤销（生成墓碑）失败：%w", err)
		}
		if _, err := s.store.PutSecret(ctx, up.ID, []byte("revoked:"+hex.EncodeToString(tomb))); err != nil {
			return fmt.Errorf("upstream: 撤销（销毁密钥材料）失败：%w", err)
		}
	}
	if err := s.store.SetCredentialStatus(ctx, credentialID, store.CredDisabled); err != nil {
		return fmt.Errorf("upstream: 撤销（写状态）失败：%w", err)
	}
	s.clearFailures(credentialID)
	return nil
}

// findContributeUpstream 按「确定性名字」反查贡献上游行。
// 找不到不是错误：历史数据/改名后的行可能对不上，此时仍执行状态撤销并保留痕迹。
func (s *ContributionService) findContributeUpstream(ctx context.Context, provider, externalID string) (store.Upstream, bool, error) {
	want := contributeUpstreamName(provider, externalID)
	ups, err := s.store.ListUpstreams(ctx)
	if err != nil {
		return store.Upstream{}, false, fmt.Errorf("upstream: 撤销前查上游行失败：%w", err)
	}
	for _, u := range ups {
		if u.Name == want {
			return u, true, nil
		}
	}
	return store.Upstream{}, false, nil
}

// —— 暂停 / 恢复 ——

// SetPaused 暂停（paused=true）或恢复一条贡献凭据，返回操作后的**可调用状态**。
//
// 落点是 upstreams.enabled —— 与撤销同一个开关，也是候选池的唯一入口
// （realModelCandidates 走 ListUpstreams 读 upstreams）。刻意不引入新状态机：
// 暂停不是凭据生命周期里的一个状态（status 仍回答「这条凭据本身能不能用」），
// 它只是「先别把它放进候选」的运行时开关，所以只动上游行、不碰密钥材料。
//
// 恢复有一道闸：只有 status=active 的凭据能被恢复。未通过鉴定的行 enabled=false
// 是**判定的结果**而不是暂停造成的，借「恢复」把它打开等于让一把没验过的 key
// 回到候选池。此时不报错（契约要求幂等 200），只是状态保持不可调用。
func (s *ContributionService) SetPaused(ctx context.Context, credentialID string, actor RevokeActor, paused bool) (bool, error) {
	cred, err := s.store.LookupCredential(ctx, credentialID)
	if err != nil {
		return false, fmt.Errorf("upstream: 切换凭据状态前读凭据失败：%w", err)
	}
	if !actor.IsAdmin && actor.AccountID != cred.OwnerAccountID {
		return false, ErrContributionForbidden
	}
	if !paused && cred.Status != string(store.CredActive) {
		return false, nil
	}
	up, found, err := s.findContributeUpstream(ctx, cred.Provider, cred.ExternalID)
	if err != nil {
		return false, err
	}
	if !found {
		// 历史行/改名对不上确定性名字：没有可切换的上游行，凭据本来就不可被调用。
		// 与 Revoke 同一口径——找不到不是错误，也不假装成功。
		return false, nil
	}
	if err := s.store.UpdateUpstream(ctx, up.ID, up.Name, up.BaseURL, !paused); err != nil {
		return false, fmt.Errorf("upstream: 切换上游行可调用性失败：%w", err)
	}
	if paused {
		// 暂停期间不记失败（凭据根本不进候选），顺手清掉历史计数：
		// 否则恢复后一次抖动就会碰上暂停前攒的哨头。
		s.clearFailures(credentialID)
	}
	return !paused, nil
}

// —— 连续失败自动禁用 ——

// RecordFailure 记一次调用失败；连续达到阈值时把凭据移出候选（disabled）。
//
// 返回值 (禁用了没有, 当前连续失败数, 错误)。
//
// 为什么计数器在内存而不是库：这是**运行时熔断状态**（01-catalog 7 节的口径），
// 不是账本——它每成功一次就清零，持久化只会带来「重启后还在冷却」的假状态。
// 重启丢失的代价是「最多再放 5 次失败进来」，而持久化带来的代价是状态机泄漏。
func (s *ContributionService) RecordFailure(ctx context.Context, credentialID string, reason string) (bool, int, error) {
	s.mu.Lock()
	s.fails[credentialID]++
	n := s.fails[credentialID]
	threshold := s.opt.FailThreshold
	s.lastAt[credentialID] = s.opt.Now()
	s.mu.Unlock()

	if n < threshold {
		return false, n, nil
	}
	// 达到阈值：写 disabled + suspect 证据（复检失败 → 移出候选，05-verification 7.3）。
	if err := s.store.SetCredentialStatus(ctx, credentialID, store.CredDisabled); err != nil {
		return false, n, fmt.Errorf("upstream: 连续失败后禁用凭据失败：%w", err)
	}
	ev := evidenceJSON("health",
		fmt.Sprintf("连续失败 %d 次（阈值 %d），已移出候选；最后一次原因：%s", n, threshold, redactText(reason)))
	if err := s.store.MarkVerified(ctx, credentialID, store.VerifySuspect, ev); err != nil {
		return true, n, fmt.Errorf("upstream: 连续失败后写证据失败：%w", err)
	}
	return true, n, nil
}

// RecordSuccess 清零连续失败计数（成功即复位，01-catalog 7 节的健康复位）。
func (s *ContributionService) RecordSuccess(credentialID string) {
	s.clearFailures(credentialID)
}

// Failures 返回当前连续失败次数（只读，测试与展示用）。
func (s *ContributionService) Failures(credentialID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fails[credentialID]
}

func (s *ContributionService) clearFailures(credentialID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fails, credentialID)
	delete(s.lastAt, credentialID)
}

// —— 默认探测实现 ——

// HTTPProbe 是 OpenAI 兼容端点的默认探测：GET {base}/models。
//
// 为什么是 /models：它是唯一**无 token 成本**的鉴权+目录双信号端点
// （05-verification 4.2 要求 L0 便宜到"全部必经"）。生成类探针要花钱，
// 属于 L1/L2，不在本文件。
type HTTPProbe struct {
	// Client 允许注入（测试用 httptest；生产用带超时的默认 client）。
	Client *http.Client
}

// modelsResponse 是 /v1/models 的最小读面（OpenAI 兼容；多余字段忽略）。
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// Probe 发一次 GET /models。
//
// 错误只表达「这次探测没法跑」（非法 URL、构造请求失败）；
// 网络失败与 4xx 都是**判定输入**（Reachable/AuthOK 为 false），不是 error——
// 它们决定凭据能不能进池，而不是服务是否故障。
func (p HTTPProbe) Probe(ctx context.Context, in ProbeInput) (ProbeOutput, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := strings.TrimRight(in.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProbeOutput{}, fmt.Errorf("构造探测请求失败：%w", err)
	}
	req.Header.Set("Accept", "application/json")
	// 鉴权头由探测自己带：明文 key 只在这一行出现（不进日志、不进错误）。
	req.Header.Set("Authorization", "Bearer "+in.APIKey)

	start := time.Now()
	resp, err := client.Do(req)
	out := ProbeOutput{Latency: time.Since(start)}
	if err != nil {
		out.Reachable = false
		out.Detail = redactText(err.Error())
		return out, nil
	}
	defer func() { _ = resp.Body.Close() }()
	out.Reachable = true

	switch {
	case resp.StatusCode == http.StatusOK:
		out.AuthOK = true
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		out.AuthOK = false
		out.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// 没有 /models：仍算鉴权通过吗？不能——404 可能来自未鉴权的通用兜底。
		// 保守起见按「未验证」处理：Reachable=true + AuthOK=true 但目录为空，
		// 判定落到 unverified（放行 + 复检），这是文档允许的冷门路径。
		out.AuthOK = true
		out.Detail = fmt.Sprintf("端点未提供 /models（HTTP %d）", resp.StatusCode)
		return out, nil
	default:
		// 5xx/429：上游暂时不可用，不是「凭据无效」也不是「冒充」。
		// 判成不可达会让一次上游抖动直接杀一条凭据——所以只记证据，交给调用方决定重试。
		out.AuthOK = resp.StatusCode < 400
		out.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out, nil
	}

	// 声称集合：目录命中按集合查（O(1)，与声称个数无关）。
	claimedSet := make(map[string]struct{}, len(in.ClaimedModels))
	for _, c := range in.ClaimedModels {
		if c = strings.TrimSpace(c); c != "" {
			claimedSet[c] = struct{}{}
		}
	}

	// 只读 1MiB：目录响应可能很大，超过就是异常（防内存放大）。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out.Detail = "读取目录响应失败：" + redactText(err.Error())
		return out, nil
	}
	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		// 非 JSON（有些端点 /models 返回 HTML 或纯文本）：按「无目录」处理，
		// 判定走 unverified 放行——不因为「格式不认识」拒绝一个可能是好的凭据。
		out.Detail = "目录响应不是 OpenAI 兼容 JSON"
		return out, nil
	}
	for _, m := range mr.Data {
		if m.ID == "" {
			continue
		}
		out.Models = append(out.Models, m.ID)
		if _, ok := claimedSet[m.ID]; ok {
			out.UsedModels = append(out.UsedModels, m.ID)
		}
	}
	return out, nil
}

// —— 小工具 ——

// redactText 脱敏错误文本 —— 直接转调 dshprovider.RedactLine。
//
// 为什么不再在本地维护一份正则：同一套「什么算敏感」的规则原先在
// dshprovider/client.go 与这里各写一份、内容逐字相同（加上 sidecar/redact.mjs 共三处）。
// 两处 Go 副本迟早漂移，而漂移的表现是「一边遮、一边漏」且没人发现。
// 现在 Go 侧只有 dshprovider 那一份（该包是 upstream 的子包，方向本就合法：
// relay.go 早已 import 它），本函数只是语义清晰的转调点。
func redactText(s string) string { return dshprovider.RedactLine(s) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
