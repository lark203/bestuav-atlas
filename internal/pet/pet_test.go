package pet

import (
	"testing"
	"time"

	"bestuav-atlas/internal/settings"
)

func TestThresholdsMatchMaxStage(t *testing.T) {
	if len(thresholds)-1 != MaxStage {
		t.Fatalf("MaxStage=%d 与 thresholds 长度不匹配", MaxStage)
	}
}

func TestStageThresholds(t *testing.T) {
	cases := []struct {
		xp   float64
		want int
	}{
		{0, 0}, {119, 0}, {120, 1}, {599, 1}, {600, 2}, {1799, 2}, {1800, 3}, {5000, 3},
	}
	for _, c := range cases {
		if got := Stage(c.xp); got != c.want {
			t.Errorf("Stage(%v) = %d，期望 %d", c.xp, got, c.want)
		}
	}
}

func TestAddXPCapsAndReportsStage(t *testing.T) {
	xp, stage := AddXP(0, 30)
	if xp != 30 || stage != 0 {
		t.Fatalf("AddXP(0,30) = %v,%d", xp, stage)
	}
	xp, stage = AddXP(119, 5)
	if xp != 124 || stage != 1 {
		t.Fatalf("跨门槛应升到少年: %v,%d", xp, stage)
	}
	xp, stage = AddXP(MaxXP-1, 1000)
	if xp != MaxXP || stage != MaxStage {
		t.Fatalf("不应超过成年门槛: %v,%d", xp, stage)
	}
	if xp, stage := AddXP(10, -5); xp != 10 || stage != 0 {
		t.Errorf("负增量应被忽略: %v,%d", xp, stage)
	}
}

func TestProgress(t *testing.T) {
	if got := Progress(60, 0); got < 0.49 || got > 0.51 {
		t.Errorf("幼崽中段进度应约 0.5，实际 %v", got)
	}
	if got := Progress(1800, MaxStage); got != 1 {
		t.Errorf("成年进度恒为 1，实际 %v", got)
	}
}

func TestReminderDue(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	last := now.Add(-46 * time.Minute)
	if !ReminderDue(last, now, 45) {
		t.Error("46 分钟前休息过，45 分钟提醒应到点")
	}
	if ReminderDue(last, now, 60) {
		t.Error("60 分钟提醒不应到点")
	}
	if ReminderDue(last, now, 0) {
		t.Error("0 表示关闭，不应提醒")
	}
	if ReminderDue(time.Time{}, now, 45) {
		t.Error("未开始计时时不应提醒")
	}
}

func TestSkins(t *testing.T) {
	if NormalizeSkin("") != settings.PetSkinCat || NormalizeSkin("dragon") != settings.PetSkinCat {
		t.Error("未知皮肤应回落到小猫")
	}
	if NormalizeSkin(settings.PetSkinDog) != settings.PetSkinDog {
		t.Error("小狗皮肤应保留")
	}
	if SkinLabel(settings.PetSkinDog) != "小狗" || SkinLabel("x") != "小猫" {
		t.Error("皮肤展示名不对")
	}
	for _, s := range settings.PetSkins {
		if NormalizeSkin(s) != s {
			t.Errorf("设置里的皮肤 %q 应是合法皮肤", s)
		}
	}
}

func TestScaleAndNames(t *testing.T) {
	last := 0.0
	for s := 0; s <= MaxStage; s++ {
		sc := Scale(s)
		if sc <= last {
			t.Errorf("阶段 %d 的缩放应更大: %v", s, sc)
		}
		last = sc
		if StageName(s) == "" {
			t.Errorf("阶段 %d 缺少名字", s)
		}
	}
	if _, ok := NextThreshold(MaxStage); ok {
		t.Error("成年后不应还有下一阶段")
	}
}
