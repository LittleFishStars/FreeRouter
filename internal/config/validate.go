package config

import (
	"fmt"
	"net"
	"strings"
)

// Validate 做配置的**硬校验**：返回 error 时进程必须拒绝启动。
//
// 判定原则（为什么这样分）：
//   - 会导致「以为自己安全其实不安全」的配置 → error；
//   - 只是可疑、但有明确合法用途的 → Warnings，交给管理员判断。
//
// 相关设计：docs/ops/01-security.md（公网安全基线）、docs/ops/02-deploy.md §2。
func (c *Config) Validate() error {
	var errs []string

	switch strings.ToLower(c.TLS.Mode) {
	case "off":
	case "files":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			errs = append(errs, "tls.mode=files 时必须同时填写 tls.cert_file 与 tls.key_file")
		}
	case "acme":
		errs = append(errs, "tls.mode=acme 本版不支持：ACME 必须针对真实域名签发，而对外域名尚未确定；"+
			"请改用 files（自备证书）或在前面放一个终止 TLS 的反代")
	default:
		errs = append(errs, fmt.Sprintf("tls.mode=%q 非法，只能是 off 或 files", c.TLS.Mode))
	}

	switch strings.ToLower(c.Server.InsecurePublicListen) {
	case "warn", "deny":
	default:
		errs = append(errs, fmt.Sprintf("server.insecure_public_listen=%q 非法，只能是 warn 或 deny",
			c.Server.InsecurePublicListen))
	}

	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		errs = append(errs, fmt.Sprintf("server.listen=%q 不是合法的 host:port（%v）", c.Server.Listen, err))
	}

	if strings.ToLower(c.Sidecar.IPC) != "stdio" {
		errs = append(errs, fmt.Sprintf("sidecar.ipc=%q 非法：只提供 stdio + NDJSON，"+
			"不提供 socket / tcp 选项（见 docs/architecture/04-sidecar.md 2.1）", c.Sidecar.IPC))
	}

	if c.Security.SessionTTL <= 0 {
		errs = append(errs, "security.session_ttl 必须为正数：取消类级提权后它是唯一的时效边界")
	}
	if c.Security.PasswordMinLength < 8 {
		errs = append(errs, "security.password_min_length 不得小于 8")
	}
	if c.Security.LoginMaxFailures < 1 {
		errs = append(errs, "security.login_max_failures 必须 ≥ 1，否则退避永不触发")
	}

	nonNegative := map[string]int64{
		"limits.default_daily_tokens":     c.Limits.DefaultDailyTokens,
		"limits.default_monthly_tokens":   c.Limits.DefaultMonthlyTokens,
		"limits.default_keys_per_account": int64(c.Limits.DefaultKeysPerAccount),
		"limits.global_daily_tokens":      c.Limits.GlobalDailyTokens,
		"limits.global_rpm":               int64(c.Limits.GlobalRPM),
		"limits.global_concurrency":       int64(c.Limits.GlobalConcurrency),
		"server.max_body_bytes":           c.Server.MaxBodyBytes,
	}
	for key, val := range nonNegative {
		if val < 0 {
			errs = append(errs, fmt.Sprintf("%s 不得为负数（当前 %d）", key, val))
		}
	}

	// 账户速率默认值允许 -1 哨兵（= 不限，2026-10-20 定案，与 accounts 速率列同口径）；
	// 除此之外不得为负。为什么不并入上面的 map：那会让 -1 这个合法值被拒。
	sentinelOK := map[string]int64{
		"limits.default_rpm":             int64(c.Limits.DefaultRPM),
		"limits.default_tpm":             c.Limits.DefaultTPM,
		"limits.default_max_concurrency": int64(c.Limits.DefaultMaxConcurrency),
	}
	for key, val := range sentinelOK {
		if val < -1 {
			errs = append(errs, fmt.Sprintf("%s 只能是 ≥ -1（-1 = 不限，0 = 全拒；当前 %d）", key, val))
		}
	}

	if c.Store.Path == "" {
		errs = append(errs, "store.path 不得为空")
	}

	// 非回环监听 + 明文：按配置决定是告警还是拒绝启动。
	// 为什么默认是 warn 而不是 deny：默认部署形态就是「IP + 端口明文直连」（PLAN 15 章），
	// 直接 deny 会让默认形态开不了机；但绝不能静默通过。
	if !c.IsLoopbackListen() && strings.EqualFold(c.TLS.Mode, "off") {
		if strings.EqualFold(c.Server.InsecurePublicListen, "deny") {
			errs = append(errs, "server.listen 绑定了非回环地址，同时 tls.mode=off：当前策略为 deny，拒绝启动")
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("配置校验失败：\n  - %s", strings.Join(errs, "\n  - "))
}

// Warnings 返回不致命但必须打印出来的配置风险。
func (c *Config) Warnings() []string {
	var out []string

	if !c.IsLoopbackListen() && strings.EqualFold(c.TLS.Mode, "off") {
		out = append(out, fmt.Sprintf(
			"监听 %s 是非回环地址且未启用 TLS：请求与凭据将以明文经过网络。"+
				"若确实要这样跑（默认部署形态），请确认所在网络可信；"+
				"想拒绝这种组合请设 server.insecure_public_listen=deny",
			c.Server.Listen))
	}

	if c.Server.BehindProxy && len(c.Security.TrustedProxies) == 0 {
		out = append(out, "server.behind_proxy=true 但 security.trusted_proxies 为空："+
			"客户端 IP 会退化为反代地址，按 IP 的限速会把所有用户算成同一个人")
	}

	if !c.IsLoopbackListen() && len(c.Security.TrustedProxies) > 0 && !c.Server.BehindProxy {
		out = append(out, "配置了 security.trusted_proxies 但未声明 server.behind_proxy=true："+
			"请确认确实由受信反代转发，否则 X-Forwarded-For 可被客户端伪造")
	}

	if c.Limits.DefaultDailyTokens == 0 && c.Limits.DefaultMonthlyTokens == 0 {
		out = append(out, "limits.default_*_tokens 均为 0：新建账户默认没有任何模型调用额度，"+
			"需要管理员显式授权（这是刻意的默认）")
	}

	if c.Sidecar.Enabled && c.Sidecar.HubPath == "" {
		out = append(out, "sidecar.enabled=true 但 sidecar.hub_path 为空：sidecar 将按默认查询路径解析 "+
			"dsh-providers-hub，解析失败时贡献功能不可用（不影响转发）")
	}

	return out
}
