package dshprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 方法名常量。这张表**就是** sidecar 契约的方法集合（docs/architecture/04-sidecar.md 2.2），
// 只加不改：sidecar 可以独立于主进程升级，旧 sidecar 配新主进程必须能工作。
//
// ⚠️ 红线①：这里**没有** credentials.set / credentials.unset / credentials.refresh，
// 而且永远不该出现。凭据续期只能由 DSH 完成，我们只读最新值；读到已过期的
// `expiresAt` 就把它标 expired 并从候选里剔除，同时提示贡献者去 DSH 重新登录。
// 一旦这里多出任何一个写方法，某天就会有人用它「顺手把刷新后的 token 写回去」，
// 那次写入会与 DSH 自己的续期竞争（refresh token rotation 互踢）。
const (
	methodHealth       = "health"
	methodProviders    = "providers.list"
	methodModels       = "models.list"
	methodCredResolve  = "credentials.resolve"
	methodCredDescribe = "credentials.describe"
	methodLoginStart   = "login.start"
	methodLoginPoll    = "login.poll"
	methodLoginSubmit  = "login.submit"
	methodLoginCancel  = "login.cancel"
	methodCreditsQuery = "credits.query"
	// methodShutdown 是关闭顺序里的那条特殊帧（04-sidecar.md 5.4 步骤 4）。
	// 它不是普通业务方法，故不在 2.2 的清单里；sidecar 收到后应清理会话并自行退出。
	methodShutdown = "shutdown"
)

// SupportedMethods 返回本客户端**能够发起**的方法名。
//
// 它存在的意义是让「红线」可被外部程序断言：控制台可以拿它与 sidecar 的
// health 做差集，测试可以断言里面没有任何写凭据方法。
// 返回的是副本——调用方改不动契约。
func SupportedMethods() []string {
	return []string{
		methodHealth, methodProviders, methodModels,
		methodCredResolve, methodCredDescribe,
		methodLoginStart, methodLoginPoll, methodLoginSubmit, methodLoginCancel,
		methodCreditsQuery,
	}
}

// IsForbiddenMethod 判断某个方法名是否触碰红线。
//
// 为什么把它做成**导出的、可测试的**函数：红线靠「大家都记得不要写」是守不住的，
// 靠「违规调用会拿到一个专用错误码」才守得住（04-sidecar.md 2.4 的设计说明）。
// 上层（错误响应映射、控制台的自查页）可以直接用这个判定，
// 从而任何一次违规调用都会变成 E_CREDENTIAL_READONLY / E_STATE_FORBIDDEN 告警。
func IsForbiddenMethod(method string) (code string, forbidden bool) {
	switch method {
	case "credentials.set", "credentials.unset", "credentials.refresh",
		"credentials.write", "credentials.delete":
		return CodeCredentialReadonly, true
	case "state.read", "state.write", "state.update":
		// 红线②：state.json 是 DSH 的运行状态（账号列表、disabledModels、aggregateOrder）。
		// 与它共享状态就是承认耦合，两个进程对同一个 JSON 做读改写会互相覆盖。
		return CodeStateForbidden, true
	}
	return "", false
}

// —— health ——

// Health 对应 health() 的返回（04-sidecar.md 2.2）。
// sidecar 会自行增加字段；本结构只声明我们**承诺使用**的那些，
// 未知字段由 encoding/json 默认忽略（契约 §3：未知返回字段必须被忽略）。
type Health struct {
	Version string `json:"version"`
	// Providers 是已装载的 provider id。主进程据此判断「能不能用」，而不是靠试调用。
	Providers []string     `json:"providers"`
	Deps      []Dependency `json:"deps"`
	// StateReadonly 应当恒为 true：sidecar 自己也只能只读 state.json（红线②）。
	// 这里不设 `true` 默认值，而是照实读——若 sidecar 报 false，那是它坏了，要能被看见。
	StateReadonly bool `json:"stateReadonly"`
	// NodeVersion 是 sidecar 自报的 node 版本，仅排障用（可选字段）。
	NodeVersion string `json:"nodeVersion,omitempty"`
}

