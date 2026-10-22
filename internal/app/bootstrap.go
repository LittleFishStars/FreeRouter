package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LittleFishStars/FreeRouter/internal/auth"
	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// bootstrap.go —— 首次启动自举：随机口令 + 首个管理员 + 口令落盘。
//
// 为什么需要它（2026-10-23 定案）：首次启动的体验应当是「直接可用」，
// 而不是先让用户在网页上填一个用户名和口令。进程自己生成一串一次性口令、
// 建好管理员、打印到终端并打开控制台，用户拿这串口令登录后，被强制改成
// 自己的口令并补全显示名——Bootstrap 只负责「第一跳」，之后的一切都回到
// 正常的登录/会话路径上。
//
// 与 /api/setup/init 向导的关系：向导保留作为兜底（例如运维清空账户表后
// 手动触发），但正常首启不再需要它。两条路径都不允许在「已有账户」时
// 重复建号（CountAccounts 判定），防二次初始化劫持同一条纪律。
//
// 口令只出现两次：终端 banner（main 打印）与 initial-password.txt（0600）。
// 不写日志、不进审计——日志会被转发与长期保留，一次性口令留在那里就是
// 一条长期有效的弱凭据。改密成功后该文件由 removeBootstrapPasswordFile 删除。

// bootstrapAdminUsername 是自举管理员的登录名。
//
// 固定为 admin 而不是随机：登录名不是秘密（口令才是），固定的名字让终端
// banner、口令文件与运维文档能写死在同一个词上，少一个需要抄写的变量。
const bootstrapAdminUsername = "admin"

// bootstrapDisplayName 是自举管理员的初始显示名（用户登录后可改）。
const bootstrapDisplayName = "管理员"

// BootstrapResult 是本次自举创建的凭据。仅在「库中 0 账户」时非 nil。
type BootstrapResult struct {
	Username     string
	Password     string
	PasswordFile string
	ListenURL    string // 控制台入口（main 填；装配层不知道监听地址）
}

// Bootstrap 返回首启自举的结果（已初始化时为 nil）。main 据此打印 banner。
func (a *App) Bootstrap() *BootstrapResult { return a.bootstrap }

// bootstrapAdmin 在账户表为空时创建首个管理员。
//
// 顺序：先算一次 CountAccounts（唯一窗口判定）→ 生成口令 → 建默认组 →
// 建账户（admin+tenant，must_change_password=1）→ 入默认组 → 写口令文件。
// 任何一步存储失败都返回错误：库都写不进去的实例不该假装初始化完成。
// 口令文件写失败只警告——终端 banner 已经给了口令，文件是二次找回手段。
func (a *App) bootstrapAdmin(ctx context.Context) error {
	n, err := a.store.Repos().Accounts.CountAccounts(ctx)
	if err != nil {
		return fmt.Errorf("统计账户数失败: %w", err)
	}
	if n > 0 {
		return nil
	}

	password, err := auth.GenerateOneTimePassword()
	if err != nil {
		return fmt.Errorf("生成初始口令失败: %w", err)
	}

	// 默认组必须先于账户存在（03-groups §5）：首个管理员也要有策略来源，
	// 否则他建不出第一把 Key（can_create_key 的 fail-closed 判定会拒绝）。
	// 与向导路径 setupStoreAdapter.CreateFirstAdmin 是同一顺序。
	gid, err := a.store.EnsureDefaultGroup(ctx, a.cfg.Limits.DefaultKeysPerAccount)
	if err != nil {
		return fmt.Errorf("创建默认组失败: %w", err)
	}

	hash, err := auth.NewPasswordHasher(a.cfg.Security.PasswordMinLength).Hash(password)
	if err != nil {
		return fmt.Errorf("哈希初始口令失败: %w", err)
	}

	id, err := a.store.Repos().Accounts.CreateAccount(ctx, store.AccountAuth{
		Account: store.Account{
			DisplayName:        bootstrapDisplayName,
			Username:           bootstrapAdminUsername,
			Status:             store.AccountActive,
			MustChangePassword: true,
		},
		PasswordHash: hash,
	}, []store.Class{store.ClassAdmin, store.ClassTenant})
	if err != nil {
		return fmt.Errorf("创建首个管理员失败: %w", err)
	}
	if err := a.store.Repos().Groups.AddMember(ctx, gid, id); err != nil {
		return fmt.Errorf("首个管理员入默认组失败: %w", err)
	}

	res := &BootstrapResult{
		Username: bootstrapAdminUsername,
		Password: password,
		// 口令文件里也要能直接读到一个可点的入口；main 在监听后会覆盖成
		// 实际地址（回环/通配端口归一化），两者端口一致。
		ListenURL: a.bootstrapListenURL(),
	}
	res.PasswordFile = a.writeBootstrapPasswordFile(res)
	if res.PasswordFile != "" {
		a.bootstrapPasswordFile = res.PasswordFile
	}
	a.bootstrap = res
	a.log.Info("首次启动：已创建管理员账户（口令见终端 banner 与口令文件）",
		"username", res.Username, "password_file", res.PasswordFile)
	return nil
}

