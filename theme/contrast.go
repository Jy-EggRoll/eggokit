// 本文件是“对比度回退”组件：主题给的一对前景/底色有时实在读不出来，这里只调前景色的明度。
//
// 为什么需要它：实测 Catppuccin Latte 自带的 button.foreground #dce0e8 压在
// button.background #df8e1d 上只有 1.98:1，而 WCAG 对正文的 AA 级要求是 4.5:1。
// 两条路都不能走——去改上游色值等于自己维护一份主题副本，上游一升级就得重做一遍，
// 每套主题还都要各修一次；放着不管则是用户真的看不清。所以只做中间那条：
// **底色一律不动，前景色在低于阈值时沿明度轴挪到刚过阈值**，色相与饱和度保持不变——
// 用户看到的仍然是这套主题里的那个颜色，只是深了一档或浅了一档。
//
// 这里只有通用的颜色数学：“哪些前景压在哪些底色上”属于调用方的页面概念，由调用方自己配对
package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MinContrast 是判定“读得出来”的门槛：WCAG 2.x 对正文（非大字）的 AA 级要求是 4.5:1
const MinContrast = 4.5

// rgba 是解析后的颜色：三个通道 0-255，透明度 0-1
type rgba struct{ r, g, b, a float64 }

// parseColor 解析 #RGB / #RGBA / #RRGGBB / #RRGGBBAA。
//
// 只认这几种十六进制写法：VSCode 主题里的颜色几乎都是它们。其余形态（具名色、rgb() 函数）
// 一律当作“解析不了”，由调用方原样保留——猜一个值出来比不动它危险得多
func parseColor(s string) (rgba, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "#") {
		return rgba{}, false
	}
	h := t[1:]
	switch len(h) {
	case 3, 4:
		var b strings.Builder
		for i := 0; i < len(h); i++ {
			b.WriteByte(h[i])
			b.WriteByte(h[i])
		}
		h = b.String()
	case 6, 8:
	default:
		return rgba{}, false
	}

	num := func(i int) (float64, bool) {
		v, err := strconv.ParseUint(h[i:i+2], 16, 8)
		if err != nil {
			return 0, false
		}
		return float64(v), true
	}
	r, ok1 := num(0)
	g, ok2 := num(2)
	b, ok3 := num(4)
	if !ok1 || !ok2 || !ok3 {
		return rgba{}, false
	}
	a := 1.0
	if len(h) == 8 {
		v, ok := num(6)
		if !ok {
			return rgba{}, false
		}
		a = v / 255
	}
	return rgba{r: r, g: g, b: b, a: a}, true
}

// hex 把颜色转回 #rrggbb（带透明度时补上 #rrggbbaa），通道值四舍五入并夹到 0-255
func (c rgba) hex() string {
	scale := func(v float64) int {
		n := int(math.Round(v))
		if n < 0 {
			n = 0
		}
		if n > 255 {
			n = 255
		}
		return n
	}
	s := fmt.Sprintf("#%02x%02x%02x", scale(c.r), scale(c.g), scale(c.b))
	if c.a < 1 {
		s += fmt.Sprintf("%02x", scale(c.a*255))
	}
	return s
}

// over 把带透明度的前景合成到不透明底色上：半透明前景的实际观感就是这个合成结果，
// 因此算对比度必须先合成，否则会高估（或低估）它
func (c rgba) over(bg rgba) rgba {
	if c.a >= 1 {
		return c
	}
	return rgba{
		r: c.r*c.a + bg.r*(1-c.a),
		g: c.g*c.a + bg.g*(1-c.a),
		b: c.b*c.a + bg.b*(1-c.a),
		a: 1,
	}
}

func (c rgba) withAlpha(a float64) rgba {
	c.a = a
	return c
}

// luminance 是 WCAG 2.x 的相对亮度
func (c rgba) luminance() float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.r) + 0.7152*f(c.g) + 0.0722*f(c.b)
}

