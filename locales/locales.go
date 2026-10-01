// locales 包把 eggokit 自身文案（日志、自升级）的语言文件嵌进二进制，
// 供下游叠加到自己的语言系统。
//
// 为什么库要自带译文：这两块文案写在库的源码里，下游的源码扫描器看不到它们，
// 若让下游各自把库的消息抄进自己的语言文件，抄的那份必然随库升级而漂移——改了英文
// 原文就再也对不上。因此译文随库发布，下游只需在 l10n.Options.ExtraLayers 里登记
// Layer()，即可在保持自己语言文件继续权威的同时，让库文案也显示译文。
package locales

import (
	"embed"

	"github.com/jy-eggroll/eggokit/l10n"
)

// Default 是库文案的默认语言。采用「英文源串即消息 id」模型，源码里写的英文原文即
// 默认语言的文案，因此默认语言必须是英文，其语言文件是自映射的生成物。
const Default = "en"

//go:embed *.json
var files embed.FS

// Layer 返回可供 l10n.Options.ExtraLayers 使用的语言层。
//
// 叠加方向即优先级：主来源最后加载、优先级最高，因此下游可以用自己的语言文件覆盖
// 库里的任何译文，冲突时以调用方为准（详见 l10n.Options.ExtraLayers）。
func Layer() l10n.Layer {
	return l10n.Layer{FS: files}
}

// Supported 返回库随二进制发布的语言列表（副本）。
//
// 下游**不必**照抄这份列表：加载时按下游自己的 Supported 逐语言取材，库未提供的
// 语言会被静默跳过（那门语言下库消息回退到源串）。这里只作"库翻译了哪些语言"的查询口。
func Supported() []string {
	return []string{"en", "zh-CN"}
}
