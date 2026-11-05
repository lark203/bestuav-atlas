// Package gitx 对本地 git 仓库做状态检查与少量写操作（fetch / pull / commit / push）。
//
// 为什么走 os/exec 调系统 git，而不是引 go-git：
//   - 用户的操作对象是「他自己已经配好的工作副本」，git 命令行看到什么，这里就
//     看到什么——凭证助手、代理、SSL、core.autocrlf 全部原样生效；
//   - go-git 对协议/凭证的兼容性始终落后官方实现，遇到私有仓库或企业代理时
//     失败方式还很难解释；
//   - 省掉一个第三方依赖，符合项目「纯 Go、只用标准库」的约束。
//
// 两条硬规则：
//  1. 永不经过 shell：参数以切片交给 exec，路径只放 cmd.Dir。因此 Windows 路径
//     里的空格、反斜杠、中文都不需要任何转义，也不存在命令注入面。
//  2. 交互式提示一律关闭：桌面进程没有终端，git 一旦等密码输入，表现出来就是
//     界面假死（只能杀进程），所以 GIT_TERMINAL_PROMPT=0 且清空 GIT_ASKPASS。
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// inspectTimeout 一次 Inspect 的总体上限。
//
// 界面会在进入仓库页/点刷新时调用 Inspect，如果目录在网络盘上、或仓库巨大，
// 单条 git 命令可能卡很久；给整次检查设上限，比让界面调用方无限等待好。
// 超时只会让后几条子命令失败并记进 Err，不会 panic。
const inspectTimeout = 15 * time.Second

// Status 一个本地目录的 git 概况。
//
// 设计原则：**部分成功也要有值**。除 IsRepo 外所有字段都可能是零值，
// 读不到就留空，并用 Err 说明第一处问题。
type Status struct {
	IsRepo       bool      // dir 是否是一个 git 工作区
	Branch       string    // 当前分支名（detached 时为空）
	Upstream     string    // 上游分支（origin/main），无则空
	Dirty        int       // 有改动的文件数（含未跟踪；用 git status --porcelain 非空行）
	Ahead        int       // 相对上游领先提交数（无上游则 0）
	Behind       int       // 相对上游落后提交数（无上游则 0）
	LastCommit   string    // "短hash 提交标题"
	LastCommitAt time.Time // 最后一次提交的时间（本地时区）
	RemoteURL    string    // origin 地址
	Err          string    // 非致命错误说明（不是 git 仓库、git 命令缺失等），空=正常
}

// ErrNoChanges 表示工作区干净、没有可提交的改动。
var ErrNoChanges = errors.New("没有需要提交的改动")