// bootstrapPasswordPath 返回首启口令文件路径：库目录下的 initial-password.txt。
//
// 为什么与库同目录：数据目录是运维唯一要备份/迁移的目录，凭据文件跟着库走
// 不会在迁移后失联；库路径的目录由用户配置（data/ 是默认值）。
func (a *App) bootstrapPasswordPath() string {
	dir := filepath.Dir(a.cfg.Store.Path)
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, "initial-password.txt")
}

// writeBootstrapPasswordFile 写口令文件（0600）。返回实际路径；失败返回空串。
//
// 为什么不返回错误：终端 banner 已给出同一串口令，文件只是找回手段；
// 在「服务已经能启动」这一点上它不该成为阻塞项。
func (a *App) writeBootstrapPasswordFile(res *BootstrapResult) string {
	path := a.bootstrapPasswordPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.log.Warn("首启口令文件目录创建失败（口令仍在终端 banner 中）", "path", path, "err", err)
		return ""
	}
	body := fmt.Sprintf(`FreeRouter 首次启动凭据
========================

用户名：%s
初始口令：%s

控制台：%s
（若从另一台机器访问，请把 127.0.0.1 换成实际的监听地址/域名）

登录后系统会强制要求：
  1. 修改口令（改成你自己的口令）；
  2. 补全显示名（账户信息）。

改密成功后本文件会被自动删除。若该文件丢失且尚未改密，请停止服务后
删除数据库重新初始化（会丢失数据），或按运维文档手工重置管理员口令。
`, res.Username, res.Password, res.ListenURL)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		a.log.Warn("首启口令文件写入失败（口令仍在终端 banner 中）", "path", path, "err", err)
		return ""
	}
	// os.WriteFile 的 0600 只在新建时生效；显式 chmod 兜住「文件已存在且权限更宽」
	// 的路径（例如上次运行留下的同名文件被手工改过权限）。
	if err := os.Chmod(path, 0o600); err != nil {
		a.log.Warn("首启口令文件权限收紧失败", "path", path, "err", err)
	}
	return path
}

// bootstrapListenURL 推导首启文案里的控制台地址（public_url 优先）。
func (a *App) bootstrapListenURL() string {
	if a.cfg.Server.PublicURL != "" {
		return strings.TrimRight(a.cfg.Server.PublicURL, "/") + "/"
	}
	scheme := "http"
	if strings.EqualFold(a.cfg.TLS.Mode, "files") {
		scheme = "https"
	}
	return scheme + "://" + a.cfg.Server.Listen + "/"
}

// removeBootstrapPasswordFile 在改密成功后删除首启口令文件（幂等）。
//
// 为什么由改密成功触发而不是启动时清理：用户可能还没登录就重启，
// 启动时删除会把唯一的找回手段弄丢；而「已改密」是口令不再有效的可靠信号。
func (a *App) removeBootstrapPasswordFile() {
	path := a.bootstrapPasswordFile
	if path == "" {
		path = a.bootstrapPasswordPath()
	}
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			a.log.Warn("删除首启口令文件失败", "path", path, "err", err)
		}
		return
	}
	a.log.Info("首启口令已废弃，口令文件已删除", "path", path)
}

// bootstrapBanner 生成终端展示的首次启动横幅（main 打印）。
//
// 放在装配层而不是 main：口令与口令文件路径都从这里产生，
// 让 main 只负责「打印」与「打开浏览器」两件 I/O 事。
func (a *App) BootstrapBanner() string {
	res := a.bootstrap
	if res == nil {
		return ""
	}
	url := res.ListenURL
	if url == "" {
		url = "http://" + a.cfg.Server.Listen + "/"
	}
	return fmt.Sprintf(`
================================================================
  FreeRouter 首次启动：已创建管理员账户
----------------------------------------------------------------
  用户名    : %s
  初始口令  : %s
  控制台    : %s
  口令文件  : %s

  首次登录会强制要求修改口令并补全账户信息。
  改密成功后口令文件自动删除；请勿把此屏截图外传。
================================================================
`, res.Username, res.Password, url, res.PasswordFile)
}
