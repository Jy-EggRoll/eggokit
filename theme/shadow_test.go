package theme

import (
	"math"
	"testing"
)

// 下表的每一行都是**实测出来的真实取值**：shadow 与 bg 取自 theme.Resolve 解析各内置主题后
// 的 widget.shadow / editor.background（含 include 链与注册表默认值的兜底），不是编造的色值。
// 改这些数字等于换一套输入，必须重新用 ggt 或本包的 Resolve 跑一遍再抄进来
var shadowCases = []struct {
	id      string
	shadow  string
	bg      string
	changed bool
}{
	// 主题自己写了全透明阴影，卡片等于是没有阴影
	{"vscode/2026-light.json", "#00000000", "#FFFFFF", true},
	// 底色近黑（最大通道 20），可暗化空间小，只能靠抬透明度达标
	{"vscode/2026-dark.json", "#0000005c", "#121314", true},
	// 这三套没写 widget.shadow，走注册表浅色默认值，差 41 级，必须一个字节都不变
	{"vscode/light_plus.json", "#00000029", "#FFFFFF", false},
	{"vscode/light_vs.json", "#00000029", "#FFFFFF", false},
	{"vscode/light_modern.json", "#00000029", "#FFFFFF", false},
	// 走注册表深色默认值，差 10.8 与 11.2 级，观感正常，同样必须原样保留
	{"vscode/dark_plus.json", "#0000005c", "#1E1E1E", false},
	{"vscode/dark_vs.json", "#0000005c", "#1E1E1E", false},
	{"vscode/dark_modern.json", "#0000005c", "#1F1F1F", false},
	// 四套 Catppuccin 自己给的阴影压在自家底色上都只有 4-5 级，几乎看不见
	{"catppuccin/latte.json", "#e6e9ef80", "#eff1f5", true},
	{"catppuccin/mocha.json", "#18182580", "#1e1e2e", true},
	{"catppuccin/macchiato.json", "#1e203080", "#24273a", true},
	{"catppuccin/frappe.json", "#292c3c80", "#303446", true},
}

// TestEnsureShadowOnRealThemes 用真实主题取值把“该改的改、不该动的不动”钉住
func TestEnsureShadowOnRealThemes(t *testing.T) {
	for _, c := range shadowCases {
		got, changed := EnsureShadow(c.shadow, c.bg)
		if changed != c.changed {
			t.Errorf("%s: 是否调整 = %v，期望 %v（调整后 %s）", c.id, changed, c.changed, got)
			continue
		}
		if !c.changed {
			// 关键判据：达标的主题渲染结果必须与现在完全一致，不是“等价”而是同一个字符串
			if got != c.shadow {
				t.Errorf("%s: 达标主题被改动，%s -> %s", c.id, c.shadow, got)
			}
			continue
		}
		if got == c.shadow {
			t.Errorf("%s: 报了调整却没有变，%s", c.id, got)
		}
		// 保底后必须真的看得见：差值不小于该底色对应的门槛
		min := minShadowDiffFor(mustParse(t, c.bg))
		if d := ShadowDiff(got, c.bg); d < min {
			t.Errorf("%s: 保底后差 %v 级，仍低于门槛 %v", c.id, d, min)
		}
	}
}

// TestEnsureShadowKeepsColorFamily 保底只该动明度与透明度，色相与饱和度要留住。
// Catppuccin 那种偏蓝灰的阴影保底后仍应是蓝灰调，不能变成纯黑。
//
// 色相容差给到 5 度而不是 0：结果要写回 8 位十六进制的色值，通道取整本身就会让反算出的
// 色相漂 1-3 度（实测 latte 1.5 度、macchiato 2.4 度）。5 度远小于“换了个色系”的量级
// （蓝灰与纯黑相差的是饱和度整条轴），足以钉住颜色家族没有变
func TestEnsureShadowKeepsColorFamily(t *testing.T) {
	for _, c := range shadowCases {
		if !c.changed {
			continue
		}
		orig := mustParse(t, c.shadow)
		fixed, _ := EnsureShadow(c.shadow, c.bg)
		got := mustParse(t, fixed)
		_, os, _ := toHSL(orig)
		// 阴影色本来就接近无彩色（色相在这些主题里是灰阶的噪声）时，不比色相
		if os < 0.05 {
			continue
		}
		if d := hueDiff(orig, got); d > 5 {
			t.Errorf("%s: 色相偏了 %v 度（%s -> %s）", c.id, d, c.shadow, fixed)
		}
		_, gs, _ := toHSL(got)
		if math.Abs(os-gs) > 0.02 {
			t.Errorf("%s: 饱和度从 %v 变到 %v", c.id, os, gs)
		}
	}
}

// TestEnsureShadowParsesNothing 解析不了的颜色原样返回，也不能报成调整过
func TestEnsureShadowParsesNothing(t *testing.T) {
	for _, c := range [][2]string{
		{"rgb(0 0 0 / 50%)", "#FFFFFF"},
		{"#00000080", "not-a-color"},
		{"", "#FFFFFF"},
	} {
		got, changed := EnsureShadow(c[0], c[1])
		if changed || got != c[0] {
			t.Errorf("EnsureShadow(%q, %q) = %q, %v，期望原样返回", c[0], c[1], got, changed)
		}
	}
}

// TestEnsureShadowNoHeadroom 全黑底色的可暗化空间为零，此时不该无限折腾，
// 但要如实报告“动过”（阴影色确实被压到了能做的最好一档）
func TestEnsureShadowNoHeadroom(t *testing.T) {
	got, _ := EnsureShadow("#00000029", "#000000")
	if d := ShadowDiff(got, "#000000"); d != 0 {
		t.Errorf("全黑底色上竟算出 %v 级差，颜色 %s", d, got)
	}
}

// TestShadowDiffOnDefaults 把“正常阴影”那两档的实测差级钉住：
// 注册表默认值是比对基准，这两条数字变了说明合成或门槛的数学被改坏了
func TestShadowDiffOnDefaults(t *testing.T) {
	for _, c := range []struct {
		shadow, bg string
		want       float64
	}{
		{"#00000029", "#FFFFFF", 41},   // 浅色默认：16% 黑压白
		{"#0000005c", "#1E1E1E", 10.8}, // 深色默认：36% 黑压 #1e1e1e
	} {
		if got := ShadowDiff(c.shadow, c.bg); math.Abs(got-c.want) > 0.05 {
			t.Errorf("ShadowDiff(%s, %s) = %v，期望 %v", c.shadow, c.bg, got, c.want)
		}
	}
}
