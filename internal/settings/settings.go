// Package settings 负责应用用户偏好的持久化（纯 Go，无 UI 依赖）。
//
// 设计约定：
//   - 本包只放业务逻辑与可独立单测的函数，禁止 import
//     github.com/egoist/mygo/ui 或 mygo 的视图层，以保证脱离窗口也能测试；
//   - UI 层（internal/view）通过本包读写设置，不直接操作配置文件。
//
// 存储选型（调研结论）：
//
//	桌面应用的「用户偏好」属于单用户、单机、小体量键值配置。业界惯例
//	（Electron 的 electron-store 写入 userData、.NET 的 user.config、
//	Wails/原生 Go 写入 os.UserConfigDir）一致采用 per-user 目录下的 JSON
//	文件；SQLite/数据库只在需要结构化关系数据或跨设备同步时才引入，
//	对当前场景属于过度设计。故此处使用 JSON 文件方案。
package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"bestuav-atlas/internal/apppath"
)

// 目录名与路径规则统一由 internal/apppath 提供（日志、数据库同用一份逻辑）：
//
//	Windows: %APPDATA%\bestuav-atlas\settings.json
//	macOS:   ~/Library/Application Support/bestuav-atlas/settings.json
//	Linux:   ~/.config/bestuav-atlas/settings.json

