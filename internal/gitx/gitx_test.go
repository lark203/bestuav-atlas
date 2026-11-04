package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn 在 dir 里跑一条 git 命令（测试专用）。
//
// 一律显式给 user.name/user.email（要么用 git config，要么用 -c），因为 CI 与本机
// 都可能没配全局身份，否则 commit 会直接失败。
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v（%s）", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

// initRepo 建一个带一次初始提交的仓库。
//
// init -b main：不依赖用户的 init.defaultBranch，分支名可断言；
// commit.gpgsign=false：用户全局开了签名时，临时仓库没有密钥会让 commit 失败。
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-b", "main")
	gitIn(t, dir, "config", "user.name", "atlas")
	gitIn(t, dir, "config", "user.email", "atlas@example.invalid")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "readme.md"), "# atlas\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "init")
	return dir
}

// newBareRemote 建一个空的裸仓库当远端，全程本地、不联网。
func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	gitIn(t, dir, "init", "--bare", "-b", "main", bare)
	return bare
}

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if !HasGit() {
		t.Skip("本机没有可用的 git 命令，跳过")
	}
}

func TestHasGit(t *testing.T) {
	// HasGit 的语义就是「PATH 里能找到 git」，与 exec.LookPath 必须一致。
	_, lerr := exec.LookPath("git")
	if got, want := HasGit(), lerr == nil; got != want {
		t.Fatalf("HasGit() = %v，exec.LookPath 判定 = %v", got, want)
	}
}

func TestInspectNotRepo(t *testing.T) {
	skipWithoutGit(t)
	dir := t.TempDir()
	st := Inspect(dir)
	if st.IsRepo {
		t.Fatalf("普通目录 IsRepo = true，期望 false")
	}
	if st.Err == "" {
		t.Fatal("不是仓库时 Err 不应为空")
	}
}

func TestInspectMissingDir(t *testing.T) {
	skipWithoutGit(t)
	st := Inspect(filepath.Join(t.TempDir(), "not-exist"))
	if st.IsRepo {
		t.Fatal("不存在的目录 IsRepo = true，期望 false")
	}
	if st.Err == "" {
		t.Fatal("目录不存在时 Err 不应为空")
	}
}

func TestInspectRepo(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)

	st := Inspect(dir)
	if !st.IsRepo {
		t.Fatalf("IsRepo = false，Err = %q", st.Err)
	}
	if st.Err != "" {
		t.Errorf("正常仓库不该有 Err：%q", st.Err)
	}
	if st.Branch != "main" {
		t.Errorf("Branch = %q，期望 main", st.Branch)
	}
	if st.Dirty != 0 {
		t.Errorf("Dirty = %d，期望 0", st.Dirty)
	}
	if st.LastCommit == "" || !strings.Contains(st.LastCommit, "init") {
		t.Errorf("LastCommit = %q，期望含短 hash 与标题 init", st.LastCommit)
	}
	if st.LastCommitAt.IsZero() {
		t.Error("LastCommitAt 为零值，期望解析出提交时间")
	}
	if st.Upstream != "" {
		t.Errorf("Upstream = %q，本地新仓库应为空", st.Upstream)
	}
	if st.Ahead != 0 || st.Behind != 0 {
		t.Errorf("Ahead/Behind = %d/%d，无上游时应为 0/0", st.Ahead, st.Behind)
	}
	if st.RemoteURL != "" {
		t.Errorf("RemoteURL = %q，未配置 origin 时应为空", st.RemoteURL)
	}
}

func TestInspectDetached(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)
	gitIn(t, dir, "checkout", "--detach", "HEAD")

	st := Inspect(dir)
	if !st.IsRepo {
		t.Fatalf("IsRepo = false，Err = %q", st.Err)
	}
	if st.Branch != "" {
		t.Errorf("detached HEAD 时 Branch = %q，期望为空", st.Branch)
	}
	if st.LastCommit == "" {
		t.Error("detached HEAD 仍应读到最后一次提交")
	}
}

