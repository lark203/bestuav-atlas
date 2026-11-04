package store

import (
	"fmt"
	"time"
)

// 本文件是「本地库的保留策略」：任务、授权、事件三类记录都只增不减，
// 长期使用后 data.db 会慢慢变大（任务每跑一次一条、每次报错一条事件）。
// 界面最多只展示 20 条任务 / 6 条授权 / 若干事件，历史留太多没有意义。
//
// 策略在启动时执行一次：按条数保留最近 N 条，事件再叠加一个时间上限。
// 删除后如果空闲页占比很高，顺手 VACUUM 把文件缩回去。

// Retention 是各类记录的保留量。
type Retention struct {
	Tasks    int           // 任务保留条数
	Events   int           // 事件保留条数
	Licenses int           // 授权记录保留条数
	EventAge time.Duration // 事件的时间上限（0 = 不限）
}

// DefaultRetention 是默认保留策略。
//
// 取值考虑：任务 1000 条足够追溯「上周跑过什么」；事件 90 天 / 2000 条，
// 覆盖绝大多数「昨天还好好的」类报障；授权记录 200 条（生成是低频动作）。
func DefaultRetention() Retention {
	return Retention{
		Tasks:    1000,
		Events:   2000,
		Licenses: 200,
		EventAge: 90 * 24 * time.Hour,
	}
}

// Prune 按保留策略裁剪历史记录，返回删除的总条数。
//
// 任何一条语句失败都直接返回错误（由调用方决定是否只记日志）。
func (s *Store) Prune(r Retention) (int64, error) {
	if r.Tasks <= 0 && r.Events <= 0 && r.Licenses <= 0 {
		return 0, nil // 全部关闭：什么也不做
	}
	var total int64
	type rule struct {
		query string
		args  []any
	}
	var rules []rule
	if r.Tasks > 0 {
		rules = append(rules, rule{
			`DELETE FROM task WHERE id NOT IN (SELECT id FROM task ORDER BY id DESC LIMIT ?)`,
			[]any{r.Tasks},
		})
	}
	if r.Events > 0 {
		rules = append(rules, rule{
			`DELETE FROM app_event WHERE id NOT IN (SELECT id FROM app_event ORDER BY id DESC LIMIT ?)`,
			[]any{r.Events},
		})
	}
	if r.EventAge > 0 {
		cutoff := time.Now().Add(-r.EventAge).UTC().Format(timeLayout)
		rules = append(rules, rule{`DELETE FROM app_event WHERE ts < ?`, []any{cutoff}})
	}
	if r.Licenses > 0 {
		rules = append(rules, rule{
			`DELETE FROM license_record WHERE id NOT IN (SELECT id FROM license_record ORDER BY id DESC LIMIT ?)`,
			[]any{r.Licenses},
		})
	}
	for _, ru := range rules {
		res, err := s.db.Exec(ru.query, ru.args...)
		if err != nil {
			return total, fmt.Errorf("store: 裁剪历史记录失败: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	if total > 0 {
		s.vacuumIfSparse()
	}
	return total, nil
}

// vacuumIfSparse 在空闲页占比超过 1/4 时 VACUUM，把文件缩回去。
//
// 直接每次启动都 VACUUM 会重写整个库（数据量大时明显变慢），只在真的删掉了
// 不少页面时才做。VACUUM 失败不影响正确性（文件只是没缩小）。
func (s *Store) vacuumIfSparse() {
	var pages, free int64
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return
	}
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		return
	}
	if pages > 0 && free*4 >= pages {
		_, _ = s.db.Exec(`VACUUM`)
	}
}
