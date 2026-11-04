package view

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"bestuav-atlas/internal/gitx"
	"bestuav-atlas/internal/github"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/store"
)

// 本文件是仓库页与首页共用的「批量动作」：刷新状态、拉取更新、提交并推送、
// 刷新远端信息。四个动作都是**手动触发**（没有定时器、没有后台轮询），
// 与产品约束一致：星图不替你决定什么时候联网。
//
// 线程模型沿用项目既有做法：后台协程只做 I/O 与写库（store 自身串行化），
// 进度与结果通过 mygo.RunOnMain 回到主线程；界面本身只读 a.taskRunning /
// a.taskProgress（页头浅线处的进度条），不直接碰控件。

// batchKind 批量动作的种类。
type batchKind int

const (
	batchInspect batchKind = iota // 只读本地 git 状态
	batchPull                     // fetch + pull --ff-only
	batchCommitPush               // add -A + commit + push
	batchRemote                   // 调 GitHub API 拉远端元信息
)

// label 动作名，用于页头进度与任务记录。
func (k batchKind) label() string {
	switch k {
	case batchPull:
		return "批量拉取更新"
	case batchCommitPush:
		return "批量提交并推送"
	case batchRemote:
		return "刷新远端信息"
	default:
		return "刷新仓库状态"
	}
}

// batchTimeout 单个仓库 I/O 的上限：网络操作给足 90 秒，本地操作 15 秒足够。
func batchTimeout(k batchKind) time.Duration {
	if k == batchPull || k == batchCommitPush || k == batchRemote {
		return 90 * time.Second
	}
	return 15 * time.Second
}

// runRepoBatch 串行处理一批仓库。
//
// 为什么串行而不是并发：git 操作会争用同一个仓库的 index.lock，而且网络请求
// 并发只会更快撞上 GitHub 限流；十个仓库串行也只要几秒到几十秒。串行还有个好处：
// 进度条是「第几个 / 共几个」，用户能看懂。
func (a *App) runRepoBatch(kind batchKind, ids []int64, commitMsg string) {
	if a.store == nil {
		a.toast("本次运行不落库，无法批量操作")
		return
	}
	if a.taskRunning || a.heavyRunning {
		a.toast("已有任务在执行，请稍候")
		return
	}
	repos := a.reposByIDs(ids)
	if len(repos) == 0 {
		a.toast("先选中要处理的仓库")
		return
	}
	// 「刷新远端信息」必须联网，没令牌时提前拦住：否则每个仓库各报一次
	// 「请填写令牌」，用户要点掉 N 条提示才知道问题在哪。
	token := strings.TrimSpace(a.settings.RepoToken)
	if kind == batchRemote && token == "" {
		a.toast("刷新远端信息需要 GitHub 令牌：设置 → 工作区")
		return
	}

	label := fmt.Sprintf("%s（%d 个）", kind.label(), len(repos))
	a.taskRunning = true
	a.taskProgress = 0
	a.taskLabel = label
	a.atlasUI.busy = true
	a.requestFrame()

	// 只把值带进后台：App 只在 RunOnMain 里被触碰。
	go func() {
		defer logging.Recover("atlas-batch")

		ok, failed := 0, 0
		var firstErr string
		for i, r := range repos {
			ctx, cancel := context.WithTimeout(context.Background(), batchTimeout(kind))
			err := a.batchOne(ctx, kind, r, commitMsg, token)
			cancel()
			if err != nil {
				failed++
				if firstErr == "" {
					firstErr = r.Name + "：" + err.Error()
				}
				logging.Errv("atlas", "批量操作单项失败", err, "repo", r.Name, "kind", kind.label())
			} else {
				ok++
			}
			done := float64(i+1) / float64(len(repos))
			mygo.RunOnMain(func() {
				a.taskProgress = done
				a.requestFrame()
			})
		}

		mygo.RunOnMain(func() {
			a.taskRunning = false
			a.taskProgress = 0
			a.taskLabel = ""
			a.atlasUI.busy = false
			a.reloadAtlas()

			status := "已完成"
			if failed > 0 {
				status = "失败"
			}
			a.pushTaskRow(fmt.Sprintf("%s（成功 %d / 失败 %d）", label, ok, failed), "轻量级", status)
			switch {
			case failed == 0:
				a.toast(fmt.Sprintf("%s完成：%d 个仓库", kind.label(), ok))
				a.celebrate(2)
			case ok == 0:
				a.toast("全部失败：" + firstErr)
			default:
				a.toast(fmt.Sprintf("%d 个成功、%d 个失败（%s）", ok, failed, firstErr))
			}
			a.requestFrame()
		})
	}()
}

