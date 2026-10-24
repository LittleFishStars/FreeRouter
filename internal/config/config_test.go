package config

// config_test.go —— 配置解析、环境变量覆盖与校验的回归测试。
//
// 为什么这个包需要测试：配置错误是**部署期最常见的故障形态**——YAML 键拼错、
// 环境变量名差一个下划线、把 `tls.mode` 写成非法值。这些错误一旦漏到运行期，
// 表现往往是「进程起来了但行为不对」（例如 trusted_proxies 没生效导致
// 所有请求的客户端 IP 都是反代地址），比启动即崩更难排查。
//
// 本包此前**零测试覆盖**（-cover 报 0.0%），而它的校验逻辑有十几个分支。

import (
	"strings"
	"testing"
	"time"
)

// envMap 把 map 包成 ApplyEnv 需要的 lookup。
func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// TestDefaults 钉住关键默认值。
//
// 为什么逐条断言而不是整体比较：这些值都有明确的设计理由（见 config.go 的
// 注释与 02-deploy.md），被无意改动会造成行为漂移。断言单点让失败的
// 那条直接指出是哪个默认值变了。
func TestDefaults(t *testing.T) {
	c := Default()
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"server.listen", c.Server.Listen, "127.0.0.1:8080"},
		{"server.max_body_bytes", c.Server.MaxBodyBytes, int64(32 << 20)},
		{"server.auto_open_browser", c.Server.AutoOpenBrowser, true},
		{"tls.mode", c.TLS.Mode, "off"},
		{"security.password_min_length", c.Security.PasswordMinLength, 12},
		{"security.session_ttl", c.Security.SessionTTL.Std(), 24 * time.Hour},
		{"security.login_max_failures", c.Security.LoginMaxFailures, 5},
		{"security.login_lockout", c.Security.LoginLockout.Std(), 15 * time.Minute},
		{"limits.default_tpm", c.Limits.DefaultTPM, int64(-1)}, // -1 = 不限
		{"limits.default_max_concurrency", c.Limits.DefaultMaxConcurrency, 2},
		{"limits.global_concurrency", c.Limits.GlobalConcurrency, 200},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v，期望 %v（默认值被改动）", tc.name, tc.got, tc.want)
		}
	}
	// 默认 Listen 必须是回环：公网绑定是显式动作（02-deploy 形态 A 的定案）。
	if !c.IsLoopbackListen() {
		t.Errorf("默认应该只监听回环，实际 listen=%q", c.Server.Listen)
	}
}

// TestMergeYAML 覆盖「YAML 覆盖默认值」这条主路径。
func TestMergeYAML(t *testing.T) {
	c := Default()
	yaml := []byte(`
server:
  listen: 0.0.0.0:9000
  max_body_bytes: 1048576
  read_header_timeout: 5s
tls:
  mode: files
  cert_file: /etc/fr/fullchain.pem
  key_file: /etc/fr/privkey.pem
security:
  session_ttl: 12h
  password_min_length: 16
`)
	if err := c.mergeYAML(yaml); err != nil {
		t.Fatalf("解析 YAML 失败: %v", err)
	}
	if c.Server.Listen != "0.0.0.0:9000" {
		t.Errorf("listen 未被覆盖: %q", c.Server.Listen)
	}
	if c.Server.MaxBodyBytes != 1048576 {
		t.Errorf("max_body_bytes 未被覆盖: %d", c.Server.MaxBodyBytes)
	}
	// duration 走自定义 UnmarshalYAML，必须能被解析成 5s。
	if c.Server.ReadHeaderTimeout.Std() != 5*time.Second {
		t.Errorf("read_header_timeout = %v，期望 5s", c.Server.ReadHeaderTimeout.Std())
	}
	if c.TLS.Mode != "files" || c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
		t.Errorf("TLS 字段未被覆盖: %+v", c.TLS)
	}
	if c.Security.SessionTTL.Std() != 12*time.Hour {
		t.Errorf("session_ttl = %v，期望 12h", c.Security.SessionTTL.Std())
	}
	// 未在 YAML 里出现的字段必须保留默认值（而不是被零值覆盖）。
	if c.Limits.GlobalConcurrency != 200 {
		t.Errorf("未提及的字段被改动了: global_concurrency=%d", c.Limits.GlobalConcurrency)
	}
}

