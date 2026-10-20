// 模型目录刷新（M1：real_models 落库 + sidecar listModels 装配）。
//
// 红线（docs/ops/03-milestones.md §M1 / ALIGN-SPEC H）：主进程**不直连**上游的
// /v1/models——模型目录只经 sidecar `models.list({provider})` 拉取（task-6 的
// dshprovider.Client）。本文件只 import dshprovider 的类型，不修改其文件。
//
// 写域：internal/upstream/refresh.go（新文件）；仓储方法（UpsertRealModel/
// MarkMissingRealModels）复用 internal/store 既有实现，real_models 表在
// 0001_init.sql 已有 last_seen_at/missing_since 列，无需新增迁移。
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// DefaultModelRefreshInterval 对应部署键 upstream.relay.model_refresh_interval
// 的默认值 10m（docs/ops/02-deploy.md §2.5）。配置解析归 internal/config，
// 这里只定义兜底默认——调用方传 0/负值时使用。
const DefaultModelRefreshInterval = 10 * time.Minute

// CatalogSource 是"模型目录从哪来"的最小接口。
//
// 为什么不直接收 *dshprovider.Client：refresh 的单测需要 fake 源（返回错误、
// 返回可控目录），而 refresh 逻辑不应该知道 client 的拨号/重连细节。
// *dshprovider.Client 天然满足该接口；refresh.go 只 import 其 ModelInfo 类型。
type CatalogSource interface {
	ModelsList(ctx context.Context, provider string) ([]dshprovider.ModelInfo, error)
}

// RefreshResult 汇总一轮刷新的结果，供日志与测试断言。
type RefreshResult struct {
	// Upstreams/Models 是本轮参与刷新的上游数与落库模型总数。
	Upstreams int
	Models    int
	// MarkedMissing 是被标记 missing 的行数（目录里消失的模型）。
	MarkedMissing int
	// Failures 记录每个上游的拉取失败原因（sidecar 崩溃只记日志，不阻塞）。
	Failures []UpstreamRefreshError
}

// UpstreamRefreshError 是单个上游刷新失败的原因。
type UpstreamRefreshError struct {
	Upstream string
	Err      error
}

func (e UpstreamRefreshError) Error() string {
	return fmt.Sprintf("上游 %s 刷新失败: %v", e.Upstream, e.Err)
}

// ModelRefresher 周期刷新模型目录。
//
// 失败不阻塞启动（任务红线）：RefreshOnce 返回错误只记日志，下一轮 ticker
// 自然重试；Start 的 goroutine 内 recover 兜底——一次 panic 不该带走整个主进程。
type ModelRefresher struct {
	store    *store.Store
	source   CatalogSource
	interval time.Duration
	log      *slog.Logger
}

