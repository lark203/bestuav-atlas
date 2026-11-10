// Package profile 负责「配置与数据」的导出 / 导入包（方案 A）。
//
// 一个包就是一个 zip：
//
//	manifest.json   格式版本、应用标识、应用版本、schemaVersion、设备、平台、导出时间、内容清单
//	settings.json   只含可移植字段（设备相关字段在导出时剔除）
//	data.db         可选：VACUUM INTO 的一致性快照（任务 / 授权 / 页面状态）
//	background.<ext> 可选：背景图（导入时复制到本机数据目录并改写路径）
//	notes-media/*   可选：笔记里的图片（正文用相对路径引用）
//	README.txt      说明包含什么、如何恢复
//
// 为什么整包而不是「直接同步 data.db」：SQLite 的 WAL 不能被两台机器同时写，
// 云盘并发覆盖会损坏数据库；导入/导出是唯一安全的跨机搬运方式（见 docs 的同步方案）。
//
// 本包不联网、不加密（zip 内含授权凭证，导出时 UI 会提示）；调用方负责对话框与重启。
package profile

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"bestuav-atlas/internal/store"
)

// Format 当前包格式版本；不兼容的改动才递增（导入时要求 <= 本版本）。
const Format = 1

// FileExt 包文件扩展名。
const FileExt = ".mygo-profile.zip"

// Manifest 包清单。
type Manifest struct {
	Format        int       `json:"format"`
	App           string    `json:"app"`
	AppVersion    string    `json:"appVersion"`
	SchemaVersion string    `json:"schemaVersion"`
	Device        string    `json:"device"`
	Platform      string    `json:"platform"`
	ExportedAt    time.Time `json:"exportedAt"`
	Includes      []string  `json:"includes"`
}

// 包内文件名。
const (
	manifestName = "manifest.json"
	settingsName = "settings.json"
	dbName       = "data.db"
	readmeName   = "README.txt"
	bgPrefix     = "background"
	mediaDirName = "notes-media"
)

// 导出时剔除的「设备相关」字段：导入后沿用本机值，而不是把旧机器的设置带过来。
var machineKeys = []string{
	"openAtLogin",        // 登录项由每台机器自己确认
	"allowMultiInstance", // 单/多开是启动参数，按机器决定
	"ignoredVersion",     // 「忽略这个版本」是本地决定
	"backgroundImage",    // 绝对路径跨平台无效，导入时按包内图片重写
	// 仓库同步 / 各平台令牌都只属于本机（令牌绝不进配置包）。
	"repoKind", "repoBaseURL", "repoToken", "repoOwner", "repoRepo",
	"repoBranch", "repoPath", "repoLast",
	"githubToken", "giteeToken", "atomGitToken", "giteaHost", "giteaToken",
}

// preservedKeys 导入时保留「本机当前值」的设备相关字段：包里的这些键已被剔除，
// 若直接覆盖整个 settings.json，本机的登录项/多开/忽略版本/仓库凭据会被重置。
// backgroundImage 不在此列：它按包内是否带图来改写或清空（见 Import）。
var preservedKeys = []string{
	"openAtLogin", "allowMultiInstance", "ignoredVersion",
	"repoKind", "repoBaseURL", "repoToken", "repoOwner", "repoRepo",
	"repoBranch", "repoPath", "repoLast",
	"githubToken", "giteeToken", "atomGitToken", "giteaHost", "giteaToken",
}

// ExportOptions 导出参数。
type ExportOptions struct {
	Out           string // 目标 zip 路径
	SettingsPath  string // settings.json 路径
	DBPath        string // data.db 路径（可选）
	Background    string // 背景图路径（可选）
	MediaDir      string // 笔记图片目录（可选，导出为 notes-media/）
	IncludeData   bool   // 是否导出 data.db
	AppID         string
	AppVersion    string
	SchemaVersion string // 当前 store 的 schemaVersion
	Now           func() time.Time
}

// ExportResult 导出结果（供 UI 提示）。
type ExportResult struct {
	Path          string
	Bytes         int64
	IncludesData  bool
	IncludesImage bool
	IncludesMedia bool // 是否带上了笔记图片
	MediaFiles    int  // 笔记图片张数
}