// TestApplyEnv 覆盖环境变量层（注入 lookup，不依赖真实进程环境）。
func TestApplyEnv(t *testing.T) {
	c := Default()
	err := c.ApplyEnv(envMap(map[string]string{
		"FR_SERVER_LISTEN":                 "127.0.0.1:7777",
		"FR_SERVER_READ_HEADER_TIMEOUT":    "3s",
		"FR_SECURITY_SESSION_TTL":          "8h",
		"FR_LIMITS_DEFAULT_RPM":            "120",
		"FR_SERVER_AUTO_OPEN_BROWSER":      "false",
		"FR_SECURITY_REGISTRATION_ENABLED": "false",
	}))
	if err != nil {
		t.Fatalf("ApplyEnv 失败: %v", err)
	}
	if c.Server.Listen != "127.0.0.1:7777" {
		t.Errorf("listen 未被环境变量覆盖: %q", c.Server.Listen)
	}
	if c.Server.ReadHeaderTimeout.Std() != 3*time.Second {
		t.Errorf("duration 未被覆盖: %v", c.Server.ReadHeaderTimeout.Std())
	}
	if c.Security.SessionTTL.Std() != 8*time.Hour {
		t.Errorf("session_ttl 未被覆盖: %v", c.Security.SessionTTL.Std())
	}
	if c.Limits.DefaultRPM != 120 {
		t.Errorf("int 未被覆盖: %d", c.Limits.DefaultRPM)
	}
	if c.Server.AutoOpenBrowser {
		t.Error("bool 未被覆盖（应为 false）")
	}
	if c.Security.RegistrationEnabled {
		t.Error("bool 未被覆盖（应为 false）")
	}
}

// TestApplyEnvRejectsBadValue 非法值必须报错并指出环境变量名。
//
// 为什么这条重要：静默忽略一个非法环境变量会让运维以为设置生效了。
// 错误消息必须带变量名，否则在几十个 FR_ 变量里无从定位。
func TestApplyEnvRejectsBadValue(t *testing.T) {
	c := Default()
	err := c.ApplyEnv(envMap(map[string]string{"FR_SECURITY_SESSION_TTL": "不是时长"}))
	if err == nil {
		t.Fatal("非法 duration 应报错")
	}
	if !strings.Contains(err.Error(), "FR_SECURITY_SESSION_TTL") {
		t.Errorf("错误消息应包含变量名，实际: %v", err)
	}
}

// TestValidateRejects 表驱动覆盖各条拒绝分支。
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string // 错误消息里应出现的片段
	}{
		{"tls files 缺证书", func(c *Config) { c.TLS.Mode = "files" }, "tls.cert_file"},
		{"tls acme 不支持", func(c *Config) { c.TLS.Mode = "acme" }, "acme"},
		{"tls 非法值", func(c *Config) { c.TLS.Mode = "whatever" }, "tls.mode"},
		{"public listen 非法", func(c *Config) { c.Server.InsecurePublicListen = "ignore" }, "insecure_public_listen"},
		{"listen 不是 host:port", func(c *Config) { c.Server.Listen = "localhost" }, "server.listen"},
		{"sidecar 非 stdio", func(c *Config) { c.Sidecar.IPC = "tcp" }, "sidecar.ipc"},
		{"session_ttl 非正", func(c *Config) { c.Security.SessionTTL = 0 }, "session_ttl"},
		{"口令最小长度过小", func(c *Config) { c.Security.PasswordMinLength = 4 }, "password_min_length"},
		{"登录失败阈值 <1", func(c *Config) { c.Security.LoginMaxFailures = 0 }, "login_max_failures"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("期望拒绝，实际通过")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误消息应含 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// TestValidateAcceptsDefaults：默认配置必须本身合法（否则新装即挂）。
func TestValidateAcceptsDefaults(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("默认配置应合法，实际: %v", err)
	}
}

// TestValidateAccumulates 多条错误应一次性全部报出。
//
// 为什么要求累积：部署时同时配错两项，如果只报第一项，运维改完重启又撞第二项，
// 一轮轮试错。一次性列出全部是"少一次重启"的差别。
func TestValidateAccumulates(t *testing.T) {
	c := Default()
	c.TLS.Mode = "acme"
	c.Security.PasswordMinLength = 1
	c.Server.InsecurePublicListen = "bad"
	err := c.Validate()
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	for _, want := range []string{"acme", "password_min_length", "insecure_public_listen"} {
		if !strings.Contains(msg, want) {
			t.Errorf("应累积报出 %q，实际: %s", want, msg)
		}
	}
}

// TestEnvNameFor 钉住路径 → 环境变量名的映射（文档与 --print-config 依赖它）。
func TestEnvNameFor(t *testing.T) {
	cases := map[string]string{
		EnvNameFor("server", "listen"):               "FR_SERVER_LISTEN",
		EnvNameFor("security", "session_ttl"):        "FR_SECURITY_SESSION_TTL",
		EnvNameFor("limits", "default_daily_tokens"): "FR_LIMITS_DEFAULT_DAILY_TOKENS",
		EnvNameFor("upstream", "relay", "hub_path"):  "FR_UPSTREAM_RELAY_HUB_PATH",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("env 名 = %q，期望 %q", got, want)
		}
	}
}