// Health 做一次健康探测。调用方通常是 Start（启动握手）与运行期的探活循环。
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var h Health
	if err := c.do(ctx, methodHealth, nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// —— 目录 ——

// ProviderInfo 对应 providers.list() 的条目。
type ProviderInfo struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"displayName"`
	// LoginKind 取值：browser_callback / device_code / qr_scan / sms / plain_credential。
	LoginKind string `json:"loginKind"`
	// LoginImplemented 为 false 时 login.start 返回 E_NOT_IMPLEMENTED，
	// 控制台应隐藏入口而不是给用户一个必失败的按钮。
	LoginImplemented bool `json:"loginImplemented"`
	// HasAccount 只表示「存在元数据」，**不含凭据值**（红线①）。
	HasAccount bool `json:"hasAccount"`
}

// ProvidersList 拉 provider 目录。
func (c *Client) ProvidersList(ctx context.Context) ([]ProviderInfo, error) {
	var out []ProviderInfo
	if err := c.do(ctx, methodProviders, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ModelInfo 对应 models.list({provider}) 的条目。
//
// 渠道语义（docs/models/01-catalog.md §2.3）：hub 的 `owned_by` 是**权威来源**
// ——限定名里的渠道段取自它。本结构保留该字段但**不做任何派生填充**：
// wire 上 hub 不给就保持空串（当前实测 hub 的 listModels 确实不给 owned_by），
// 调用方需要渠道时走 ModelOwnedBy()（它把「权威值」与「派生值」显式区分开）。
type ModelInfo struct {
	UpstreamModelID string `json:"upstreamModelId"`
	DisplayName     string `json:"displayName"`
	// ContextWindow 可空：hub 的 listModels() 实测不含该字段（PLAN 11.1 第 2 条）。
	ContextWindow *int     `json:"contextWindow"`
	Modalities    []string `json:"modalities"`
	Efforts       []string `json:"efforts"`
	// OwnedBy 是上游报告的权威渠道标识（OpenAI 兼容 `owned_by` 字段）。
	// 为空 = hub 未给（当前实测如此），**不代表渠道为空**——请调 ModelOwnedBy()
	// 拿「权威值或派生值」的渠道串，并读它返回的 Authoritative/Derived 标记。
	OwnedBy string `json:"ownedBy"`
}

// ModelOwnedByResult 是渠道解析结果（权威值与派生值的显式区分）。
//
// 为什么带标记而不是只回 string：派生值（从 `渠道/模型` 前缀切出）与权威值
// （hub 的 owned_by）在**可信度**上完全不同——派生只与 id 前缀一致，挡不住
// 「冒用别人的渠道前缀」。调用方（落 excluded_from_candidates、按渠道禁用、
// 归因展示）做决策时必须知道手上是哪种，否则派生值会被当成权威用。
type ModelOwnedByResult struct {
	// Channel 是渠道标识；派生值取自 id 前缀；裸 id 无前缀且 hub 未给时为空串。
	Channel string
	// Authoritative=true 表示 Channel 来自 hub 的 owned_by
	//（权威值，01-catalog.md §2.3 的映射来源）。
	Authoritative bool
	// Derived=true 表示 Channel 从 upstreamModelId 的 `渠道/模型` 前缀**派生**——
	// 它只证明「id 前缀自称该渠道」，不是上游的权威声明。
	Derived bool
}

// ModelOwnedBy 解析一条目录条目的渠道（任务要求：hub 给 owned_by 则直用，
// 否则从 `渠道/模型` 前缀派生，并在类型与注释里写明「这是派生值不是权威值」）。
//
// 为什么不只提供 string 返回：调用方若只拿到字符串就分不出权威/派生
// （01-catalog.md §2.3 明确 owned_by 是权威来源）；把标记做成返回类型的一部分，
// 「拿派生值当权威用」就从口头约定变成编译期可见的字段。
//
// 派生规则：upstreamModelId 形如 `渠道/模型`（如 `deepseek-official/deepseek-v4.1`、
// `aggregate/auto`）时取第一个斜杠前段为渠道；无斜杠的裸 id（如 `gpt-4o`）
// 返回空串——**不 panic、不猜测**（猜一个渠道名比留空更危险：它会被当成权威值
// 参与 excluded_from_candidates 与渠道禁用决策）。
//
// 权威值也不做规范化：hub 给什么就是什么（01-catalog.md「原样存，不改写」，
// 渠道段同理；大小写/别名对齐不替上游做）。
func (m ModelInfo) ModelOwnedBy() ModelOwnedByResult {
	if ch := strings.TrimSpace(m.OwnedBy); ch != "" {
		return ModelOwnedByResult{Channel: ch, Authoritative: true}
	}
	// 派生路径：只切第一个斜杠前的段（provider/渠道名不含斜杠——与
	// upstream/relay.go 的 splitProviderModel 同一约定）。
	id := m.UpstreamModelID
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			if i == 0 {
				return ModelOwnedByResult{} // 前导斜杠 = 畸形 id，不猜
			}
			return ModelOwnedByResult{Channel: id[:i], Derived: true}
		}
	}
	return ModelOwnedByResult{} // 裸 id + 无权威值 = 渠道不可知
}

type modelsParams struct {
	Provider string `json:"provider"`
}

// ModelsList 拉某个 provider 的模型目录。
// provider 为空时本地就返回 E_BAD_REQUEST：让空参数变成一次**本地**失败，
// 而不是一次跨进程往返后才失败（登录页每次渲染都会调它）。
func (c *Client) ModelsList(ctx context.Context, provider string) ([]ModelInfo, error) {
	if provider == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodModels, Message: "provider 不能为空"}
	}
	var out []ModelInfo
	if err := c.do(ctx, methodModels, modelsParams{Provider: provider}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// —— 凭据（只读） ——

type credentialParams struct {
	CredentialRef string `json:"credentialRef"`
}

// ResolvedCredential 对应 credentials.resolve 的返回值。
type ResolvedCredential struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// CredentialsResolve 只读地取一次凭据值。
//
// 语义对齐 DSH：**空值视为不存在**，故接口返回 (*ResolvedCredential, error)，
// 而不存在时 result 为 JSON null —— 解到 nil 指针，调用方据此标 invalid。
//
// 本方法**没有**对应的 set/unset/refresh（红线①，见文件头的说明）。
func (c *Client) CredentialsResolve(ctx context.Context, credentialRef string) (*ResolvedCredential, error) {
	if credentialRef == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodCredResolve, Message: "credentialRef 不能为空"}
	}
	var raw json.RawMessage
	if err := c.do(ctx, methodCredResolve, credentialParams{CredentialRef: credentialRef}, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out ResolvedCredential
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeProtocol, Method: methodCredResolve,
			Message: "credentials.resolve 的返回不是预期形状：" + err.Error(), Cause: err}
	}
	if out.Value == "" {
		// 空值视为不存在（对齐 DSH），不要把它当「值为空字符串」流传下去。
		return nil, nil
	}
	return &out, nil
}

// CredentialInfo 对应 credentials.describe 的返回。
type CredentialInfo struct {
	Provider      string `json:"provider"`
	ExternalLabel string `json:"externalLabel"`
	// ExpiresAt 是 RFC3339 时间串，可能为空（不可知或不过期）。
	ExpiresAt string `json:"expiresAt"`
	// Refreshable 表示**DSH 能不能续期**，不是「我们能不能续期」。
	// 我们永远不能——续期只能由 DSH 完成（红线①）。
	Refreshable bool `json:"refreshable"`
}

// ExpiredAt 把 ExpiresAt 解析出来，并判断「相对 now 是否已过期」。
// 为什么要这个辅助函数：读到已过期的凭据不是错误，是**一种状态**——
// 上层要标 expired 并剔除候选，而不是把它当异常。
func (ci *CredentialInfo) ExpiredAt(now time.Time) (expired bool, at time.Time, known bool) {
	if ci == nil || ci.ExpiresAt == "" {
		return false, time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, ci.ExpiresAt)
	if err != nil {
		return false, time.Time{}, false
	}
	return now.After(t), t, true
}

// CredentialsDescribe 只读地取凭据元数据（不含值）。
func (c *Client) CredentialsDescribe(ctx context.Context, credentialRef string) (*CredentialInfo, error) {
	if credentialRef == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodCredDescribe, Message: "credentialRef 不能为空"}
	}
	var out CredentialInfo
	if err := c.do(ctx, methodCredDescribe, credentialParams{CredentialRef: credentialRef}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// —— 登录流 ——

// LoginSession 对应 login.start 的返回。
type LoginSession struct {
	SessionID       string `json:"sessionId"`
	Kind            string `json:"kind"`
	Instructions    string `json:"instructions"`    // 给用户看的一句话（中文）
	VerificationURL string `json:"verificationUrl"` // 设备码：用户要访问的地址
	UserCode        string `json:"userCode"`        // 设备码：用户要输入的码
	QRData          string `json:"qrData"`          // 扫码：二维码内容（由前端渲染）
	// CallbackPort 是浏览器回调的本地监听端口；0/NULL 表示不是回调类。
	CallbackPort *int   `json:"callbackPort"`
	ExpiresAt    string `json:"expiresAt"`
}

type loginStartParams struct {
	Provider string `json:"provider"`
}

// LoginStart 发起一次登录会话。
// 超时短（10s）是刻意的：慢的一方是用户而不是我们（04-sidecar.md 5.3）。
func (c *Client) LoginStart(ctx context.Context, provider string) (*LoginSession, error) {
	if provider == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodLoginStart, Message: "provider 不能为空"}
	}
	var out LoginSession
	if err := c.do(ctx, methodLoginStart, loginStartParams{Provider: provider}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// 登录状态取值（契约里是字符串枚举，这里给常量避免拼写漂移）。
const (
	LoginStatusPending       = "pending"
	LoginStatusAwaitingInput = "awaiting_input"
	LoginStatusOK            = "ok"
	LoginStatusFailed        = "failed"
	LoginStatusExpired       = "expired"
)

// LoginState 对应 login.poll 的返回。
type LoginState struct {
	Status string `json:"status"`
	// CredentialRef 在 status=ok 时给出：指向**已存在于 DSH 凭据存储**的那条引用。
	// 注意它是引用而不是值——我们从不把凭据值搬进 FreeRouter 的库外的地方，
	// 需要落库时由贡献者在控制台确认后写入自己的 upstream_secrets（AES-GCM）。
	CredentialRef string `json:"credentialRef"`
	SecretRef     string `json:"secretRef"`
	Detail        string `json:"detail"` // 失败原因（面向用户的中文）
}

// LoginPoll 轮询一次登录会话。
func (c *Client) LoginPoll(ctx context.Context, sessionID string) (*LoginState, error) {
	if sessionID == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodLoginPoll, Message: "sessionId 不能为空"}
	}
	var out LoginState
	if err := c.do(ctx, methodLoginPoll, sessionIDParams{SessionID: sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type sessionIDParams struct {
	SessionID string `json:"sessionId"`
}

type loginSubmitParams struct {
	SessionID string `json:"sessionId"`
	Input     string `json:"input"`
}

// LoginSubmit 提交短信/验证码类的输入。
// 失败时会拿到 E_SUBMIT_REJECTED（可重试、限次），让用户重输而不是重开会话。
func (c *Client) LoginSubmit(ctx context.Context, sessionID, input string) (string, error) {
	if sessionID == "" {
		return "", &Error{Code: CodeBadRequest, Method: methodLoginSubmit, Message: "sessionId 不能为空"}
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, methodLoginSubmit, loginSubmitParams{SessionID: sessionID, Input: input}, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// LoginCancel 取消一次登录会话。
// 不关心结果：会话可能已经过期或不存在，报错没有可执行的后续动作。
func (c *Client) LoginCancel(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return &Error{Code: CodeBadRequest, Method: methodLoginCancel, Message: "sessionId 不能为空"}
	}
	var ignore json.RawMessage
	return c.do(ctx, methodLoginCancel, sessionIDParams{SessionID: sessionID}, &ignore)
}

// —— 余额 ——

type creditsParams struct {
	Provider      string `json:"provider"`
	CredentialRef string `json:"credentialRef"`
}

// Credits 对应 credits.query 的返回。字段全用指针：上游可能只给其中几项，
// 用 0 当「没有」会让「余额真的是 0」与「上游没给」无法区分。
type Credits struct {
	Balance    *float64        `json:"balance"`
	QuotaUsed  *float64        `json:"quotaUsed"`
	QuotaTotal *float64        `json:"quotaTotal"`
	Unit       string          `json:"unit"`
	Raw        json.RawMessage `json:"raw"` // 上游原始响应的子集，仅供排障
}

// CreditsQuery 查询某个 provider 的余额。
// 它可能走公网，故超时给到 15s（04-sidecar.md 5.3）。
func (c *Client) CreditsQuery(ctx context.Context, provider, credentialRef string) (*Credits, error) {
	if provider == "" {
		return nil, &Error{Code: CodeBadRequest, Method: methodCreditsQuery, Message: "provider 不能为空"}
	}
	var out Credits
	if err := c.do(ctx, methodCreditsQuery, creditsParams{Provider: provider, CredentialRef: credentialRef}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do 是所有类型化方法的共同通道：发请求、解 JSON、把解码失败归一成 E_PROTOCOL。
//
// 为什么解码失败要单独归一：sidecar 返回一个形状不对的对象时，若让它变成
// encoding/json 的原始错误，上层就只能看到 "cannot unmarshal ..."，
// 分不清是通道坏了还是对方版本不对——而这正是 04-sidecar.md §3
// 「旧 sidecar 配新主进程必须能工作」要覆盖的那类故障。
func (c *Client) do(ctx context.Context, method string, params any, out any) error {
	raw, err := c.Call(ctx, method, params)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	// json.RawMessage 目标：直接交出原始字节，不做二次解码（resolve 的 null 语义要保留）。
	if rm, ok := out.(*json.RawMessage); ok {
		*rm = append((*rm)[:0], raw...)
		return nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Code: CodeProtocol, Method: method,
			Message: fmt.Sprintf("%s 的返回不是预期形状：%v", method, err), Cause: err}
	}
	return nil
}
