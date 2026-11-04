// Package imgtest 是自绘页面的离屏断言工具：取像素、比颜色、数一片区域里有
// 多少像素接近某个颜色。
//
// 只给 _test.go 用（正式代码不该 import 它）。自绘的东西在 DOM 里没有节点：
// 文字、按钮都断言不到，「画歪了」「压根没画」在离屏测试里一声不吭。所以自绘
// 页面的测试都得落到像素上，而这几行是每个页面都要重写的，收在这里。
//
// 用法：ui.NewTester(...) 渲染一帧 → tt.Image() 取图 → 用本包断言。
package imgtest

import (
	"image"

	"github.com/egoist/mygo/ui"
)

// At 取 (x, y) 处的颜色。
func At(img *image.RGBA, x, y int) ui.Color {
	i := img.PixOffset(x, y)
	return ui.Color{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}
}

// Near 两个颜色的 RGB 是否都在 tol 以内。
//
// 不看 Alpha：画布是不透明底，半透明的东西画上去之后 Alpha 总是 255，比它没意
// 义；真正的差别在合成后的 RGB 上。
func Near(got, want ui.Color, tol int) bool {
	return dist(got.R, want.R) <= tol && dist(got.G, want.G) <= tol && dist(got.B, want.B) <= tol
}

func dist(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// CountNear 数 rect 里有多少像素接近 want（越界的坐标不算，省得调用方裁剪）。
func CountNear(img *image.RGBA, rect image.Rectangle, want ui.Color, tol int) int {
	n := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if x < 0 || y < 0 || x >= img.Bounds().Dx() || y >= img.Bounds().Dy() {
				continue
			}
			if Near(At(img, x, y), want, tol) {
				n++
			}
		}
	}
	return n
}
