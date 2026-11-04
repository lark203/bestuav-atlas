package kit

import (
	"fmt"
	"image"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 本文件只测「动」的部分：数字滚动的中间值与终值、脉冲的出现与退场。
// 断言都用 Tester 的时钟（Advance 推动），不碰 time.Now()。

func testTheme() *ui.Theme { return &ui.Theme{FontSize: 14} }

// number 从渲染出的文本里读回数字（视图里只有一个数字文本）。
func number(t *testing.T, tt *ui.Tester) float64 {
	t.Helper()
	texts := tt.Texts()
	if len(texts) != 1 {
		t.Fatalf("期望只有一个文本，实际 %v", texts)
	}
	v, err := strconv.ParseFloat(texts[0], 64)
	if err != nil {
		t.Fatalf("文本 %q 不是数字", texts[0])
	}
	return v
}

// TestCountUpRollsToTarget 数字从 0 滚到终值：中途严格在两者之间，跑完正好落在终值。
func TestCountUpRollsToTarget(t *testing.T) {
	start := time.Now()
	tt := ui.NewTester(func(c *ui.Context) {
		CountUp(c, 1286, start, CountUpDur, func(v float64) string { return fmt.Sprintf("%.0f", v) })
	}, 200, 60)

	first := number(t, tt)
	if first < 0 || first > 200 {
		t.Errorf("刚起步应接近 0，实际 %v", first)
	}
	tt.Advance(CountUpDur / 2)
	mid := number(t, tt)
	if !(first < mid && mid < 1286) {
		t.Errorf("中途应落在开局与终值之间，实际 %v（开局 %v）", mid, first)
	}
	tt.Advance(CountUpDur)
	if got := number(t, tt); got != 1286 {
		t.Errorf("跑完应正好是终值，实际 %v", got)
	}
}

// TestCountUpZeroStartShowsTarget start 为零值 = 「没安排过动画」，直接显示终值。
//
// 这条的意义：调用方把起点清零（如每次进入页面）时不能先闪一个 0 再滚。
func TestCountUpZeroStartShowsTarget(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		CountUp(c, 37, time.Time{}, CountUpDur, func(v float64) string { return fmt.Sprintf("%.0f", v) })
	}, 200, 60)
	if got := number(t, tt); got != 37 {
		t.Errorf("零值起点应直接显示终值，实际 %v", got)
	}
}

// TestCountUpRespectsReduceMotion 系统要求「减弱动态效果」时不滚动。
func TestCountUpRespectsReduceMotion(t *testing.T) {
	start := time.Now()
	tt := ui.NewTester(func(c *ui.Context) {
		CountUp(c, 1286, start, CountUpDur, func(v float64) string { return fmt.Sprintf("%.0f", v) })
	}, 200, 60)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Advance(time.Millisecond)
	if got := number(t, tt); got != 1286 {
		t.Errorf("减弱动态效果时应直接显示终值，实际 %v", got)
	}
}

// greenBias 整幅图的「偏绿」程度：Σ(G−R)。
//
// 不能用绿色总量：药丸画在白底上，叠加绿光后 G 反而**降低**（#2fbf71 的 G=191
// 比白的 255 小）。而 R 降得更多（47 vs 191），所以 G−R 在绿光处为正、白底处为 0，
// 正好把「绿光」和「白底」分开。
func greenBias(img *image.RGBA) int64 {
	return biasIn(img, img.Bounds(), 1, 0)
}

// biasIn 只数 rect 里的 Σ(第 a 通道 − 第 b 通道)，越界的坐标跳过。
//
// 分区域统计是为了「同页混杂」的断言：页面别处还在动（呼吸点）时，要看某个动
// 画有没有停，只能比它自己那块地。
func biasIn(img *image.RGBA, rect image.Rectangle, a, b int) int64 {
	var s int64
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
				continue
			}
			i := img.PixOffset(x, y)
			s += int64(img.Pix[i+a]) - int64(img.Pix[i+b])
		}
	}
	return s
}

// TestPillPulseFadesOutAndStops 脉冲：刚完成时亮一下，1.1 秒后回到常态，且不再请求帧。
func TestPillPulseFadesOutAndStops(t *testing.T) {
	th := testTheme()
	col := ui.Hex("#2fbf71")
	at := time.Now()
	tt := ui.NewTester(func(c *ui.Context) {
		PillPulse(c, th, "已完成", col, at)
	}, 200, 80)

	// 头 12% 是淡入，第一帧还没有光：采样要等它亮起来。
	tt.Advance(PulseDur / 5)
	hot := greenBias(tt.Image())
	tt.Advance(2 * PulseDur)
	cold := greenBias(tt.Image())
	if hot <= cold {
		t.Errorf("刚完成时应该更亮：脉冲 %d，常态 %d", hot, cold)
	}
	// 常态与「从未发生」一致：脉冲退干净，不留残影。
	plain := ui.NewTester(func(c *ui.Context) {
		PillPulse(c, th, "已完成", col, time.Time{})
	}, 200, 80)
	base := greenBias(plain.Image())
	if diff := cold - base; diff > 200 || diff < -200 {
		t.Errorf("脉冲结束后应回到与 kit.Pill 一致的底色，差 %d（脉冲后 %d，常态 %d）", diff, cold, base)
	}
	// 动画必须自己停下来：跑完还在请求帧的话，窗口会永远 invalidation。
	if tt.FramePending() {
		t.Error("脉冲结束后不应再请求帧")
	}
}

