package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"bestuav-atlas/internal/forge"
	"bestuav-atlas/internal/gitx"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/settings"
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
	batchInspect    batchKind = iota // 只读本地 git 状态
	batchPull                        // fetch + pull --ff-only
	batchCommitPush                  // add -A + commit + push
	batchRemote                      // 调平台 API 拉远端元信息
	batchClone                       // 从远端克隆到本地根目录
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
	case batchClone:
		return "克隆仓库"
	default:
		return "刷新仓库状态"
	}
}

// batchTimeout 单个仓库 I/O 的上限：网络操作给足 90 秒，克隆给 5 分钟
// （大仓库在慢网下确实会超过 90 秒），本地操作 15 秒足够。
func batchTimeout(k batchKind) time.Duration {
	switch k {
	case batchClone:
		return 5 * time.Minute
	case batchPull, batchCommitPush, batchRemote:
		return 90 * time.Second
	default:
		return 15 * time.Second
	}
}

// asyncLimit 当前设置允许的后台并发数；越界值兜底到默认，避免脏配置把
// 任务池开成 0（永远不开始）或几百（把机器打满）。
//
// 只在主线程、启动协程之前调用；协程里不要再读 a.settings。
func (a *App) asyncLimit() int {
	n := a.settings.AsyncConcurrency
	if n < 1 || n > settings.MaxAsyncConcurrency {
		return settings.DefaultAsyncConcurrency
	}
	return n
}

// tokenSet 是主线程读好的令牌快照。
//
// 为什么不在协程里直接读 a.settings：设置可能正被用户编辑（主线程写），
// 跨线程读同一份结构在 -race 下是数据竞争。批次开始前拍一份快照，协程只用它。
type tokenSet struct {
	github, gitee, atomgit, gitea, giteaHost string
	// giteaScheme 自建 Gitea 的协议（http/https），同样在批次前定好。
	giteaScheme string
}

func (a *App) tokensSnapshot() tokenSet {
	return tokenSet{
		github:      strings.TrimSpace(a.settings.RepoToken),
		gitee:       strings.TrimSpace(a.settings.GiteeToken),
		atomgit:     strings.TrimSpace(a.settings.AtomGitToken),
		gitea:       strings.TrimSpace(a.settings.GiteaToken),
		giteaHost:   hostOf(a.settings.GiteaHost),
		giteaScheme: a.giteaScheme(),
	}
}

// tokenFor 按平台取令牌（与 App.tokenFor 同规则，但只读快照）。
func (ts tokenSet) tokenFor(host string) string {
	switch host {
	case "github.com":
		return ts.github
	case "gitee.com":
		return ts.gitee
	case "atomgit.com":
		return ts.atomgit
	default:
		return ts.gitea
	}
}

// client 按平台构造 API 客户端（自建 Gitea 用快照里的实例地址与协议）。
func (ts tokenSet) client(p forge.Platform) *forge.Client {
	if p.Provider == forge.Gitea {
		if ts.giteaHost != "" {
			p.Host = ts.giteaHost
		}
		p.Scheme = ts.giteaScheme
	}
	return forge.New(p, ts.tokenFor(p.Host))
}