// Export 写出一个配置包。
func Export(o ExportOptions) (ExportResult, error) {
	var res ExportResult
	if strings.TrimSpace(o.Out) == "" {
		return res, fmt.Errorf("未指定导出文件")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}

	settings, err := readSettingsMap(o.SettingsPath)
	if err != nil {
		return res, err
	}
	for _, k := range machineKeys {
		delete(settings, k)
	}

	includes := []string{"settings"}
	var bgData []byte
	var bgExt string
	if o.Background != "" {
		if data, rerr := os.ReadFile(o.Background); rerr == nil {
			bgData = data
			bgExt = strings.ToLower(filepath.Ext(o.Background))
			includes = append(includes, "background")
			res.IncludesImage = true
		}
	}

	var dbTmp string
	if o.IncludeData {
		if _, statErr := os.Stat(o.DBPath); statErr == nil {
			f, terr := os.CreateTemp("", "mygo-profile-db-*.db")
			if terr != nil {
				return res, terr
			}
			dbTmp = f.Name()
			_ = f.Close()
			// BackupTo（VACUUM INTO）要求目标不存在：占位文件要先删掉，只借它的名字。
			_ = os.Remove(dbTmp)
			defer os.Remove(dbTmp)
			s, oerr := store.Open(o.DBPath)
			if oerr != nil {
				return res, fmt.Errorf("读取数据库失败: %w", oerr)
			}
			berr := s.BackupTo(dbTmp)
			_ = s.Close()
			if berr != nil {
				return res, fmt.Errorf("导出数据库快照失败: %w", berr)
			}
			includes = append(includes, "data")
			res.IncludesData = true
		}
	}

	// 笔记图片：整个 notes-media 目录进包（正文里的相对路径在导入后照样成立）。
	mediaFiles := 0
	var mediaList []string
	if o.IncludeData && o.MediaDir != "" {
		_ = filepath.WalkDir(o.MediaDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(o.MediaDir, p)
			if rerr != nil {
				return nil
			}
			mediaList = append(mediaList, rel)
			mediaFiles++
			return nil
		})
		if mediaFiles > 0 {
			includes = append(includes, "media")
			res.IncludesMedia = true
			res.MediaFiles = mediaFiles
		}
	}

	m := Manifest{
		Format:        Format,
		App:           o.AppID,
		AppVersion:    o.AppVersion,
		SchemaVersion: o.SchemaVersion,
		Device:        DeviceName(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		ExportedAt:    now(),
		Includes:      includes,
	}

	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return res, err
	}
	// 先写临时文件再重命名：导出中途失败不会留下半个包。
	tmp, err := os.CreateTemp(filepath.Dir(o.Out), ".profile-*.zip")
	if err != nil {
		return res, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	zw := zip.NewWriter(tmp)
	writeJSON := func(name string, v any) error {
		w, werr := zw.Create(name)
		if werr != nil {
			return werr
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	if err := writeJSON(manifestName, m); err != nil {
		return res, err
	}
	if err := writeJSON(settingsName, settings); err != nil {
		return res, err
	}
	if err := writeText(zw, readmeName, readmeText(m)); err != nil {
		return res, err
	}
	if bgData != nil {
		w, werr := zw.Create(bgPrefix + bgExt)
		if werr != nil {
			return res, werr
		}
		if _, werr := w.Write(bgData); werr != nil {
			return res, werr
		}
	}
	if dbTmp != "" {
		if err := addFile(zw, dbTmp, dbName); err != nil {
			return res, err
		}
	}
	for _, rel := range mediaList {
		if err := addFile(zw, filepath.Join(o.MediaDir, rel),
			mediaDirName+"/"+filepath.ToSlash(rel)); err != nil {
			return res, err
		}
	}
	if err := zw.Close(); err != nil {
		return res, err
	}
	if err := tmp.Close(); err != nil {
		return res, err
	}
	if err := os.Rename(tmpName, o.Out); err != nil {
		return res, err
	}
	if st, serr := os.Stat(o.Out); serr == nil {
		res.Bytes = st.Size()
	}
	res.Path = o.Out
	return res, nil
}

// ImportOptions 导入参数。
type ImportOptions struct {
	In            string // 包路径
	SettingsPath  string
	DBPath        string
	DataDir       string // 背景图落到 <DataDir>/backgrounds
	AppID         string
	SchemaVersion string // 当前应用支持的 schemaVersion
	// BeforeDB 在替换 data.db 之前调用：调用方用它关闭自己的数据库连接。
	BeforeDB func() error
	Now      func() time.Time
}

// ImportResult 导入结果（备份路径供 UI 告知用户）。
type ImportResult struct {
	Manifest       Manifest
	SettingsBackup string
	DBBackup       string
	ImportedData   bool
	ImportedImage  bool
	ImportedMedia  bool
	BackgroundPath string
}

// Inspect 只读清单（导入前预览 / 同步文件夹列表用）。
func Inspect(path string) (Manifest, error) {
	var m Manifest
	zr, err := zip.OpenReader(path)
	if err != nil {
		return m, fmt.Errorf("打不开配置包: %w", err)
	}
	defer zr.Close()
	rc, err := zr.Open(manifestName)
	if err != nil {
		return m, fmt.Errorf("配置包缺少 manifest.json")
	}
	defer rc.Close()
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return m, fmt.Errorf("manifest.json 解析失败: %w", err)
	}
	return m, nil
}

// Import 校验并应用一个配置包。任何校验失败都在改文件之前返回。
func Import(o ImportOptions) (ImportResult, error) {
	var res ImportResult
	if strings.TrimSpace(o.In) == "" {
		return res, fmt.Errorf("未指定配置文件")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}

	m, err := Inspect(o.In)
	res.Manifest = m
	if err != nil {
		return res, err
	}
	if err := validate(m, o); err != nil {
		return res, err
	}

	// 解到临时目录：先在旁边验证完，再动真实文件。
	tmpDir, err := os.MkdirTemp("", "mygo-profile-import-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmpDir)

	zr, err := zip.OpenReader(o.In)
	if err != nil {
		return res, err
	}
	defer zr.Close()
	hasDB := false
	hasMedia := false
	bgRel := ""
	for _, f := range zr.File {
		slash := filepath.ToSlash(f.Name)
		if strings.HasPrefix(slash, mediaDirName+"/") {
			rel := strings.TrimPrefix(slash, mediaDirName+"/")
			// 只接受 notes-media/ 下的相对路径，杜绝 ../ 路径穿越。
			if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
				continue
			}
			dst := filepath.Join(tmpDir, mediaDirName, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return res, err
			}
			if err := extract(f, dst); err != nil {
				return res, err
			}
			hasMedia = true
			continue
		}
		name := filepath.Base(f.Name) // 只认包根，杜绝路径穿越
		switch {
		case name == settingsName:
			if err := extract(f, filepath.Join(tmpDir, settingsName)); err != nil {
				return res, err
			}
		case name == dbName:
			if err := extract(f, filepath.Join(tmpDir, dbName)); err != nil {
				return res, err
			}
			hasDB = true
		case strings.HasPrefix(name, bgPrefix+"."):
			if err := extract(f, filepath.Join(tmpDir, name)); err != nil {
				return res, err
			}
			bgRel = name
		}
	}
	settingsBytes, err := os.ReadFile(filepath.Join(tmpDir, settingsName))
	if err != nil {
		return res, fmt.Errorf("配置包缺少 settings.json")
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(settingsBytes, &settings); err != nil || settings == nil {
		return res, fmt.Errorf("settings.json 不是合法的 JSON 对象")
	}
	// 设备相关字段沿用本机当前值（包里没有这些键）。
	if cur, cerr := readSettingsMap(o.SettingsPath); cerr == nil {
		for _, k := range preservedKeys {
			if v, ok := cur[k]; ok {
				settings[k] = v
			}
		}
	}

	// 备份当前文件（先备份再覆盖；同名备份已存在时加序号）。
	ts := now().Format("20060102-150405")
	if _, statErr := os.Stat(o.SettingsPath); statErr == nil {
		res.SettingsBackup = o.SettingsPath + ".bak-" + ts
		if err := copyFile(o.SettingsPath, res.SettingsBackup); err != nil {
			return res, fmt.Errorf("备份设置失败: %w", err)
		}
	}
	if hasDB {
		if _, statErr := os.Stat(o.DBPath); statErr == nil {
			res.DBBackup = o.DBPath + ".bak-" + ts
			if err := copyFile(o.DBPath, res.DBBackup); err != nil {
				return res, fmt.Errorf("备份数据库失败: %w", err)
			}
		}
		if o.BeforeDB != nil {
			if err := o.BeforeDB(); err != nil {
				return res, fmt.Errorf("关闭数据库失败: %w", err)
			}
		}
		// 旧的 WAL/SHM 必须一起清掉：换了主库却留着旧 WAL 会被回放，数据会串。
		_ = os.Remove(o.DBPath + "-wal")
		_ = os.Remove(o.DBPath + "-shm")
		if err := copyFile(filepath.Join(tmpDir, dbName), o.DBPath); err != nil {
			return res, fmt.Errorf("写入数据库失败: %w", err)
		}
		res.ImportedData = true
	}

	// 笔记图片：整目录替换（正文里的 notes-media/... 相对路径不变）。
	if hasMedia {
		if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
			return res, err
		}
		dest := filepath.Join(o.DataDir, mediaDirName)
		if _, statErr := os.Stat(dest); statErr == nil {
			_ = os.Rename(dest, dest+".bak-"+ts)
		}
		if err := copyDirTree(filepath.Join(tmpDir, mediaDirName), dest); err != nil {
			return res, fmt.Errorf("写入笔记图片失败: %w", err)
		}
		res.ImportedMedia = true
	}

	// 背景图：复制进本机数据目录，settings 里的路径指向新位置。
	if bgRel != "" {
		destDir := filepath.Join(o.DataDir, "backgrounds")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return res, err
		}
		ext := strings.ToLower(filepath.Ext(bgRel))
		dest := filepath.Join(destDir, "imported-"+ts+ext)
		if err := copyFile(filepath.Join(tmpDir, bgRel), dest); err != nil {
			return res, fmt.Errorf("写入背景图失败: %w", err)
		}
		settings["backgroundImage"], _ = json.Marshal(dest)
		res.BackgroundPath = dest
		res.ImportedImage = true
	} else {
		// 包内没有图：清掉旧路径，避免导入后仍指向另一台机器的绝对路径。
		delete(settings, "backgroundImage")
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return res, err
	}
	out = append(out, '\n')
	if err := writeFileAtomic(o.SettingsPath, out); err != nil {
		return res, fmt.Errorf("写入设置失败: %w", err)
	}
	return res, nil
}

