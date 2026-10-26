package upstream

// 自定义供应商「声称的模型」非必填 + 自动获取的契约测试（2026-10-26 用户决定）。
//
// 独立成文件是因为这里需要一个**带调用计数**的探针：既有 fakeProbe 是值类型
// 且不计数，而"闸门探过一次、Submit 复用同一次"正是本功能的核心成本约束——
// 用户在前端同步等待，探测两次等于把提交延迟翻倍。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// countingProbe 记录被探测了几次，并按脚本返回结果。
type countingProbe struct {
	mu     sync.Mutex
	calls  int
	out    ProbeOutput
	probeE error
}

func (p *countingProbe) Probe(context.Context, ProbeInput) (ProbeOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.out, p.probeE
}

func (p *countingProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestContractDiscoverGate 钉住 Discover 闸门的四种端点状态分别落到哪里。
//
// 判据只有一条主张：**只有"可达 + 鉴权过 + 目录为空"才要求贡献者补声明**。
// 填模型能救的只有"列不出目录"这一种；不可达与鉴权失败填了也还是失败，
// 那种情形必须放行到正常链路去落库留痕（02-credentials 7 节：拒绝也保留
// 记录，它是申诉与惯犯识别的证据）。
func TestContractDiscoverGate(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		out          ProbeOutput
		probeErr     error
		wantNeed     bool
		wantModels   int
		wantErrNil   bool
		wantErrText  string
		wantReasonAt string
	}{
		{
			name:     "可达+鉴权过+有目录 → 放行，回目录",
			out:      ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m-a", "m-b"}},
			wantNeed: false, wantModels: 2, wantErrNil: true,
		},
		{
			name:     "可达+鉴权过+空目录 → 要求补声明",
			out:      ProbeOutput{Reachable: true, AuthOK: true},
			wantNeed: true, wantErrNil: true, wantReasonAt: "没有列出任何模型",
		},
		{
			// 不可达：不是"缺声明"。放行给正常链路，它会落库并记 L0 拒绝。
			name:     "不可达 → 不要求补声明（交给正常链路留痕）",
			out:      ProbeOutput{Reachable: false, Detail: "connection refused"},
			wantNeed: false, wantErrNil: true,
		},
		{
			name:     "鉴权失败 → 不要求补声明（同上）",
			out:      ProbeOutput{Reachable: true, AuthOK: false, Models: []string{"m-a"}},
			wantNeed: false, wantErrNil: true,
		},
		{
			// 探测跑不起来（非法 URL 等）：无法证明端点可用，让贡献者显式声明
			// 比替他猜更安全。
			name:     "探测本身失败 → 要求补声明并带原因",
			probeErr: errors.New("invalid url"),
			wantNeed: true, wantErrNil: true, wantReasonAt: "探测无法执行",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &countingProbe{out: tc.out, probeE: tc.probeErr}
			svc := NewContributionService(newFakeContributionStore(), p, ContributionOptions{})
			models, need, reason, pre, err := svc.Discover(ctx, "custom", "https://gw.example.com/v1", "sk-123456789")
			if (err == nil) != tc.wantErrNil {
				t.Fatalf("err=%v，期望 errNil=%v", err, tc.wantErrNil)
			}
			if need != tc.wantNeed {
				t.Errorf("needClaimed=%v，期望 %v（reason=%q）", need, tc.wantNeed, reason)
			}
			if len(models) != tc.wantModels {
				t.Errorf("models=%v，期望长度 %d", models, tc.wantModels)
			}
			if tc.wantReasonAt != "" && !strings.Contains(reason, tc.wantReasonAt) {
				t.Errorf("reason 应含 %q，实得 %q", tc.wantReasonAt, reason)
			}
			// 令牌必须回传：Submit 靠它复用这次探测，否则用户要等两次网络。
			if err == nil && pre == nil {
				t.Error("Discover 必须回传预探测令牌（PreProbe）")
			}
			if p.count() != 1 {
				t.Errorf("Discover 应恰好探测一次，实际 %d 次", p.count())
			}
		})
	}
}

// TestContractSubmitReusesPreProbe 钉住「闸门探过一次，Submit 不再探第二次」。
//
// 这是本功能唯一的性能红线：提交是用户同步等待的路径。
func TestContractSubmitReusesPreProbe(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	p := &countingProbe{out: ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m-a"}}}
	svc := NewContributionService(fs, p, ContributionOptions{})

	_, _, _, pre, err := svc.Discover(ctx, "custom", "https://gw.example.com/v1", "sk-123456789")
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	if p.count() != 1 {
		t.Fatalf("闸门应探一次，实际 %d", p.count())
	}

	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: "acct-auto", Provider: "custom", BaseURL: "https://gw.example.com/v1",
		APIKey: "sk-123456789", Label: "自动获取", PreProbe: pre,
	})
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	if p.count() != 1 {
		t.Errorf("Submit 复用了闸门那次探测才对，总探测数=%d", p.count())
	}
	// 未声明 → 判定口径不变：L0 放行 + unverified（复检队列）。
	if res.Status != store.CredActive || res.VerifyStatus != store.VerifyUnverified {
		t.Errorf("未声明模型应 active/unverified，得 %s/%s", res.Status, res.VerifyStatus)
	}
	// 自动获取的产物要能回给前端显示。
	if len(res.DiscoveredModels) != 1 || res.DiscoveredModels[0] != "m-a" {
		t.Errorf("DiscoveredModels 应回探测到的目录，得 %v", res.DiscoveredModels)
	}
}

