// 本文件是“阴影保底”组件：主题给的阴影色有时等于没有阴影，这里只调阴影色本身。
//
// 为什么需要它：阴影是靠“比周围暗一点”被看见的，而有些主题给的 shadow 令牌根本没有这个差值。
// 实测（阴影按 alpha 合成到页面底色后的最大单通道差，0-255）：官方 2026-light 给的是
// #00000000（全透明）差 0 级，卡片等于没阴影；Catppuccin Latte 给 #e6e9ef80 压在自家底色
// #eff1f5 上差 4 级，肉眼看不见。两条路都不能走——去改主题文件等于自己维护一份上游副本，
// 上游一升级就得重做一遍，每套主题还都要各修一次；放着不管则是用户真的看不到卡片浮起来。
// 所以只做中间那条：**主题文件一律不动，阴影色浅到看不见时沿明度轴压深，压到底还不够再抬透明度**，
// 目标是刚过“看得见”的门槛。色相与饱和度保持不变，用户看到的仍是这套主题里的那个颜色，
// 只是深了一档（Catppuccin 的偏蓝灰阴影保底后仍是蓝灰，不会变成纯黑）。
//
// 这里只有通用的颜色数学：“哪个前景压在哪个底色上”属于调用方的页面概念，由调用方自己配对
package theme

import (
	"math"
)

// MinShadowDiff 是判定“阴影看得见”的门槛：合成后与底色的最大单通道差，单位是 0-255 的灰阶。
//
// 10 级这个数是量出来的：注册表默认值（浅色 16% 黑、深色 36% 黑）压在各自底色上的实际差是
// 41 级与 11 级，两档都是正常可辨的观感；而 4-7 级那几套主题实测在屏幕上就是看不见。
// 因此门槛取在两者之间偏下处，够低到不改变“正常阴影”的观感，够高到能把看不见的挑出来
const MinShadowDiff = 10.0

// nearBlackMaxChannel 是“近黑底色”的判定线：底色最大通道低于它时，可暗化空间本来就小。
//
// 比如 2026-dark 的底色 #121314 最大通道只有 20，纯黑以它自己的透明度压上去最多只能差
// 0.36*20≈7 级，怎么压深都到不了 10 级——这种情况门槛按底色最大通道减半，留一半余量，
// 并允许继续抬透明度（抬透明度不改变色调，是暗色主题下唯一还能挤出差值的手段）
//
// 这条线只用来“放宽”，绝不“收紧”：门槛取 max/2 与统一门槛中更小的那个。
// 否则底色最大通道落在 20-31 之间时减半反而会把门槛抬到 10 以上——官方 dark_plus 底色
// #1e1e1e（最大通道 30）本来差 10.8 级、观感正常，会被判成不够而白白改掉，
// 那就成了“顺手把所有主题都改了”，与“达标的必须原样渲染”相冲突
const nearBlackMaxChannel = 32.0

// ShadowDiff 返回阴影色按 alpha 合成到底色上之后、与底色的最大单通道差（0-255）。
//
// 这是阴影“看不看得见”的直接度量：差值落在底色上就是它的真实观感，
// 半透明阴影不先合成是算不出来的（透明度越低，合成结果越贴底色，差值越小）。
// 任一颜色解析不了时返回 0，调用方据此知道“这个值不该参与调整”。
//
// 用最大单通道差而不是 WCAG 对比度：对比度是给“读文字”用的，它对深色底上的细微差别
// 会算出 1.0x 这种难以解释的数字；而阴影是纯视觉的明暗差，直接用灰阶差更贴合实际观感
func ShadowDiff(shadow, bg string) float64 {
	s, okS := parseColor(shadow)
	b, okB := parseColor(bg)
	if !okS || !okB {
		return 0
	}
	// 阴影落在不透明底色上，fb 即它的合成结果
	fb := s.over(b)
	return math.Max(math.Abs(fb.r-b.r), math.Max(math.Abs(fb.g-b.g), math.Abs(fb.b-b.b)))
}

// minShadowDiffFor 按底色算出该用的门槛：近黑底色减半，其余用统一门槛
func minShadowDiffFor(bg rgba) float64 {
	max := math.Max(bg.r, math.Max(bg.g, bg.b))
	if max < nearBlackMaxChannel {
		return math.Min(MinShadowDiff, max/2)
	}
	return MinShadowDiff
}

// EnsureShadow 在阴影色与底色差得看不见时调整阴影色，返回调整后的颜色与“是否真的调过”。
//
// 四个刻意为之的取舍：
//   - 主题文件一律不动，底色也一律不动：动底色等于改主题的设计；阴影色是我们唯一该动的东西
//   - 达标就原样返回：注册表默认值那两档（差 41 级与 11 级）必须一个字节都不变，
//     否则就是“顺手把所有主题都改了”
//   - 先压明度、再抬透明度：压明度保持色相与饱和度，是保住主题颜色家族的手段；
//     只有压到全黑还不够（近黑暗色主题）才抬透明度，因为那时再压也没有空间了
//   - 解析不了就原样返回：宁可放着不动，也不要拿一个没解析成功的颜色算出一个新色值出来
func EnsureShadow(shadow, bg string) (string, bool) {
	s, ok := parseColor(shadow)
	if !ok {
		return shadow, false
	}
	b, ok := parseColor(bg)
	if !ok {
		return shadow, false
	}
	min := minShadowDiffFor(b)
	if ShadowDiff(shadow, bg) >= min {
		return shadow, false
	}

	h, sat, l := toHSL(s)
	diff := func(c rgba) float64 {
		fb := c.over(b)
		return math.Max(math.Abs(fb.r-b.r), math.Max(math.Abs(fb.g-b.g), math.Abs(fb.b-b.b)))
	}

	// 第一段：保持色相、饱和度与透明度不变，明度按 1% 步进压向全黑，命中即停。
	// 步进而不是一次算到位，为的是“尽可能少改”——主题原本的深浅关系还在
	best := s
	bestDiff := diff(s)
	for step := 1; step <= 100; step++ {
		cand := fromHSL(h, sat, l*(1-float64(step)/100)).withAlpha(s.a)
		d := diff(cand)
		if d > bestDiff {
			best, bestDiff = cand, d
		}
		if d >= min {
			return cand.hex(), true
		}
	}

	// 第二段：明度已经压到全黑仍不达标（近黑底色），改为抬透明度。
	// 抬透明度不改变色调，只是让这个已经压深的颜色更多地覆盖到底色上
	if sat > 0 || h > 0 {
		best = fromHSL(h, sat, 0).withAlpha(s.a)
	}
	for step := 1; step <= 100; step++ {
		a := s.a + (1-s.a)*float64(step)/100
		cand := best.withAlpha(a)
		d := diff(cand)
		if d > bestDiff {
			best, bestDiff = cand, d
		}
		if d >= min {
			return cand.hex(), true
		}
	}

	// 走到尽头都没达标：底色是全黑（没有任何可暗化空间），交出能做到的最好一档并如实报告“动过”
	return best.hex(), true
}
