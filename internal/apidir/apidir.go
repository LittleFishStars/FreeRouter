// Package apidir 是「API 供应商」入口的内置目录（贡献入口三分类之一，
// 见 docs/ui/05-contributor.md）。
//
// # 数据来源
//
// catalog.json 由 Tingly Box 的 provider catalog（internal/catalog/providers.json，
// catalog.json 由 Tingly Box 的 provider catalog（internal/catalog/providers.json，
// 2026-10-24 首次提取，2026-10-25 修订回加 modelscope/opencode）整理而来，
// **只保留事实字段**：端点、模型 id、官网与文档链接。
// 它不是我们的判定逻辑，也不是「哪些供应商能用」的结论——只是「这家叫什么、
// 端点在哪、文档在哪」的抄录。任何需要推断的东西（可用性、地区合规、费率）
// 都不在这里。
//
// # 为什么只收 base_url_openai
//
// 本目录服务的唯一交互是「用户粘贴一把 Key，我们按 OpenAI 兼容协议调用」。
// 所以只收同时具备 base_url_openai 的条目：只有 anthropic 原生端点的供应商
// （例如 anthropic-com）进来也无法用同一条链路驱动——把端点放进目录等于
// 承诺「粘贴 Key 就能用」，承诺不了的不收。全表 protocol 恒为 "openai"
// 正是这条边界的体现，而不是默认值。
//
// # 为什么端点清单里会有 anthropic
//
// endpoints 回答的是「这家供应商自己开放了哪些原生协议的端点」这一**事实**，
// 而不是「我们支持哪些协议」。本站出站链路当前只实现 OpenAI 兼容形态
// （internal/app/upstream_shim.go 的 adapterForKind 恒取 openai.Adapter()），
// 所以 anthropic 端点进来是事实标注：管理面用它画徽标，语义是「供应商具备、
// 本站未接入」，不是「可以用 A 调它」。
//
// 为什么不给全表都补一份 anthropic：没有原生 anthropic 端点的供应商补一条，
// 等于替它承诺它没有的东西；缺键才是「没有这个事实」的正确表达。
//
// endpoints.openai 与 base_url 值恒相等、由测试钉住，重复一次是有意的：
// base_url 的语义是「调用入口」（relay 形态下它甚至不是 HTTP 端点），而端点
// 清单要的是「协议 → 地址」这一份自洽的事实，不依赖 base_url 怎么解释。
//
// # 为什么排除 localhost / {占位符}
//
//   - localhost 与 127.0.0.1：那是用户自己的服务，属于「自定义供应商」入口
//     （kind=custom，用户自填 base_url），内置目录只描述公网端点；
//   - 带 {占位符} 的模板 URL（如 https://{region}.example.com/v1）：它不是可
//     直接使用的端点，收进来会让用户在表单里看到一个永远填不对的地址。
//
// # modelscope / opencode 为什么在目录里
//
// 2026-10-25 用户澄清：这两家移除的只是**反代（OAuth 登录）形态**——上游
// 没有可脱离 IDE 的登录面，反代入口恒不可用；但它们的 OpenAI 兼容端点
// （modelscope-cn / opencode-ai / opencode-go）贴 Key 即用，正是本目录服务的
// 交互。把「形态不适用」误伤成「供应商整体移除」，用户就没法贡献这两家的
// Key 了。反代入口的移除在 sidecar（registry/index/hub 的表），与本目录无关。
//
// # 边界
//
// 这里是**事实数据**，不承载逻辑：排序由数据文件自己决定（JSON 已按 id 升序），
// 本包不做重排、不做去重、不做可用性判断、不联网校验端点。改数据就改
// catalog.json（由 Lead 统一维护），改逻辑才改本文件。
package apidir

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// catalogJSON 是目录数据。go:embed 只能嵌入本包目录下的文件，因此数据文件
// 与实现同目录——这也让「数据与读数据的方式」在评审时出现在同一处。
//
//go:embed catalog.json
var catalogJSON []byte