// runRepoBatch 用「有限并发的工作池」处理一批仓库。
//
// 并发数来自设置里的「异步任务 → 最大并发」（默认 4，1–8 可调）。做法是
// workers 个协程从同一个 jobs 通道取仓库：一个跑完立刻取下一个，直到这批
// 全部完成——正好对应「20 个仓库一次最多克隆 3~5 个，完成一个再补一个」。
//
// 为什么不无限并发：git 操作会争用磁盘与网络，几十个 git 进程同时起只会更慢；
// 为什么不再串行：克隆/拉取是纯 I/O，几个仓库并行能明显缩短总时间，而进度条
// 仍按「已完成 / 总数」推进，用户看得懂。
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
	// 「刷新远端信息」必须联网：没令牌时提前拦住，否则每个仓库各报一次
	// 「请填写令牌」，用户要点掉 N 条提示才知道问题在哪。Gitea 还要实例地址。
	tokens := a.tokensSnapshot()
	if kind == batchRemote {
		p := a.platformForRepo(repos[0])
		if tokens.tokenFor(p.Host) == "" {
			a.toast("刷新远端信息需要 " + p.Label() + " 令牌：设置 → 工作区")
			return
		}
	}

	limit := a.asyncLimit()
	label := fmt.Sprintf("%s（%d 个）", kind.label(), len(repos))
	a.taskRunning = true
	a.taskProgress = 0
	a.taskLabel = label
	a.atlasUI.busy = true
	a.requestFrame()

	// 只把值带进后台：App 只在 RunOnMain 里被触碰。
	go func() {
		defer logging.Recover("atlas-batch")

		workers := min(limit, len(repos))
		// 缓冲队列一次装下整批任务：worker 一个跑完就取下一条。即使某个
		// worker 内部 panic（下面 Recover 住），也不会让投递方永远阻塞。
		jobs := make(chan int, len(repos))
		for i := range repos {
			jobs <- i
		}
		close(jobs)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, failed, done := 0, 0, 0
		var firstErr string

		work := func() {
			defer wg.Done()
			defer logging.Recover("atlas-batch-worker")
			for i := range jobs {
				r := repos[i]
				ctx, cancel := context.WithTimeout(context.Background(), batchTimeout(kind))
				err := a.batchOne(ctx, kind, r, commitMsg, tokens)
				cancel()

				mu.Lock()
				if err != nil {
					failed++
					if firstErr == "" {
						firstErr = r.Name + "：" + err.Error()
					}
					logging.Errv("atlas", "批量操作单项失败", err, "repo", r.Name, "kind", kind.label())
				} else {
					ok++
				}
				done++
				progress := float64(done) / float64(len(repos))
				mu.Unlock()

				mygo.RunOnMain(func() {
					a.taskProgress = progress
					a.requestFrame()
				})
			}
		}

		wg.Add(workers)
		for range workers {
			go work()
		}
		wg.Wait()

		// 所有 worker 已退出，这里读计数不需要再拿锁；交给主线程的是值拷贝。
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
func (a *App) batchOne(ctx context.Context, kind batchKind, r store.Repo, commitMsg string, tokens tokenSet) error {
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
		return a.refreshRemoteOne(ctx, r, tokens)

	case batchClone:
		return a.cloneOne(ctx, r)

	default:
		a.refreshRepoStatus(r)
		return nil
	}
}

// cloneOne 把一个仓库克隆到它的本地目录。
//
// 目录已有内容就不动（用户可能已经手动克隆过，或用本地导入登记了已有目录）——
// 这时只刷新一次状态，把结果当作「已克隆」。
func (a *App) cloneOne(ctx context.Context, r store.Repo) error {
	url := strings.TrimSpace(r.URL)
	if url == "" {
		return errors.New("没有远端地址")
	}
	dir := strings.TrimSpace(r.LocalPath)
	if dir == "" {
		dir = a.repoLocalPath(r.Name)
		r.LocalPath = dir
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		a.refreshRepoStatus(r)
		return nil
	}
	if err := gitx.Clone(ctx, url, dir); err != nil {
		return err
	}
	// 克隆完把本地目录与分支写回登记，状态也顺手算出来。
	next := r
	next = a.inspectRepo(next)
	if a.store != nil {
		if err := a.store.UpdateRepo(next); err != nil {
			return err
		}
	}
	return nil
}

// cloneRepos 批量克隆（导入时勾了「顺手 clone」就走这里）。
func (a *App) cloneRepos(ids []int64) {
	a.runRepoBatch(batchClone, ids, "")
}

// refreshRemoteOne 用平台 API 读一次远端元信息。
//
// 本地 git 能回答「我领先/落后远端几个提交」，但回答不了「远端仓库本身怎么样」：
// 默认分支、最近一次推送、star、未关闭 issue。未克隆的仓库更是连本地状态都没有，
// 只有 API 能告诉我们它还活着。结果写进 status_msg（本地状态词不动）。
//
// 平台由仓库登记里的 Host / URL 决定：GitHub / Gitee / Gitea / AtomGit 各用各的
// 端点与令牌，见 internal/forge。
func (a *App) refreshRemoteOne(ctx context.Context, r store.Repo, tokens tokenSet) error {
	p := a.platformForRepo(r)
	owner, name, ok := forgeOwnerRepo(r, p)
	if !ok {
		return fmt.Errorf("无法从地址解析出 owner/repo")
	}
	info, err := tokens.client(p).Repo(ctx, owner, name)
	if err != nil {
		return err
	}
	next := r
	next.StatusMsg = fmt.Sprintf("%s 远端 %s · 最近推送 %s · ★%d · issues %d",
		p.Label(), firstNonEmpty(info.DefaultBranch, "main"), timeAgo(info.PushedAt), info.Stars, info.OpenIssues)
	next.CheckedAt = time.Now()
	if r.Status == RepoOK || r.Status == RepoUnknown {
		next.Status = RepoOK
	}
	if a.store != nil {
		if err := a.store.SaveRepoStatus(r.ID, next); err != nil {
			return err
		}
	}
	return nil
}

// forgeOwnerRepo 从仓库登记里解析 owner/repo；URL 解析不出来时退回名字。
func forgeOwnerRepo(r store.Repo, def forge.Platform) (owner, repo string, ok bool) {
	if _, o, n, k := forge.Detect(r.URL); k {
		return o, n, true
	}
	if _, o, n, k := forge.Detect(r.Name); k {
		return o, n, true
	}
	// 纯 owner/repo（没有域名）：按给定平台理解。
	if o, n, k := splitOwnerRepo(r.Name); k {
		return o, n, true
	}
	return "", "", false
}