// Inspect 读取目录的本地 git 状态。永不 panic；目录不存在/不是仓库时返回
// IsRepo=false 并填 Err。
//
// 为什么要连跑五六条子命令而不是解析 `git status -sb` 之类的一次输出：
// 每条命令职责单一、输出格式稳定（porcelain 与 --format 都是为脚本设计的），
// 坏处只是多几次进程启动——在本地磁盘上是毫秒级，值得换来的可读性与稳定性。
func Inspect(dir string) Status {
	var st Status
	if strings.TrimSpace(dir) == "" {
		st.Err = "未设置本地目录"
		return st
	}
	if !HasGit() {
		st.Err = "未找到 git 命令，请先安装 Git 并加入 PATH"
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()

	// 是不是工作区：普通目录里 git 会以 128 退出（not a git repository）；
	// 裸仓库里命令成功但输出 "false"，两种情况都不算工作区。
	out, err := run(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		// 常见两种原因：目录不存在（cannot change to ...）、不是仓库（not a git repository）。
		// 把 git 的原话附在后面，用户改路径时能立刻看出是「路径不对」还是「目录不对」。
		st.Err = "不是 git 仓库" + reason(out)
		return st
	}
	if strings.TrimSpace(out) != "true" {
		st.Err = "不是 git 工作区（可能是裸仓库）"
		return st
	}
	st.IsRepo = true

	// 分支：detached HEAD 时命令成功但输出为空——正好符合「detached 时为空」。
	if out, err := run(ctx, dir, "branch", "--show-current"); err != nil {
		st.note(err)
	} else {
		st.Branch = strings.TrimSpace(out)
	}

	// 上游：没有配置 upstream 是常态（本地新建仓库、还没 push 过），
	// 这里失败不记为错误，否则每个纯本地仓库都会顶一条「异常」。
	if up, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		st.Upstream = strings.TrimSpace(up)
	}

	// 领先/落后必须在有上游时才有意义；@{u}...HEAD 的三个点不能少。
	if st.Upstream != "" {
		if out, err := run(ctx, dir, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err != nil {
			st.note(err)
		} else if f := strings.Fields(out); len(f) == 2 {
			// --left-right 的两列是「左=@{u}，右=HEAD」，所以顺序是 behind、ahead，
			// 与字段名的直觉顺序相反，写反了会出现「领先 3、落后 0」的假象。
			st.Behind, _ = strconv.Atoi(f[0])
			st.Ahead, _ = strconv.Atoi(f[1])
		}
	}

	// 脏文件数：porcelain 一行一个变动文件，未跟踪的以 "??" 开头，
	// 正是「含未跟踪」的口径；不用 --short 是为了不受颜色/终端宽度影响。
	if out, err := run(ctx, dir, "status", "--porcelain"); err != nil {
		st.note(err)
	} else {
		st.Dirty = countLines(out)
	}

	// 最后一次提交：用 %x00 当分隔符而不是默认的换行，提交标题里可能出现任意
	// 字符（含换行被 git 压成空格，但仍可能有制表符），NUL 是唯一不会撞车的。
	// %cI 给的是严格 ISO 8601（含时区偏移），比 %ci 更好解析。
	if out, err := run(ctx, dir, "log", "-1", "--format=%h%x00%s%x00%cI"); err != nil {
		// 还没有任何提交的新仓库，git log 必然失败——这是正常状态，不记为错误。
		if !emptyRepoOut(out) {
			st.note(err)
		}
	} else if parts := strings.SplitN(out, "\x00", 3); len(parts) == 3 {
		st.LastCommit = strings.TrimSpace(parts[0] + " " + parts[1])
		if ts, perr := time.Parse(time.RFC3339, strings.TrimSpace(parts[2])); perr == nil {
			st.LastCommitAt = ts.Local() // 统一转本地时区，界面直接展示
		}
	}

	// origin 地址：没配远程的纯本地仓库很常见，同样不记为错误。
	if out, err := run(ctx, dir, "config", "--get", "remote.origin.url"); err == nil {
		st.RemoteURL = strings.TrimSpace(out)
	}

	return st
}

// Fetch 执行 git fetch --prune；返回 git 的精简输出（失败时错误里带 stderr 摘要）。
//
// 带 --prune：远端已删除的分支顺手清掉，否则界面上会一直列出「远端早已不存在的分支」。
func Fetch(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "fetch", "--prune")
	if err != nil {
		return out, fmt.Errorf("gitx: 拉取远端更新失败: %w", err)
	}
	return out, nil
}

// Pull 执行 git pull --ff-only --prune（不产生合并提交）；返回精简输出。
//
// 为什么强制 --ff-only：替用户自动做合并决策（解决冲突、生成 merge commit）在桌面
// 工具里是灾难——冲突解错了没人知道。快进不了就明确报错，让用户回命令行处理。
func Pull(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "pull", "--ff-only", "--prune")
	if err != nil {
		return out, fmt.Errorf("gitx: 快进拉取失败（本地与远端可能已分叉）: %w", err)
	}
	return out, nil
}

