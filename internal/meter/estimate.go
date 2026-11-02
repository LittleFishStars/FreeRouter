package meter

// estimate.go —— 上游不报 usage 时的 token 估算（Source = estimated）。
//
// 背景（2026-11-02）：仪表盘的 token 图一直是平的，因为多数上游 adapter 不向
// 上游请求 usage（只有 6/17 家会带 stream_options.include_usage），
// 于是 requests.in_tokens 恒为 NULL。修完「adapter 报了我们就别丢」之后，
// 剩下的缺口是「adapter 压根不报」——这一层只能由我们估。
//
// 设计约束（02-ir.md §9，逐条落地）：
//   - 估算值**必须**标 Source=estimated，绝不与上游上报混在同一字段，
//     否则"对不对得上账"的判断立刻失效；
//   - 必须有**独立分词器**，不能按字符数拍脑袋（PLAN 4.4 把
//     "独立 tokenizer 估算 vs 上报用量"列为鉴定信号）；
//   - 估算失败（分词器不可用）时返回 nil，让上层继续记 NULL——
//     "未知"比"编一个数"诚实。

import (
	"sync"

	"github.com/LittleFishStars/FreeRouter/internal/ir"
	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktokenloader "github.com/pkoukk/tiktoken-go-loader"
)

// encoder 是 cl100k_base 编码器（GPT-4/DeepSeek 系列同族）。
//
// 为什么用 cl100k_base 而不是按模型选：我们要的是**量级正确的估算**，
// 不是逐 token 复刻。各家 BPE 在中文/代码上的差异通常在 ±15% 内，
// 对"仪表盘有没有数"这个问题完全够用；而为每个模型维护一张 tokenizer 映射表
// 需要随上游更新持续维护，收益远低于成本。
// 标注 estimated 就是承认"这是估算"——精度要求本来就低于上报值。
var (
	encOnce sync.Once
	enc     *tiktoken.Tiktoken
	encErr  error
)

// encoderOf 惰性初始化分词器（首次调用会加载 BPE 表，约几十毫秒）。
func encoderOf() (*tiktoken.Tiktoken, error) {
	encOnce.Do(func() {
		// 必须用**离线加载器**：默认的 NewDefaultBpeLoader 会在首次调用时
		// 从 openaipublic.blob.core.windows.net 下载 BPE 词表——实测首次
		// 48 秒，且生产环境若出不了外网就永久失败。
		// 这对"结算时顺手估个数"的用途完全不可接受：它会把一次普通请求
		// 拖成 48 秒（2026-11-02 实测，测试用例被拖到 50s 超时）。
		//
		// 离线加载器把词表 embed 进二进制，加载是纯内存解析（毫秒级）。
		// 代价是二进制大几 MB——远比"每个新进程首个请求卡 48 秒"划算。
		tiktoken.SetBpeLoader(tiktokenloader.NewOfflineLoader())
		enc, encErr = tiktoken.GetEncoding("cl100k_base")
	})
	return enc, encErr
}

// CountTokens 估算一段文本的 token 数；分词器不可用时返回 (0, false)。
//
// 为什么不复用调用方传入的界：本函数要能被纯文本调用（如工具结果拼接），
// 收窄到 string 让它到处可用，不逼调用方先包成 ir 结构。
func CountTokens(text string) (int, bool) {
	if text == "" {
		return 0, true
	}
	e, err := encoderOf()
	if err != nil || e == nil {
		return 0, false
	}
	return len(e.Encode(text, nil, nil)), true
}

// EstimateUsage 在**上游没报 usage** 时，按请求与已产出的内容估一份。
//
// 入参：
//   - reqText：请求侧全部文本（消息正文 + 系统提示 + 工具定义），由调用方拼接；
//   - outText：响应侧已产出的文本（流式为累计内容，非流式为完整正文）。
//
// 返回 nil 表示"估不出来"（分词器不可用或两侧都为空）——此时上层应继续
// 记 NULL，而不是记 0：0 的语义是"上游明确说是零"，与"我们也不知道"相反。
//
// 为什么输出侧用已产出文本而不是 usage：本函数的存在前提就是上游没报，
// 唯一的依据只有我们实际转发过的字节。
func EstimateUsage(reqText, outText string) *ir.Usage {
	in, okIn := CountTokens(reqText)
	out, okOut := CountTokens(outText)
	if !okIn && !okOut {
		return nil
	}
	// 两侧都估不出来才算失败；有一侧可用就报那一侧（另一侧留 nil = 未知），
	// 而不是用 0 冒充。
	u := &ir.Usage{Source: ir.UsageEstimated}
	if okIn && in > 0 {
		u.InputTokens = ir.Ptr(in)
	}
	if okOut && out > 0 {
		u.OutputTokens = ir.Ptr(out)
	}
	if u.InputTokens == nil && u.OutputTokens == nil {
		return nil
	}
	return u
}
