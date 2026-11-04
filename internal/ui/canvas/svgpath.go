package canvas

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 图标库（Lucide、Material…）给的都是 SVG 的 path data 字符串，而画家要的是
// ui.Path。本文件只做这一件事：把 path data 解析成一份与位置、尺寸无关的命令，
// 之后能在任意位置按任意缩放贴到 ui.Path 上（见 AppendTo、Icon）。
//
// 解析只做一次（ParsePath），缩放与平移在 AppendTo 里做：图标要按 DIP 画，
// 而 DIP 每帧都可能变。

// Path 是一条 SVG <path> 的 path data，解析成与位置、尺寸无关的命令。
type Path struct {
	cmds []pathCmd
}

// ParsePaths 解析多条 path data：一个图标常常由好几条 path 组成（轮廓与细节
// 分层，才好分别填充/描边）。
func ParsePaths(ds ...string) []Path {
	out := make([]Path, len(ds))
	for i, d := range ds {
		out[i] = ParsePath(d)
	}
	return out
}

// Icon 把若干条 24 单位方格的 path（Lucide 那套约定）拼成一个 ui.Path：
// size DIP 见方，中心在 (cx, cy)。
func Icon(paths []Path, cx, cy, size float32) *ui.Path {
	var out ui.Path
	s := size / 24
	for _, p := range paths {
		p.AppendTo(&out, cx-12*s, cy-12*s, s)
	}
	return &out
}

type pathCmd struct {
	op  byte // 'M', 'L', 'C' or 'Z', in absolute coordinates
	pts [3][2]float64
}

// ParsePath parses path data of moves, lines, curves and elliptical
// arcs, absolute and relative, as icon sets write them; arcs become cubic
// curves.
func ParsePath(d string) Path {
	var p Path
	toks := tokenize(d)
	var cx, cy, sx, sy float64 // current point, subpath start
	var lastCtrl [2]float64    // the last curve's second control point, for S
	var lastOp byte
	i := 0
	num := func() float64 {
		v, _ := strconv.ParseFloat(toks[i], 64)
		i++
		return v
	}
	flag := func() bool {
		// Flags can be written without separators: "a3 3 0 1 0 5 5"
		// tokenizes them as numbers.
		return num() != 0
	}
	var op byte
	for i < len(toks) {
		if c := toks[i][0]; c >= 'A' && c <= 'z' && c != 'e' && c != 'E' {
			op = c
			i++
		} else if op == 'M' {
			op = 'L' // implicit lines after a move
		} else if op == 'm' {
			op = 'l'
		}
		rel := op >= 'a'
		base := func() (float64, float64) {
			if rel {
				return cx, cy
			}
			return 0, 0
		}
		switch op {
		case 'M', 'm':
			bx, by := base()
			cx, cy = bx+num(), by+num()
			sx, sy = cx, cy
			p.cmds = append(p.cmds, pathCmd{op: 'M', pts: [3][2]float64{{cx, cy}}})
		case 'L', 'l':
			bx, by := base()
			cx, cy = bx+num(), by+num()
			p.cmds = append(p.cmds, pathCmd{op: 'L', pts: [3][2]float64{{cx, cy}}})
		case 'H', 'h':
			bx, _ := base()
			cx = bx + num()
			p.cmds = append(p.cmds, pathCmd{op: 'L', pts: [3][2]float64{{cx, cy}}})
		case 'V', 'v':
			_, by := base()
			cy = by + num()
			p.cmds = append(p.cmds, pathCmd{op: 'L', pts: [3][2]float64{{cx, cy}}})
		case 'C', 'c':
			bx, by := base()
			c1 := [2]float64{bx + num(), by + num()}
			c2 := [2]float64{bx + num(), by + num()}
			cx, cy = bx+num(), by+num()
			p.cmds = append(p.cmds, pathCmd{op: 'C', pts: [3][2]float64{c1, c2, {cx, cy}}})
			lastCtrl = c2
		case 'S', 's':
			bx, by := base()
			c1 := [2]float64{cx, cy}
			if lastOp == 'C' || lastOp == 'c' || lastOp == 'S' || lastOp == 's' {
				c1 = [2]float64{2*cx - lastCtrl[0], 2*cy - lastCtrl[1]}
			}
			c2 := [2]float64{bx + num(), by + num()}
			cx, cy = bx+num(), by+num()
			p.cmds = append(p.cmds, pathCmd{op: 'C', pts: [3][2]float64{c1, c2, {cx, cy}}})
			lastCtrl = c2
		case 'A', 'a':
			rx, ry, rot := num(), num(), num()
			large, sweep := flag(), flag()
			bx, by := base()
			x, y := bx+num(), by+num()
			p.arc(cx, cy, rx, ry, rot, large, sweep, x, y)
			cx, cy = x, y
		case 'Z', 'z':
			cx, cy = sx, sy
			p.cmds = append(p.cmds, pathCmd{op: 'Z'})
		default:
			return p
		}
		lastOp = op
	}
	return p
}

