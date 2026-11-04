package canvas

import (
	"image"
	"math"

	"github.com/egoist/mygo/ui"
)

// 本文件是两段经常被一起用到的排版：
//   - Word：把一串字拆成字母，好让它们各自动画（上一串上浮淡出、下一串逐字母
//     浮入时，两种排版要混着用）；
//   - TextSheet：画家的字是清晰的，要「糊一下」得自己来 —— 离屏把字形栅格化到
//     一张白字透明底的「字表」上，之后在 CPU 上按高斯核糊开。

type wordKey struct {
	s    string
	font ui.Font
}

// Word 是一串成好形的字：letters 是逐段（字母）的字形，advance 是每段相对整串
// 字起点的偏移。
type Word struct {
	letters [][]ui.Glyph
	advance []float32
	width   float32
}

var (
	letterWords = map[wordKey]Word{}
	kernedWords = map[wordKey]Word{}
)

// wordCacheMax 是每个字表缓存的条目上限。
//
// 缓存的键是字串本身，而界面里会出现「数字滚动」「任务名」「时间戳」这类
// 不断变化的文本：不设上限的话，一次长会话能攒下几千条字形表（每条都要
// 保留字形位图），内存只涨不降。超过上限直接清空重来——这些表重建很便宜
// （成形一次），比泄漏可控得多。
const wordCacheMax = 1024

// putWord 写入字表缓存，超上限时清空。
func putWord(m map[wordKey]Word, k wordKey, w Word) {
	if len(m) >= wordCacheMax {
		clear(m)
	}
	m[k] = w
}

// Letters 把 s 逐字母成形：不带 Core Text 在字母之间加的 kerning 与 tracking，
// 这样每个字母才能各自挪动、各自淡入，而不互相牵扯（逐字母进场用它）。
//
// 结果按 (s, font) 缓存：成形不便宜，而标签就那么几个词。
func Letters(s string, f ui.Font) Word {
	k := wordKey{s, f}
	if w, ok := letterWords[k]; ok {
		return w
	}
	var w Word
	for _, r := range s {
		g := ui.Shape(string(r), f)
		w.letters = append(w.letters, g)
		w.advance = append(w.advance, w.width)
		for _, gl := range g {
			w.width += gl.Advance
		}
	}
	putWord(letterWords, k, w)
	return w
}

// Kerned 把 s 当整体成形，带正常的字距（页面上的普通文字用它）。
//
// 与 Letters 的区别：整串一次成形，字母间的字距是字体给的，位置也最准；代价是
// 没法逐字母动画。
func Kerned(s string, f ui.Font) Word {
	k := wordKey{s, f}
	if w, ok := kernedWords[k]; ok {
		return w
	}
	g := ui.Shape(s, f)
	w := Word{letters: [][]ui.Glyph{g}, advance: []float32{0}}
	for _, gl := range g {
		w.width = max(w.width, gl.X+gl.Advance)
	}
	putWord(kernedWords, k, w)
	return w
}

// Width 整串字的宽度（DIP）。
func (w Word) Width() float32 { return w.width }

// Len 段数：Letters 出来的是字母数，Kerned 出来的是 1。
func (w Word) Len() int { return len(w.letters) }

// Advance 第 i 段相对整串字起点的横向偏移（DIP）。
func (w Word) Advance(i int) float32 { return w.advance[i] }

// Glyphs 第 i 段的字形（给要自己画、或要交给 TextSheet 模糊的场合）。
func (w Word) Glyphs(i int) []ui.Glyph { return w.letters[i] }

// Draw 把整串字画出来，笔在 x、基线在 y。
func (w Word) Draw(p *ui.Painter, x, y float32, c ui.Color) {
	for i, g := range w.letters {
		p.Glyphs(g, x+w.advance[i], y, c)
	}
}