// NewModelRefresher 构造刷新器。interval<=0 时用 DefaultModelRefreshInterval；
// log 为 nil 时用 slog.Default()。
func NewModelRefresher(st *store.Store, source CatalogSource, interval time.Duration, log *slog.Logger) *ModelRefresher {
	if interval <= 0 {
		interval = DefaultModelRefreshInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &ModelRefresher{store: st, source: source, interval: interval, log: log}
}

// Interval 返回生效的刷新间隔（测试与可观测用）。
func (r *ModelRefresher) Interval() time.Duration { return r.interval }

// RefreshOnce 拉一轮全部 relay 类上游的目录并落库。
//
// 为什么只刷 kind='relay'：ALIGN-SPEC H 定案后，模型目录**只**经 sidecar
// provider adapter 拉取；vendor/selfhosted 类上游是传统 API 出口（M5），
// 它们的目录来源不是 sidecar，混进来只会对同一个 provider 名发出错误的
// models.list 调用。
//
// 单个上游失败不中断其余上游（Failures 汇总）：一个 provider 的 sidecar
// 崩溃不该让其他 provider 的目录也停止刷新。
func (r *ModelRefresher) RefreshOnce(ctx context.Context) (RefreshResult, error) {
	var res RefreshResult
	ups, err := r.store.Repos().Models.ListUpstreams(ctx)
	if err != nil {
		return res, fmt.Errorf("upstream: 列上游失败: %w", err)
	}

	for _, u := range ups {
		if !u.Enabled || u.Kind != "relay" {
			continue
		}
		res.Upstreams++
		n, err := r.refreshUpstream(ctx, u)
		if err != nil {
			// 失败只记录（红线：sidecar 崩溃时刷新失败只记日志下轮重试），
			// 绝不 MarkMissing——拉取失败≠模型真的没了，全标 missing 是自伤
			// （MarkMissingRealModels 对空 keepIDs 的拒绝也同源）。
			res.Failures = append(res.Failures, UpstreamRefreshError{Upstream: u.Name, Err: err})
			r.log.Warn("upstream: 模型目录刷新失败，下轮重试",
				"upstream", u.Name, "err", err)
			continue
		}
		res.Models += n
	}
	return res, nil
}

// refreshUpstream 刷新单个上游：拉目录 → 逐条 upsert → 标记消失模型。
// 返回落库的模型条数。
func (r *ModelRefresher) refreshUpstream(ctx context.Context, u store.Upstream) (int, error) {
	models, err := r.source.ModelsList(ctx, u.Name)
	if err != nil {
		return 0, fmt.Errorf("upstream: 拉取模型目录失败: %w", err)
	}

	// 目录为空时不 MarkMissing（仓储对空 keepIDs 拒绝，语义同源），
	// 也不计失败——空目录是合法应答，只是没有可落的行。
	if len(models) == 0 {
		r.log.Info("upstream: 模型目录为空，跳过落库", "upstream", u.Name)
		return 0, nil
	}

	keepIDs := make([]int64, 0, len(models))
	for _, m := range models {
		if m.UpstreamModelID == "" {
			// 目录里混入无名条目：跳过而不是整体失败——一个坏条目
			// 不该挡住其余几百个模型落库。
			r.log.Warn("upstream: 目录条目缺 upstreamModelId，跳过", "upstream", u.Name)
			continue
		}
		id, err := r.upsertModel(ctx, u.ID, m)
		if err != nil {
			return 0, err
		}
		keepIDs = append(keepIDs, id)
	}

	// 标记目录里消失的模型（status=missing + missing_since 首次标记时间）。
	// 仓储层保证：missing 保留行不删除（候选集 CASCADE 安全），上游恢复时
	// UpsertRealModel 写回 active 并清 missing_since。
	marked, err := r.store.Repos().Models.MarkMissingRealModels(ctx, u.ID, keepIDs)
	if err != nil {
		return 0, err
	}
	r.log.Info("upstream: 模型目录刷新完成",
		"upstream", u.Name, "models", len(keepIDs), "marked_missing", marked)
	return len(keepIDs), nil
}

// upsertModel 把一条目录条目转成仓储行并 upsert。
//
// last_seen_at 不在这里手工赋值：UpsertRealModel 对 nil 自动取 now()，
// 且 ON CONFLICT 分支同样刷新 last_seen_at、清 missing_since——
// "目录里又见到了"与"首次见到"在存储侧是同一次写入。
func (r *ModelRefresher) upsertModel(ctx context.Context, upstreamID int64, m dshprovider.ModelInfo) (int64, error) {
	row := store.RealModel{
		UpstreamID:      upstreamID,
		UpstreamModelID: m.UpstreamModelID,
		DisplayName:     m.DisplayName,
		Status:          store.RealModelActive,
		Source:          "auto",
	}
	if m.DisplayName == "" {
		// 目录没给展示名时回退到模型 id，避免前端拿到空字符串。
		row.DisplayName = m.UpstreamModelID
	}
	if m.ContextWindow != nil {
		cw := int64(*m.ContextWindow)
		row.ContextWindow = &cw
	}
	// modalities/efforts 存 JSON 数组原文（05-storage.md real_models 列口径）；
	// 空数组存 NULL 而不是 "[]"——NULL 表达"目录没说"，[] 表达"明确没有"。
	if len(m.Modalities) > 0 {
		if b, err := json.Marshal(m.Modalities); err == nil {
			row.Modalities = ptr(string(b))
		}
	}
	if len(m.Efforts) > 0 {
		if b, err := json.Marshal(m.Efforts); err == nil {
			row.Efforts = ptr(string(b))
		}
	}
	id, err := r.store.Repos().Models.UpsertRealModel(ctx, row)
	if err != nil {
		return 0, fmt.Errorf("upstream: 落库模型 %s 失败: %w", m.UpstreamModelID, err)
	}
	return id, nil
}

// Start 启动周期刷新：立即刷一轮，然后按 interval 周期刷。
//
// 失败不阻塞启动（任务红线）：RefreshOnce 的单上游失败在 RefreshOnce 内部
// 已记日志（Failures），机制性错误（列上游失败）在 runLoop 记一次——
// Start 自身不返回错误，启动方无需等待。panic recover 兜底：
// 目录刷新是旁路任务，一次 panic 不该带走主进程，堆栈进日志供排障。
func (r *ModelRefresher) Start(ctx context.Context) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.log.Error("upstream: 模型目录刷新 panic，刷新器退出",
					"panic", p, "stack", string(debug.Stack()))
			}
		}()
		r.runLoop(ctx)
	}()
}

func (r *ModelRefresher) runLoop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if _, err := r.RefreshOnce(ctx); err != nil {
			r.log.Warn("upstream: 模型目录刷新轮失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func ptr[T any](v T) *T { return &v }