// Settings 用户偏好。
type Settings struct {
	Theme string `json:"theme"` // "system" | "light" | "dark"

	// Palette 配色方案（强调色系）："" / "system" 跟随系统强调色；其余为内置配色。
	// 背景、描边、正文颜色仍由 system/light/dark 决定，配色只换强调色相关的语义色。
	Palette string `json:"palette"`

	// Tone 背景色调：default（主题默认）/ warm / green / midnight。
	// 与 Palette 正交，只换背景与控件面颜色，不改强调色。
	Tone string `json:"tone"`

	FontSize float64 `json:"fontSize"` // 文本基准字号（px）

	CloseMode string `json:"closeMode"` // "ask" | "minimize" | "quit"

	// SidebarCollapsed 侧栏整体是否处于收缩（图标栏）状态。false=展开，true=收缩。
	SidebarCollapsed bool `json:"sidebarCollapsed"`

	// OpenAtLogin 是否在用户登录系统时自动启动（开机自启）。
	// 真源在操作系统（Windows HKCU Run / macOS 登录项 / Linux XDG autostart），
	// 这里只是记住用户的意图，启动时不必每次重写系统设置。
	OpenAtLogin bool `json:"openAtLogin"`

	// AllowMultiInstance 是否允许同时运行多个应用实例。
	// false（默认）=单实例：重复启动只会激活已在运行的窗口并退出新进程；
	// true=多开：每次启动都是独立窗口，互不干扰。
	AllowMultiInstance bool `json:"allowMultiInstance"`

	// BackgroundImage 背景图片的绝对路径；空串表示不使用背景图（主题默认外观）。
	BackgroundImage string `json:"backgroundImage"`

	// ---- 仓库同步（GitHub / Gitea / Gitee，手动上传） ----
	// 都属于本机设置：令牌是敏感信息，导出配置包时会被剔除、导入时保留本机值。
	RepoKind    string `json:"repoKind"`    // github / gitea / gitee
	RepoBaseURL string `json:"repoBaseURL"` // Gitea 实例地址；其他平台留空用默认 API
	RepoToken   string `json:"repoToken"`   // 配置包同步用的 PAT（明文，仅存本机）
	RepoOwner   string `json:"repoOwner"`
	RepoRepo    string `json:"repoRepo"`
	RepoBranch  string `json:"repoBranch"` // 默认 main
	RepoPath    string `json:"repoPath"`   // 留空用 profiles/<设备名>.mygo-profile.zip
	RepoLast    string `json:"repoLast"`   // 最近一次上传/下载的展示文本（本机）

	// ---- 星图：仓库管理用的本机设置 ----
	// WorkRoot 仓库本地根目录：导入仓库时默认把代码放在 <WorkRoot>/<仓库名>。
	// 空串 = <用户主目录>/atlas-repos（见 internal/view/atlas_data.go 的 workRoot）。
	WorkRoot string `json:"workRoot"`

	// GitHubToken 工作区里的 GitHub PAT：只用于星图的「读取我的仓库 / 刷新远端信息 /
	// 读 README」，与上面 RepoToken（配置包同步）是两套凭据，互不覆盖。
	// 两者可以填同一个 PAT，也可以各用各的权限范围；字段分开是为了避免改一处连带
	// 改掉另一处。
	GitHubToken string `json:"githubToken"`

	// RepoAutoRefresh 仓库页是否按固定间隔自动刷新「本地状态 + 远端信息」。
	// 自动刷新和手动批量共用异步工作池与最大并发设置；失败仓库跳过、下一轮再试。
	RepoAutoRefresh bool `json:"repoAutoRefresh"`
	// RepoAutoRefreshMinutes 自动刷新间隔（分钟），取 RepoRefreshMinutes 之一。
	RepoAutoRefreshMinutes int `json:"repoAutoRefreshMinutes"`

	// 其它代码托管平台的令牌：一个平台一个，互不通用（Gitee 的私人令牌不能访问
	// GitHub）。都只存本机、不随配置包导出。Gitea 的实例地址各人不同，单列 GiteaHost。
	GiteeToken   string `json:"giteeToken"`
	AtomGitToken string `json:"atomGitToken"`
	GiteaHost    string `json:"giteaHost"`
	GiteaToken   string `json:"giteaToken"`

	// ---- 跨设备同步 ----
	// 同步服务是自建的 atlas-sync（cmd/atlas-sync），单用户、单实例。
	// 设备令牌与管理员令牌都只存本机，不随配置包导出（见 internal/profile）。
	// SyncEnabled 关掉时后台不联网，但已产生的变更保留在 outbox 里，下次开启继续推。
	SyncEnabled bool `json:"syncEnabled"`
	// SyncURL 服务地址，如 http://127.0.0.1:7420 或 https://sync.example.com。
	SyncURL string `json:"syncURL"`
	// SyncDeviceID 本机在服务端的设备 id（只用于展示与排障）。
	SyncDeviceID string `json:"syncDeviceID"`
	// SyncToken 本机设备令牌（服务端只存它的 SHA-256）。
	SyncToken string `json:"syncToken"`
	// SyncAdminToken 管理员令牌：只用于「连接服务」「生成配对码」，与设备令牌是两回事。
	SyncAdminToken string `json:"syncAdminToken"`
	// SyncLastAt 上次同步完成的本地时间（展示文本，不参与逻辑）。
	SyncLastAt string `json:"syncLastAt"`
	// SyncAutoOff 关掉「定时同步」：连接与令牌都保留，只是不再每 30 秒自动跑一轮，
	// 需要时手动点「立即同步」。
	//
	// 用「反向命名」是为了老配置的零值语义：字段不存在时 false = 自动同步开启，
	// 不会因为升级突然把所有人的定时同步关掉。
	SyncAutoOff bool `json:"syncAutoOff"`

	// ---- 同步服务部署（工具 → 同步服务） ----
	// 这几项只用于「从桌面端把 atlas-sync 发到服务器上」，都只属于本机。
	// 以前靠 deploy/vps 里的脚本 + 手抄参数，换台机器就得重来一遍。
	DeployHost string `json:"deployHost"` // SSH 主机
	DeployPort int    `json:"deployPort"` // SSH 端口，0 → 22
	DeployUser string `json:"deployUser"` // SSH 用户
	DeployKey  string `json:"deployKey"`  // 私钥路径（支持 ~）
	DeployDir  string `json:"deployDir"`  // 远端目录，如 ~/atlas-sync
	DeploySrc  string `json:"deploySrc"`  // 本机源码目录（含 cmd/atlas-sync）

	// ---- 伴生桌宠 ----
	// PetEnabled 是否显示桌宠（无边框透明置顶小窗，见 internal/view/pet.go）。
	PetEnabled bool `json:"petEnabled"`
	// PetSkin 桌宠皮肤：cat / dog。
	PetSkin string `json:"petSkin"`
	// PetReminderMinutes 久坐提醒间隔（分钟）；0 = 关闭。
	PetReminderMinutes int `json:"petReminderMinutes"`
	// PetXP 累计「陪伴」分钟数（决定成长阶段）；桌宠关掉时也保留。
	PetXP float64 `json:"petXP"`
	// PetX / PetY 桌宠窗口位置；负数表示未设置（让窗口居中）。
	PetX int `json:"petX"`
	PetY int `json:"petY"`

	// ---- 今日便签 ----
	// StickyEnabled 是否显示「今日便签」桌面浮窗（无边框小窗，见
	// internal/view/sticky_note.go）：按早 / 中 / 晚列出今天要做的待办。
	StickyEnabled bool `json:"stickyEnabled"`
	// StickyAlwaysOnTop 便签是否始终置顶。默认 true；关掉后它像普通窗口一样
	// 会被其它窗口盖住，不会永远挡住屏幕右上角。
	StickyAlwaysOnTop bool `json:"stickyAlwaysOnTop"`
	// StickyX / StickyY 便签窗口位置；负数 = 未设置（默认贴屏幕右上角）。
	StickyX int `json:"stickyX"`
	StickyY int `json:"stickyY"`
	// StickyW / StickyH 便签窗口尺寸；0 = 未设置（用内置默认 300×440）。
	// 右下角缩放手柄拖动后写入这里并落盘，下次启动保持调整过的大小。
	StickyW int `json:"stickyW"`
	StickyH int `json:"stickyH"`

	// MenuOpacity 侧栏（菜单区）浮层的不透明度，0.2–1.0。仅在设置了背景图时生效：
	// 1.0=完全遮蔽背景图（与未设背景图时观感一致），数值越小背景图越透出来。
	MenuOpacity float64 `json:"menuOpacity"`

	// ContentOpacity 内容区浮层的不透明度，0.2–1.0，语义同 MenuOpacity。
	ContentOpacity float64 `json:"contentOpacity"`

	// PageIdleMinutes 页面缓存闲置多久后销毁（分钟）。
	// 0 = 不按闲置销毁（页面状态一直保留，直到被显式销毁）；默认 15。
	// 与 javafx 版的 idleTime 配置对应，详见 internal/view/pagelife.go。
	PageIdleMinutes int `json:"pageIdleMinutes"`

	// AsyncConcurrency 是后台异步任务同时执行的上限（1–MaxAsyncConcurrency）。
	// 作用范围是「一批同类仓库操作」：导入后克隆、批量拉取/提交推送/刷新远端、
	// 全量状态探测。每个任务完成一个再补一个，避免二十个仓库一起克隆把磁盘与
	// 网络打满。
	AsyncConcurrency int `json:"asyncConcurrency"`

	// Shortcuts 是用户自定义的全局快捷键：动作 id → 键位字符串（如 "cmd+2"）。
	// 没写的动作用默认键位；空串表示禁用该动作。格式见 internal/shortcut。
	Shortcuts map[string]string `json:"shortcuts,omitempty"`

	// IgnoredVersion 是用户点过「忽略」的新版本号（如 "0.1.6"）。
	// 空串 = 没忽略过。检查更新时发现同版本不再主动提示；手动点「检查更新」仍会显示。
	IgnoredVersion string `json:"ignoredVersion"`
}

