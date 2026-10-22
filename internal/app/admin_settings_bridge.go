package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/config"
	"github.com/LittleFishStars/FreeRouter/internal/httpapi"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// admin_settings_bridge.go —— 设置页的读、写与源 IP 自检（M10 补齐）。
//
// 设置分两类，桥必须区别对待：
//  1. **只读项**（limits.* / security.* 的建账初值、session_ttl 等）：改它们
//     需要重启才有意义（它们只影响新建账户与登录策略的快照），所以视图里
//     如实返回当前生效值，Patch 直接拒绝——不制造「改了但没生效」的错觉。
//  2. **运行时开关**（registration_enabled / registration_requires_approval）：
//     立刻生效、且必须跨重启保持，所以走内存 + settings 表的双写。
//
// 为什么不把全部键都做成热改：热改意味着每个请求路径都要在锁下重读配置，
// 而配置里绝大多数值是启动期快照（05-storage §1）。只让真正需要热改的两项
// 付这个代价，其余保持启动期语义。

// runtimeSettingKeys 是允许热改且持久化的键（白名单，未知键拒绝）。
// 键名是**扁平**的（不带组名前缀）：handler 的 adminSettingKeys 把分组文档
// 摊平后只取组内键名，PATCH 载荷因此必须用 "registration_enabled" 而不是
// "security.registration_enabled"（实测带前缀会被判未知键）。当前两组内
// 键名无重名，摊平不丢信息。
var runtimeSettingKeys = map[string]bool{
	"registration_enabled":           true,
	"registration_requires_approval": true,
}

type settingsBridge struct {
	b   *adminBridge
	cfg *config.Config
}



// GetSettings 返回分组视图 {"security":{...},"limits":{...},"runtime":{...}}。
func (s *settingsBridge) GetSettings(ctx context.Context) (json.RawMessage, error) {
	_ = ctx
	c := s.cfg
	doc := map[string]any{
		"security": map[string]any{
			"registration_enabled":           c.Security.RegistrationEnabled,
			"registration_requires_approval": c.Security.RegistrationRequiresApproval,
			"session_ttl":                    c.Security.SessionTTL.String(),
			"password_min_length":            c.Security.PasswordMinLength,
			"login_max_failures":             c.Security.LoginMaxFailures,
			"login_lockout":                  c.Security.LoginLockout.String(),
			"behind_proxy":                   c.Server.BehindProxy,
			"trusted_proxies":                c.Security.TrustedProxies,
		},
		"limits": map[string]any{
			"default_rpm":              c.Limits.DefaultRPM,
			"default_tpm":              c.Limits.DefaultTPM,
			"default_daily_tokens":     c.Limits.DefaultDailyTokens,
			"default_monthly_tokens":   c.Limits.DefaultMonthlyTokens,
			"default_max_concurrency":  c.Limits.DefaultMaxConcurrency,
			"default_keys_per_account": c.Limits.DefaultKeysPerAccount,
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("settings: 序列化视图: %w", err)
	}
	return b, nil
}

// PatchSettings 只接受白名单键；业务写（内存 + settings 表）与审计同一事务。
func (s *settingsBridge) PatchSettings(ctx context.Context, patch map[string]json.RawMessage, audit httpapi.AdminAuditEntry) (string, error) {
	if len(patch) == 0 {
		return "", fmt.Errorf("settings: 空补丁（body 至少含一个键）")
	}
	// 先校验全部键：任何一个未知键 → 整批拒绝（不做部分成功，部分成功让
	// 前端无法判断“哪些生效了”）。
	for k := range patch {
		if !runtimeSettingKeys[k] {
			return "", fmt.Errorf("settings: 设置项 %q 不可热改（只读项需重启生效）", k)
		}
	}

	// 内存先算好新值，再进事务：事务内不做可能失败的副作用之外的写。
	newVals := map[string]bool{}
	for k, raw := range patch {
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", fmt.Errorf("settings: 设置项 %q 需要布尔值", k)
		}
		newVals[k] = v
	}

	var auditID string
	err := s.b.st.WithTx(ctx, func(tx *store.Repos) error {
		for k, v := range newVals {
			raw, _ := json.Marshal(v)
			if err := tx.Settings.SetInTx(ctx, k, string(raw)); err != nil {
				return err
			}
		}
		e := audit
		if e.Action == "" {
			e.Action = string(store.ActionCategorySystem) + ".settings.patch"
		}
		var err error
		auditID, err = appendAudit(ctx, tx.Logs, e)
		return err
	})
	if err != nil {
		return "", err
	}
	// 事务成功后改内存（顺序不能反：先改内存再失败回滚会让运行态与库不一致）。
	for k, v := range newVals {
		switch k {
		case "registration_enabled":
			s.cfg.Security.RegistrationEnabled = v
		case "registration_requires_approval":
			s.cfg.Security.RegistrationRequiresApproval = v
		}
	}
	return auditID, nil
}

// CheckIP 的判据是"该源 IP 会不会被正确解析"，不是允许列表。
//
// 为什么不做允许列表：2026-10-19 定案取消 admin_ip_allowlist；这里只回答
// 「你的请求经过可信反代吗、后端能否看到真实 IP」。
func (s *settingsBridge) CheckIP(ctx context.Context, sourceIP string) (*httpapi.AdminIPCheckResult, error) {
	_ = ctx
	ip := net.ParseIP(strings.TrimSpace(sourceIP))
	if ip == nil {
		return &httpapi.AdminIPCheckResult{
			SourceIP:       sourceIP,
			Allowed:        false,
			Reason:         "来源地址不是合法 IP（可能是直连地址解析失败）",
			BehindProxy:    s.cfg.Server.BehindProxy,
			TrustedProxies: s.cfg.Security.TrustedProxies,
		}, nil
	}
	res := &httpapi.AdminIPCheckResult{
		SourceIP:       ip.String(),
		BehindProxy:    s.cfg.Server.BehindProxy,
		TrustedProxies: s.cfg.Security.TrustedProxies,
	}
	if !s.cfg.Server.BehindProxy {
		res.Allowed = true
		res.Reason = "未声明在反代之后：后端直接使用直连地址，X-Forwarded-For 不被采信"
		return res, nil
	}
	if len(s.cfg.Security.TrustedProxies) == 0 {
		res.Allowed = false
		res.Reason = "声明了 behind_proxy 但 trusted_proxies 为空：客户端 IP 会退化为反代地址"
		return res, nil
	}
	for _, cidr := range s.cfg.Security.TrustedProxies {
		if cidr == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			if n.Contains(ip) {
				res.Allowed = true
				res.Reason = "该地址落在 trusted_proxies 内：它会被当作反代，真实客户端在更右侧"
				return res, nil
			}
			continue
		}
		if single := net.ParseIP(cidr); single != nil && single.Equal(ip) {
			res.Allowed = true
			res.Reason = "该地址在 trusted_proxies 中：它会被当作反代"
			return res, nil
		}
	}
	res.Allowed = false
	res.Reason = "该地址不在 trusted_proxies 内：它会被当作真实客户端（X-Forwarded-For 被忽略）"
	return res, nil
}
