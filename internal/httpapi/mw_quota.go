package httpapi

import (
	"context"
	"net/http"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/meter"
)

// mw_quota.go：L1 链第 7 步（配额）中间件（docs/architecture/01-overview.md）。
//
// 与 mw_rate 的分工（06-runtime §2.3 / 05-keys §4）：
//   - 限速 = **速率**闸（次数/分钟、在途数）——进程内计数，meter.Limiter；
//   - 配额 = **周期总量**闸（日/月 request/token/credit，账户行 + 组池）——
//     落库计数，store 的条件 UPDATE（判定即扣减）。
//
// 本中间件做**预检**（TryDebit 判定即扣减，请求数维度 cost=1；token 维度
// 用估计值）。token 实际用量在终态结算（RecordRequest 的 Settlement）多退
// 少不补：预检估计值偏大导致的少量少放是可接受的（比超发好），
// 预检偏小的部分在结算时**不再追扣**——总量上界由 daily 汇总与对账兜底
// （05-storage §7.4"本周期总量可能超上限"的既定口径）。
//
// 装配归 Lead：Handler() 里在 mwRate 之后挂 s.mwQuota(adapter, modelIDOf)。

// ModelIDSource 把"请求路径里的模型名"解析成 store 的抽象模型 id。
//
// 为什么不在本中间件查模型表：模型解析是 L1 的第 8 步（独立中间件，
// 404 model_not_found 的归属面），配额只需要它的**结果**。装配层把
// 解析器接进来；配额拿不到模型 id 时按合计行扣（mid=0），
// 语义是"模型未知先记总账"——比"拒掉请求"温和，比"跳过配额"正确。
type ModelIDSource func(ctx context.Context, modelName string) (int64, bool)

// GroupIDsSource 给出账户所属组 id（池扣减的遍历范围）。
type GroupIDsSource func(ctx context.Context, accountID string) ([]int64, bool)

// TokenBudget 是 token 维度的预检估计值（配置的每请求预算或上限推导）。
type TokenBudget int64

// mwQuota 是配额中间件：合计行 → 按模型行 → 组池，判定即扣减。
//
// 失败响应由 adapter 翻好的 auth.Error 出（quota_exceeded / pool_exhausted，
// 带 quota{unit,period,model} 与 Retry-After）；本层只搬运输出。
// 全局熔断不在这里（mwRate 的 CheckGlobalFuse 已拦；两个闸都是
// "请求开始前"的预检，重复扣减无意义）。
func (s *Server) mwQuota(debiter QuotaDebiter, modelIDOf ModelIDSource, groupIDsOf GroupIDsSource, tokenBudget int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := KeyPrincipalFromContext(r.Context())
			if p == nil {
				// 公共路径豁免（同 mw_rate 的说明）：无 Key 主体即无配额
				// 主体——额度挂在账户上，登录/向导路径没有"账户在烧额度"
				// 的语义。
				if isNonKeyPath(r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
				writeError(w, http.StatusInternalServerError,
					string(auth.CodeInternal), "配额中间件缺少鉴权主体（装配顺序错误）", nil)
				return
			}

			// 模型 id：装配层解析不出（未知模型）时走合计行（mid=0）。
			// 未知模型的 404 归模型解析中间件；这里不抢那个码。
			var mid int64
			if modelIDOf != nil {
				if id, ok := modelIDOf(r.Context(), pathModel(r.URL.Path)); ok {
					mid = id
				}
			}
			var gids []int64
			if groupIDsOf != nil {
				if got, ok := groupIDsOf(r.Context(), p.AccountID); ok {
					gids = got
				}
			}

			cost := meter.UsageCost{Request: 1, Token: tokenBudget}
			err := debiter.TryDebit(r.Context(), meter.DebitContext{
				AccountID:       p.AccountID,
				AbstractModelID: mid,
				GroupIDs:        gids,
				Cost:            cost,
			})
			if err != nil {
				s.writeAuthErr(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// QuotaDebiter 是配额扣减的依赖面（meter.StoreAdapter 实现）。
//
// 为什么又是接口而不是直接依赖 *meter.StoreAdapter：测试需要假体；
// 且 adapter 的构造需要 store 仓储，装配归 Lead——中间件面依赖窄接口，
// Handler() 里用真 adapter，测试里用假体，两边不耦合。
type QuotaDebiter interface {
	TryDebit(ctx context.Context, dc meter.DebitContext) error
}

// pathModel 从 /v1/... 路径里粗提模型段（仅配额预检的降级用途；
// 精确解析归协议/模型中间件，404 判定不在这）。
func pathModel(p string) string {
	// 常见形态：/v1/chat/completions 或 /v1/messages/{model}。
	// 配额只把它交给 ModelIDSource 去解析；解析失败按合计行扣。
	if len(p) > 4 && p[:4] == "/v1/" {
		rest := p[4:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == '/' {
				return rest[i+1:]
			}
		}
	}
	return ""
}
