package anim

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Easing 就是框架的 ui.Easing（起别名而不是另立门户）：自绘里调出来的曲线与
// ui.Animation 里用的是同一种，可以互相通用。
//
// 框架那套（ui.Linear / EaseIn / EaseOut / EaseInOut / Bounce）是给元素动画用的
// 简单三次曲线；本包补的是**自绘**要的那几条：CSS 的标准曲线、插值辅助，以及对
// 越界进度的夹紧（见 CubicBezier）。
type Easing = ui.Easing

// At 求 t 处的动画进度（t 越界时由曲线自己夹紧，见 CubicBezier）。
func At(e Easing, t float32) float32 { return e(t) }

// Tween 在 from 与 to 之间按进度 k 插值。
//
// 自绘里到处是 from+(to-from)*ease(k)，一律改用它：少写一遍，也免得漏乘。
func Tween(e Easing, from, to, k float32) float32 { return from + (to-from)*e(k) }

// CSS 的标准曲线（cubic-bezier 版，不是框架的三次版），外加两条 effort 卡片用
// 的：EaseOutExpo 是拇指滑到位那条，BackOut 是大脑长大时先冲过头再回来那条。
//
// 曲线在包初始化时算好，不是每帧算。
var (
	// EaseOutExpo 一上来就冲得极快、尾巴拖长：CSS 没有同名的标准曲线，
	// 这是 cubic-bezier(0.19, 1, 0.22, 1) 的俗名。
	EaseOutExpo = CubicBezier(0.19, 1, 0.22, 1)

	// BackOut 冲过 1 再回落，用于「弹一下」。
	BackOut = CubicBezier(0.34, 1.56, 0.64, 1)

	Ease      = CubicBezier(0.25, 0.1, 0.25, 1)
	EaseIn    = CubicBezier(0.42, 0, 1, 1)
	EaseOut   = CubicBezier(0, 0, 0.58, 1)
	EaseInOut = CubicBezier(0.42, 0, 0.58, 1)

	// Linear 是「原速」。不直接用框架的 ui.Linear，是因为那一条对越界的输入
	// 原样返回，而本包的约定是夹紧（见 CubicBezier）：预设之间要一致。
	Linear = Easing(func(t float32) float32 { return Clamp01(t) })
)

// CubicBezier 返回 CSS 的 cubic-bezier(x1, y1, x2, y2) 曲线：端点固定在
// (0,0) 与 (1,1)，另外两点由参数给（y 可以超出 [0,1]，才会过冲）。
//
// 求解用框架的 ui.CubicBezier（牛顿法，斜率平坦处退化为二分），这里只补一层
// **夹紧**：自绘的进度常常是负的或大于 1（「还没轮到这个字母」「动画跑过头
// 了」），而框架的曲线对越界输入是原样返回的 —— 不夹的话缓动会算出越界的值，
// 接着把透明度和位移一起带飞。
func CubicBezier(x1, y1, x2, y2 float32) Easing {
	e := ui.CubicBezier(x1, y1, x2, y2)
	return func(t float32) float32 {
		if t <= 0 {
			return 0
		}
		if t >= 1 {
			return 1
		}
		return e(t)
	}
}

// Smoothstep 把 x 落在 [lo,hi] 之间的部分平滑地铺到 0→1（两端导数为 0，
// 不像线性那样在端点拐一下）。区间退化为一点时按「过了就是 1」处理。
func Smoothstep(lo, hi, x float32) float32 {
	if hi <= lo {
		if x < lo {
			return 0
		}
		return 1
	}
	k := Clamp01((x - lo) / (hi - lo))
	return k * k * (3 - 2*k)
}

// Clamp01 把 v 夹到 [0,1]；NaN 按 0 处理（区间退化的除法会算出 NaN）。
func Clamp01[T float32 | float64](v T) T {
	if v != v {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Progress 从 since 起、历时 d 的动画在 t 时刻的进度（跑过头会大于 1）。
//
// since 是零值时（动画还没开始过）返回 1 —— 这样调用方不必到处写
// `if startedAt.IsZero()`：「没开始」等价于「早就结束了」。
func Progress(t, since time.Time, d time.Duration) float32 {
	if since.IsZero() || d <= 0 {
		return 1
	}
	return float32(float64(t.Sub(since)) / float64(d))
}

// ---- 循环动画的时间 ----
//
// 上面那套（Progress + 缓动）答的是「过去了多少」，一次性动画用它；循环动画
// （呼吸、流光）答的是「钟面上的哪个位置」，用 Seconds + Sine。

// Seconds 把帧时刻换成秒。循环动画按秒算周期，不看「从何时起」—— 它永远在跑，
// 没有起点，也就没有「跑完了」这一说。
func Seconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Sine 周期 period 秒、相位 phase（0→1 走完一个周期）的连续正弦波，返回 0→1。
//
// 一堆东西要「各自起伏而不齐步走」时，phase 用 Hash 给（见 noise.go）：周期
// 相同、相位不同，看起来才像一片各自呼吸的点，而不是一起闪的灯。
func Sine(t, period, phase float64) float32 {
	if period <= 0 {
		return 0
	}
	return float32(0.5 + 0.5*math.Sin(2*math.Pi*(t/period+phase)))
}