func TestCommitAllFlow(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)
	ctx := context.Background()

	writeFile(t, filepath.Join(dir, "new.txt"), "hello\n")
	if st := Inspect(dir); st.Dirty < 1 {
		t.Fatalf("新增未跟踪文件后 Dirty = %d，期望 >= 1", st.Dirty)
	}

	sha, err := CommitAll(ctx, dir, "")
	if err != nil {
		t.Fatalf("CommitAll 失败: %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("短 hash = %q，长度不足 7", sha)
	}
	if st := Inspect(dir); st.Dirty != 0 {
		t.Errorf("提交后 Dirty = %d，期望 0", st.Dirty)
	}
	// 空 message 应回落到默认文案。
	if got := strings.TrimSpace(gitIn(t, dir, "log", "-1", "--format=%s")); got != "chore: 自动提交" {
		t.Errorf("默认提交信息 = %q，期望 chore: 自动提交", got)
	}

	if _, err := CommitAll(ctx, dir, "再提交一次"); !errors.Is(err, ErrNoChanges) {
		t.Errorf("干净工作区 CommitAll 的 err = %v，期望 ErrNoChanges", err)
	}
}

func TestAheadBehindAndPush(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)
	bare := newBareRemote(t)
	ctx := context.Background()

	// 本地路径换成正斜杠，避免 git 在 Windows 上把反斜杠当转义符处理。
	gitIn(t, dir, "remote", "add", "origin", filepath.ToSlash(bare))
	gitIn(t, dir, "push", "-u", "origin", "main")

	st := Inspect(dir)
	if st.Upstream != "origin/main" {
		t.Fatalf("Upstream = %q，期望 origin/main", st.Upstream)
	}
	if st.RemoteURL == "" {
		t.Error("RemoteURL 为空，期望读到 origin 地址")
	}
	if st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("刚推送后 Ahead/Behind = %d/%d，期望 0/0", st.Ahead, st.Behind)
	}

	writeFile(t, filepath.Join(dir, "ahead.txt"), "x\n")
	if _, err := CommitAll(ctx, dir, "本地领先一个提交"); err != nil {
		t.Fatalf("CommitAll 失败: %v", err)
	}
	if st := Inspect(dir); st.Ahead != 1 {
		t.Fatalf("未推送时 Ahead = %d，期望 1", st.Ahead)
	}

	out, err := Push(ctx, dir)
	if err != nil {
		t.Fatalf("Push 失败: %v（输出 %s）", err, out)
	}
	if st := Inspect(dir); st.Ahead != 0 {
		t.Fatalf("推送后 Ahead = %d，期望 0", st.Ahead)
	}
}

func TestPushSetsUpstream(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)
	bare := newBareRemote(t)
	ctx := context.Background()

	gitIn(t, dir, "remote", "add", "origin", filepath.ToSlash(bare))
	// 没有 upstream：Push 应自动走 git push -u origin main。
	if out, err := Push(ctx, dir); err != nil {
		t.Fatalf("无上游时 Push 失败: %v（输出 %s）", err, out)
	}
	if st := Inspect(dir); st.Upstream != "origin/main" {
		t.Fatalf("Push 后 Upstream = %q，期望 origin/main", st.Upstream)
	}
}

