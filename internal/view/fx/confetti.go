// Package fx 放「视觉特效」类可复用组件：目前是彩带（confetti）。
//
// 本包移植自 mygo 官方示例 examples/confetti（MIT），只做两处改造：
//  1. 由 package main 改为独立包，导出 Options / Shape / Burst 等类型；
//  2. 位置改用**百分比**（LeftPercent/TopPercent）而不是窗口像素，这样无需知道
//     窗口尺寸即可把彩带撒在任意相对位置（我们的应用是全窗口自适应布局）。
//
// 原理（与 canvas-confetti 相同）：每一片彩带是三层叠加的位姿——
//   - 整团彩带的下落（重力 + 横移，同时淡出）；
//   - 每片从起点向外「炸开」的位移（速度按 decay 逐帧衰减）；
//   - 每片绕自身法线旋转，再看一个倾斜面（RotationX），于是翻飞打转。
//
// 位姿动画只在「重绘」的帧里推进，不重建视图，所以几百片同时飞也不掉帧。
package fx

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/egoist/mygo/ui"
)

// Options 描述一团彩带怎么飞（单位：DIP / 每帧，一帧为 1/60 秒）。
type Options struct {
	// Count 这一团撒多少片。
	Count int
	// Angle 撒出的主方向（度，逆时针，90 为向上），Spread 是偏离该方向的散布角度。
	Angle, Spread float64
	// StartVelocity 初速度（每片在 0.5~1.5 倍之间浮动）；Decay 是每帧速度保留比例。
	StartVelocity, Decay float64
	// Gravity 每帧向下 3 DIP × Gravity；Drift 是横向漂移。
	Gravity, Drift float64
	// Ticks 寿命（帧数），期间逐渐淡出。
	Ticks int
	// X、Y 起点，取窗口宽高的百分比（0~1）。
	X, Y float64
	// Colors、Shapes 每片从中随机取。
	Colors []ui.Color
	Shapes []Shape
	// Scalar 缩放彩带尺寸。
	Scalar float64
	// Flat 让彩带始终正对屏幕（不翻飞）。
	Flat bool
}

// Shape 一片彩带的形状：方块、圆、星、心，或一段文字（emoji）。
type Shape struct {
	kind shapeKind
	text string
}

type shapeKind uint8

const (
	square shapeKind = iota
	circle
	star
	heart
	text
)

var (
	Square = Shape{kind: square}
	Circle = Shape{kind: circle}
	Star   = Shape{kind: star}
	Heart  = Shape{kind: heart}
)

// Text 返回一段展示 s 的彩带（通常是 emoji）。
func Text(s string) Shape { return Shape{kind: text, text: s} }

// defaultColors 沿用 canvas-confetti 的配色。
var defaultColors = []ui.Color{
	ui.Hex("#26ccff"), ui.Hex("#a25afd"), ui.Hex("#ff5e7e"), ui.Hex("#88ff5a"),
	ui.Hex("#fcff42"), ui.Hex("#ffa62d"), ui.Hex("#ff36ff"),
}

// Defaults 返回 canvas-confetti 的默认参数：50 片方块与圆，从窗口中间向上撒。
func Defaults() Options {
	return Options{
		Count:         50,
		Angle:         90,
		Spread:        45,
		StartVelocity: 45,
		Decay:         0.9,
		Gravity:       1,
		Ticks:         200,
		X:             0.5,
		Y:             0.5,
		Colors:        defaultColors,
		Shapes:        []Shape{Square, Circle},
		Scalar:        1,
	}
}

// tick 是彩带位姿推进一次的时长（一帧）。
const tick = time.Second / 60

// Burst 是一团正在飞的彩带。
type Burst struct {
	id     uint64
	xf, yf float32 // 起点，窗口宽高的百分比
	life   time.Duration
	fall   ui.Pose
	ease   ui.Easing
	pieces []piece
	// launched 是这团彩带第一次被绘制的时刻；零值表示还没落位。
	launched time.Time
}

type piece struct {
	id    int
	w, h  float32
	color ui.Color
	shape Shape
	to    ui.Pose
	spin  float32
	tilt  float32
	turn  float32
}