// 浮层不透明度的默认值与合法区间。
//
// 说明：这里用「不透明度（opacity）」而非「透明度（transparency）」作为存储与
// UI 语义，避免 0 同时被理解为「全透明」和「无穷不透明」两种歧义；界面上
// 滑块展示的百分比仍是用户直觉的「不透明度」。
const (
	// 默认留一点透出，让用户设置背景图后能立刻看出效果，同时保证正文可读。
	DefaultMenuOpacity    = 0.80
	DefaultContentOpacity = 0.88
	MinOpacity            = 0.20
	MaxOpacity            = 1.00

	// DefaultPageIdleMinutes 页面缓存闲置销毁的默认时长（分钟）。
	DefaultPageIdleMinutes = 15
	// MaxPageIdleMinutes 设置页可调上限（分钟）。
	MaxPageIdleMinutes = 120

	// DefaultAsyncConcurrency 后台异步任务的默认并发数：一次克隆 3~5 个是
	// 「快」与「不打满网络/磁盘」的折中，默认取中间值 4。
	DefaultAsyncConcurrency = 4
	// MaxAsyncConcurrency 设置页可调的并发上限。再高只会让几十个 git 进程
	// 抢同一块磁盘，收益很小。
	MaxAsyncConcurrency = 8

	// DefaultRepoAutoRefreshMinutes 仓库自动刷新的默认间隔（分钟）。
	DefaultRepoAutoRefreshMinutes = 30
)

