# eggokit

eggokit 是 [Jy-EggRoll](https://github.com/Jy-EggRoll) 的 Go 通用基础库，把多个命令行工具反复需要的底层能力收敛成单一实现，供它们共同依赖，避免同一份逻辑在多个仓库里各写一遍、各自漂移。

## 包含的包

| 包 | 作用 |
| --- | --- |
| `l10n` | 轻量国际化。以英文源串作为消息 id，支持运行期切语言、整体原子快照、多来源语言层叠加，以及 CLI 与 HTML 页面文案的统一提取与校验 |
| `locales` | 库自身文案（日志、升级）的语言文件，供下游通过 `l10n.Options.ExtraLayers` 叠加 |
| `jsonfile` | JSON 序列化的统一口径（缩进、转义、结尾换行），保证写出的配置文件在版本控制里 diff 稳定 |
| `atomicfile` | 原子写文件：同目录临时文件 + 落盘 + rename，避免写入中途失败留下半截文件 |
| `logger` | 基于标准库 `log/slog` 的分级日志，级别可运行时切换，供诊断与审计使用 |
| `updater` | 自升级：查询 GitHub Release、下载并校验 SHA-256 摘要、跨平台替换可执行文件（含 Windows 原地改名交接） |

各包的具体约定写在各自的包注释里，改代码前先读那里。

## 库自带的译文怎么用

`logger` 与 `updater` 的文案由库自己提供译文。下游只要在自己的语言层之外多登记一层，这些文案就会随下游的语言一起切换，而不必把它们抄进自己的语言文件：

```go
import (
	"github.com/jy-eggroll/eggokit/l10n"
	eggolocales "github.com/jy-eggroll/eggokit/locales"
)

opts := l10n.Options{
	Default:     "en",
	Supported:   []string{"en", "zh-CN"},
	FS:          myEmbeddedLocales,   // 下游自己的语言文件
	ExtraLayers: []l10n.Layer{eggolocales.Layer()},
}
```

叠加时**主来源最后加载、优先级最高**，因此下游自己的译文始终压过库提供的译文。

## 安装

```bash
go get github.com/jy-eggroll/eggokit@latest
```

## 版本约定

库采用语义化版本打 tag；下游通过普通的 `require` 依赖即可。包级 API 的破坏性变更会体现在主版本号或 CHANGELOG 中。

## 开发

```bash
task        # 等价于 task verify：依赖整洁 + 格式 + vet + 单测 + 竞态
```

## 许可证

暂未声明。
