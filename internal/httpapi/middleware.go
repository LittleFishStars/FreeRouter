package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// 本文件实现 L1 中间件链的前四步。
//
// 为什么顺序是契约而不是实现细节：
//   - 请求ID 必须最先，否则后面任何一步的日志都没有可关联的标识；
//   - 安全头要在任何可能产生响应体的处理之前设置，否则 4xx 响应会缺头；
//   - 可信代理必须在读取客户端 IP 之前完成，否则限速按 IP 计数会统计到伪造值；
//   - body 上限必须在鉴权之前，否则未认证的巨型请求可以先把内存吃满；
//   - 鉴权必须在限速之前，否则攻击者能用无效密钥刷爆限速计数器、误伤真实用户。
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyClientIP
)

// RequestIDFromContext 取请求 ID；不在 HTTP 上下文里时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// ClientIPFromContext 取已完成可信代理解析后的客户端 IP。
func ClientIPFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyClientIP).(string)
	return v
}

// mwRequestID 生成或沿用请求 ID，并写回响应头。
// 沿用上游传来的 X-Request-ID 让跨进程排障成为可能（反代 → 我们 → sidecar）。
func (s *Server) mwRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// validRequestID 只接受短的可打印 ASCII，避免把日志注入或超长值带进日志。
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见；退化到固定前缀仍然保持唯一性由时间戳保证，
		// 但这里不做兼容——直接 panic 会让进程在真随机源坏掉时不断重启。
		// 选择返回一个常量前缀的随机串：调用方拿到的是「唯一的可能性很低」的值，
		// 而不是一个让服务崩掉的值。
		return "req-unknown"
	}
	return "req-" + hex.EncodeToString(b[:])
}

// mwSecurityHeaders 设置与安全相关的响应头。
// CSP 只允许同源脚本：管理界面是自带的前端，不需要外域资源。
func (s *Server) mwSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// CSP 统一含阿里云滑块域：Raccoon 短信登录的 captcha_param 由官方
		// AliyunCaptcha.js 产出（o.alicdn.com 脚本 + aliyuncs/alicdn 验证接口）。
		// 为什么不做「按路径放宽」：前端是 HashRouter 单文档——文档加载时
		// URL 是根路径，hash 跳到贡献页不会重新加载文档，路径分流永不生效
		//（2026-10-27 playwright 实测 CSP 仍拦滑块脚本）。放宽域限定为阿里
		// 官方 CDN；本服务自带前端、无第三方脚本注入点，风险面可控。
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' https://o.alicdn.com https://g.alicdn.com; "+
				"connect-src 'self' https://*.aliyuncs.com https://*.alicdn.com; "+
				"img-src 'self' data: https://*.aliyuncs.com https://*.alicdn.com; "+
				"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		// 本服务承载 API 与自带前端，不申请任何浏览器能力。
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		if strings.EqualFold(s.cfg.TLS.Mode, "files") {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// mwTrustedProxy 解析客户端真实 IP。
//
// 规则（docs/ops/01-security.md）：
//   - 只有当直连对端落在 security.trusted_proxies 内，才采信 X-Forwarded-For；
//   - 取 XFF 中最右边一个**非可信**地址（从右往左跳过可信代理），
//     这样客户端伪造的前缀（客户端可以自己往 XFF 里加 IP）不会生效；
//   - 否则一律使用直连对端地址。
func (s *Server) mwTrustedProxy(next http.Handler) http.Handler {
	trusted := parseCIDRs(s.cfg.Security.TrustedProxies)
	behindProxy := s.cfg.Server.BehindProxy

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r.RemoteAddr)

		if behindProxy && len(trusted) > 0 && ipTrusted(ip, trusted) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				for i := len(parts) - 1; i >= 0; i-- {
					cand := strings.TrimSpace(parts[i])
					parsed := net.ParseIP(cand)
					if parsed == nil {
						continue
					}
					if ipTrusted(parsed, trusted) {
						continue // 仍是反代自己，继续往左找
					}
					ip = parsed
					break
				}
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyClientIP, ip.String())))
	})
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

func parseCIDRs(list []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			// 允许直接写单个 IP，按 /32 或 /128 处理，省掉管理员手算掩码。
			if ip := net.ParseIP(raw); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			}
			continue
		}
		if _, n, err := net.ParseCIDR(raw); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func ipTrusted(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// mwBodyLimit 在读取 body 前设硬上限。
// 为什么不用 http.MaxBytesReader 包一下就算完：分配器会在超出时才报错，
// 而我们要的是「声明 Content-Length 就已经过大时立刻 413」——
// 这样攻击者连发大包都省不掉，也避免为注定失败的请求读几 MB。
func (s *Server) mwBodyLimit(next http.Handler) http.Handler {
	limit := s.cfg.Server.MaxBodyBytes
	if limit <= 0 {
		limit = 32 << 20
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			// 码名是契约：docs/api/03-errors.md 里 413 对应 request_too_large。
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				"请求体超过入站上限", map[string]any{"limit_bytes": limit})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
