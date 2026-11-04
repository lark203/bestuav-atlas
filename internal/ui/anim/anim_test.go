package anim

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 本包的测试只验「数学性质」，不验观感：曲线端点是否接得上、是否单调、噪声是否
// 确定且在范围内。观感归真机/截图，这里只挡住「算错了」和「改坏了」。

// TestEndpoints 曲线两端必须接在 (0,0) 与 (1,1) 上 —— 接不上动画会跳。
func TestEndpoints(t *testing.T) {
	all := map[string]Easing{
		"Linear": Linear, "Ease": Ease, "EaseIn": EaseIn,
		"EaseOut": EaseOut, "EaseInOut": EaseInOut,
		"EaseOutExpo": EaseOutExpo, "BackOut": BackOut,
	}
	for name, e := range all {
		if got := At(e, 0); math.Abs(float64(got)) > 1e-5 {
			t.Errorf("%s(0) = %v，应为 0", name, got)
		}
		if got := At(e, 1); math.Abs(float64(got)-1) > 1e-5 {
			t.Errorf("%s(1) = %v，应为 1", name, got)
		}
		// 越界的输入也要夹住：自绘的进度常常是负的（还没轮到）或大于 1
		// （跑过头了），不夹的话会把透明度和位移一起带飞。
		if got := At(e, -3); math.Abs(float64(got)) > 1e-6 {
			t.Errorf("%s(-3) = %v，应夹到 0", name, got)
		}
		if got := At(e, 4); math.Abs(float64(got)-1) > 1e-6 {
			t.Errorf("%s(4) = %v，应夹到 1", name, got)
		}
	}
}

// TestMonotonic 非 back 类的曲线必须单调不减：回头会导致元件抖动。
func TestMonotonic(t *testing.T) {
	all := map[string]Easing{
		"Linear": Linear, "Ease": Ease, "EaseIn": EaseIn,
		"EaseOut": EaseOut, "EaseInOut": EaseInOut, "EaseOutExpo": EaseOutExpo,
	}
	for name, e := range all {
		prev := math.Inf(-1)
		for i := range 200 {
			v := At(e, float32(i)/199)
			if float64(v) < prev-1e-5 {
				t.Errorf("%s 在 t=%.3f 处回头：%.6f < %.6f", name, float64(i)/199, v, prev)
				break
			}
			prev = float64(v)
		}
	}
}

// TestBackOutOvershoots back 类的曲线就是要冲过 1：没有过冲就白叫 back 了。
func TestBackOutOvershoots(t *testing.T) {
	var max float32
	for i := range 200 {
		max = max2(max, At(BackOut, float32(i)/199))
	}
	if max < 1.05 {
		t.Errorf("BackOut 的最大值是 %.4f，应明显冲过 1", max)
	}
}

func max2(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// TestTween 插值：两端要正好落在 from / to 上。
func TestTween(t *testing.T) {
	if got := Tween(Ease, 10, 20, 0); math.Abs(float64(got-10)) > 1e-4 {
		t.Errorf("进度 0 应在 from=10，实际 %v", got)
	}
	if got := Tween(Ease, 10, 20, 1); math.Abs(float64(got-20)) > 1e-4 {
		t.Errorf("进度 1 应在 to=20，实际 %v", got)
	}
	if got := Tween(Linear, -5, 5, 0.25); math.Abs(float64(got+2.5)) > 1e-5 {
		t.Errorf("-5→5 的四分之一应是 -2.5，实际 %v", got)
	}
}

// TestUsableByFramework 本包的 Easing 与框架的是同一种，能直接交给 ui.Animation
// 用 —— 这是用别名而不是另立类型的全部意义。
func TestUsableByFramework(t *testing.T) {
	a := ui.Animation{Duration: time.Second, Ease: EaseOutExpo}
	if a.Ease == nil {
		t.Fatal("本包的曲线塞不进 ui.Animation")
	}
	if got := a.Ease(1); math.Abs(float64(got-1)) > 1e-5 {
		t.Errorf("交给框架的曲线在 t=1 处是 %v，应为 1", got)
	}
}

// TestSmoothstep 端点值与中点值，以及区间退化（lo==hi）时不许算出 NaN。
func TestSmoothstep(t *testing.T) {
	if got := Smoothstep(0, 10, -1); got != 0 {
		t.Errorf("区间左侧应是 0，实际 %v", got)
	}
	if got := Smoothstep(0, 10, 99); got != 1 {
		t.Errorf("区间右侧应是 1，实际 %v", got)
	}
	if got := Smoothstep(0, 10, 5); math.Abs(float64(got)-0.5) > 1e-5 {
		t.Errorf("中点应是 0.5，实际 %v", got)
	}
	if got := Smoothstep(4, 4, 4); got != 1 {
		t.Errorf("退化的区间应给 1（不是 NaN），实际 %v", got)
	}
	if got := Smoothstep(4, 4, 3.9); got != 0 {
		t.Errorf("退化的区间左侧应给 0，实际 %v", got)
	}
}

// TestClamp01 含 NaN：Smoothstep 的退化分支靠它兜底。
func TestClamp01(t *testing.T) {
	if got := Clamp01(0.5); got != 0.5 {
		t.Errorf("Clamp01(0.5) = %v", got)
	}
	if got := Clamp01(-2.0); got != 0 {
		t.Errorf("Clamp01(-2) = %v", got)
	}
	if got := Clamp01(2.0); got != 1 {
		t.Errorf("Clamp01(2) = %v", got)
	}
	if got := Clamp01(math.NaN()); got != 0 {
		t.Errorf("Clamp01(NaN) = %v，应为 0", got)
	}
	if got := Clamp01(float32(1.5)); got != 1 {
		t.Errorf("Clamp01(float32 1.5) = %v", got)
	}
}

// TestProgress 零时刻（动画还没开始）算作已经结束，省得调用方到处判空。
func TestProgress(t *testing.T) {
	now := time.Now()
	if got := Progress(now, time.Time{}, time.Second); got != 1 {
		t.Errorf("零时刻的进度应是 1，实际 %v", got)
	}
	if got := Progress(now, now, 0); got != 1 {
		t.Errorf("时长为 0 的进度应是 1，实际 %v", got)
	}
	if got := Progress(now.Add(250*time.Millisecond), now, time.Second); math.Abs(float64(got)-0.25) > 1e-5 {
		t.Errorf("四分之一秒的进度应是 0.25，实际 %v", got)
	}
	if got := Progress(now.Add(2*time.Second), now, time.Second); got <= 1 {
		t.Errorf("跑过头了进度应大于 1（不夹），实际 %v", got)
	}
}

// TestSeconds 帧时刻换秒：只关心差值（循环动画看的是「钟面上的位置」）。
func TestSeconds(t *testing.T) {
	now := time.Now()
	if got := Seconds(now.Add(1500*time.Millisecond)) - Seconds(now); math.Abs(got-1.5) > 1e-6 {
		t.Errorf("1.5 秒的差应是 1.5，实际 %v", got)
	}
	// 零值时刻（有人误传 time.Time{}）不许算出 NaN 把后面带飞。
	if got := Seconds(time.Time{}); math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("零值时刻应算出有限值，实际 %v", got)
	}
}

