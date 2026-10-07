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

// shadowDiffPerChannel 是比例下界：门槛不低于 底色最大通道 / 7，单位是 0-255 的灰阶。
//
// 为什么门槛要跟着底色走，而不是只给一个固定级数：同一个 10 级差在浅色底与近黑底上的含义
// 完全不同。底色越浅，可暗化的绝对空间越大，“10 级”只占其中很小一段，压在 #FFFFFF 上的
// 16% 黑实际有 41 级差、观感正常，10 级门槛对浅色几乎不设防；底色越黑，整块空间只剩几十级，
// 同一个 10 级已经接近“把阴影压成纯黑”才能换来的量。按比例给门槛，两侧才是同一把尺子。
//
// 7 这个数不是取的整数好看，是按当前被用户接受的默认阴影反推的下界：浅色默认 #00000029
// 压 #FFFFFF 得 41 级，而 255/7≈36.4 < 41。余量足够，所以官方浅色主题在新判据下一个字节
// 都不会变。比例再往上调（比如除以 6）就会动到浅色默认值，那等于顺手改掉官方主题的观感
// ——见 theme/shadow_test.go 里钉住这几套的用例
const shadowDiffPerChannel = 7.0

// shadowDiffAbsolute 是绝对下界：不论底色多暗，阴影的差都不低于这一档。
//
// 只有比例下界时，底色一暗，门槛就跟着缩到几级，等于对近黑主题不设防：2026-dark 的底色
// #121314 最大通道只有 20，比例算出的门槛是 2.9 级，它自带阴影的 7.2 级就算“达标”，
// 而那点差铺到屏幕上只剩 4 级，卡片照样看不出浮起来。官方深色主题自带的 #0000005c 压
// #1e1e1e 实测 10.82 级，这一档是已经被接受为“看得出阴影”的下界；绝对下界取它下方的
// 10.5，既把近黑主题托到与官方深色主题相当的可见度，又留出余量，不会因为小数点后的差异
// 反过来去改那两套官方深色主题（见 TestShadowFloorBounds 末尾的余量断言）
const shadowDiffAbsolute = 10.5

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

// minShadowDiffFor 算出该用的门槛：先取比例下界与绝对下界中大的那个，再夹进这块底色的余量里。
//
//	门槛 = min( max(底色最大通道 / 7, 10.5), 底色最大通道 )
//
// 最后那一下夹是必要的：一块底色能暗下去的幅度最多就是它的最大通道（阴影压到全黑也就到此
// 为止），门槛不能高过它。纯黑底色因此算出 0 级门槛，也就是“什么都算达标”——这是对的，
// 纯黑上再压深也压不出任何差值（ShadowDiff 必然为 0），此时唯一能做的就是别去改主题给的值
func minShadowDiffFor(bg rgba) float64 {
	max := math.Max(bg.r, math.Max(bg.g, bg.b))
	return math.Min(math.Max(max/shadowDiffPerChannel, shadowDiffAbsolute), max)
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

	// 判据要对着**真正发出去的那个色值**判，不能对中途的浮点值判。
	// 输出是 8 位十六进制，每个通道都要取整，取整后的差可能比浮点值小零点几级；
	// 实测 Catppuccin mocha 的候选浮点差刚过门槛 6.571，写下取整后只剩 6.525，
	// 于是“单测说达标、页面上其实没过线”。这里统一先落成十六进制、解析回来再判，
	// 保证单测与页面看到的是同一个数
	emit := func(c rgba) (string, float64) {
		out := c.hex()
		q, ok := parseColor(out)
		if !ok {
			return out, diff(c)
		}
		return out, diff(q)
	}

	// 第一段：保持色相、饱和度与透明度不变，明度按 1% 步进压向全黑，命中即停。
	// 步进而不是一次算到位，为的是“尽可能少改”——主题原本的深浅关系还在
	best := s
	bestDiff := diff(s)
	for step := 1; step <= 100; step++ {
		cand := fromHSL(h, sat, l*(1-float64(step)/100)).withAlpha(s.a)
		out, d := emit(cand)
		if d > bestDiff {
			best, bestDiff = cand, d
		}
		if d >= min {
			return out, true
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
		out, d := emit(cand)
		if d > bestDiff {
			best, bestDiff = cand, d
		}
		if d >= min {
			return out, true
		}
	}

	// 走到尽头都没达标：交出能做到的最好一档并如实报告“动过”。
	// 只有底色接近全黑、确实挤不出差值时才会走到这里（门槛此时本身就接近 0）
	out, _ := emit(best)
	return out, true
}