func TestFetchAndPull(t *testing.T) {
	skipWithoutGit(t)
	dir := initRepo(t)
	bare := newBareRemote(t)
	ctx := context.Background()

	gitIn(t, dir, "remote", "add", "origin", filepath.ToSlash(bare))
	gitIn(t, dir, "push", "-u", "origin", "main")

	// 第二个工作副本：模拟同事推来一个提交。
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", filepath.ToSlash(bare), filepath.ToSlash(other))
	gitIn(t, other, "config", "user.name", "atlas2")
	gitIn(t, other, "config", "user.email", "atlas2@example.invalid")
	gitIn(t, other, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(other, "from-other.txt"), "y\n")
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-m", "第二个副本的提交")
	gitIn(t, other, "push")

	if _, err := Fetch(ctx, dir); err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	// fetch 只更新远端跟踪引用，工作区还没合并，所以这里应落后 1。
	if st := Inspect(dir); st.Behind != 1 {
		t.Fatalf("fetch 后 Behind = %d，期望 1", st.Behind)
	}

	out, err := Pull(ctx, dir)
	if err != nil {
		t.Fatalf("Pull 失败: %v", err)
	}
	if out == "" {
		t.Error("Pull 输出为空，期望至少包含 git 的进度提示")
	}
	st := Inspect(dir)
	if st.Behind != 0 || st.Ahead != 0 {
		t.Fatalf("pull 后 Ahead/Behind = %d/%d，期望 0/0", st.Ahead, st.Behind)
	}
	if _, err := os.Stat(filepath.Join(dir, "from-other.txt")); err != nil {
		t.Errorf("pull 后没拿到同事的文件: %v", err)
	}
}

// TestInspectChinesePath 覆盖 Windows 上最容易出问题的路径：含中文与空格。
// 路径只交给 cmd.Dir，不拼进命令行，所以这些字符不需要任何转义。
func TestInspectChinesePath(t *testing.T) {
	skipWithoutGit(t)
	dir := filepath.Join(t.TempDir(), "项目 路径", "仓库")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	gitIn(t, dir, "init", "-b", "main")
	gitIn(t, dir, "config", "user.name", "atlas")
	gitIn(t, dir, "config", "user.email", "atlas@example.invalid")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "说明.md"), "内容\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "中文路径的初始提交")

	// 再加一个中文名文件：core.quotepath=false 时应算作 1 个改动，
	// 且路径不会变成 \350\257\264 这样的八进制转义。
	writeFile(t, filepath.Join(dir, "新增 文件.txt"), "x\n")
	st := Inspect(dir)
	if !st.IsRepo {
		t.Fatalf("IsRepo = false，Err = %q", st.Err)
	}
	if st.Dirty != 1 {
		t.Errorf("Dirty = %d，期望 1", st.Dirty)
	}
	if !strings.Contains(st.LastCommit, "中文路径的初始提交") {
		t.Errorf("LastCommit = %q，期望含中文提交标题", st.LastCommit)
	}
	out := gitIn(t, dir, "-c", "core.quotepath=false", "status", "--porcelain")
	if !strings.Contains(out, "新增 文件.txt") {
		t.Errorf("status 输出 = %q，期望中文文件名原样出现", out)
	}
}

func TestEmptyDir(t *testing.T) {
	skipWithoutGit(t)
	st := Inspect("")
	if st.IsRepo {
		t.Error("空目录 IsRepo = true，期望 false")
	}
	if st.Err != "未设置本地目录" {
		t.Errorf("Err = %q，期望「未设置本地目录」", st.Err)
	}

	ctx := context.Background()
	if _, err := Fetch(ctx, ""); err == nil {
		t.Error("Fetch 空目录应报错")
	}
	if _, err := Pull(ctx, ""); err == nil {
		t.Error("Pull 空目录应报错")
	}
	if _, err := CommitAll(ctx, "", "x"); err == nil {
		t.Error("CommitAll 空目录应报错")
	}
	if _, err := Push(ctx, ""); err == nil {
		t.Error("Push 空目录应报错")
	}
}

// newRemoteWithCommit 建一个「已有一次提交」的裸仓库远端，全程本地、不联网。
//
// 做法：普通仓库 initRepo 后加 origin、push -u；沿用 initRepo 里已经配好的
// 身份与 commit.gpgsign=false，避免 CI 上没有全局 git 身份导致 push/commit 失败。
func newRemoteWithCommit(t *testing.T) string {
	t.Helper()
	work := initRepo(t)
	bare := newBareRemote(t)
	// 本地路径统一换成正斜杠：Windows 上反斜杠会被 git 当转义符。
	gitIn(t, work, "remote", "add", "origin", filepath.ToSlash(bare))
	gitIn(t, work, "push", "-u", "origin", "main")
	return bare
}