// batchOne 处理单个仓库；返回该仓库的错误（汇总展示用）。
func (a *App) batchOne(ctx context.Context, kind batchKind, r store.Repo, commitMsg, token string) error {
	switch kind {
	case batchPull:
		if strings.TrimSpace(r.LocalPath) == "" {
			return errors.New("未设置本地目录")
		}
		if _, err := gitx.Fetch(ctx, r.LocalPath); err != nil {
			// fetch 失败仍探测一次：至少把「落后/领先」刷新成最后一次已知状态。
			a.refreshRepoStatus(r)
			return err
		}
		if _, err := gitx.Pull(ctx, r.LocalPath); err != nil {
			a.refreshRepoStatus(r)
			return err
		}
		a.refreshRepoStatus(r)
		return nil

	case batchCommitPush:
		if strings.TrimSpace(r.LocalPath) == "" {
			return errors.New("未设置本地目录")
		}
		msg := strings.TrimSpace(commitMsg)
		if msg == "" {
			msg = "chore: 同步 " + r.Name + " 的本地改动"
		}
		if _, err := gitx.CommitAll(ctx, r.LocalPath, msg); err != nil && !errors.Is(err, gitx.ErrNoChanges) {
			a.refreshRepoStatus(r)
			return err
		}
		// 没有新提交也要推一次：可能是上次提交后推送失败（例如断网），
		// 这时本地领先远端，正是需要补推的状态。
		if _, err := gitx.Push(ctx, r.LocalPath); err != nil {
			a.refreshRepoStatus(r)
			return err
		}
		a.refreshRepoStatus(r)
		return nil

	case batchRemote:
		return a.refreshRemoteOne(ctx, r, token)

	default:
		a.refreshRepoStatus(r)
		return nil
	}
}

// refreshRemoteOne 用 GitHub API 读一次远端元信息。
//
// 本地 git 能回答「我领先/落后远端几个提交」，但回答不了「远端仓库本身怎么样」：
// 默认分支、最近一次推送、star、未关闭 issue。未克隆的仓库更是连本地状态都没有，
// 只有 API 能告诉我们它还活着。结果写进 status_msg（本地状态词不动）。
func (a *App) refreshRemoteOne(ctx context.Context, r store.Repo, token string) error {
	owner, name, ok := github.ParseRemote(r.URL)
	if !ok {
		owner, name, ok = github.SplitFull(r.Name)
	}
	if !ok {
		return fmt.Errorf("无法从地址解析出 owner/repo")
	}
	info, err := github.New(token).Repo(ctx, owner, name)
	if err != nil {
		return err
	}
	info2 := r
	info2.StatusMsg = fmt.Sprintf("远端 %s · 最近推送 %s · ★%d · issues %d",
		firstNonEmpty(info.DefaultBranch, "main"), timeAgo(info.PushedAt), info.Stars, info.OpenIssues)
	info2.CheckedAt = time.Now()
	if r.Status == RepoOK || r.Status == RepoUnknown {
		info2.Status = RepoOK
	}
	if a.store != nil {
		if err := a.store.SaveRepoStatus(r.ID, info2); err != nil {
			return err
		}
	}
	return nil
}

// reposByIDs 按 id 取出仓库快照（保持调用方给的顺序，去重）。
func (a *App) reposByIDs(ids []int64) []store.Repo {
	d := a.atlas()
	index := make(map[int64]store.Repo, len(d.repos))
	for _, r := range d.repos {
		index[r.ID] = r
	}
	out := make([]store.Repo, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if r, ok := index[id]; ok {
			seen[id] = true
			out = append(out, r)
		}
	}
	return out
}

