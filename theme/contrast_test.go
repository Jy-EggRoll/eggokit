package theme

import (
	"math"
	"testing"
)

func mustParse(t *testing.T, s string) rgba {
	t.Helper()
	c, ok := parseColor(s)
	if !ok {
		t.Fatalf("解析不了 %q", s)
	}
	return c
}

// hueDiff 返回两个色相之间的最小夹角（0-180），用于判断“色系有没有变”
func hueDiff(a, b rgba) float64 {
	ha, _, _ := toHSL(a)
	hb, _, _ := toHSL(b)
	d := math.Abs(ha - hb)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// TestContrastKnownValues 用可手算的极值校准公式：黑白是 21:1，同色是 1:1
func TestContrastKnownValues(t *testing.T) {
	if got := Contrast("#000000", "#ffffff"); math.Abs(got-21) > 0.01 {
		t.Errorf("黑白对比度应为 21，实得 %.3f", got)
	}
	if got := Contrast("#ffffff", "#ffffff"); math.Abs(got-1) > 0.001 {
		t.Errorf("同色对比度应为 1，实得 %.3f", got)
	}
	// bg 与 fg 互换不影响结果（对比度是对称的）
	if a, b := Contrast("#0069cc", "#ffffff"), Contrast("#ffffff", "#0069cc"); math.Abs(a-b) > 0.001 {
		t.Errorf("对比度应对称：%.3f vs %.3f", a, b)
	}
}

// TestLatteButtonPairIsUnreadable 记录一个事实：Catppuccin Latte 自带的按钮那一对读不出来。
//
// 取值直接来自内置主题文件（button.foreground #dce0e8 / button.background #df8e1d），
// 这条断言同时是“为什么需要对比度回退”的证据——把它删掉，回退组件就没人记得为什么存在了
func TestLatteButtonPairIsUnreadable(t *testing.T) {
	got := Contrast("#dce0e8", "#df8e1d")
	if got >= MinContrast {
		t.Fatalf("这对色值本应低于阈值，实得 %.2f（上游主题改了？）", got)
	}
	if math.Abs(got-1.98) > 0.05 {
		t.Errorf("记录值是 1.98，实得 %.2f", got)
	}
}

// TestEnsureContrastAdjustsForegroundOnly 调完之后必须达标，且只动明度：
// 色相保持不变（仍是主题里的那个色系）、明度朝能提高对比度的方向走
func TestEnsureContrastAdjustsForegroundOnly(t *testing.T) {
	const fg, bg = "#dce0e8", "#df8e1d"
	out, changed := EnsureContrast(fg, []string{bg}, MinContrast)
	if !changed {
		t.Fatalf("这对色值低于阈值，应当被调整，实得未调整")
	}
	if got := Contrast(out, bg); got < MinContrast {
		t.Errorf("调完仍未达标：%s on %s = %.2f", out, bg, got)
	}
	if d := hueDiff(mustParse(t, fg), mustParse(t, out)); d > 10 {
		t.Errorf("色相偏移过大（%.1f 度），不再是同一个色系：%s -> %s", d, fg, out)
	}
	// 底色是亮橙，前景只能往暗里走
	_, _, l0 := toHSL(mustParse(t, fg))
	_, _, l1 := toHSL(mustParse(t, out))
	if l1 >= l0 {
		t.Errorf("底色比前景暗，前景应当调暗：%s(L=%.2f) -> %s(L=%.2f)", fg, l0, out, l1)
	}
}

// TestEnsureContrastKeepsReadablePairs 已经达标的一律不动：不能因为“能调”就把所有主题都调一遍
func TestEnsureContrastKeepsReadablePairs(t *testing.T) {
	out, changed := EnsureContrast("#ffffff", []string{"#0069cc"}, MinContrast)
	if changed || out != "#ffffff" {
		t.Errorf("本已达标不该被调整，实得 %q changed=%v", out, changed)
	}
}

// TestEnsureContrastMultiBackgrounds 多个底色时按“最差的那一对”判定：
// 按钮前景既要压在常态底色上、也要压在 hover 底色上，只满足其中一个不算达标。
//
// 用例取真实形状：VSCode 的 button.hoverBackground 由 button.background 派生，两者明暗同族，
// 因此存在一个前景色能同时满足
func TestEnsureContrastMultiBackgrounds(t *testing.T) {
	bgs := []string{"#df8e1d", "#e8ad4a"} // Catppuccin Latte 的按钮底色与它更亮的 hover 变体
	out, changed := EnsureContrast("#dce0e8", bgs, MinContrast)
	if !changed {
		t.Fatalf("两张底色都不达标，应当被调整")
	}
	for _, bg := range bgs {
		if got := Contrast(out, bg); got < MinContrast {
			t.Errorf("调完在 %s 上仍未达标：%.2f", bg, got)
		}
	}
}

// TestEnsureContrastContradictoryBackgrounds 底色一亮一暗时不存在能同时满足两者的前景色——
// 这时交出“最差那一对能做到的最好一档”，并如实报告动过。
//
// 把边界写清楚的意义在于约束调用方：只能把“真的会同时出现”的前景/底色配对进来
// （由调用方自己决定配对关系，例如按钮只配它自己的常态与悬停底色）
func TestEnsureContrastContradictoryBackgrounds(t *testing.T) {
	bgs := []string{"#1e1e1e", "#df8e1d"}
	before := math.Min(Contrast("#dce0e8", bgs[0]), Contrast("#dce0e8", bgs[1]))
	out, changed := EnsureContrast("#dce0e8", bgs, MinContrast)
	if !changed {
		t.Fatalf("最差那一对不达标，应当被调整（哪怕调不到阈值）")
	}
	after := math.Min(Contrast(out, bgs[0]), Contrast(out, bgs[1]))
	if after < before {
		t.Errorf("调整让最差的一对更糟了：%.2f -> %.2f", before, after)
	}
}

// TestEnsureContrastIgnoresUnparseable 解析不了的颜色原样返回：
// 宁可放着不动，也不要拿一个没解析成功的颜色算出一个新色值
func TestEnsureContrastIgnoresUnparseable(t *testing.T) {
	cases := []struct{ fg, bg string }{
		{"transparent", "#0069cc"},
		{"#ffffff", "rgb(1,2,3)"},
		{"#ffffff", "currentColor"},
		{"", "#0069cc"},
	}
	for _, c := range cases {
		out, changed := EnsureContrast(c.fg, []string{c.bg}, MinContrast)
		if changed || out != c.fg {
			t.Errorf("解析不了时应当原样返回：fg=%q bg=%q 实得 %q changed=%v", c.fg, c.bg, out, changed)
		}
	}
}

// TestParseHexForms 四种十六进制写法都要认，其余一律不认
func TestParseHexForms(t *testing.T) {
	for _, ok := range []string{"#fff", "#ffff", "#ffffff", "#ffffffff", "#0069CC", " #0069cc "} {
		if _, got := parseColor(ok); !got {
			t.Errorf("%q 应当能解析", ok)
		}
	}
	for _, bad := range []string{"fff", "#ff", "#fffff", "#gggggg", "red", "rgb(0,0,0)", ""} {
		if _, got := parseColor(bad); got {
			t.Errorf("%q 不该被解析", bad)
		}
	}
}

// TestRGBRoundTrip 颜色与十六进制之间来回转不应变样（微调组件对颜色的所有操作都建立在这两个函数上）
func TestRGBRoundTrip(t *testing.T) {
	for _, s := range []string{"#dce0e8", "#df8e1d", "#0069cc", "#000000", "#ffffff"} {
		c := mustParse(t, s)
		if got := c.hex(); got != s {
			t.Errorf("来回转变了样：%s -> %s", s, got)
		}
	}
}

// registryPairs 是注册表里成对收录的“前景压在底色上”，一条测试锁住两件事。
//
// 一是必须成对收录：这类缺陷的真实形态就是只抄一半——list.activeSelectionBackground 抄了、
// Foreground 没抄，于是同一份注册表在浅色档下发出去的是“深灰字压在蓝底上”（实测 1.01:1）。
// 逐个令牌检查值的测试发现不了它，只有把一对拿出来算一次对比度才会暴露。
//
// 二是这一对本身要过 AA：注册表是给所有使用方的兜底值，兜底值自己就不达标，
// 使用方再不做二次兜底就没人管了。
//
// 只收底色不透明的配对：keybindingLabel.* 那种底色本身就是半透明色，脱离它实际落在哪个
// 表面上算不出有意义的比值，硬算只会得到一个假结论；那类靠使用方在自己的页面上兜底
var registryPairs = []struct{ fg, bg string }{
	{"list.activeSelectionForeground", "list.activeSelectionBackground"},
	{"badge.foreground", "badge.background"},
	{"button.foreground", "button.background"},
	{"dropdown.foreground", "dropdown.background"},
	{"input.foreground", "input.background"},
}

// TestRegistryPairsMeetAA 注册表里成对收录的配对必须齐全且过 AA
func TestRegistryPairsMeetAA(t *testing.T) {
	for _, ty := range []Type{Dark, Light} {
		base, err := defaultsFor(ty)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range registryPairs {
			fg, bg := base[p.fg], base[p.bg]
			if fg == "" || bg == "" {
				t.Errorf("%s：%s 与 %s 应当成对收录，实际 %q / %q", ty, p.fg, p.bg, fg, bg)
				continue
			}
			if r := Contrast(fg, bg); r < MinContrast {
				t.Errorf("%s：%s(%s) 压在 %s(%s) 上只有 %.2f:1，低于 AA 的 %.1f:1",
					ty, p.fg, fg, p.bg, bg, r, MinContrast)
			}
		}
	}
}

// TestOverCompositesOntoSurface 同一个半透明色压在不同表面上，得到的实际颜色不同——
// 这正是“半透明底色不能当不透明色来算对比度”的根据
func TestOverCompositesOntoSurface(t *testing.T) {
	// 0x25 = 37，即 37/255 的黑，压在纯白上得 #dadada，压在纯黑上仍是黑的
	if got := Over("#00000025", "#ffffff"); got != "#dadada" {
		t.Errorf("37/255 的黑压在纯白上应为 #dadada，实际 %s", got)
	}
	if got := Over("#00000025", "#000000"); got != "#000000" {
		t.Errorf("37/255 的黑压在纯黑上应仍是 #000000，实际 %s", got)
	}
	// 不透明的前景不受底色影响，只做格式归一
	if got := Over("#0069cc", "#ffffff"); got != "#0069cc" {
		t.Errorf("不透明前景应原样返回，实际 %s", got)
	}
	// 解析不了就原样返回，不猜
	if got := Over("rgb(1,2,3)", "#ffffff"); got != "rgb(1,2,3)" {
		t.Errorf("解析不了时应原样返回，实际 %s", got)
	}
}

// TestContrastIgnoresBgAlpha 锁住 Contrast 在底色带透明度时的行为：它把底色当不透明色算，
// 因此会低估问题的严重程度。这条测试不是认可这个结果，而是把“坑在这里”写进代码里——
// 谁想改这个行为，得先看到上面 TestOverCompositesOntoSurface 说明的那件事
func TestContrastIgnoresBgAlpha(t *testing.T) {
	got := Contrast("#757575", "#00000025")
	if got < MinContrast {
		t.Fatalf("本条测试锁的是“按不透明黑底算会虚高”，实际算得 %.2f:1", got)
	}
	// 合成到浅色表面上之后才是真实值：压不住 AA
	real := Contrast("#757575", Over("#00000025", "#ffffff"))
	if real >= MinContrast {
		t.Fatalf("合成到浅色表面上应当读不出来，实际算得 %.2f:1", real)
	}
}
