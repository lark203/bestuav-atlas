package anim

import "math"

// 自绘的「随机」必须是**确定性**的：同一颗像素在每一帧都要拿到同一个随机数，
// 否则它每帧乱跳，看起来是噪点而不是星星。所以不用 math/rand，而是把坐标当
// 种子做一次整数哈希 —— 函数式的，没有状态，也不必先播种。

// Hash 返回一个只由参数决定的 0→1 的数：参数相同，每次调用都一样。
//
// a、b、c 通常放「格子下标 + 用途」：同一个格子要取周期、相位、颜色三个互不相干
// 的随机数时，靠第三个参数区分（hash 对 c 很敏感）。
func Hash(a, b, c int) float32 {
	h := uint32(a)*0x9e3779b1 ^ uint32(b)*0x85ebca77 ^ uint32(c)*0xc2b2ae3d
	h ^= h >> 15
	h *= 0x2c1b3c6d
	h ^= h >> 12
	h *= 0x297a2d39
	h ^= h >> 15
	return float32(h&0xffffff) / 0xffffff
}

// Twinkle 闪烁：每个周期里亮一段、灭一段，亮的那段按 sin 由暗到亮再到暗，
// 灭的时候干脆是 0。
//
// 周期、相位、亮多久都由 (a,b) 决定，所以一片像素各自闪各自的，不会齐步走。
// 用来做「星星眨眼」；要连续起伏的呼吸感用 Wave。
func Twinkle(a, b int, t float64) float32 {
	period := 0.7 + 1.1*float64(Hash(a, b, 1))
	phase := float64(Hash(a, b, 2))
	k := math.Mod(t/period+phase, 1)
	on := 0.35 + 0.4*float64(Hash(a, b, 3)) // 一个周期里亮着的部分
	if k > on {
		return 0
	}
	return float32(math.Sin(k / on * math.Pi))
}

// Wave 连续波动：0→1 的正弦，周期与相位由 (a,b) 决定。
//
// 与 Twinkle 的差别是不灭：一直在 0→1 之间起伏，用来做「呼吸」「微光流动」。
func Wave(a, b int, t float64) float32 {
	period := 0.5 + float64(Hash(a, b, 1))
	return float32(0.5 + 0.5*math.Sin(2*math.Pi*(t/period+float64(Hash(a, b, 2)))))
}