// DrawLetter 只画第 i 段，笔在**整串字的起点** x（段自己的偏移在里面加，
// 这样逐字母动画时每段都按同一根基准线算，不会累积误差）。
func (w Word) DrawLetter(p *ui.Painter, i int, x, y float32, c ui.Color) {
	p.Glyphs(w.letters[i], x+w.advance[i], y, c)
}

// The painter draws glyphs sharp; the demo blurs its letters as they come and
// go, as CSS's filter: blur() does. So the letters are rasterized once, before
// the window opens, and blurred on the CPU as they move.

// sheetScale 字表栅格化的像素/DIP：先画大，再缩到屏幕的，小字才不糊。
const sheetScale = 4

// Each text of the sheet has its pen sheetPad DIPs right of its cell's left
// and its baseline sheetBaseline DIPs from the top.
const sheetPad, sheetBaseline, sheetH = 4, 18, 24

// TextSheet 是一张白字透明底的位图：建表时给的词，连同每个词的每个字母，都按
// 同一字体栅格化在上面，各占一格。
type TextSheet struct {
	font  ui.Font
	img   *image.RGBA
	cells map[string][2]int // 每个文本那一格的左边界与宽度（DIP）
}

// NewTextSheet 为 texts 建一张字表（字体 f；每个词里的字母会自动一并栅格化）。
//
// ⚠️ 它内部走 ui.Render，而 ui.Render 自己要跑完整一帧（layout + paint），
// **不能在窗口（或 Tester）的某一帧里调用** —— 持有字表的对象必须在进入帧之前
// 就造好（见 effort.New 与 App 的注释）。
func NewTextSheet(f ui.Font, texts ...string) *TextSheet {
	ts := &TextSheet{font: f, cells: map[string][2]int{}}
	var all []string
	var width int
	add := func(t string) {
		if _, ok := ts.cells[t]; ok {
			return
		}
		var adv float32
		for _, g := range ui.Shape(t, f) {
			adv = max(adv, g.X+g.Advance)
		}
		w := int(math.Ceil(float64(adv))) + 2*sheetPad
		ts.cells[t] = [2]int{width, w}
		all = append(all, t)
		width += w
	}
	for _, w := range texts {
		add(w)
		for _, r := range w {
			add(string(r))
		}
	}
	cells := ts.cells
	ts.img = ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Box(c).Fill().Draw(func(p *ui.Painter, _ ui.Rect) {
			for _, t := range all {
				p.Glyphs(ui.Shape(t, f), float32(cells[t][0]+sheetPad), sheetBaseline, ui.Hex("#ffffff"))
			}
		})
	}, width, sheetH, sheetScale)
	return ts
}

// Has 字表里有没有 t（没有就无法模糊，只能画清晰字形）。
func (ts *TextSheet) Has(t string) bool {
	_, ok := ts.cells[t]
	return ok
}

// Draw 用字表把 str 画在 x、基线 y 处，按 sigma DIP 模糊（CSS 的 filter:
// blur(radius) 那个 radius）。
//
// sigma 太小（模糊不足半个像素）或字表里没有这个词时，退化成画清晰字形 ——
// 模糊是要算的，能省就省。
func (ts *TextSheet) Draw(p *ui.Painter, str string, x, y, sigma float32, c ui.Color) {
	ts.draw(p, str, ui.Shape(str, ts.font), x, y, sigma, c)
}

// DrawGlyphs 同 Draw，但字形由调用方给（已经成过形的，省一次 Shape）。
func (ts *TextSheet) DrawGlyphs(p *ui.Painter, str string, glyphs []ui.Glyph, x, y, sigma float32, c ui.Color) {
	ts.draw(p, str, glyphs, x, y, sigma, c)
}