// splitOwnerRepo 只认「恰好两段、都不为空」的 owner/repo。
func splitOwnerRepo(s string) (owner, repo string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
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

// ---- 本地文件夹导入 ----

// pickLocalRepos 弹出系统文件夹选择框（可多选），把选中的目录解析成仓库登记。
//
// 解析完全靠本地信息：`.git/config` 里的 origin 地址决定名字，当前分支与
// ahead/behind 由 gitx.Inspect 读出。没有远端地址（纯本地仓库）时用文件夹名，
// 这类仓库仍然能看状态、能提交，只是「在 GitHub 打开」会没有链接。
func (a *App) pickLocalRepos(projectID int64) {
	if a.store == nil {
		a.toast("本次运行不落库，无法导入")
		return
	}
	// 在起协程之前读窗口句柄：协程里读 a.win 属于跨线程读 App 状态。
	parent := a.win
	a.atlasUI.busy = true
	go func() {
		defer logging.Recover("atlas-pick-local")
		dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:      parent,
			Title:       "选择本地仓库文件夹",
			ButtonLabel: "导入",
			Directory:   true,
			Multiple:    true,
		})
		mygo.RunOnMain(func() {
			a.atlasUI.busy = false
			switch {
			case err != nil:
				logging.Errv("atlas", "选择本地文件夹失败", err)
				a.toast("打开文件夹选择框失败：" + err.Error())
			case len(dirs) == 0:
				// 取消：什么都不做，弹窗保持打开，用户可以继续贴地址。
			default:
				// 选择框已经关闭，剩下的是逐个目录跑 git：交给重量级任务
				// （全窗口遮罩 + 进度），期间界面不可操作，避免用户以为卡死。
				a.startLocalImport(dirs, projectID)
			}
			a.requestFrame()
		})
	}()
}

// startLocalImport 在重量级任务里解析并登记一批本地目录。
//
// 重量级而不是轻量级：这一步会逐个目录读 .git（几十个目录时可能要数秒），
// 且中途不该让用户再点一次导入——遮罩 + 进度条正好表达「正在干活，别动」。
func (a *App) startLocalImport(dirs []string, projectID int64) {
	if a.heavyRunning || a.taskRunning {
		a.toast("已有任务在执行，请稍候")
		return
	}
	if a.store == nil {
		a.toast("本次运行不落库，无法导入")
		return
	}
	// 快照既有的仓库登记：去重逻辑在协程里跑，不能再去读 a.atlas()。
	existing := append([]store.Repo(nil), a.atlas().repos...)
	label := "从本地导入（" + itoa(len(dirs)) + " 个文件夹）"
	// 导入弹窗先关掉：接下来是遮罩的世界，弹窗留在下面没有意义。
	a.closeAtlasDialog()
	a.heavyRunning = true
	a.heavyProgress = 0
	a.heavyLabel = label
	a.requestFrame()

	go func() {
		defer logging.Recover("atlas-local-import")

		added, skipped, firstSkip, failed := a.importLocalCore(dirs, projectID, existing,
			func(done, total int) {
				p := float64(done) / float64(total)
				mygo.RunOnMain(func() {
					a.heavyProgress = p
					a.requestFrame()
				})
			})

		mygo.RunOnMain(func() {
			a.heavyRunning = false
			a.heavyProgress = 0
			a.heavyLabel = ""
			a.reloadAtlas()

			status := "已完成"
			if failed > 0 {
				status = "失败"
			}
			a.pushTaskRow(label+"（导入 "+itoa(added)+" / 跳过 "+itoa(skipped)+"）", "重量级", status)
			msg := "已从本地导入 " + itoa(added) + " 个仓库"
			if skipped > 0 {
				msg += "，跳过 " + itoa(skipped) + " 个"
				if firstSkip != "" {
					msg += "（" + firstSkip + "）"
				}
			}
			a.toast(msg)
			a.requestFrame()
		})
	}()
}

// importLocalRepos 解析并登记一批本地目录（同步版，测试与兜底用）。
func (a *App) importLocalRepos(dirs []string, projectID int64) (added, skipped int, firstSkip string) {
	existing := append([]store.Repo(nil), a.atlas().repos...)
	added, skipped, firstSkip, _ = a.importLocalCore(dirs, projectID, existing, nil)
	a.reloadAtlas()
	return added, skipped, firstSkip
}

