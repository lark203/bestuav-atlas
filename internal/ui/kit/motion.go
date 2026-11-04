package kit

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/anim"
	"bestuav-atlas/internal/ui/canvas"
)

// 本文件是「会动的构件」：数字滚动（CountUp）与完成脉冲（PillPulse）。
//
// 它们不是某个页面的一次性写法，所以有三条铁律，改这里之前先看：
//
//  1. **时钟用帧时钟**。构件里只有 c.Now() / p.Now()，绝不出现 time.Now()：
//     mygo 是即时模式，每帧重建视图，而离屏测试的帧时钟由 Tester 持有
//     （只被 Advance 推动）。两个时钟混用的表现是「测试里动画早已结束」
//     而真机上正常 —— 最难查的那种假象。
//     起点时刻由**调用方**持有并传进来（谁想动，谁记开始时间）。
//
//  2. **动画期间必须请求下一帧**，否则只画一帧就停住。用哪个请求，取决于动的
//     是什么：
//       - 自绘的东西（Painter 画的光晕、进度）→ p.AnimationFrame()：下一帧
//         只重绘，不重建视图，最便宜；
//       - 控件的内容（这里就是那个数字文本）→ c.Invalidate()：只重绘的帧
//         **不会重建视图**，文本因此永远停在旧值（踩过：数字一动不动，而
//         同一页面里自绘的光晕照常更新）。
//
//  3. **结束的那一帧不要再请求**。否则窗口会永远 invalidation（真机上看不出
//     错，只表现为风扇一直转）。
//
//  4. **停不下来的动画要报帧率**。呼吸这类永远在动的，用
//     p.AnimationFrameRate(fps) 而不是 AnimationFrame()：后者按显示器的刷新率
//     重绘（120Hz 的屏就是每秒 120 次），而一轮 2.6 秒的呼吸在 12fps 下一样平
//     滑 —— 省下来的都是电。
//     反过来说，**不动的时候一次都别请求**（脉冲过期、系统要求减弱动态效果），
//     否则窗口会一直 invalidation。
//     同页混杂时的判据（本文件下半部分的两个循环构件就是为此而来）：画面在动
//     ≠ 某个动画还在跑 —— 呼吸会让整页一直重绘。要断言「某个动画停了」，只能
//     比它自己那块区域的像素，不能看 FramePending。

const (
	// CountUpDur 数字滚动的默认时长。
	CountUpDur = 900 * time.Millisecond
	// PulseDur 完成脉冲的时长：够长能看清，又不至于盖住后续操作。
	PulseDur = 1100 * time.Millisecond

	// BreathePeriod 呼吸一轮的时长（正常状态）：比人的呼吸略慢，长时间盯着不烦。
	BreathePeriod = 2600 * time.Millisecond
	// AlarmPeriod 呼吸一轮的时长（异常状态）：明显比正常快，一眼分得出好坏。
	AlarmPeriod = 1100 * time.Millisecond
	// BreatheFPS 呼吸的重绘帧率（见上面第 4 条）。
	BreatheFPS = 12
)

// CountUp 大数字进场：从 0 滚到 target，历时 d，按 format 显示中间值。
//
// 参数：
//   - start：滚动开始的**帧时刻**（用 c.Now() 取；零值视为「早就滚完了」）。
//     想重滚一次就把调用方持有的这个时刻换成新的。
//   - format：把中间值变成字符串。它每帧都会被调用，所以必须便宜，且对中间
//     值也要说得通 —— 小数位写死，否则数字宽度会一路跳。
//   - d <= 0 或系统开了「减弱动态效果」时直接显示终值，不做动画。
//
// 不用框架的 Element.AnimateTo：那条要求调用方先把初值 Jump 到 0，而「只第一
// 帧 Jump」在即时模式里没有可靠的判据（Jump 之后值仍是 0）。自己按帧算进度反
// 而简单，也和自绘那套（anim.Progress）用同一个时钟。
func CountUp(c *ui.Context, target float64, start time.Time, d time.Duration, format func(float64) string) ui.Element {
	if format == nil {
		format = func(v float64) string { return fmt.Sprintf("%.0f", v) }
	}
	v := target
	moving := false
	if d > 0 && !c.Preferences().ReduceMotion {
		// 进度可能大于 1（跑过头）：交给曲线自己夹紧，见 anim.CubicBezier。
		if k := anim.Clamp01(anim.Progress(c.Now(), start, d)); k < 1 {
			moving = true
			v = target * float64(anim.EaseOutExpo(k))
		}
	}
	// 数字是控件内容，不是自绘：必须请求重建视图（见本文件开头的第 2 条）。
	if moving {
		c.Invalidate()
	}
	return ui.Text(c, format(v)).FontSize(24).Bold().SingleLine()
}