func ratio(fg, bg rgba) float64 {
	l1 := fg.luminance()
	l2 := bg.luminance()
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// Contrast 返回两色的 WCAG 对比度（1 到 21）。任一颜色解析不了时返回 0，
// 调用方据此知道“这个值不该参与调整”
//
// 底色带透明度时这个函数给不出有意义的答案：它把底色当成不透明色算，而半透明色实际是深是浅
// 取决于落在什么表面上。那种情形要先用 Over 合成出人眼真正看到的那一层颜色，再拿结果进来
func Contrast(fg, bg string) float64 {
	f, okF := parseColor(fg)
	b, okB := parseColor(bg)
	if !okF || !okB {
		return 0
	}
	return ratio(f.over(b), b)
}

// Over 把 top 按自身透明度合成到 bottom 上，返回不透明色。
//
// 存在的理由是“半透明色单独看没有对比度可言”：它真实的观感取决于落在什么表面上。要判断一个
// 半透明的前景或底色能不能读出来，必须先用它合成出人眼看到的那一层颜色，再拿去算比值。
//
// 把半透明底色当不透明色算会得出偏乐观的结论，这是实际发生过的：某主题的选中底色是
// #00000025，字色 #757575，按“不透明黑底”算得 11.6:1，看着却几乎读不出来——它实际压在
// 浅色表面上合成出一层浅灰雾，真实比值只有 3.3:1
//
// 任一方解析不了时原样返回 top：宁可不算，也不要拿一个没解析成功的颜色合成出一个新色值
func Over(top, bottom string) string {
	t, okTop := parseColor(top)
	b, okBottom := parseColor(bottom)
	if !okTop || !okBottom {
		return top
	}
	return t.over(b).hex()
}

// EnsureContrast 在前景色与 bgs 里任一底色读不出来时，沿明度轴调整前景直到全部达标。
//
// 返回调整后的颜色与“是否真的调过”。三个刻意为之的取舍：
//   - 底色一律不动：动底色等于改主题的设计；动“读不出来的那一档明度”才是我们该管的
//   - 只调到刚过阈值：明度按 1% 步进、命中即停，尽可能少改，主题原本的深浅关系还在
//   - 解析不了就原样返回：宁可放着不动，也不要拿一个没解析成功的颜色算出一个新色值
//
// 多个底色时按“最差的那一对”判定：按钮的前景既要压在常态底色上、也要压在 hover 底色上，
// 两个都得达标才算达标
//
// 底色带透明度时，调用方必须先把它合成到实际落着的表面上（用 Over）再传进来：本函数不做
// 这一步，因为它不知道那个表面是什么——那是调用方的页面知识。不合成就会漏判，见 Over 里
// 记着的那个实例
func EnsureContrast(fg string, bgs []string, min float64) (string, bool) {
	f, ok := parseColor(fg)
	if !ok {
		return fg, false
	}
	parsed := make([]rgba, 0, len(bgs))
	for _, s := range bgs {
		if c, ok := parseColor(s); ok {
			parsed = append(parsed, c)
		}
	}
	if len(parsed) == 0 {
		return fg, false
	}

	worst := func(c rgba) float64 {
		m := math.MaxFloat64
		for _, bg := range parsed {
			if r := ratio(c.over(bg), bg); r < m {
				m = r
			}
		}
		return m
	}
	if worst(f) >= min {
		return fg, false
	}

	// 方向：往黑还是往白，取能让“最差那一对”更高的一端
	black := rgba{a: 1}
	white := rgba{r: 255, g: 255, b: 255, a: 1}
	towardWhite := worst(white) > worst(black)

	h, s, l := toHSL(f)
	target := 0.0
	if towardWhite {
		target = 1
	}
	best := f
	bestRatio := worst(f)
	for step := 1; step <= 100; step++ {
		nl := l + (target-l)*float64(step)/100
		if nl < 0 {
			nl = 0
		}
		if nl > 1 {
			nl = 1
		}
		cand := fromHSL(h, s, nl).withAlpha(f.a)
		r := worst(cand)
		if r > bestRatio {
			best, bestRatio = cand, r
		}
		if r >= min {
			return cand.hex(), true
		}
	}
	// 走到明度尽头都没达标（极端配色）：交出能做到的最好一档，并如实报告“动过”
	return best.hex(), true
}

// toHSL 转到 HSL（h 0-360，s/l 0-1）。保留色相与饱和度是“微调”的关键：
// 只有明度变了，用户仍认得出那是主题里的哪个色
func toHSL(c rgba) (h, s, l float64) {
	r, g, b := c.r/255, c.g/255, c.b/255
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = (max + min) / 2
	if max == min {
		return 0, 0, l
	}
	d := max - min
	if l > 0.5 {
		s = d / (2 - max - min)
	} else {
		s = d / (max + min)
	}
	switch max {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h * 60, s, l
}

// fromHSL 是 toHSL 的逆运算
func fromHSL(h, s, l float64) rgba {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	if s == 0 {
		v := l * 255
		return rgba{r: v, g: v, b: v, a: 1}
	}
	q := l * (1 + s)
	if l >= 0.5 {
		q = l + s - l*s
	}
	p := 2*l - q
	hue := func(t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 1.0/2:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	return rgba{
		r: hue(h/360+1.0/3) * 255,
		g: hue(h/360) * 255,
		b: hue(h/360-1.0/3) * 255,
		a: 1,
	}
}