func (ts *TextSheet) draw(p *ui.Painter, str string, glyphs []ui.Glyph, x, y, sigma float32, c ui.Color) {
	if ts == nil || sigma*p.Scale() < 0.05 || !ts.Has(str) {
		p.Glyphs(glyphs, x, y, c)
		return
	}
	bmp, dx, dy := ts.blurred(str, p.Scale(), sigma, c)
	if bmp == nil {
		p.Glyphs(glyphs, x, y, c)
		return
	}
	bw, bh := bmp.Size()
	p.Image(bmp, ui.Rect{
		X: x + dx, Y: y + dy,
		W: float32(bw) / p.Scale(), H: float32(bh) / p.Scale(),
	}, ui.FillBox)
}

// blurred returns text t of the sheet blurred by sigma DIPs at scale pixels
// per DIP, in color c, and where its top left corner is from the text's pen
// on its baseline, in DIPs.
func (ts *TextSheet) blurred(t string, scale, sigma float32, c ui.Color) (*ui.Bitmap, float32, float32) {
	cell, ok := ts.cells[t]
	if !ok {
		return nil, 0, 0
	}
	// The cell, shrunk to the display's pixels: each pixel the mean of the
	// sheet's under it.
	k := float64(sheetScale) / float64(scale)
	nw, nh := int(math.Ceil(float64(cell[1])*float64(scale))), int(math.Ceil(sheetH*float64(scale)))
	pad := int(math.Ceil(float64(3 * sigma * scale)))
	w, h := nw+2*pad, nh+2*pad
	cov := make([]float32, w*h)
	x0, sw, sh := cell[0]*sheetScale, cell[1]*sheetScale, sheetH*sheetScale
	for y := range nh {
		for x := range nw {
			sx0, sy0 := float64(x)*k, float64(y)*k
			var sum, area float64
			for sy := int(sy0); float64(sy) < sy0+k && sy < sh; sy++ {
				for sx := int(sx0); float64(sx) < sx0+k && sx < sw; sx++ {
					// The share of the sheet's pixel under the display's.
					fx := min(float64(sx+1), sx0+k) - max(float64(sx), sx0)
					fy := min(float64(sy+1), sy0+k) - max(float64(sy), sy0)
					a := fx * fy
					sum += a * float64(ts.img.Pix[sy*ts.img.Stride+(x0+sx)*4+3])
					area += a
				}
			}
			if area > 0 {
				cov[(y+pad)*w+x+pad] = float32(sum / area / 255)
			}
		}
	}
	gaussian(cov, w, h, sigma*scale)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for j, a := range cov {
		a *= float32(c.A) / 255
		img.Pix[j*4] = uint8(float32(c.R)*a + 0.5)
		img.Pix[j*4+1] = uint8(float32(c.G)*a + 0.5)
		img.Pix[j*4+2] = uint8(float32(c.B)*a + 0.5)
		img.Pix[j*4+3] = uint8(255*a + 0.5)
	}
	return ui.NewBitmap(img), -sheetPad - float32(pad)/scale, -sheetBaseline - float32(pad)/scale
}

// gaussian blurs the w×h coverage in place by sigma pixels, rows then
// columns: a separable kernel, so it is two passes rather than a window.
func gaussian(cov []float32, w, h int, sigma float32) {
	if sigma < 0.01 {
		return
	}
	r := int(math.Ceil(float64(3 * sigma)))
	kernel := make([]float32, 2*r+1)
	var sum float32
	for i := range kernel {
		d := float64(i - r)
		kernel[i] = float32(math.Exp(-d * d / (2 * float64(sigma*sigma))))
		sum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= sum
	}
	tmp := make([]float32, len(cov))
	for y := range h {
		for x := range w {
			var v float32
			for i, kv := range kernel {
				if xx := x + i - r; xx >= 0 && xx < w {
					v += kv * cov[y*w+xx]
				}
			}
			tmp[y*w+x] = v
		}
	}
	for y := range h {
		for x := range w {
			var v float32
			for i, kv := range kernel {
				if yy := y + i - r; yy >= 0 && yy < h {
					v += kv * tmp[yy*w+x]
				}
			}
			cov[y*w+x] = v
		}
	}
}