// Provider 是目录里的一家供应商。
//
// 字段与 catalog.json 逐一对应；JSON 侧键是 snake_case（对外接口原样转发，
// 前端直接消费），结构体侧用 Go 命名——两者只在下面的 tag 里对齐一次。
type Provider struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	Protocol     string `json:"protocol"`
	VendorFamily string `json:"vendor_family"`
	Region       string `json:"region"`
	Website      string `json:"website"`
	APIDoc       string `json:"api_doc"`
	// Endpoints 是按协议划分的原生端点清单（见包注释「为什么端点清单里会有
	// anthropic」）。它与 BaseURL 是两条平行的事实，不是它的派生：BaseURL 是
	// 「调用入口」，Endpoints 是「有哪些协议的端点」。
	Endpoints Endpoints `json:"endpoints"`
	Models    []Model   `json:"models"`
}

// Model 是目录里的一个模型 id。
//
// 只收 id：本目录不承担模型元数据（上下文窗口、模态、费率）的权威性，
// 那些随供应商自己变化，写进内置表就会过期。
type Model struct {
	ID string `json:"id"`
}

// Endpoints 是一家供应商按协议划分的原生端点清单（键只允许 openai / anthropic，
// 见包注释「为什么端点清单里会有 anthropic」）。
type Endpoints struct {
	OpenAI string `json:"openai"`
	// Anthropic 缺键（omitempty）而不是空串：空串会让「有 anthropic 端点但地址
	// 是空的」与「没有 anthropic 端点」变成同一个值，而这两件事的运维动作不同。
	Anthropic string `json:"anthropic,omitempty"`
}

// Protocols 返回该供应商具备原生端点的协议，顺序恒 openai 在前。
//
// 为什么是方法而不是让调用方自己看 Endpoints：协议列表的顺序本身就是对外契约
// （管理面徽标、契约 §1.1 的排序），散在各个调用方里拼装，早晚有人写出
// ["anthropic","openai"] 这种顺序；判断「有没有 anthropic」也只有这一处口径。
// 返回新切片，调用方改动不会污染目录数据（与 List 返回深拷贝同一条理由）。
func (p Provider) Protocols() []string {
	if p.Endpoints.Anthropic == "" {
		return []string{"openai"}
	}
	return []string{"openai", "anthropic"}
}

// catalog 是 catalog.json 的读入形状。
type catalog struct {
	Source    string     `json:"_source"`
	Note      string     `json:"_note"`
	Count     int        `json:"count"`
	Providers []Provider `json:"providers"`
}

var (
	// load 在包初始化时跑一次：数据是编译期常量，重复解析没有意义，
	// 而且运行期每次请求都解析会把这个纯读路径变成潜在的错误源。
	load = sync.OnceValue(func() []Provider {
		var c catalog
		if err := json.Unmarshal(catalogJSON, &c); err != nil {
			// embed 常量不可能解析失败；失败即数据文件或结构体 tag 写错，
			// 属于编程错误——崩给开发者看，而不是让接口返回半张空目录。
			panic("apidir: catalog.json 解析失败（数据或结构体 tag 写错）：" + err.Error())
		}
		if len(c.Providers) == 0 {
			panic("apidir: catalog.json 的 providers 为空——空目录会让「API 供应商」入口整个消失")
		}
		if c.Count != len(c.Providers) {
			// count 是给人工核对用的冗余字段；两者对不上说明数据被手改坏过，
			// 此时信谁都是猜，直接拒绝服务。
			panic(fmt.Sprintf("apidir: catalog.json 的 count=%d 与 providers 条数 %d 不一致",
				c.Count, len(c.Providers)))
		}
		return c.Providers
	})
)

// List 返回目录全部条目（顺序即 catalog.json 的顺序，已按 id 升序）。
//
// 返回的是深拷贝：调用方（HTTP 层）可能往里塞东西做展示，不该影响下一次请求；
// Models 是切片，只拷结构体会仍共享底层数组，所以连内容一起拷。
func List() []Provider {
	src := load()
	out := make([]Provider, len(src))
	for i, p := range src {
		models := make([]Model, len(p.Models))
		copy(models, p.Models)
		p.Models = models
		out[i] = p
	}
	return out
}

// Get 按 id 取一家供应商。找不到返回 false（调用方据此给 400 invalid_request）。
func Get(id string) (Provider, bool) {
	for _, p := range load() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}