// ---- 下面测两个**循环**构件（一直动，没有终点）----
//
// 它们让「整页还在请求帧」不再能说明任何一次性动画还在跑，所以断言一律落
// 到具体区域（biasIn）；「该停的时候要停」由各自的条件（空闲、过期、减弱动态
// 效果）单独测。

// swing 走 n 步，返回这段时间里 bias 的极小与极大值。
//
// 循环动画的相位取决于「钟面上的位置」，测试跑在什么时刻是随机的，所以不能断言
// 某一帧的值，只能断言**它确实在起伏**。
func swing(tt *ui.Tester, n int, step time.Duration, rect image.Rectangle, a, b int) (lo, hi int64) {
	lo, hi = int64(1<<60), int64(-1<<60)
	for range n {
		v := biasIn(tt.Image(), rect, a, b)
		lo, hi = min(lo, v), max(hi, v)
		tt.Advance(step)
	}
	return lo, hi
}

// TestBreathingDotBreathes 呼吸点：明暗随时间起伏，而不是一个静止的点。
func TestBreathingDotBreathes(t *testing.T) {
	col := ui.Hex("#2fbf71")
	tt := ui.NewTester(func(c *ui.Context) {
		BreathingDot(c, col, 7, BreathePeriod)
	}, 60, 60)

	rect := image.Rect(0, 0, 40, 40) // 点画在左上角，光晕最大也就这个范围
	// 实测一轮的振幅约 2000（阈值留 1/4，挡得住「压根没动」也挡得住「太弱」）。
	lo, hi := swing(tt, 24, BreathePeriod/24, rect, 1, 0)
	if hi-lo < 500 {
		t.Errorf("呼吸点应随时间明暗变化，实测波动 %d（%d→%d）", hi-lo, lo, hi)
	}
	// 它永远在动，所以永远在请求帧（这是循环构件与一次性构件的根本差别）。
	if !tt.FramePending() {
		t.Error("呼吸点应持续请求帧")
	}
}

// TestBreathingDotStillUnderReduceMotion 减弱动态效果时必须是纯静态点：
// 不画光、不请求帧 —— 否则「省电」偏好在这类常驻动画上完全落空。
func TestBreathingDotStillUnderReduceMotion(t *testing.T) {
	col := ui.Hex("#2fbf71")
	tt := ui.NewTester(func(c *ui.Context) {
		BreathingDot(c, col, 7, BreathePeriod)
	}, 60, 60)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Advance(time.Millisecond)

	rect := image.Rect(0, 0, 40, 40)
	first := biasIn(tt.Image(), rect, 1, 0)
	tt.Advance(2 * BreathePeriod)
	if got := biasIn(tt.Image(), rect, 1, 0); got != first {
		t.Errorf("减弱动态效果时应完全静止，实际 %d → %d", first, got)
	}
	if tt.FramePending() {
		t.Error("减弱动态效果时呼吸点不该请求帧")
	}
}

// TestBreathingDotPhasesDiffer 相位由 seed 决定：两个不同 seed 的点不会齐步走
// （一排服务点各自呼吸）。
func TestBreathingDotPhasesDiffer(t *testing.T) {
	col := ui.Hex("#2fbf71")
	view := func(seed int) func(*ui.Context) {
		return func(c *ui.Context) { BreathingDot(c, col, seed, BreathePeriod) }
	}
	a := ui.NewTester(view(1), 60, 60)
	b := ui.NewTester(view(200), 60, 60)
	rect := image.Rect(0, 0, 40, 40)
	// 两个 Tester 的时钟几乎同时起步，故同一帧的差别就是相位差。
	if d := biasIn(a.Image(), rect, 1, 0) - biasIn(b.Image(), rect, 1, 0); d == 0 {
		t.Error("不同 seed 的点在同一帧一模一样，相位没起作用")
	}
}

// TestPulseStopsWhileDotKeepsBreathing 同页混杂：呼吸点让整页一直重绘，而过期的
// 脉冲必须已经退干净。
//
// 这正是「画面在动 ≠ 那个动画还在跑」的具体含义 —— 判断脉冲有没有停，只能比
// 它自己那块地（药丸区域），看整页 FramePending 是永远 true 的。
func TestPulseStopsWhileDotKeepsBreathing(t *testing.T) {
	th := testTheme()
	col := ui.Hex("#2fbf71")

	// 视口左端放呼吸点，右端放药丸：两块地不重叠，好分开统计。
	view := func(at time.Time) func(*ui.Context) {
		return func(c *ui.Context) {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				BreathingDot(c, col, 7, BreathePeriod)
				ui.Row(c).Grow(1)
				PillPulse(c, th, "已完成", col, at)
			})
		}
	}
	// 基线：脉冲从未发生（at 为零值）。
	base := ui.NewTester(view(time.Time{}), 400, 80)
	// 实验：脉冲发生在一小时前 —— 早就该退干净了。
	tt := ui.NewTester(view(time.Now().Add(-time.Hour)), 400, 80)
	tt.Advance(time.Second)

	// 呼吸点在跑，整页当然一直在请求帧。
	if !tt.FramePending() {
		t.Error("页面上有呼吸点时，应持续请求帧")
	}
	// 但药丸那块地必须和「从未发生过」一模一样：脉冲确实停了。
	rect := image.Rect(300, 0, 400, 80)
	got := biasIn(tt.Image(), rect, 1, 0)
	want := biasIn(base.Image(), rect, 1, 0)
	if d := got - want; d > 100 || d < -100 {
		t.Errorf("脉冲退场后药丸区域应与从未发生一致，实际 %d，基线 %d", got, want)
	}
}
