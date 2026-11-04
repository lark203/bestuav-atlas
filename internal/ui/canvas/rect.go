package canvas

import "github.com/egoist/mygo/ui"

// The painter's rounded rectangles curve continuously on macOS, as AppKit's
// do; CSS's border-radius rounds a corner with a circle of radius r, with a
// straight line between the corners. This is the latter, as the demo's card
// is rounded.

// RoundRect 往 dst 里加一个 CSS 那样倒角的矩形（四个角是半径 r 的圆弧，角与角
// 之间是直线；画家的圆角矩形在 macOS 上是连续曲线，不是这个样子）。
//
// r 会被调用方夹到不超过短边的一半 —— 这里不夹：调用方常常要 r=w/2 的胶囊形，
// 而 w 可能是小值，夹与不夹各有主张，交给调用方。
func RoundRect(dst *ui.Path, x, y, w, h, r float32) {
	const k = 1 - 0.5522847498 // 圆弧的四分之一用一段三次贝塞尔拟合时的控制点位置
	dst.MoveTo(x+r, y)
	dst.LineTo(x+w-r, y)
	dst.CubeTo(x+w-r*k, y, x+w, y+r*k, x+w, y+r)
	dst.LineTo(x+w, y+h-r)
	dst.CubeTo(x+w, y+h-r*k, x+w-r*k, y+h, x+w-r, y+h)
	dst.LineTo(x+r, y+h)
	dst.CubeTo(x+r*k, y+h, x, y+h-r*k, x, y+h-r)
	dst.LineTo(x, y+r)
	dst.CubeTo(x, y+r*k, x+r*k, y, x+r, y)
	dst.Close()
}
