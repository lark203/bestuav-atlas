package view

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/apppath"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/profile"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/store"
)

// 本文件把「配置与数据」的导出 / 导入接到界面上（方案 A）。
//
// 约定：
//   - 导入会替换 settings.json 与 data.db，必须在主线程做、且替换数据库前关掉连接；
//     所以按钮只登记 pendingImport，真正执行放到下一帧开头（见 applyPendingImport）。
//   - 导出是只读磁盘操作，放到 goroutine，完成后回主线程提示。

// profilePaths 返回导出/导入需要的三个路径。
func (a *App) profilePaths() (settingsPath, dbPath, dataDir string) {
	settingsPath, _ = settings.Path()
	dbPath, _ = apppath.DBPath()
	dataDir, _ = apppath.DataDir()
	return
}

// exportProfile 弹出保存框并导出（withData=false 时只导出设置）。
func (a *App) exportProfile(withData bool) {
	settingsPath, dbPath, _ := a.profilePaths()
	bg := a.settings.BackgroundImage
	go func() {
		defer logging.Recover("export-profile")
		name := "quantum-" + profile.DeviceName() + "-" + time.Now().Format("20060102") + profile.FileExt
		out, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出配置与数据",
			DefaultPath: filepath.Join(filepath.Dir(settingsPath), name),
			ButtonLabel: "导出",
			Filters:     []mygo.FileFilter{{Name: "配置包", Extensions: []string{"zip"}}},
		})
		if err != nil || strings.TrimSpace(out) == "" {
			return
		}
		if !strings.HasSuffix(strings.ToLower(out), ".zip") {
			out += profile.FileExt
		}
		res, err := profile.Export(profile.ExportOptions{
			Out:           out,
			SettingsPath:  settingsPath,
			DBPath:        dbPath,
			Background:    bg,
			IncludeData:   withData,
			AppID:         appmeta.Identifier,
			AppVersion:    appmeta.BuildVersion(),
			SchemaVersion: store.SchemaVersion(),
		})
		mygo.RunOnMain(func() {
			if err != nil {
				logging.Errv("profile", "导出配置与数据失败", err)
				a.toast("导出失败：" + err.Error())
				return
			}
			logging.Audit("profile", "导出配置与数据", "path", res.Path, "data", res.IncludesData)
			parts := []string{"设置"}
			if res.IncludesData {
				parts = append(parts, "数据")
			}
			if res.IncludesImage {
				parts = append(parts, "背景图")
			}
			a.toast(fmt.Sprintf("已导出（%s）%s", strings.Join(parts, " + "), formatBytes(res.Bytes)))
			a.requestFrame()
		})
	}()
}

// importProfileDialog 选择配置包；真正导入由下一帧的 applyPendingImport 完成。
func (a *App) importProfileDialog() {
	go func() {
		defer logging.Recover("import-profile")
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:       "导入配置与数据",
			ButtonLabel: "导入",
			Filters:     []mygo.FileFilter{{Name: "配置包", Extensions: []string{"zip"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		path := paths[0]
		mygo.RunOnMain(func() {
			a.pendingImport = path
			a.pendingImportTemp = false
			a.requestFrame()
		})
	}()
}

// applyPendingImport 在帧开头执行待处理的导入（主线程；会关掉数据库连接）。
func (a *App) applyPendingImport() {
	path := a.pendingImport
	if path == "" {
		return
	}
	a.pendingImport = ""
	isTemp := a.pendingImportTemp
	a.pendingImportTemp = false
	if isTemp {
		defer os.Remove(path) // 仓库下载的临时包：导入/失败后都清掉
	}
	settingsPath, dbPath, dataDir := a.profilePaths()
	res, err := profile.Import(profile.ImportOptions{
		In:            path,
		SettingsPath:  settingsPath,
		DBPath:        dbPath,
		DataDir:       dataDir,
		AppID:         appmeta.Identifier,
		SchemaVersion: store.SchemaVersion(),
		BeforeDB: func() error {
			if a.store == nil {
				return nil
			}
			err := a.store.Close()
			a.store = nil
			return err
		},
	})
	if err != nil {
		logging.Errv("profile", "导入配置与数据失败", err)
		a.toast("导入失败：" + err.Error())
		a.requestFrame()
		return
	}
	logging.Audit("profile", "导入配置与数据", "path", path,
		"data", res.ImportedData, "image", res.ImportedImage,
		"settingsBackup", res.SettingsBackup, "dbBackup", res.DBBackup)
	// 设置与数据库都被替换了：重启是最可靠的「重新加载」。
	if a.win != nil {
		mygo.App.Relaunch()
		return
	}
	a.toast("导入完成（重启应用后生效）")
	a.requestFrame()
}

// formatBytes 人类可读的大小。
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}
