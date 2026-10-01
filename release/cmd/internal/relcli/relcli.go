// Package relcli 收纳 release 下两个命令行工具（buildall、changelog）共用的少量基础件
//
// 之所以单独成包而不是各写一份：语言初始化与"相对项目根解析路径"这两件事在两个工具里
// 完全一致，复制两份必然漂移——尤其是语言默认值，一旦两边不一致，就会出现"构建提示是中文、
// 发布说明是英文"这种让人困惑的组合
//
// 本包放在 internal 下：它是命令行的内部实现细节，不应被下游当作稳定 API 依赖
package relcli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/locales"
)

// DefaultLanguage 是 release 工具在未显式指定语言时使用的语言
//
// 之所以默认英文而不是中文：发布说明最终发布在 GitHub 上，英文是更正式、面向更广读者的
// 展示语言；中文用户可用 --lang zh-CN 或清单里的 releaseNotesLang 覆盖
// 命令行输出（构建进度等）与发布说明共用这一个缺省，避免出现"构建提示是中文、发布说明是英文"
// 这种让人困惑的组合（该参数由调用方提供，见两个 main 的 flag 定义）
const DefaultLanguage = "en"

// ResolveLanguage 按优先级决定最终生效的语言：命令行 --lang > 清单的 releaseNotesLang > 内置默认
//
// 为什么把这条规则收敛到一处而不是各命令各写一遍：两个来源都可能留空，且优先级一旦分散，
// 早晚会在某处漏判，表现为"清单里明明写了英文、发布说明却还是中文"这类极难定位的问题
//
// 只认非空白取值：一个全是空格的语言串等同于未填，否则会把它当成合法输入传下去，
// 最终在 l10n 里静默回退默认语言，掩盖用户的误输入
func ResolveLanguage(flagLang, manifestLang string) string {
	if strings.TrimSpace(flagLang) != "" {
		return flagLang
	}
	if strings.TrimSpace(manifestLang) != "" {
		return manifestLang
	}
	return DefaultLanguage
}

// InitL10n 按给定语言初始化进程级语言状态，空串表示使用 DefaultLanguage
//
// 语言文件直接复用随库发布的 locales 层：这些工具自身的文案与 logger、updater 的文案
// 同属 eggokit，落在同一个 locales 目录里，因此这里把它当作主来源即可，无需再 embed 一份
//
// 主来源必须为每个受支持语言都提供文件，否则 Init 会报错——locales 目录里 en 与 zh-CN 齐全，
// 满足这一前提（这也是不能只把它登记成 ExtraLayers 的原因：附加层允许缺语言，主来源不允许）
func InitL10n(lang string) error {
	if lang == "" {
		lang = DefaultLanguage
	}
	layer := locales.Layer()
	return l10n.Init(lang, l10n.Options{
		Default:   locales.Default,
		Supported: locales.Supported(),
		FS:        layer.FS,
		Dir:       layer.Dir,
	})
}

// ResolveUnderRoot 把命令行传来的路径收敛到"相对项目根"的绝对路径
//
// 规则：绝对路径原样返回，相对路径一律相对 root 解析。之所以不让相对路径相对进程 cwd，
// 是因为这两个工具都会带 --root 指向某个项目目录，若路径相对 cwd，用户就必须在心里同时
// 维护"当前在哪"和"项目在哪"两套坐标，实际使用时极易读错清单或把产物写到仓库外面
func ResolveUnderRoot(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// PrescanLang 在正式解析 flag 之前预扫描 --lang 的取值
//
// 之所以必须提前拿到语言：flag 的说明文案本身也要翻译，而它们是在定义 flag 时就求值的，
// 若等到 Parse 之后才 Init，帮助信息只会是英文源串
//
// 用最朴素的形式匹配四种等价写法（--lang/--lang=/-lang/-lang=），刻意不引入 pflag：
// 本工具参数很少，一个能在十行内写清、且只服务于"提前选语言"这一处的小扫描足够，
// 为此引入一整套 flag 库得不偿失
func PrescanLang(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--lang="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "-lang="); ok {
			return v
		}
		if a == "--lang" || a == "-lang" {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// Fail 把错误按当前语言包装后写入 stderr，并返回命令约定的失败退出码
//
// 统一入口的意义是让两个命令的错误前缀、包装方式与退出码完全一致：
// "错误: xxx" 这种前缀若各写一遍，早晚会出现一处忘翻译或换了措辞
func Fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, l10n.T("Error: {{.Message}}", map[string]any{"Message": err.Error()}))
	return 1
}
