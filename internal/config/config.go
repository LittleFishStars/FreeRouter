// Package config 定义 FreeRouter 的配置结构、默认值、YAML 加载与 FR_ 环境变量覆盖。
//
// 唯一事实源：docs/ops/02-deploy.md 第 2 节「配置项全表」。
// 本文件只做结构与取值，不做业务判断（业务校验见各子系统的 Validate）。
//
// 约定：
//   - YAML 键全小写，层级用 `.`；环境变量 = `FR_` + 层级大写、`.` 换成 `_`。
//   - 环境变量永远覆盖 YAML（部署时覆盖镜像内配置是常态）。
//   - 默认值写在本文件的 Default()，文档表格里的「默认」列必须与它一致。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 是 time.Duration 的 YAML 包装：允许写 "10s" / "24h" 这类人类可读值。
// 为什么不用 int 秒：配置表里全是 duration 语义，写成难以校对的秒数会引入量级错误。
type Duration time.Duration

// UnmarshalYAML 支持字符串形式（"30s"）与裸数字（按纳秒，兼容 machine 生成）。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err == nil {
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("非法 duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if err := node.Decode(&n); err != nil {
		return fmt.Errorf("duration 必须是字符串（如 \"30s\"）或整数纳秒")
	}
	*d = Duration(time.Duration(n))
	return nil
}

// MarshalYAML 输出可读字符串，保证 --print-config 的结果能直接改回配置文件。
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// String 实现 fmt.Stringer。
func (d Duration) String() string { return time.Duration(d).String() }

// Std 返回标准库类型，供业务代码使用。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config 是整份配置的根。
type Config struct {
	Server   Server   `yaml:"server"`
	TLS      TLS      `yaml:"tls"`
	Security Security `yaml:"security"`
	Limits   Limits   `yaml:"limits"`
	Upstream Upstream `yaml:"upstream"`
	Sidecar  Sidecar  `yaml:"sidecar"`
	Store    Store    `yaml:"store"`
	Log      Log      `yaml:"log"`
}

// Server 见 docs/ops/02-deploy.md §2.1。
type Server struct {
	Listen               string   `yaml:"listen"`
	PublicURL            string   `yaml:"public_url"`
	InsecurePublicListen string   `yaml:"insecure_public_listen"` // warn | deny
	BehindProxy          bool     `yaml:"behind_proxy"`
	ReadHeaderTimeout    Duration `yaml:"read_header_timeout"`
	IdleTimeout          Duration `yaml:"idle_timeout"`
	MaxBodyBytes         int64    `yaml:"max_body_bytes"`
	ShutdownGrace        Duration `yaml:"shutdown_grace"`
}