// TestContractDiscoverSeedsCatalogOnPool 钉住本功能闭环的最后一环：
// 自动获取探到的目录必须在**凭据入池那一刻**就地落成 real_models 行，
// 不能只回显给前端然后指望 10 分钟一轮的定时刷新——那段时间里凭据的
// 模型卡是空的（管理面看到"有凭据、模型 0"），抽象模型也没有候选可展开。
func TestContractDiscoverSeedsCatalogOnPool(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	p := &countingProbe{out: ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m-a", "m-b", "m-c"}}}
	svc := NewContributionService(fs, p, ContributionOptions{})

	_, _, _, pre, err := svc.Discover(ctx, "custom", "https://gw.example.com/v1", "sk-123456789")
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: "acct-seed", Provider: "custom", BaseURL: "https://gw.example.com/v1",
		APIKey: "sk-123456789", Label: "目录就地落库", PreProbe: pre,
	})
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	if !res.InPool {
		t.Fatalf("应入池，得 status=%s", res.Status)
	}
	// 三个模型都要落进目录，且挂在同一条上游行上。
	if len(res.DiscoveredModels) != 3 || res.SeededModels != 3 {
		t.Fatalf("Discovered=%v Seeded=%d，期望各 3", res.DiscoveredModels, res.SeededModels)
	}
	var seeded []string
	for _, u := range fs.upstreams {
		seeded = append(seeded, fs.seeded[u.ID]...)
	}
	if len(seeded) != 3 {
		t.Errorf("fake 里应记录 3 条目录种子，得 %v", seeded)
	}
	// 拒绝路径（不入池）不落目录：那是"凭据都没进池"的世界，目录先行没有意义。
	fs2 := newFakeContributionStore()
	p2 := &countingProbe{out: ProbeOutput{Reachable: true, AuthOK: false}}
	svc2 := NewContributionService(fs2, p2, ContributionOptions{})
	res2, err := svc2.Submit(ctx, Submission{
		OwnerAccountID: "acct-reject", Provider: "custom", BaseURL: "https://gw.example.com/v1",
		APIKey: "sk-999999999", Label: "拒绝路径",
	})
	if err != nil {
		t.Fatalf("拒绝路径 Submit 失败: %v", err)
	}
	if res2.InPool {
		t.Fatal("鉴权失败应被拒")
	}
	total := 0
	for _, v := range fs2.seeded {
		total += len(v)
	}
	if total != 0 {
		t.Errorf("拒绝路径不得落目录种子，实际 %d 条", total)
	}
}

// TestContractSubmitWithoutPreProbeStillProbes 钉住未传令牌时的原行为：
// 自己探一次，且**不**因为走了这条路径就凭空多出 DiscoveredModels 之外的行为变化。
func TestContractSubmitWithoutPreProbeStillProbes(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	p := &countingProbe{out: ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m-x"}, UsedModels: []string{"m-x"}}}
	svc := NewContributionService(fs, p, ContributionOptions{})

	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: "acct-claimed", Provider: "custom", BaseURL: "https://gw.example.com/v1",
		APIKey: "sk-123456789", ClaimedModels: []string{"m-x"},
	})
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	if p.count() != 1 {
		t.Errorf("未传令牌时 Submit 应自己探一次，实际 %d 次", p.count())
	}
	if res.VerifyStatus != store.VerifyVerified {
		t.Errorf("声明且命中应 verified，得 %s", res.VerifyStatus)
	}
	// 声明过的凭据不该回"自动获取"目录（那条语义只属于未声明路径）。
	if len(res.DiscoveredModels) != 0 {
		t.Errorf("已声明时不该填 DiscoveredModels，得 %v", res.DiscoveredModels)
	}
}

// TestContractPreProbeNotUsedForClaimedJudgement 钉住一条容易写错的复用条件：
// 预探测那次没有带 ClaimedModels 入参，所以**不能**拿它回答"声明过"的判定。
func TestContractPreProbeNotUsedForClaimedJudgement(t *testing.T) {
	ctx := context.Background()
	fs := newFakeContributionStore()
	// 目录里有 m-a，但没有用户将要声明的 m-z。
	p := &countingProbe{out: ProbeOutput{Reachable: true, AuthOK: true, Models: []string{"m-a"}}}
	svc := NewContributionService(fs, p, ContributionOptions{})

	_, _, _, pre, err := svc.Discover(ctx, "custom", "https://gw.example.com/v1", "sk-123456789")
	if err != nil {
		t.Fatalf("Discover 失败: %v", err)
	}
	res, err := svc.Submit(ctx, Submission{
		OwnerAccountID: "acct-mixed", Provider: "custom", BaseURL: "https://gw.example.com/v1",
		APIKey: "sk-123456789", ClaimedModels: []string{"m-z"}, PreProbe: pre,
	})
	if err != nil {
		t.Fatalf("Submit 失败: %v", err)
	}
	// 带声明时那次预探测不可复用（UsedModels 没算过），必须重探一次。
	if p.count() != 2 {
		t.Errorf("已声明的提交应重新探测（总 2 次），实际 %d 次", p.count())
	}
	// 重探后 fake 仍返回"目录里没有 m-z"→ 全不命中应判 suspect。
	if res.VerifyStatus != store.VerifySuspect {
		t.Errorf("声称 m-z 而目录无此项应 suspect，得 %s（证据 %s）", res.VerifyStatus, res.Evidence)
	}
}