// RepoRefreshMinutes 仓库自动刷新可选的间隔（分钟），顺序即界面顺序。
// 10 分钟适合正在密集开发的仓库；3 小时适合长期归档、偶尔看一眼的仓库。
var RepoRefreshMinutes = []int{10, 30, 60, 180}

// 配色方案（强调色系）的稳定标识：设置文件里存这些字符串，视图层再把它们映射到
// 具体颜色与展示名。校验放在这里，避免外部手改 JSON 后把界面弄成不可读的颜色。
const (
	PaletteSystem   = "system" // 跟随系统强调色（默认）
	PaletteAmber    = "amber"
	PaletteTeal     = "teal"
	PaletteIndigo   = "indigo"
	PaletteGraphite = "graphite"
)

// Palettes 全部合法配色（顺序与设置页一致）。
var Palettes = []string{PaletteSystem, PaletteAmber, PaletteTeal, PaletteIndigo, PaletteGraphite}

// 背景色调（surface tone）：只换背景/控件面/描边，不改强调色与正文颜色。
// 与 Palette 正交：用户可以「护眼绿 + 青碧」这样组合。
const (
	ToneDefault  = "default"  // 主题默认（不覆盖）
	ToneWarm     = "warm"     // 暖白
	ToneGreen    = "green"    // 护眼绿
	ToneMidnight = "midnight" // 午夜蓝（深色底 + 冷色描边）
	ToneSakura   = "sakura"   // 樱粉
	ToneMist     = "mist"     // 雾蓝
	ToneCeladon  = "celadon"  // 青瓷
	ToneLavender = "lavender" // 薰衣草
	ToneMocha    = "mocha"    // 摩卡
)

// Tones 全部合法色调（顺序与设置页一致）。
var Tones = []string{
	ToneDefault, ToneWarm, ToneGreen, ToneMidnight,
	ToneSakura, ToneMist, ToneCeladon, ToneLavender, ToneMocha,
}

// 桌宠皮肤与提醒的取值。
const (
	PetSkinCat = "cat"
	PetSkinDog = "dog"

	// DefaultPetReminderMinutes 久坐提醒默认间隔（分钟）。
	DefaultPetReminderMinutes = 45
	// MaxPetReminderMinutes 久坐提醒可设上限（分钟）。
	MaxPetReminderMinutes = 120
)

// PetSkins 全部合法桌宠皮肤。
var PetSkins = []string{PetSkinCat, PetSkinDog}

// Default 返回首次启动时的默认偏好。
func Default() *Settings {
	return &Settings{
		Theme:                  "system",
		Palette:                "system",
		Tone:                   ToneDefault,
		FontSize:               14,
		CloseMode:              "ask",
		MenuOpacity:            DefaultMenuOpacity,
		ContentOpacity:         DefaultContentOpacity,
		PageIdleMinutes:        DefaultPageIdleMinutes,
		AsyncConcurrency:       DefaultAsyncConcurrency,
		RepoAutoRefreshMinutes: DefaultRepoAutoRefreshMinutes,
		PetSkin:                PetSkinCat,
		PetReminderMinutes:     DefaultPetReminderMinutes,
		PetX:                   -1,
		PetY:                   -1,
		StickyX:                -1,
		StickyY:                -1,
		StickyAlwaysOnTop:      true,
		RepoKind:               "github",
		RepoBranch:             "main",
	}
}

var mu sync.Mutex // 保护 Save 的临时文件写与重命名（视图层与可能的并发调用）

// configDir 返回本应用的 per-user 配置目录：<用户配置目录>/<appName>。
//
// 路径规则统一收敛在 internal/apppath（日志目录、数据库路径也用同一份逻辑）。
func configDir() (string, error) { return apppath.ConfigDir() }

// path 定位配置文件路径（<配置目录>/settings.json）。
func path() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// legacyPath 早期版本的配置文件路径：直接放在用户配置目录根下，没有按应用名
// 分子目录。仅在读取时用于一次性迁移，避免老用户升级后偏好（主题、侧栏状态等）
// 丢失。
func legacyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Path 暴露配置文件路径，供 UI 层「打开配置目录」等场景展示。
func Path() (string, error) { return path() }