// TLS 见 §2.2。acme 本版不支持，配置校验直接拒绝。
type TLS struct {
	Mode     string `yaml:"mode"` // off | files
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Security 见 §2.3。
type Security struct {
	TrustedProxies               []string `yaml:"trusted_proxies"`
	PasswordMinLength            int      `yaml:"password_min_length"`
	SessionTTL                   Duration `yaml:"session_ttl"`
	LoginMaxFailures             int      `yaml:"login_max_failures"`
	LoginLockout                 Duration `yaml:"login_lockout"`
	RegistrationEnabled          bool     `yaml:"registration_enabled"`
	RegistrationRequiresApproval bool     `yaml:"registration_requires_approval"`
}

// Limits 见 §2.4。0 的语义按字段注释而定，不统一。
type Limits struct {
	DefaultDailyTokens    int64    `yaml:"default_daily_tokens"`   // 0 = 不可用
	DefaultMonthlyTokens  int64    `yaml:"default_monthly_tokens"` // 0 = 不可用
	DefaultRPM            int      `yaml:"default_rpm"`
	DefaultTPM            int64    `yaml:"default_tpm"` // 0 = 不限制
	DefaultMaxConcurrency int      `yaml:"default_max_concurrency"`
	DefaultKeysPerAccount int      `yaml:"default_keys_per_account"`
	GlobalDailyTokens     int64    `yaml:"global_daily_tokens"` // 0 = 不熔断
	GlobalRPM             int      `yaml:"global_rpm"`
	GlobalConcurrency     int      `yaml:"global_concurrency"`
	QueueTimeout          Duration `yaml:"queue_timeout"`
}

// Upstream 见 §2.5。
type Upstream struct {
	Relay Relay `yaml:"relay"`
}

// Relay 是 relay 类上游（providers-hub 账号池）的参数。
type Relay struct {
	Enabled              bool     `yaml:"enabled"`
	Timeout              Duration `yaml:"timeout"`
	ConnectTimeout       Duration `yaml:"connect_timeout"`
	MaxRetries           int      `yaml:"max_retries"`
	ModelRefreshInterval Duration `yaml:"model_refresh_interval"`
	AggregateExcluded    bool     `yaml:"aggregate_excluded"`
	Allowlist            []string `yaml:"allowlist"`
}

// Sidecar 见 §2.6。
type Sidecar struct {
	Enabled        bool     `yaml:"enabled"`
	NodePath       string   `yaml:"node_path"`
	Entry          string   `yaml:"entry"`
	IPC            string   `yaml:"ipc"` // 固定 stdio
	HubPath        string   `yaml:"hub_path"`
	StartupTimeout Duration `yaml:"startup_timeout"`
}

// Store 见 §2.7。
type Store struct {
	Path                  string   `yaml:"path"`
	MasterKeyFile         string   `yaml:"master_key_file"`
	BusyTimeout           Duration `yaml:"busy_timeout"`
	BackupDir             string   `yaml:"backup_dir"`
	AuditRetainDays       int      `yaml:"audit_retain_days"`
	RequestLogRetainDays  int      `yaml:"request_log_retain_days"`
}

// Log 是文档配置表未列出的实现补充段：日志级别与格式。
// 为什么需要：M0 的验收要求「--print-config 输出可读配置」，而排障第一件事就是调日志。
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Default 返回与部署文档「默认」列逐项一致的配置。
func Default() *Config {
	return &Config{
		Server: Server{
			Listen:               "127.0.0.1:8080",
			PublicURL:            "",
			InsecurePublicListen: "warn",
			BehindProxy:          false,
			ReadHeaderTimeout:    Duration(10 * time.Second),
			IdleTimeout:          Duration(120 * time.Second),
			MaxBodyBytes:         32 << 20, // 32 MiB
			ShutdownGrace:        Duration(30 * time.Second),
		},
		TLS: TLS{Mode: "off"},
		Security: Security{
			TrustedProxies:               []string{},
			PasswordMinLength:            12,
			SessionTTL:                   Duration(24 * time.Hour),
			LoginMaxFailures:             5,
			LoginLockout:                 Duration(15 * time.Minute),
			RegistrationEnabled:          true,
			RegistrationRequiresApproval: true,
		},
		Limits: Limits{
			DefaultDailyTokens:    0,
			DefaultMonthlyTokens:  0,
			DefaultRPM:            60,
			DefaultTPM:            0,
			DefaultMaxConcurrency: 2,
			DefaultKeysPerAccount: 10,
			GlobalDailyTokens:     0,
			GlobalRPM:             0,
			GlobalConcurrency:     200,
			QueueTimeout:          Duration(5 * time.Second),
		},
		Upstream: Upstream{
			Relay: Relay{
				Enabled:              true,
				Timeout:              Duration(300 * time.Second),
				ConnectTimeout:       Duration(5 * time.Second),
				MaxRetries:           2,
				ModelRefreshInterval: Duration(10 * time.Minute),
				AggregateExcluded:    true,
				Allowlist:            []string{},
			},
		},
		Sidecar: Sidecar{
			Enabled:        true,
			NodePath:       "node",
			Entry:          "sidecar/index.mjs",
			IPC:            "stdio",
			HubPath:        "",
			StartupTimeout: Duration(20 * time.Second),
		},
		Store: Store{
			Path:                 "data/freerouter.db",
			MasterKeyFile:        "",
			BusyTimeout:          Duration(5 * time.Second),
			BackupDir:            "data/backups",
			AuditRetainDays:      0,
			RequestLogRetainDays: 30,
		},
		Log: Log{Level: "info", Format: "text"},
	}
}

// Load 读取 YAML 文件并叠加环境变量覆盖。
// path 为空时只用默认值 + 环境变量（容器里用环境变量配置是常见形态）。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		if err := cfg.mergeYAML(raw); err != nil {
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	if err := cfg.ApplyEnv(os.LookupEnv); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeYAML 把 YAML 覆盖到已有默认值上（而不是替换整个结构）：
// 只写三项的配置文件不应把其余默认值清成零值。
func (c *Config) mergeYAML(raw []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // 拼错的键必须报错，否则会静默用默认值运行
	if err := dec.Decode(c); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Marshal 输出可读 YAML，供 --print-config 使用。
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// IsLoopbackListen 判断监听地址是否只绑回环。
// 为什么单独一个函数：初始化向导与「公网监听必须 TLS」两条规则都依赖它。
func (c *Config) IsLoopbackListen() bool {
	host, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return false
	}
	if host == "" {
		return false // ":8080" 是通配地址，不是回环
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