// tokenize splits path data into commands and numbers, which may run
// together as "1-2.5.5".
func tokenize(d string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	dot := false
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c == 'e' || c == 'E':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
			flush()
			out = append(out, string(c))
			dot = false
		case c == '-' || c == '+':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "e") {
				flush()
				dot = false
			}
			b.WriteByte(c)
		case c == '.':
			if dot {
				flush()
			}
			dot = true
			b.WriteByte(c)
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		default: // separators
			flush()
			dot = false
		}
	}
	flush()
	return out
}

// arc appends an elliptical arc from (x0, y0) to (x, y) as SVG's
// implementation notes convert it, in cubic curves of at most 90°.
func (p *Path) arc(x0, y0, rx, ry, rot float64, large, sweep bool, x, y float64) {
	if rx == 0 || ry == 0 {
		p.cmds = append(p.cmds, pathCmd{op: 'L', pts: [3][2]float64{{x, y}}})
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := rot * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx, dy := (x0-x)/2, (y0-y)/2
	x1 := cos*dx + sin*dy
	y1 := -sin*dx + cos*dy
	if l := x1*x1/(rx*rx) + y1*y1/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	k := math.Sqrt(math.Max(num/den, 0))
	if large == sweep {
		k = -k
	}
	cxp, cyp := k*rx*y1/ry, -k*ry*x1/rx
	ccx := cos*cxp - sin*cyp + (x0+x)/2
	ccy := sin*cxp + cos*cyp + (y0+y)/2
	angle := func(ux, uy, vx, vy float64) float64 {
		a := math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
		return a
	}
	t1 := angle(1, 0, (x1-cxp)/rx, (y1-cyp)/ry)
	dt := angle((x1-cxp)/rx, (y1-cyp)/ry, (-x1-cxp)/rx, (-y1-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dt) / (math.Pi / 2)))
	step := dt / float64(n)
	h := 4.0 / 3 * math.Tan(step/4)
	point := func(t float64) (float64, float64) {
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		return cos*ex - sin*ey + ccx, sin*ex + cos*ey + ccy
	}
	deriv := func(t float64) (float64, float64) {
		ex, ey := -rx*math.Sin(t), ry*math.Cos(t)
		return cos*ex - sin*ey, sin*ex + cos*ey
	}
	t := t1
	for range n {
		ax, ay := point(t)
		bx, by := point(t + step)
		dax, day := deriv(t)
		dbx, dby := deriv(t + step)
		p.cmds = append(p.cmds, pathCmd{op: 'C', pts: [3][2]float64{
			{ax + h*dax, ay + h*day}, {bx - h*dbx, by - h*dby}, {bx, by}}})
		t += step
	}
	// The last point exactly, as the next command starts from it.
	p.cmds[len(p.cmds)-1].pts[2] = [2]float64{x, y}
}

// AppendTo adds the path to dst, scaled by s and moved to (ox, oy).
func (p Path) AppendTo(dst *ui.Path, ox, oy, s float32) {
	f := func(v [2]float64) (float32, float32) {
		return ox + float32(v[0])*s, oy + float32(v[1])*s
	}
	for _, c := range p.cmds {
		switch c.op {
		case 'M':
			dst.MoveTo(f(c.pts[0]))
		case 'L':
			dst.LineTo(f(c.pts[0]))
		case 'C':
			x1, y1 := f(c.pts[0])
			x2, y2 := f(c.pts[1])
			x, y := f(c.pts[2])
			dst.CubeTo(x1, y1, x2, y2, x, y)
		case 'Z':
			dst.Close()
		}
	}
}