// New 以起点 (xf, yf)（窗口宽高的百分比，0~1）创建一团彩带。
func New(o Options, xf, yf float64, id uint64) *Burst {
	shapes, colors := o.Shapes, o.Colors
	if len(shapes) == 0 {
		shapes = []Shape{Square, Circle}
	}
	if len(colors) == 0 {
		colors = defaultColors
	}
	ticks := float64(max(o.Ticks, 1))
	b := &Burst{
		id:     id,
		xf:     float32(xf),
		yf:     float32(yf),
		life:   time.Duration(ticks) * tick,
		fall:   ui.Pose{X: float32(o.Drift * ticks), Y: float32(3 * o.Gravity * ticks), Fade: 1},
		ease:   decayEase(o.Decay, ticks),
		pieces: make([]piece, o.Count),
	}
	reach := ticks
	if o.Decay != 1 {
		reach = (1 - math.Pow(o.Decay, ticks)) / (1 - o.Decay)
	}
	angle, spread := o.Angle*math.Pi/180, o.Spread*math.Pi/180
	for i := range b.pieces {
		dir := -angle + spread/2 - rand.Float64()*spread
		speed := o.StartVelocity*0.5 + rand.Float64()*o.StartVelocity
		shape := shapes[rand.IntN(len(shapes))]
		p := piece{
			id:    i,
			color: colors[rand.IntN(len(colors))],
			shape: shape,
			to:    ui.Pose{X: float32(math.Cos(dir) * speed * reach), Y: float32(math.Sin(dir) * speed * reach)},
		}
		s := float32(o.Scalar)
		switch shape.kind {
		case square:
			p.w, p.h = 10*s, 10*s*(0.5+0.4*rand.Float32())
		case circle:
			p.w, p.h = 8*s, 8*s
		case star:
			p.w, p.h = 16*s, 16*s
		case heart:
			p.w, p.h = 14*s, 13*s
		case text:
			p.w, p.h = 12*s, 12*s
		}
		if !o.Flat {
			p.tilt = 45 + 40*rand.Float32()
			p.turn = 360 * rand.Float32()
			if shape.kind != circle {
				turns := (0.6 + 0.8*rand.Float64()) * b.life.Seconds()
				if rand.IntN(2) == 0 {
					turns = -turns
				}
				p.spin = float32(turns * 360)
			}
		}
		b.pieces[i] = p
	}
	return b
}

// decayEase 是「速度每帧保留 decay」时，t 时刻走过的距离比例。
func decayEase(decay, ticks float64) ui.Easing {
	if decay == 1 {
		return ui.Linear
	}
	k := math.Log(decay) * ticks
	total := 1 - math.Exp(k)
	return func(t float32) float32 {
		return float32((1 - math.Exp(k*float64(t))) / total)
	}
}

// Build 绘制这团彩带。每帧都会调用它；位姿只在重绘帧推进。
func (b *Burst) Build(c *ui.Context, now time.Time) {
	// 铺满窗口的定位层 + 百分比定位，使我们不必知道窗口尺寸。
	group := ui.Box(c.Key(b.id)).Absolute().
		LeftPercent(b.xf*100).TopPercent(b.yf*100).Size(1, 1).PassThrough()
	if b.launched.IsZero() && group.Bounds().W > 0 {
		b.launched = now
	}
	launched := !b.launched.IsZero()
	if !launched {
		c.AnimationFrame()
	}
	linear := ui.Animation{Duration: b.life, Ease: ui.Linear}
	var fall ui.Pose
	if launched {
		fall = b.fall
	}
	group.AnimatePose(fall, linear).Children(func() {
		for i := range b.pieces {
			p := &b.pieces[i]
			var to, spin ui.Pose
			if launched {
				to, spin = p.to, ui.Pose{Rotate: p.spin}
			}
			var e ui.Element
			if p.spin == 0 {
				e = p.shapeBox(c.Key(p.id))
			} else {
				e = ui.Box(c.Key(p.id))
			}
			e.Absolute().Left(-p.w/2).Top(-p.h/2).Size(p.w, p.h).PassThrough()
			if p.tilt != 0 {
				e.Transform(ui.RotationX(p.tilt).Then(ui.Rotation(p.turn)))
			}
			e.AnimatePose(to, ui.Animation{Duration: b.life, Ease: b.ease})
			if p.spin != 0 {
				e.Children(func() {
					p.shapeBox(c).Size(p.w, p.h).PassThrough().AnimatePose(spin, linear)
				})
			}
		}
	})
}

// Expired 报告这团彩带是否已经放完（可安全从列表里移除）。
func (b *Burst) Expired(now time.Time) bool {
	if b.launched.IsZero() {
		return false
	}
	return now.Sub(b.launched) > b.life
}

// shapeBox 绘制一片彩带的外观。
func (p *piece) shapeBox(c *ui.Context) ui.Element {
	switch p.shape.kind {
	case circle:
		return ui.Box(c).Radius(p.w / 2).Background(p.color)
	case star:
		return ui.Box(c).Draw(func(pt *ui.Painter, r ui.Rect) { pt.FillPath(starPath(r), p.color) })
	case heart:
		return ui.Box(c).Draw(func(pt *ui.Painter, r ui.Rect) { pt.FillPath(heartPath(r), p.color) })
	case text:
		return ui.Text(c, p.shape.text).FontSize(p.h).LineHeight(1).Center()
	}
	return ui.Box(c).Background(p.color)
}

// starPath 是填满 r 的五角星路径。
func starPath(r ui.Rect) *ui.Path {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	outer := r.W / 2
	inner := outer * 0.45
	path := &ui.Path{}
	for i := range 10 {
		rad := outer
		if i%2 == 1 {
			rad = inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/5
		x, y := cx+rad*float32(math.Cos(a)), cy+rad*float32(math.Sin(a))
		if i == 0 {
			path.MoveTo(x, y)
		} else {
			path.LineTo(x, y)
		}
	}
	return path.Close()
}

// heartPath 是填满 r 的心形路径。
func heartPath(r ui.Rect) *ui.Path {
	x, y, w, h := r.X, r.Y, r.W, r.H
	return (&ui.Path{}).
		MoveTo(x+w/2, y+h).
		CubeTo(x-w*0.15, y+h*0.55, x+w*0.05, y-h*0.1, x+w/2, y+h*0.22).
		CubeTo(x+w*0.95, y-h*0.1, x+w*1.15, y+h*0.55, x+w/2, y+h).
		Close()
}
