// Package apppath 统一计算应用的 per-user 目录与文件路径。
//
// 为什么单独一个包：配置（settings.json）、日志（logs/）、数据库（data.db）都放在
// 同一个应用目录下，但分别由 internal/settings、internal/logging、internal/store
// 使用。若各自写一遍目录拼接，一旦目录规则调整就要改三处，且很容易改漏。
//
// 两类目录，规则不同：
//
//	配置目录（roaming）：settings.json 这种几百字节、需要跟着用户漫游的偏好。
//	数据目录（本机）  ：data.db 与 logs/。数据库文件放进漫游目录是个坑——
//	                    Windows 域环境里 %APPDATA% 会被漫游配置文件同步，
//	                    SQLite 的 WAL 文件在同步/锁文件时容易冲突，登录也会变慢。
//
// 目录布局（Windows 为例）：
//
//	%APPDATA%\bestuav-atlas\settings.json                 配置（roaming）
//	%LOCALAPPDATA%\bestuav-atlas\data.db                  数据库（本机）
//	%LOCALAPPDATA%\bestuav-atlas\logs\app-YYYY-MM-DD.log   日志（本机）
//
// macOS：~/Library/Application Support/bestuav-atlas（配置与数据同处，符合平台惯例）
// Linux：~/.config/bestuav-atlas（配置）、~/.local/share/bestuav-atlas（数据）
package apppath

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"bestuav-atlas/internal/appmeta"
)

// ConfigDir 返回本应用的配置目录：<用户配置目录>/<产品名>。
func ConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("无法定位配置目录: %w", err)
		}
		dir = home
	}
	return filepath.Join(dir, appmeta.Name), nil
}

// DataDir 返回本机的数据目录：<本机应用数据目录>/<产品名>。
//
// 与 ConfigDir 分开：数据库与日志不该跟着漫游配置走（见包注释）。
func DataDir() (string, error) {
	base, err := localDataBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appmeta.Name), nil
}

// localDataBase 返回平台的本机数据根目录。
func localDataBase() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA% 是 Windows 的「本机」配置目录，不参与漫游。
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return os.UserConfigDir() // 极端情况下兜底
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法定位用户目录: %w", err)
		}
		// macOS 没有「本机 vs 漫游」之分，Application Support 就是标准位置。
		return filepath.Join(home, "Library", "Application Support"), nil
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return d, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法定位用户目录: %w", err)
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

// LogDir 返回日志目录：<数据目录>/logs。
func LogDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// DBPath 返回 SQLite 数据库文件路径：<数据目录>/data.db。
func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "data.db"), nil
}

// EnsureDir 创建目录（含父级），已存在时返回 nil。
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// MigrateLegacy 把旧版放在配置目录下的 data.db 与 logs/ 挪到数据目录。
//
// 旧版把三类文件都放在 <配置目录> 下；升级后数据库和日志改到本机数据目录，
// 不迁移就等于「历史任务、授权记录、日志全没了」。只在新位置还没有文件时搬，
// 搬不动（跨盘、被占用）就保留旧文件由用户手动处理，不阻断启动。
// 返回已迁移的项目名（便于写日志）。
func MigrateLegacy() []string {
	oldDir, err := ConfigDir()
	if err != nil {
		return nil
	}
	newDir, err := DataDir()
	if err != nil || samePath(oldDir, newDir) {
		return nil
	}
	var moved []string
	// 数据库：三件套必须一起搬（原因见 migrateDBFiles）。
	moved = append(moved, migrateDBFiles(oldDir, newDir)...)
	// 日志目录：整目录搬过去（里面是按天轮转的文件）。
	oldLogs := filepath.Join(oldDir, "logs")
	newLogs := filepath.Join(newDir, "logs")
	if _, err := os.Stat(oldLogs); err == nil {
		if _, err := os.Stat(newLogs); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(newLogs), 0o755); err == nil {
				if err := os.Rename(oldLogs, newLogs); err == nil {
					moved = append(moved, "logs/")
				}
			}
		}
	}
	return moved
}

// migrateDBFiles 把 SQLite 库的三件套（data.db / -wal / -shm）作为一组搬到新目录，
// 任一失败就整体回滚。返回实际搬成功的文件名。
//
// 为什么必须成组：WAL 模式下「已提交」的事务可能还在 -wal 里。只把 data.db 搬过去、
// 把 -wal 留在旧目录，新位置打开时读到的是「少了最近若干次提交」的库——用户看到的
// 是数据凭空回退，而且全程没有任何报错（这种问题几乎无法从日志追溯）。
// 所以要么三个都搬成，要么一个都不动。
func migrateDBFiles(oldDir, newDir string) []string {
	const mainName = "data.db"
	names := []string{mainName, "data.db-wal", "data.db-shm"}

	if _, err := os.Stat(filepath.Join(oldDir, mainName)); err != nil {
		return nil // 旧位置没有库：没什么可迁
	}
	if _, err := os.Stat(filepath.Join(newDir, mainName)); err == nil {
		return nil // 新位置已有库：不覆盖（用户可能已经在用新版）
	}

	var moved []string
	for _, name := range names {
		src := filepath.Join(oldDir, name)
		if _, err := os.Stat(src); err != nil {
			continue // 边车文件不一定存在（上次是干净退出就没有）
		}
		if err := moveFile(src, filepath.Join(newDir, name)); err != nil {
			// 回滚：把已经搬过去的搬回旧位置，保证旧位置仍是完整的一组。
			for _, done := range moved {
				_ = moveFile(filepath.Join(newDir, done), filepath.Join(oldDir, done))
			}
			return nil
		}
		moved = append(moved, name)
	}
	return moved
}

// moveFile 尽力搬移一个文件：优先 rename（同盘瞬时），失败则退化为复制 + 删除。
func moveFile(oldPath, newPath string) error {
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err == nil {
		return nil
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(newPath, data, 0o644); err != nil {
		return err
	}
	return os.Remove(oldPath)
}

// samePath 判断两个路径是否指向同一目录（比较清理后的绝对路径）。
func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}
