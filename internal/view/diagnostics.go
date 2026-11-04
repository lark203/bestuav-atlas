package view

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"bestuav-atlas/internal/apppath"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/store"
)

// 本文件是「支持与诊断」：把用户环境、配置、日志与数据库快照打成一个 zip。
//
// 为什么需要：用户报障时最常听到的是「它自己坏了」。让他去 %APPDATA% 里找
// 三个不同位置的文件并分辨哪个该发，基本等于让他放弃。这里一键打包，
// 支持人员拿到一个文件就能复现与定位（版本、系统、路径、错误计数、最近日志、
// 库里的任务/授权记录）。

// diagLogDays 诊断包里只带最近几天的日志（日志本身按天轮转、保留 7 天）。
const diagLogDays = 3

// openConfigDir 打开 per-user 配置目录（settings.json 所在处）。
func (a *App) openConfigDir() {
	dir, err := apppath.ConfigDir()
	if err != nil {
		a.toast("定位配置目录失败：" + err.Error())
		return
	}
	if err := mygo.Shell.OpenPath(dir); err != nil {
		a.toast("打开配置目录失败：" + err.Error())
	}
}

// openLogDir 打开日志目录。
func (a *App) openLogDir() {
	dir, err := apppath.LogDir()
	if err != nil {
		a.toast("定位日志目录失败：" + err.Error())
		return
	}
	if err := mygo.Shell.OpenPath(dir); err != nil {
		a.toast("打开日志目录失败：" + err.Error())
	}
}

// exportDiagnostics 导出诊断包，成功后打开所在目录并提示完整路径。
//
// 真正的打包（VACUUM INTO 快照 + 压缩 + 日志打包）放在后台协程：库快照要复制整库、
// 日志分片后也可能有几十 MB，在主线程做会卡住界面（实测 1000 条任务时约 17ms，
// 库再大就更久）。
//
// 注意「先在主线程取状态」：App 的字段只在主线程读写（见 persist / View 的约定），
// 后台协程直接读会构成数据竞争。所以这里只把纯数据（环境信息文本、库指针）带进去。
func (a *App) exportDiagnostics() {
	info := a.basicInfoText()
	st := a.store
	a.toast("正在导出诊断包…")
	go func() {
		defer logging.Recover("diag-export")
		path, err := writeDiagnosticsArchive(st, info)
		mygo.RunOnMain(func() {
			if err != nil {
				logging.Errv("diag", "导出诊断包失败", err)
				a.toast("导出失败：" + err.Error())
				return
			}
			logging.Audit("diag", "导出诊断包", "path", path)
			// 打开所在目录：用户接下来要做的就是把这个文件拖给支持人员。
			if oerr := mygo.Shell.OpenPath(filepath.Dir(path)); oerr != nil {
				a.toast("诊断包已导出：" + path)
				return
			}
			a.toast("诊断包已导出：" + path)
		})
	}()
}

// writeDiagnosticsArchive 生成诊断包，返回 zip 的完整路径。
//
// 不接收 *App 是刻意的：它要在后台协程里跑，而 App 的字段只在主线程读写。
// 需要的信息由调用方在主线程取好后传进来。
func writeDiagnosticsArchive(st *store.Store, info string) (string, error) {
	name := fmt.Sprintf("bestuav-atlas-diag-%s.zip", time.Now().Format("20060102-150405"))
	zipPath := filepath.Join(os.TempDir(), name)

	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	// 1) 用户偏好。
	if p, err := settings.Path(); err == nil {
		if err := addFileToZip(zw, "settings.json", p); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}

	// 2) 最近的日志文件。
	if dir, err := apppath.LogDir(); err == nil {
		if err := addRecentLogs(zw, dir); err != nil {
			return "", err
		}
	}

	// 3) 数据库一致性快照（库未启用时跳过）。
	if st != nil {
		tmp := filepath.Join(os.TempDir(), fmt.Sprintf("bestuav-atlas-diag-%d.db", time.Now().UnixNano()))
		if err := st.BackupTo(tmp); err != nil {
			return "", err
		}
		err := addFileToZip(zw, "data.snapshot.db", tmp)
		_ = os.Remove(tmp)
		if err != nil {
			return "", err
		}
	}

	// 4) 环境与版本信息（纯文本，方便直接贴进 issue）。
	if err := addBytesToZip(zw, "env.txt", []byte(info)); err != nil {
		return "", err
	}

	// 5) 说明文件。
	if err := addBytesToZip(zw, "README.txt", []byte(diagReadme)); err != nil {
		return "", err
	}

	if err := zw.Close(); err != nil {
		return "", err
	}
	return zipPath, nil
}

const diagReadme = `bestuav-atlas 诊断包

包含：
- settings.json          用户偏好（主题、字号、关闭行为、背景图路径等）
- logs/                  最近三天的运行日志（JSON，每行一条）
- data.snapshot.db       数据库一致性快照（任务 / 授权 / 事件记录）
- env.txt                版本、系统、运行时、路径与错误计数

注意：settings.json 与日志里可能包含本机文件路径等个人信息，
请只发送给你信任的支持人员。
`

// addRecentLogs 把 dir 下最近 diagLogDays 天的日志写入 zip 的 logs/ 目录。
// diagLogBudget 诊断包里日志的总字节上限。
//
// 日志现在有单文件上限并按天分片（见 internal/logging），三天加起来可能有上百 MB；
// 全塞进 zip 会让「导出诊断包」变成一个巨型下载（还要先压一遍）。
// 这里按「从新到旧」取，取到预算为止，保证新日志一定在包里。
// 变量而非常量：测试要把它调小，才能在不造 32MB 文件的前提下验证挑选逻辑。
var diagLogBudget int64 = 32 << 20

func addRecentLogs(zw *zip.Writer, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -diagLogDays)
	type cand struct {
		name string
		mod  time.Time
		size int64
	}
	var cands []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "app-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		cands = append(cands, cand{name: e.Name(), mod: info.ModTime(), size: info.Size()})
	}

	// 从新到旧挑，累计到预算为止；最新的那个无论多大都要带上（否则可能一个都没有）。
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	var picked []cand
	var total int64
	for _, c := range cands {
		if len(picked) > 0 && total+c.size > diagLogBudget {
			continue
		}
		picked = append(picked, c)
		total += c.size
	}
	// 写进 zip 时按名字升序（阅读顺序即时间顺序）。
	sort.Slice(picked, func(i, j int) bool { return picked[i].name < picked[j].name })
	for _, c := range picked {
		if err := addFileToZip(zw, "logs/"+c.name, filepath.Join(dir, c.name)); err != nil {
			return err
		}
	}
	return nil
}

// addFileToZip 把一个磁盘文件写入 zip（保持原内容）。
func addFileToZip(zw *zip.Writer, name, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

// addBytesToZip 把内存里的内容写入 zip。
func addBytesToZip(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