// validate 校验包与应用是否兼容。
func validate(m Manifest, o ImportOptions) error {
	if m.Format <= 0 || m.Format > Format {
		return fmt.Errorf("配置包格式版本 %d 不受支持（本版本支持 %d）", m.Format, Format)
	}
	if o.AppID != "" && m.App != "" && m.App != o.AppID {
		return fmt.Errorf("配置包属于另一个应用（%s），不能导入", m.App)
	}
	cur, err1 := strconv.Atoi(strings.TrimSpace(o.SchemaVersion))
	pkg, err2 := strconv.Atoi(strings.TrimSpace(m.SchemaVersion))
	if err1 == nil && err2 == nil && pkg > cur {
		return fmt.Errorf("配置包来自更新版本（schema %d > 当前 %d），请先升级应用", pkg, cur)
	}
	return nil
}

// DeviceName 本机设备名（导出文件名与 manifest 用），只保留安全字符。
func DeviceName() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "device"
	}
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "device"
	}
	return name
}

// ---- 小工具 ----

func readSettingsMap(path string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("读取设置失败: %w", err)
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, nil
}

func writeText(zw *zip.Writer, name, text string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, text)
	return err
}

func addFile(zw *zip.Writer, src, name string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readmeText(m Manifest) string {
	var b strings.Builder
	b.WriteString("量子科学 · 配置与数据包\n\n")
	fmt.Fprintf(&b, "应用：%s %s\n", m.App, m.AppVersion)
	fmt.Fprintf(&b, "设备：%s（%s）\n", m.Device, m.Platform)
	fmt.Fprintf(&b, "导出时间：%s\n", m.ExportedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "内容：%s\n\n", strings.Join(m.Includes, "、"))
	b.WriteString("在应用的「设置 → 关于与诊断 → 数据与诊断 → 导入配置与数据」里选择本文件即可恢复。\n")
	b.WriteString("导入前会自动备份本机现有的 settings.json 与 data.db（同目录 .bak-<时间>）。\n\n")
	b.WriteString("说明：\n")
	b.WriteString("- 包内可能包含授权凭证（license token），请按敏感文件保管，不要公开分享。\n")
	b.WriteString("- 不含日志、窗口位置和「开机自启 / 多开」这类设备相关设置。\n")
	b.WriteString("- 背景图片已随包携带；导入后会复制到本机数据目录并自动指向它。\n")
	return b.String()
}

// copyDirTree 递归复制目录（跨盘时不能用 os.Rename，导入的临时目录常在系统盘）。
func copyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}