// importLocalCore 逐目录解析并写库；不碰 a.atlas() 与 reloadAtlas，因此可以在
// 协程里跑（store 自身是串行化的）。done 用于推进进度（可空）。
//
// 返回成功数、跳过数、第一条跳过原因、写库失败数。
func (a *App) importLocalCore(dirs []string, projectID int64, existing []store.Repo,
	done func(done, total int)) (added, skipped int, firstSkip string, failed int) {
	byPath := make(map[string]bool, len(existing))
	byName := make(map[string]bool, len(existing))
	for _, r := range existing {
		byPath[strings.ToLower(filepath.Clean(r.LocalPath))] = true
		byName[strings.ToLower(r.Name)] = true
	}
	skip := func(reason string) {
		skipped++
		if firstSkip == "" {
			firstSkip = reason
		}
	}
	for i, dir := range dirs {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if byPath[strings.ToLower(dir)] {
			skip(filepath.Base(dir) + "：已在列表里")
			reportProgress(done, i+1, len(dirs))
			continue
		}
		st := gitx.Inspect(dir)
		if !st.IsRepo {
			skip(filepath.Base(dir) + "：不是 git 仓库")
			reportProgress(done, i+1, len(dirs))
			continue
		}
		name := ""
		// platform 只在本条目录内有效：没有远端的本地仓库不猜平台（Host 留空）。
		var platform forge.Platform
		if p, owner, repo, ok := forge.Detect(st.RemoteURL); ok {
			platform = p
			name = owner + "/" + repo
		}
		if name == "" {
			name = filepath.Base(dir)
		}
		if byName[strings.ToLower(name)] {
			skip(name + "：同名仓库已存在")
			reportProgress(done, i+1, len(dirs))
			continue
		}
		r := a.inspectRepo(store.Repo{
			ProjectID: projectID,
			Name:      name,
			Host:      platform.Host,
			URL:       st.RemoteURL,
			LocalPath: dir,
			Branch:    st.Branch,
			Status:    RepoUnknown,
		})
		if _, err := a.store.CreateRepo(r); err != nil {
			logging.Errv("atlas", "导入本地仓库失败", err, "dir", dir)
			failed++
			skip(filepath.Base(dir) + "：写库失败")
			reportProgress(done, i+1, len(dirs))
			continue
		}
		byPath[strings.ToLower(dir)] = true
		byName[strings.ToLower(name)] = true
		added++
		reportProgress(done, i+1, len(dirs))
	}
	return added, skipped, firstSkip, failed
}

// reportProgress 安全地回调进度（done 可能为 nil）。
func reportProgress(done func(done, total int), i, n int) {
	if done != nil {
		done(i, n)
	}
}

// ---- 远程批量导入 ----

// importRepos 把一批「owner/repo」或仓库地址登记进库。
//
// 导入只写元数据（名字、平台、地址、本地目录），状态留给随后的批量探测——导入时
// 不该逐个去跑 git（用户可能一次贴 20 个仓库，界面会卡住）。返回新登记的 id，
// 供「顺手 clone」使用。
//
// def 是「纯 owner/repo 行」（没有域名）时假定的平台，来自导入弹窗里的平台选择。
func (a *App) importRepos(lines []string, projectID int64, root string, def forge.Platform) (added, skipped int, ids []int64) {
	if a.store == nil {
		a.toast("本次运行不落库，无法导入")
		return 0, 0, nil
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
		platform, owner, name, ok := parseRepoLine(line, def)
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
			Host:      platform.Host,
			URL:       platform.WebURL(owner, name),
			LocalPath: repoPathUnder(root, full),
			Status:    RepoUnknown,
		}
		id, err := a.store.CreateRepo(r)
		if err != nil {
			logging.Errv("atlas", "导入仓库失败", err, "repo", full)
			skipped++
			continue
		}
		existing[strings.ToLower(full)] = true
		ids = append(ids, id)
		added++
	}
	a.reloadAtlas()
	return added, skipped, ids
}

// parseRepoLine 解析一行导入文本：完整地址走 forge.Detect，纯 owner/repo 用默认平台。
//
// 平台不是「猜出来的」就不猜：一行 `a/b` 到底是 GitHub 还是 Gitee，取决于用户在
// 导入弹窗里选的是哪个平台。
func parseRepoLine(line string, def forge.Platform) (forge.Platform, string, string, bool) {
	if p, owner, repo, ok := forge.Detect(line); ok {
		return p, owner, repo, true
	}
	if owner, repo, ok := splitOwnerRepo(line); ok {
		return def, owner, repo, true
	}
	return forge.Platform{}, "", "", false
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
	limit := a.asyncLimit() // 主线程读设置，协程只用快照
	go func() {
		defer logging.Recover("atlas-inspect-all")
		var wg sync.WaitGroup
		sem := make(chan struct{}, limit) // 与批量任务共用同一个「最大并发」设置
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