// PillPulse 「刚刚发生」的脉冲：at 之后的 PulseDur 内，柔光从药丸中心扩散、
// 淡出，像一圈涟漪散开。
//
// at 为零值 = 这件事从未发生 = 不画（和 Pill 完全一样）；系统开了「减弱动态
// 效果」时也不画 —— 纯装饰，省掉不损失信息。
//
// 光晕画在药丸**背景之上、文字之下**（Element.Draw 的顺序），所以药丸本身也
// 跟着亮一下；Pill 没有裁剪，光会溢出到药丸之外，正是想要的效果。
func PillPulse(c *ui.Context, t *ui.Theme, text string, col ui.Color, at time.Time) ui.Element {
	el := Pill(c, t, text, col)
	if at.IsZero() || c.Preferences().ReduceMotion {
		return el
	}
	return el.Draw(func(p *ui.Painter, r ui.Rect) {
		k := anim.Clamp01(anim.Progress(p.Now(), at, PulseDur))
		if k >= 1 {
			return // 脉冲结束：这一帧不再请求，动画就此停下
		}
		p.AnimationFrame()
		// 亮度先淡入再淡出：一开始突然点亮的那一帧很跳。
		a := 0.8 * (1 - k) * anim.Smoothstep(0, 0.12, k)
		canvas.Glow(p, r.X+r.W/2, r.Y+r.H/2, 12+26*k, col, a)
		// 再向外推一圈描边：柔光是位图，可能被父容器（如表格单元格）裁掉，
		// 而药丸边缘这一圈几乎总在裁剪区内，保证「亮了」这件事一定看得见。
		pad := 1 + 7*k
		var ring ui.Path
		canvas.RoundRect(&ring, r.X-pad, r.Y-pad, r.W+2*pad, r.H+2*pad, (r.H+2*pad)/2)
		p.StrokePath(&ring, 1.5, Alpha(col, float64(0.6*(1-k))))
	})
}

// ---- 下面两个是**循环**构件：一直动，没有终点 ----
//
// 与上面两个（一次性：有个起点，跑完就停）的区别全在时间上：它们不用
// anim.Progress（「过去了多少」），而用 anim.Seconds + anim.Sine（「钟面上的
// 哪个位置」）。因此它们也没有「结束的那一帧」，帧率必须给死（见第 4 条）。

// BreathingDot 会呼吸的状态点：8px 的圆点，外圈柔光与描边一起缓慢起伏。
//
//   - seed 决定相位：同一个 seed 每一帧都落在波形的同一个位置，而不同的 seed
//     互不相同 —— 一排点因此各自呼吸，而不是一起闪（见 anim.Hash）；
//   - period 是一轮的时长：正常状态用 BreathePeriod，异常用 AlarmPeriod（更
//     快，像在报警）；传 0 或系统开了「减弱动态效果」时是静态点。
func BreathingDot(c *ui.Context, col ui.Color, seed int, period time.Duration) ui.Element {
	el := ui.Column(c).Width(8).Height(8).Radius(999).Shrink(0).Background(col)
	if period <= 0 || c.Preferences().ReduceMotion {
		return el // 静态：不画光，也不请求帧
	}
	return el.Draw(func(p *ui.Painter, r ui.Rect) {
		p.AnimationFrameRate(BreatheFPS)
		k := anim.Sine(anim.Seconds(p.Now()), period.Seconds(), float64(anim.Hash(seed, 0, 1)))
		canvas.Glow(p, r.X+r.W/2, r.Y+r.H/2, 3.5+4*k, col, 0.22+0.38*k)
		// 一圈随之起伏的描边：柔光是位图，可能被父容器裁掉，而这一圈几乎总在
		// 裁剪区内 —— 与 PillPulse 同一个道理，保证「在呼吸」一定看得见。
		pad := 1.5 + 3.5*k
		var ring ui.Path
		canvas.RoundRect(&ring, r.X-pad, r.Y-pad, r.W+2*pad, r.H+2*pad, (r.H+2*pad)/2)
		p.StrokePath(&ring, 1, Alpha(col, float64(0.15+0.45*float64(k))))
	})
}

// 说明：这里曾经还有一个 GlowProgress（进度条头部柔光 + 已完成段里的流光），
// 真机上看效果不好（细条上糊成一团、流光显得脏），已撤掉，两条进度条回到
// 框架自带的 ui.Progress。撤掉时一并删掉了它的两条测试；循环构件的规矩
// （AnimationFrameRate + 空闲不请求帧）由 BreathingDot 继续守着。
