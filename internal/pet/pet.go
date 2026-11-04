// Package pet 是伴生桌宠的纯逻辑：成长阶段、经验、提醒判定与皮肤取值。
//
// 不依赖 UI / 窗口，便于单测；绘制与窗口生命周期在 internal/view/pet.go。
// 设计目标是「低占用」：没有动画循环、没有图片资源，状态只有几个数字。
package pet

import (
	"math"
	"time"

	"bestuav-atlas/internal/settings"
)

// 阶段门槛（累计陪伴分钟数）：幼崽 0、少年 2h、成熟 10h、成年 30h。
//
// 到了阈值就不再长大：成年即「长到头」，避免无限膨胀。
var thresholds = []float64{0, 120, 600, 1800}

// MaxStage 最后一个阶段的下标（与 thresholds 的长度保持一致，测试会校验）。
const MaxStage = 3

// MaxXP 经验上限（成年所需分钟数）。
var MaxXP = thresholds[MaxStage]

// Stage 返回累计 xp 分钟对应的阶段（0..MaxStage）。
func Stage(xp float64) int {
	for i := len(thresholds) - 1; i > 0; i-- {
		if xp >= thresholds[i] {
			return i
		}
	}
	return 0
}

// StageName 阶段展示名。
func StageName(stage int) string {
	switch clampStage(stage) {
	case 1:
		return "少年"
	case 2:
		return "成熟"
	case 3:
		return "成年"
	default:
		return "幼崽"
	}
}

// Scale 阶段对应的绘制缩放：从小不点长到成年，最大约 1.5 倍。
func Scale(stage int) float64 {
	return []float64{0.72, 0.86, 1.0, 1.12}[clampStage(stage)]
}

// NextThreshold 返回下一阶段门槛；已成年时 ok=false。
func NextThreshold(stage int) (float64, bool) {
	s := clampStage(stage)
	if s >= MaxStage {
		return 0, false
	}
	return thresholds[s+1], true
}

// Progress 当前阶段内的进度（0..1）；成年恒为 1。
func Progress(xp float64, stage int) float64 {
	s := clampStage(stage)
	if s >= MaxStage {
		return 1
	}
	lo, hi := thresholds[s], thresholds[s+1]
	if hi <= lo {
		return 1
	}
	p := (xp - lo) / (hi - lo)
	return math.Max(0, math.Min(1, p))
}

// AddXP 累加陪伴分钟数，返回新的 xp 与阶段。xp 不会超过成年门槛。
func AddXP(xp, minutes float64) (float64, int) {
	if minutes < 0 || math.IsNaN(minutes) {
		minutes = 0
	}
	xp = math.Max(0, xp+minutes)
	if xp > MaxXP {
		xp = MaxXP
	}
	return xp, Stage(xp)
}

// ReminderDue 久坐提醒是否到点：minutes<=0 表示关闭；last 为零值时不提醒（等第一次计时）。
func ReminderDue(last, now time.Time, minutes int) bool {
	if minutes <= 0 || last.IsZero() {
		return false
	}
	return now.Sub(last) >= time.Duration(minutes)*time.Minute
}

// NormalizeSkin 把未知皮肤回落到猫。
func NormalizeSkin(name string) string {
	switch name {
	case settings.PetSkinDog:
		return settings.PetSkinDog
	default:
		return settings.PetSkinCat
	}
}

// SkinLabel 皮肤展示名。
func SkinLabel(name string) string {
	if NormalizeSkin(name) == settings.PetSkinDog {
		return "小狗"
	}
	return "小猫"
}

func clampStage(stage int) int {
	if stage < 0 {
		return 0
	}
	if stage > MaxStage {
		return MaxStage
	}
	return stage
}