// Load 读取设置；文件不存在或损坏时返回默认偏好（不写盘、不报错中断）。
func Load() (*Settings, error) {
	p, err := path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			// 老路径迁移：读到旧文件则原样解析，并尽力写入新位置。
			if legacy, lerr := legacyPath(); lerr == nil {
				if old, oerr := os.ReadFile(legacy); oerr == nil {
					s := Default()
					if json.Unmarshal(old, s) == nil {
						migrateLegacyGitHubToken(old, s)
						normalize(s)
						_ = s.Save() // 迁移失败不影响本次使用
						return s, nil
					}
				}
			}
			return Default(), nil
		}
		return Default(), fmt.Errorf("读取设置失败，使用默认: %w", err)
	}
	s := Default()
	if err := json.Unmarshal(data, s); err != nil {
		return Default(), fmt.Errorf("解析设置失败，使用默认: %w", err)
	}
	migrateLegacyGitHubToken(data, s)
	normalize(s)
	return s, nil
}

// migrateLegacyGitHubToken 兼容拆分前的设置文件：老版本只有一个 repoToken，
// 工作区和配置包同步共用它。第一次读到没有 githubToken 键的老文件时，把它同时
// 填到 GitHubToken，避免升级后工作区的 GitHub 功能突然失效。
//
// 只看「JSON 里有没有这个键」而不是「值是否为空」：新版 Save 会写出空字符串，
// 用户以后主动清空工作区令牌时，重启不能被旧 repoToken 又填回来。
func migrateLegacyGitHubToken(data []byte, s *Settings) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	if _, ok := raw["githubToken"]; ok {
		return
	}
	if s.GitHubToken == "" {
		s.GitHubToken = s.RepoToken
	}
}

// MustLoad 同 Load，但忽略错误返回默认兜底，便于调用方一行初始化。
func MustLoad() *Settings {
	s, _ := Load()
	return s
}

// Reset 把用户偏好恢复默认（--reset-settings 用），返回备份文件路径（无备份时为空）。
//
// 为什么不直接删：`--reset-settings` 是「设置写坏、界面打不开」时的救命通道，用户
// 往往是被迫执行它——主题、快捷键、背景图路径、忽略过的版本号全在这里，删掉就再也
// 找不回来了。改成改名备份，代价是磁盘上多几十 KB，换来「后悔药」。
//
// 改名失败（文件被其它进程占用等）时退化为删除：恢复默认的目的更重要，不能让用户
// 卡在这里。
func Reset() (backup string, err error) {
	p, err := path()
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(p); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", nil // 本来就没有：目的已达成
		}
		return "", fmt.Errorf("检查设置文件失败: %w", statErr)
	}
	backup = fmt.Sprintf("%s.bak-%s", p, time.Now().Format("20060102-150405"))
	if err := os.Rename(p, backup); err == nil {
		return backup, nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("删除设置失败: %w", err)
	}
	return "", nil
}