// CommitAll 等价于 git add -A && git commit -m message；返回短 hash。
// 工作区干净时返回 ErrNoChanges。message 为空时用 "chore: 自动提交"。
func CommitAll(ctx context.Context, dir, message string) (string, error) {
	if err := checkDir(dir); err != nil {
		return "", err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "chore: 自动提交"
	}
	// add -A 而不是 add .：前者覆盖整个工作区（含删除），且不受当前目录影响。
	if _, err := run(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("gitx: 暂存改动失败: %w", err)
	}
	// 先用 diff --cached --quiet 探测暂存区：有差异时退出码是 1（预期），
	// 其它失败码才是真错误。不采用「直接 commit，再从报错文案判断无可提交」，
	// 因为那段文案随 git 版本与语言变化，解析它迟早会踩空。
	if _, err := run(ctx, dir, "diff", "--cached", "--quiet"); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return "", fmt.Errorf("gitx: 检查暂存区失败: %w", err)
		}
	} else {
		return "", ErrNoChanges
	}
	if _, err := run(ctx, dir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("gitx: 提交失败: %w", err)
	}
	// 单独取一次短 hash：从 commit 的 "[main abc1234] ..." 输出里抠字符串不稳，
	// rev-parse --short 会遵守 core.abbrev，长度与用户命令行一致。
	out, err := run(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitx: 读取提交号失败: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Push 推送当前分支；没有 upstream 时自动 git push -u origin <branch>。返回精简输出。
func Push(ctx context.Context, dir string) (string, error) {
	if err := checkDir(dir); err != nil {
		return "", err
	}
	branchOut, err := run(ctx, dir, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("gitx: 读取当前分支失败: %w", err)
	}
	branch := strings.TrimSpace(branchOut)

	if _, uerr := run(ctx, dir, "rev-parse", "--abbrev-ref", "@{u}"); uerr != nil {
		// 没有 upstream：必须显式给远端与分支名，否则 git 会以
		// "The current branch has no upstream branch" 直接失败。
		// 顺便用 -u 把 upstream 建起来，下次就能裸 push。
		if branch == "" {
			return "", fmt.Errorf("gitx: 当前是 detached HEAD，没有可推送的分支")
		}
		out, perr := run(ctx, dir, "push", "-u", "origin", branch)
		if perr != nil {
			return out, fmt.Errorf("gitx: 推送到 origin 失败: %w", perr)
		}
		return out, nil
	}
	out, err := run(ctx, dir, "push")
	if err != nil {
		return out, fmt.Errorf("gitx: 推送失败: %w", err)
	}
	return out, nil
}

// Clone 把远程仓库克隆到 dir。
//
//   - dir 已存在且非空 → 返回错误（调用方应先用本地导入识别已有目录）；
//   - 父目录不存在会自动创建；
//   - ctx 控制超时（调用方给 5 分钟量级）；
//   - 失败时错误里带 git 的输出摘要（截断 800 字符）。
//
// 参数顺序沿用 git 的位置参数：`git clone -- url dir`。`--` 之后的两个词都是
// 位置参数，url 以 "-" 开头（极少见，但合法）时不会被当成选项。
func Clone(ctx context.Context, url, dir string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("gitx: 克隆地址不能为空")
	}
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("gitx: 克隆目标目录不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gitx: 克隆已取消: %w", err)
	}

	// 解析成绝对路径：git 子进程的工作目录会被换成父目录，目标路径必须是
	// 与它无关的绝对路径，否则 `git clone -- url dst` 会落到父目录里。
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("gitx: 解析克隆目标目录失败: %w", err)
	}

	// 已存在且非空：git 自己也会拒绝，但先查一次既给出更明确的中文提示，
	// 也保证一个字节都不碰已有文件——调用方会先用本地导入识别这种目录。
	switch entries, rerr := os.ReadDir(abs); {
	case rerr == nil:
		if len(entries) > 0 {
			return fmt.Errorf("gitx: 目标目录已存在且非空: %s（请先用本地导入识别已有目录）", dir)
		}
		// 空目录是允许的：git clone 自己也接受，等价于原地初始化后拉取。
	case errors.Is(rerr, fs.ErrNotExist):
		// 目标还不存在是常态，父目录可能也不存在，下面一并创建。
	default:
		return fmt.Errorf("gitx: 读取克隆目标目录失败: %w", rerr)
	}

	// git clone 不会替调用方建父目录；而父目录还得当子进程的工作目录。
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("gitx: 创建克隆目标的父目录失败: %w", err)
	}

	// 目标目录此刻还不存在（或为空），不能作为 cmd.Dir，所以工作目录用父目录，
	// 目标路径作为位置参数交给 git——其余（-c core.quotepath=false、关闭交互、
	// 合并 stderr、失败摘要）全部复用 runIn。
	if _, err := runIn(ctx, parent, "clone", "--", url, abs); err != nil {
		return fmt.Errorf("gitx: 克隆 %s 失败: %w", url, err)
	}
	return nil
}