// TestClone 克隆本地裸仓库：父目录不存在也要自动创建，落地后应是可识别的
// git 工作区，文件齐全、分支跟随远端。
func TestClone(t *testing.T) {
	skipWithoutGit(t)
	bare := newRemoteWithCommit(t)

	// 刻意给三层不存在的父目录：Clone 必须自己 MkdirAll，git clone 不会建父目录。
	dst := filepath.Join(t.TempDir(), "a", "b", "dst")
	if err := Clone(context.Background(), filepath.ToSlash(bare), dst); err != nil {
		t.Fatalf("Clone 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "readme.md")); err != nil {
		t.Errorf("克隆目录里没拿到 readme.md: %v", err)
	}
	st := Inspect(dst)
	if !st.IsRepo {
		t.Fatalf("克隆后 Inspect.IsRepo = false，Err = %q", st.Err)
	}
	if st.Branch != "main" {
		t.Errorf("Branch = %q，期望 main", st.Branch)
	}
	if st.RemoteURL == "" {
		t.Error("RemoteURL 为空，克隆后应能读到 origin 地址")
	}
}

// TestCloneTargetNotEmpty 目标目录已存在且非空：直接报错，且一个字节都不动。
func TestCloneTargetNotEmpty(t *testing.T) {
	skipWithoutGit(t)
	bare := newRemoteWithCommit(t)

	dst := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	keep := filepath.Join(dst, "keep.txt")
	writeFile(t, keep, "原有内容\n")

	if err := Clone(context.Background(), filepath.ToSlash(bare), dst); err == nil {
		t.Fatal("非空目标目录应报错")
	}
	b, err := os.ReadFile(keep)
	if err != nil || string(b) != "原有内容\n" {
		t.Errorf("原文件被破坏: %q err=%v", b, err)
	}
	// 也不该留下任何克隆产物。
	if _, err := os.Stat(filepath.Join(dst, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("失败后不应留下 .git: %v", err)
	}
}

// TestCloneIntoEmptyDir 目标目录存在但为空：允许，等价于 git 自己的行为。
func TestCloneIntoEmptyDir(t *testing.T) {
	skipWithoutGit(t)
	bare := newRemoteWithCommit(t)

	dst := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := Clone(context.Background(), filepath.ToSlash(bare), dst); err != nil {
		t.Fatalf("克隆到空目录应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "readme.md")); err != nil {
		t.Errorf("克隆结果不完整: %v", err)
	}
	if st := Inspect(dst); !st.IsRepo {
		t.Errorf("克隆后不是 git 工作区: %q", st.Err)
	}
}

// TestCloneCanceledContext 已被取消的 ctx 必须立刻失败，且不留下半成品目录。
func TestCloneCanceledContext(t *testing.T) {
	skipWithoutGit(t)
	bare := newRemoteWithCommit(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dst := filepath.Join(t.TempDir(), "canceled")
	err := Clone(ctx, filepath.ToSlash(bare), dst)
	if err == nil {
		t.Fatal("已取消的 ctx 应报错")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误应包裹 context.Canceled，实际 %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dst, ".git")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("取消后不应留下克隆结果: %v", serr)
	}
}

// TestCloneValidation 空 url / 空 dir（含纯空白）都要给明确错误，不能落到 git。
func TestCloneValidation(t *testing.T) {
	skipWithoutGit(t)
	ctx := context.Background()
	dst := filepath.Join(t.TempDir(), "x")

	for _, c := range []struct {
		name, url, dir string
	}{
		{"空 url", "", dst},
		{"空白 url", "   ", dst},
		{"空 dir", "https://example.invalid/a/b.git", ""},
		{"空白 dir", "https://example.invalid/a/b.git", "  \t "},
	} {
		if err := Clone(ctx, c.url, c.dir); err == nil {
			t.Errorf("%s：应报错", c.name)
		}
	}
}