// selectedRepoIDs 返回仓库页当前选中的 id（按列表顺序，未选中为空）。
func (a *App) selectedRepoIDs() []int64 {
	rows := a.filteredRepos()
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if a.atlasUI.sel[r.ID] {
			out = append(out, r.ID)
		}
	}
	return out
}

// filteredRepos 按搜索框、状态筛选、项目筛选过滤后的仓库列表（已排序）。
func (a *App) filteredRepos() []store.Repo {
	d := a.atlas()
	ui := &a.atlasUI
	q := strings.ToLower(strings.TrimSpace(ui.repoSearch))
	out := make([]store.Repo, 0, len(d.repos))
	for _, r := range d.sortedRepos() {
		if ui.repoStatus != "" && ui.repoStatus != "全部" && r.Status != ui.repoStatus {
			continue
		}
		if ui.repoProject != "" && ui.repoProject != "全部项目" &&
			d.projectNameOrDash(r.ProjectID) != ui.repoProject {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.URL+" "+r.LocalPath+" "+r.Note), q) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ---- 批量导入 ----

// importRepos 把一批「owner/repo」或仓库地址登记进库。
//
// 导入只写元数据（名字、地址、本地目录），状态留给随后的批量探测——导入时不该
// 逐个去跑 git（用户可能一次贴 20 个仓库，界面会卡住）。
func (a *App) importRepos(lines []string, projectID int64, root string) (added, skipped int) {
	if a.store == nil {
		a.toast("本次运行不落库，无法导入")
		return 0, 0
	}
	d := a.atlas()
	existing := make(map[string]bool, len(d.repos))
	for _, r := range d.repos {
		existing[strings.ToLower(r.Name)] = true
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		owner, name, ok := github.ParseRemote(line)
		if !ok {
			owner, name, ok = github.SplitFull(line)
		}
		if !ok {
			skipped++
			continue
		}
		full := owner + "/" + name
		if existing[strings.ToLower(full)] {
			skipped++
			continue
		}
		r := store.Repo{
			ProjectID: projectID,
			Name:      full,
			URL:       "https://github.com/" + full,
			LocalPath: repoPathUnder(root, full),
			Status:    RepoUnknown,
		}
		if _, err := a.store.CreateRepo(r); err != nil {
			logging.Errv("atlas", "导入仓库失败", err, "repo", full)
			skipped++
			continue
		}
		existing[strings.ToLower(full)] = true
		added++
	}
	a.reloadAtlas()
	return added, skipped
}

// repoPathUnder 在指定根目录下算出仓库的本地目录；root 为空时返回空（调用方
// 会在导入后按全局工作目录补一次），这样「把仓库留在别处」的条目不会被硬塞路径。
func repoPathUnder(root, full string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return filepath.Join(root, repoSafeDir(full))
}

// ---- 并行探测（导入后首次填状态）----
//
// 与 runRepoBatch 的区别：这里不显示页头进度，也不写任务记录——导入完成的那一刻
// 用户关心的是「列表里出现哪些仓库」，状态晚一两秒无所谓。用 waitgroup 并发探测
// 纯本地操作（status/log/rev-list），几十个仓库也是毫秒级。

// inspectAllAsync 后台探测全部仓库的本地状态，结束后刷新快照。
func (a *App) inspectAllAsync() {
	if a.store == nil {
		return
	}
	repos := append([]store.Repo(nil), a.atlas().repos...)
	if len(repos) == 0 {
		return
	}
	go func() {
		defer logging.Recover("atlas-inspect-all")
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4) // 限制并发，别让几十个 git 进程同时起
		for _, r := range repos {
			wg.Add(1)
			sem <- struct{}{}
			go func(r store.Repo) {
				defer wg.Done()
				defer func() { <-sem }()
				a.refreshRepoStatus(r)
			}(r)
		}
		wg.Wait()
		mygo.RunOnMain(func() {
			a.reloadAtlas()
			a.requestFrame()
		})
	}()
}