// normalize 把脏数据/越界值纠正为合法枚举，避免外部手动改坏文件导致崩溃。
func normalize(s *Settings) {
	switch s.Theme {
	case "light", "dark", "system":
	default:
		s.Theme = "system"
	}
	// 配色：老文件没有该字段（空串）或写了未知值时回落到「跟随系统」。
	if !slices.Contains(Palettes, s.Palette) {
		s.Palette = PaletteSystem
	}
	// 色调：同上，缺省/未知回落到主题默认。
	if !slices.Contains(Tones, s.Tone) {
		s.Tone = ToneDefault
	}
	// 桌宠：皮肤白名单、提醒区间、经验非负、位置哨兵。
	if !slices.Contains(PetSkins, s.PetSkin) {
		s.PetSkin = PetSkinCat
	}
	if s.PetReminderMinutes < 0 || s.PetReminderMinutes > MaxPetReminderMinutes {
		s.PetReminderMinutes = DefaultPetReminderMinutes
	}
	if s.PetXP < 0 || math.IsNaN(s.PetXP) || math.IsInf(s.PetXP, 0) {
		s.PetXP = 0
	}
	if s.PetX < -1 {
		s.PetX = -1
	}
	if s.PetY < -1 {
		s.PetY = -1
	}
	if s.StickyX < -1 {
		s.StickyX = -1
	}
	if s.StickyY < -1 {
		s.StickyY = -1
	}
	// 便签尺寸：0 = 走默认；正数不得小于最小可交互尺寸（与前端常量对齐），
	// 否则折叠/缩放下限之外的值会让便签小到放不下一行字。
	const minStickyW, minStickyH = 220, 160
	if s.StickyW != 0 && s.StickyW < minStickyW {
		s.StickyW = minStickyW
	}
	if s.StickyH != 0 && s.StickyH < minStickyH {
		s.StickyH = minStickyH
	}
	// 仓库同步：平台白名单、默认分支、去掉首尾空白（令牌尤其不能带空格）。
	if s.RepoKind != "gitea" && s.RepoKind != "gitee" {
		s.RepoKind = "github"
	}
	if s.RepoBranch == "" {
		s.RepoBranch = "main"
	}
	s.RepoBaseURL = strings.TrimSpace(s.RepoBaseURL)
	s.RepoToken = strings.TrimSpace(s.RepoToken)
	// 工作区 GitHub 令牌是另一套凭据（只给星图读仓库/远端/README 用）。
	s.GitHubToken = strings.TrimSpace(s.GitHubToken)
	s.RepoOwner = strings.TrimSpace(s.RepoOwner)
	s.RepoRepo = strings.TrimSpace(s.RepoRepo)
	s.RepoBranch = strings.TrimSpace(s.RepoBranch)
	s.RepoPath = strings.Trim(strings.TrimSpace(s.RepoPath), "/")
	if s.FontSize < 11 || s.FontSize > 22 {
		s.FontSize = 14
	}
	switch s.CloseMode {
	case "ask", "minimize", "quit":
	default:
		s.CloseMode = "ask"
	}
	// 浮层不透明度：越界/零值（老版本文件没有这两个字段，反序列化得 0）
	// 一律回落到默认，避免出现「完全透明」的不可用界面。
	if s.MenuOpacity < MinOpacity || s.MenuOpacity > MaxOpacity {
		s.MenuOpacity = DefaultMenuOpacity
	}
	if s.ContentOpacity < MinOpacity || s.ContentOpacity > MaxOpacity {
		s.ContentOpacity = DefaultContentOpacity
	}
	// 页面缓存闲置销毁：负数/超上限一律纠正（负值在老文件里不存在，故只在
	// 显式越界时处理；缺字段得 0，按语义就是「不销毁」，这里保留用户意图）。
	if s.PageIdleMinutes < 0 || s.PageIdleMinutes > MaxPageIdleMinutes {
		s.PageIdleMinutes = DefaultPageIdleMinutes
	}
	// 异步并发：0 是老文件缺字段（默认值），负数/超上限是脏数据；都回落到默认。
	if s.AsyncConcurrency < 1 || s.AsyncConcurrency > MaxAsyncConcurrency {
		s.AsyncConcurrency = DefaultAsyncConcurrency
	}
	// 自动刷新间隔只接受预设的几档；老文件缺字段/手改坏值都回落到默认。
	if !slices.Contains(RepoRefreshMinutes, s.RepoAutoRefreshMinutes) {
		s.RepoAutoRefreshMinutes = DefaultRepoAutoRefreshMinutes
	}
	// 同步服务地址与令牌：去掉首尾空白与末尾斜杠（令牌带空格是最常见的粘贴事故）。
	s.SyncURL = strings.TrimRight(strings.TrimSpace(s.SyncURL), "/")
	s.SyncToken = strings.TrimSpace(s.SyncToken)
	s.SyncAdminToken = strings.TrimSpace(s.SyncAdminToken)
	s.SyncDeviceID = strings.TrimSpace(s.SyncDeviceID)
	if s.SyncURL == "" || s.SyncToken == "" {
		s.SyncEnabled = false // 没配好就不该有后台线程去撞网络
	}
}

// Save 以「临时文件 + 重命名」方式原子写入，避免写入过程中断电/崩溃留下
// 半截配置导致下次启动解析失败。
func (s *Settings) Save() error {
	mu.Lock()
	defer mu.Unlock()

	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