// HasGit 报告本机是否有可用的 git 命令。
//
// 刻意不做缓存：用户完全可能在应用运行期间装好 Git（安装器就是在同一个会话里
// 跑完的），缓存会让界面上的「重新检测」永远返回缺失。LookPath 只查 PATH。
func HasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// ---- 内部工具 ----

// run 执行一条 git 命令，返回合并后的输出。
//
// 为什么合并 stdout 与 stderr：git 把进度和结果几乎都写 stderr（fetch 的
// "From ..."、push 的 "Everything up-to-date"、pull 的 "Fast-forward"），
// 只取 stdout 会得到空串，调用方就无从展示「到底做了什么」。
// 失败时把输出压成一行、截断后放进错误——git 的根因（fatal: ...）都在输出里，
// 但完整的 stderr 可能几十行，不能原样塞进界面。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	if err := checkDir(dir); err != nil {
		return "", err
	}
	return runIn(ctx, dir, args...)
}

// runIn 是 run 的实际执行体：进程工作目录与 git 参数分开给。
//
// 拆出这一层只为一个场景——Clone。clone 的目标目录是位置参数，在命令跑起来
// 之前它可能压根不存在，不能当 cmd.Dir 用；此时把 cmdDir 设成它的父目录即可。
// run 的行为保持不变（cmdDir 恒等于 dir）。
func runIn(ctx context.Context, cmdDir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// -c core.quotepath=false：让 status/diff 里的中文路径原样输出。
	// 默认 git 会把非 ASCII 路径转成 \344\275\240 这种八进制转义，界面上没法看。
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = cmdDir // 路径只交给 Dir，绝不拼进命令行字符串
	cmd.Env = gitEnv()
	// Windows 上不给这个标志，每次 git 调用都会闪一个命令行黑框（刷新状态时
	// 十几个仓库 × 每个仓库十来个调用 = 满屏闪烁）。非 Windows 上是空实现。
	hideWindow(cmd)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		msg := shorten(out, 800)
		if msg == "" {
			return out, fmt.Errorf("git %s 失败: %w", strings.Join(args, " "), err)
		}
		return out, fmt.Errorf("git %s 失败: %w（输出：%s）", strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// gitEnv 返回子进程环境：在继承环境上去掉并重设交互相关变量。
//
// 为什么要过滤而不是直接 append：进程环境里可能已经有 GIT_TERMINAL_PROMPT=1
// （用户从自己的终端启动的情况），重复键在不同平台上生效规则不一致，先删后加才确定。
func gitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "GIT_TERMINAL_PROMPT", "GIT_ASKPASS":
			continue
		}
		out = append(out, kv)
	}
	// GIT_TERMINAL_PROMPT=0：需要账号密码时直接失败，不要卡在等终端输入；
	// GIT_ASKPASS=：清掉可能存在的图形化密码助手（同样会把操作变成弹窗阻塞）。
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
}

// note 记录第一个错误：后面的失败往往只是前面的连带结果，全写进去反而看不出根因。
func (s *Status) note(err error) {
	if s.Err == "" && err != nil {
		s.Err = err.Error()
	}
}

// checkDir 统一空目录判断。空路径交给 exec 会被当成当前进程目录，
// 「检查了一个莫名其妙的仓库」比明确报错难查得多。
func checkDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("gitx: 未设置本地目录")
	}
	return nil
}

// countLines 数非空行数。
func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// shorten 把多行输出压成一行并截断，便于放进错误与状态里展示。
func shorten(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// reason 把 git 的一句话说明包进括号；没有输出时返回空串。
func reason(out string) string {
	if s := shorten(out, 200); s != "" {
		return "（" + s + "）"
	}
	return ""
}

// emptyRepoOut 判断 git log 的失败是否只是「仓库还没有提交」。
//
// git 在不同版本里的说法不同（没有提交时是 "does not have any commits yet"，
// 引用不存在时是 "unknown revision" / "bad default revision"），这些都是
// 新仓库的正常状态，不该写进 Err 让界面误报。
func emptyRepoOut(out string) bool {
	s := strings.ToLower(out)
	return strings.Contains(s, "does not have any commits") ||
		strings.Contains(s, "unknown revision") ||
		strings.Contains(s, "bad default revision")
}
