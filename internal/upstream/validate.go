package upstream

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 本文件守卫 03-upstream.md §9.3 的出站边界——旧「唯一出口原则」改名后的版本：
// 真正要防的是「我们被当成开放代理」，而不是某个具体地址。
//
// 守卫发生在两个时机：配置读取（本包 ValidateTarget）与启动校验（装配层调用同函数）。
// 违反时拒绝——不是降级、不是告警后放行。

// ValidateTarget 校验一次尝试的目标是否越界。
//
// 三条规则（§9.3 表）：
//  1. relay 不持有任何可配地址——Target.BaseURL 对它恒为空，写入即视为缺陷；
//  2. vendor / selfhosted 的 base_url 必须是 https，仅回环可显式放行 http；
//  3. 拒绝 0.0.0.0 / :: 作为目标主机。
//
// 为什么 relay 的违规在这里报错而不是静默忽略：BaseURL 非空意味着有人试图给 relay
// 配一个「网关地址」——这正是 ALIGN-SPEC H 推翻的旧设计。静默忽略会让这个配置
// 在数据库里躺到某天被别的代码路径读走；报错让它当场被发现。
func ValidateTarget(t Target) error {
	if t.Kind == KindRelay {
		if t.BaseURL != "" {
			return fmt.Errorf("upstream#%s：relay 不持有可配地址（BaseURL 必须为空，adapter 直连由 sidecar 决定），got %q", t.UpstreamID, t.BaseURL)
		}
		return nil
	}
	if t.Kind != KindVendor && t.Kind != KindSelfHosted {
		return fmt.Errorf("upstream#%s：未知上游类型 %q", t.UpstreamID, t.Kind)
	}
	if strings.TrimSpace(t.BaseURL) == "" {
		return fmt.Errorf("upstream#%s：%s 类上游缺少 base_url", t.UpstreamID, t.Kind)
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil {
		return fmt.Errorf("upstream#%s：base_url 不可解析：%w", t.UpstreamID, err)
	}
	host := u.Hostname()
	if u.Scheme == "http" {
		// 唯一例外：回环地址，用于本地联调（§4「只允许 https」的理由是贡献凭据
		// 不得在公网裸奔，回环上没有公网）。
		if !isLoopback(host) {
			return fmt.Errorf("upstream#%s：http 协议仅回环地址放行，got %q", t.UpstreamID, t.BaseURL)
		}
	} else if u.Scheme != "https" {
		return fmt.Errorf("upstream#%s：base_url 必须是 https（仅回环可放行 http），got scheme %q", t.UpstreamID, u.Scheme)
	}
	// 0.0.0.0 / :: 是「本机所有地址」的通配形式，作为**目标**出现说明配置
	// 写的是监听语句而不是端点——打出去的行为未定义且必然出界。
	if host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("upstream#%s：拒绝通配地址作为目标主机：%q", t.UpstreamID, host)
	}
	return nil
}

// isLoopback 判定主机名是否回环。带内置解析：url.Hostname() 对 https://127.0.0.1:8443
// 返回 "127.0.0.1"，可直接 net.ParseIP；"localhost" 是常见写法，一并放行。
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
