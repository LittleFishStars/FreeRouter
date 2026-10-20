package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MasterKeySize 是主密钥的字节数（AES-256）。
const MasterKeySize = 32

// masterKeyPerm 是主密钥文件唯一允许的权限位。
//
// 为什么只允许 0600：主密钥解开 upstream_secrets 里的全部上游凭据。任何组/他人
// 可读位都意味着"同机另一个用户能拿走所有凭据"，而这类问题一旦成为既成事实，
// 无法通过改代码补救——只能让全部贡献者重新提交。所以这里用"拒绝启动"而不是
// "警告后继续"：能启动的进程会一直跑下去，警告会被忽略。
const masterKeyPerm os.FileMode = 0o600

// ErrMasterKeyPerm 表示主密钥文件权限过宽，调用方可用 errors.Is 判定后给出运维提示。
var ErrMasterKeyPerm = errors.New("store: 主密钥文件权限过宽")

// LoadOrCreateMasterKey 读取主密钥；文件不存在时生成 32 字节随机密钥并以 0600 写入。
//
// 行为约定：
//   - path 为空 → 返回错误。主密钥没有默认路径：默认路径等于"随库一起被备份"，
//     而 05-storage.md 8.2 明确要求两者分开存放。
//   - 文件存在且权限恰好 0600 → 读取；内容长度必须正好 32 字节。
//   - 文件存在但权限过宽（任何组/他人位，例如 0644、0640、0666）→ 拒绝并给修复命令。
//   - 文件不存在 → os.OpenFile 带 O_EXCL 创建，从根上避免"两个进程同时生成两把密钥、
//     后写的覆盖先写的"——那会让先启动的进程拿着已经作废的密钥继续工作。
//
// 主密钥永不入库：落到同一个库里等于把锁和钥匙放进同一个抽屉（PLAN 9 / 05-storage.md 1）。
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New(
			"store: 未配置 master_key_file。主密钥必须独立于数据库存放（05-storage.md 8.2），" +
				"请设置 store.master_key_file，例如 /etc/freerouter/master.key")
	}

	key, err := readMasterKey(path)
	switch {
	case err == nil:
		return key, nil
	case errors.Is(err, os.ErrNotExist):
		return createMasterKey(path)
	default:
		return nil, err
	}
}

// readMasterKey 读取并校验既有主密钥。
func readMasterKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("store: 主密钥路径 %s 是一个目录，应为普通文件", path)
	}

	// 先查权限再读内容：权限不对时连内容都不该取进内存。
	if perm := info.Mode().Perm(); perm != masterKeyPerm {
		return nil, fmt.Errorf(
			"%w：%s 的权限是 %04o，必须是 0600。修复：chmod 600 %s"+
				"（组/他人可读意味着同机另一个用户能解出全部上游凭据）",
			ErrMasterKeyPerm, path, perm, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("store: 读取主密钥 %s 失败: %w", path, err)
	}

	// 长度必须精确：允许变长会让"换了一把长度不同的密钥"静默通过，直到解密时
	// 才以 AES-GCM 认证失败的形式暴露，那时错误信息离真正的原因已经隔了很远。
	if len(data) != MasterKeySize {
		return nil, fmt.Errorf(
			"store: 主密钥 %s 的长度是 %d 字节，必须是 %d 字节（AES-256）。"+
				"注意用 openssl rand -out %s %d 生成，不要用 echo 追加换行",
			path, len(data), MasterKeySize, path, MasterKeySize)
	}

	out := make([]byte, MasterKeySize)
	copy(out, data)
	return out, nil
}

// createMasterKey 生成并落盘一把新主密钥。
func createMasterKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: 创建主密钥目录失败: %w", err)
	}

	key := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("store: 生成主密钥随机数失败: %w", err)
	}

	// O_EXCL：文件已存在就失败。并发启动的两个进程里只会有一个成功创建，
	// 另一个会走 readMasterKey 读到同一把密钥——而不是各自生成一把然后互相覆盖。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, masterKeyPerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return readMasterKey(path)
		}
		return nil, fmt.Errorf("store: 创建主密钥 %s 失败: %w", path, err)
	}

	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		// 半写的密钥文件比没有更危险：它会让下次启动读到错误长度而报"长度不对"，
		// 掩盖真正的原因（磁盘满/权限问题）。所以失败时把它删掉。
		_ = os.Remove(path)
		return nil, fmt.Errorf("store: 写入主密钥 %s 失败: %w", path, err)
	}

	// fsync：密钥必须真正落盘，否则断电后会得到"库里有密文、密钥没了"的局面，
	// 而这正是 8.2 想避免的最坏情况。
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("store: 同步主密钥 %s 失败: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("store: 关闭主密钥 %s 失败: %w", path, err)
	}

	// 某些文件系统/umask 组合下 OpenFile 的 mode 会被削，这里再确认一次：
	// 创建出来的密钥文件必须就是 0600，否则下一轮启动会被自己的校验拒绝。
	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm != masterKeyPerm {
			if err := os.Chmod(path, masterKeyPerm); err != nil {
				return nil, fmt.Errorf(
					"store: 主密钥 %s 权限为 %04o 且修正为 0600 失败: %w", path, perm, err)
			}
		}
	}

	return key, nil
}
