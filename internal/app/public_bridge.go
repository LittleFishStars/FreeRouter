package app

// public_bridge.go —— 注册面与贡献者面的装配桥（httpapi 窄接口 → store/upstream）。
//
// 与 tenant_bridge/console_routes 同一条纪律：只翻译不判定，错误原样透传；
// httpapi 不认识 store/upstream，这里是唯一同时认识两边的数据翻译层。
//
// 贡献提交的 OwnerAccountID 是 int64（upstream 域）；httpapi 侧账户 id 是
// 字符串（认证面）——两套 id 在本桥互转，转换失败一律 ErrNotFound 同形。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// ---------------------------------------------------------------------------
// 注册面
// ---------------------------------------------------------------------------

type registerBridge struct {
	st      *store.Store
	maxKeys int
}

var _ httpapi.RegisterStore = registerBridge{}

func newRegisterBridge(st *store.Store, maxKeys int) registerBridge {
	return registerBridge{st: st, maxKeys: maxKeys}
}

// CreatePendingAccount 创建 pending 账户（contributor 容器类，不进组）。
//
// 为什么不进默认组：组成员要求 tenant 类（03-groups §9，组策略面向调用者），
// pending 账户没有；组的落位发生在审批通过时（02-admin §5.2 批准必带组与配额）。
// 账户与权限类仍在同一事务（不变式：类集合非空）。
func (b registerBridge) CreatePendingAccount(ctx context.Context, username, passwordHash, displayName string) error {
	err := b.st.WithTx(ctx, func(tx *store.Repos) error {
		_, err := tx.Accounts.CreateAccount(ctx, store.AccountAuth{
			Account: store.Account{
				DisplayName: displayName,
				Username:    username,
				Status:      store.AccountPending,
			},
			PasswordHash: passwordHash,
		}, []store.Class{store.ClassContributor})
		if err != nil {
			return err
		}
		// pending 账户不进默认组：组策略面向调用者（AddMember 要求 tenant 类），
		// 审批通过时由管理员显式落组与配额（02-admin §5.2）。
		return nil
	})
	if err != nil {
		// 唯一约束冲突 = 用户名被占用：翻译成 auth.ErrConflict，
		// handler 按「对外与成功同形」的原则只记日志（02-public 4.2）。
		if isDuplicateError(err) {
			return auth.ErrConflict
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 贡献者面
// ---------------------------------------------------------------------------

type contributionBridge struct {
	st      *store.Store
	svc     *upstream.ContributionService
	maxKeys int
}

var (
	_ httpapi.ContributionStore     = (*contributionBridge)(nil)
	_ httpapi.ContributionSubmitter = (*contributionBridge)(nil)
)

func newContributionBridge(st *store.Store, svc *upstream.ContributionService, maxKeys int) *contributionBridge {
	return &contributionBridge{st: st, svc: svc, maxKeys: maxKeys}
}

// AccountIDOf 现在只做存在性检查：认证面 id 与库内主键已是同一串 UUID，
// 不再需要 string→int64 的翻译（那层翻译正是 UUID 改造删掉的接缝之一）。
func (b *contributionBridge) AccountIDOf(ctx context.Context, accountID string) (string, error) {
	if _, err := parseID(accountID); err != nil {
		return "", err
	}
	if _, err := b.st.Repos().Accounts.GetAccount(ctx, accountID); err != nil {
		return "", err
	}
	return accountID, nil
}

// CreateContributor 创建 contributor 类账户（active、无密码）。
//
// 用户名随机生成（contrib-<hex>）：贡献者没有登录口令，用户名只是唯一约束
// 的载体；与恢复码一起使用登录（RecoverWithCode）。
func (b *contributionBridge) CreateContributor(ctx context.Context, displayName string) (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	username := "contrib-" + hex.EncodeToString(raw)
	var id string
	err := b.st.WithTx(ctx, func(tx *store.Repos) error {
		nid, err := tx.Accounts.CreateAccount(ctx, store.AccountAuth{
			Account: store.Account{
				DisplayName: displayName,
				Username:    username,
				Status:      store.AccountActive,
			},
			// 无密码：贡献者凭恢复码登录（RecoverWithCode）。
			PasswordHash: "",
		}, []store.Class{store.ClassContributor})
		if err != nil {
			return err
		}
		id = nid
		// contributor 账户不进默认组：组成员要求 tenant 类（03-groups §9）；
		// 贡献者走贡献统计/凭据面，不需要组策略来源。
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (b *contributionBridge) SetRecoveryCodeHash(ctx context.Context, accountID string, hash string) error {
	return b.st.Repos().Accounts.SetRecoveryCodeHash(ctx, accountID, hash)
}

func (b *contributionBridge) RecoveryHashOf(ctx context.Context, accountID string) (string, error) {
	return b.st.Repos().Accounts.RecoveryCodeHashOf(ctx, accountID)
}

func (b *contributionBridge) RecoveryStateOf(ctx context.Context, accountID string) (bool, error) {
	hash, err := b.st.Repos().Accounts.RecoveryCodeHashOf(ctx, accountID)
	if err != nil {
		return false, err
	}
	return hash != "", nil
}

func (b *contributionBridge) CredentialsOf(ctx context.Context, owner string) ([]httpapi.ContributionCredentialRow, error) {
	rows, err := b.st.Repos().Models.ListCredentialsByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.ContributionCredentialRow, 0, len(rows))
	for _, c := range rows {
		out = append(out, httpapi.ContributionCredentialRow{
			ID: c.ID, Label: c.ExternalLabel, Status: c.Status,
			VerifyStatus: string(c.VerifyStatus), VerifyDetail: c.VerifyDetail,
			CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt,
		})
	}
	return out, nil
}

func (b *contributionBridge) CredentialOf(ctx context.Context, owner, id string) (*httpapi.ContributionCredentialRow, error) {
	c, err := b.st.Repos().Models.LookupCredential(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.OwnerAccountID != owner {
		// 非属主与不存在同语义（不给跨账户探测面）。
		return nil, store.ErrNotFound
	}
	return &httpapi.ContributionCredentialRow{
		ID: c.ID, Label: c.ExternalLabel, Status: c.Status,
		VerifyStatus: string(c.VerifyStatus), VerifyDetail: c.VerifyDetail,
		CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt,
	}, nil
}

func (b *contributionBridge) ContributionUsage(ctx context.Context, owner string, since int64) (int64, int64, int64, error) {
	return b.st.Repos().Usage.ContributionUsageByOwner(ctx, owner, since)
}

// Submit 把 httpapi 形状翻成 upstream.Submission 并执行完整链路。
func (b *contributionBridge) Submit(ctx context.Context, sub httpapi.ContributionSubmission) (httpapi.ContributionOutcome, error) {
	if b.svc == nil {
		return httpapi.ContributionOutcome{}, fmt.Errorf("贡献服务未装配")
	}
	// 复用 Discover 闸门那次探测（pre 是 *upstream.PreProbe，穿过 httpapi 时
	// 被装成 any）。类型不匹配就当没探过——宁可多打一次网络，不可误用结果。
	pre, _ := sub.PreProbe.(*upstream.PreProbe)
	out, err := b.svc.Submit(ctx, upstream.Submission{
		PreProbe:       pre,
		OwnerAccountID: sub.OwnerAccountID,
		Provider:       sub.Provider,
		BaseURL:        sub.BaseURL,
		APIKey:         sub.APIKey,
		ClaimedModels:  sub.ClaimedModels,
		Label:          sub.Label,
		Kind:           sub.Kind,
	})
	if err != nil {
		return httpapi.ContributionOutcome{}, err
	}
	return httpapi.ContributionOutcome{
		CredentialID:     out.CredentialID,
		Status:           string(out.Status),
		VerifyStatus:     string(out.VerifyStatus),
		InPool:           out.InPool,
		Reused:           out.Reused,
		DiscoveredModels: out.DiscoveredModels,
	}, nil
}

// Discover 实现 httpapi.ContributionSubmitter 的提交前闸门（理由见
// upstream.ContributionService.Discover 的注释）。返回的 pre 原样随下一次
// Submit 传回即可复用这次探测，省掉用户等待路径上的第二次网络往返。
func (b *contributionBridge) Discover(ctx context.Context, provider, baseURL, apiKey string) ([]string, bool, string, any, error) {
	if b.svc == nil {
		return nil, false, "", nil, fmt.Errorf("贡献服务未装配")
	}
	models, needClaimed, reason, pre, err := b.svc.Discover(ctx, provider, baseURL, apiKey)
	if err != nil {
		return nil, false, "", nil, err
	}
	return models, needClaimed, reason, pre, nil
}

// StatusOf 读账户状态与权限类（待审核页「刷新状态」）。
func (b registerBridge) StatusOf(ctx context.Context, accountID string) (string, []string, error) {
	id, err := parseID(accountID)
	if err != nil {
		return "", nil, err
	}
	info, err := b.st.Repos().Accounts.AccountStatusOf(ctx, id)
	if err != nil {
		return "", nil, err
	}
	return info.Status, info.Classes, nil
}