// TestSine 周期与相位：一个周期后回到原处，相位 0.5 处正好反相，值恒在 0→1。
func TestSine(t *testing.T) {
	const p = 2.0
	if got := Sine(0.5, p, 0); got != Sine(0.5+p, p, 0) {
		t.Errorf("过一个周期应回到原处：%.6f / %.6f", got, Sine(0.5+p, p, 0))
	}
	a, b := Sine(0.5, p, 0), Sine(0.5, p, 0.5)
	if math.Abs(float64(a+b)-1) > 1e-5 {
		t.Errorf("相位 0.5 应正好反相：%.6f + %.6f ≠ 1", a, b)
	}
	lo, hi := float64(1), float64(0)
	for i := range 400 {
		v := float64(Sine(float64(i)*0.01, p, 0.25))
		lo, hi = min(lo, v), max(hi, v)
	}
	if lo > 0.05 || hi < 0.95 {
		t.Errorf("Sine 应铺满 0→1，实际 %.3f→%.3f", lo, hi)
	}
	if got := Sine(1, 0, 0); got != 0 {
		t.Errorf("周期为 0 应给 0（不是 NaN），实际 %v", got)
	}
}

// TestHashDeterministic 同一组参数永远同一个值：否则像素每帧乱跳，成了噪点。
func TestHashDeterministic(t *testing.T) {
	for _, c := range []int{0, 1, 2, 3, 4} {
		a, b := Hash(7, 11, c), Hash(7, 11, c)
		if a != b {
			t.Errorf("同参数两次调用不一样：%v / %v", a, b)
		}
		if a < 0 || a > 1 {
			t.Errorf("Hash 应落在 0→1，实际 %v", a)
		}
	}
	// 同一个格子靠第三个参数取不同的随机数：它们不该都相等。
	seen := map[float32]bool{}
	for c := range 8 {
		seen[Hash(3, 4, c)] = true
	}
	if len(seen) < 7 {
		t.Errorf("同一格子的 8 个用途只得到 %d 个不同的值，hash 可能对 c 不敏感", len(seen))
	}
}

// TestHashSpreads 不同格子的值要铺开，不能都挤在一处（否则一片像素长得一样）。
func TestHashSpreads(t *testing.T) {
	var buckets [4]int
	const n = 400
	for i := range n {
		buckets[int(Hash(i, 0, 0)*4)]++
	}
	for b, count := range buckets {
		if count < n/4/2 {
			t.Errorf("第 %d 个桶只有 %d 个样本（共 %d），分布太偏", b, count, n)
		}
	}
}

// TestTwinkle 闪烁：值在 0→1 之间、有灭的时候也有亮的时候，且是确定性的。
func TestTwinkle(t *testing.T) {
	var anyOff, anyOn bool
	for i := range 400 {
		v := Twinkle(2, 3, float64(i)*0.01)
		if v < 0 || v > 1 {
			t.Fatalf("Twinkle = %v，超出 0→1", v)
		}
		if v == 0 {
			anyOff = true
		}
		if v > 0.6 {
			anyOn = true
		}
	}
	if !anyOff || !anyOn {
		t.Errorf("闪烁应既有灭（%v）也有亮（%v）", anyOff, anyOn)
	}
	if a, b := Twinkle(5, 6, 1.25), Twinkle(5, 6, 1.25); a != b {
		t.Errorf("Twinkle 不是确定性的：%v / %v", a, b)
	}
}

// TestWave 波动：连续起伏（不像 Twinkle 那样归零），且两端都要到过。
func TestWave(t *testing.T) {
	lo, hi := float32(1), float32(0)
	for i := range 600 {
		v := Wave(1, 2, float64(i)*0.01)
		if v < 0 || v > 1 {
			t.Fatalf("Wave = %v，超出 0→1", v)
		}
		lo, hi = min(lo, v), max(hi, v)
	}
	if lo > 0.1 || hi < 0.9 {
		t.Errorf("波动应铺满 0→1，实际 %.3f→%.3f", lo, hi)
	}
}
