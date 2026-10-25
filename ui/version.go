package ui

import (
	"fmt"
	"io"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/upgradecli"
	"github.com/spf13/cobra"
)

// VersionOptions 是渲染版本信息与生成 version 命令所需的宿主取值。
//
// 版本与构建时间用函数而不是字符串，是因为它们的取值来源属于宿主：发布构建通过 -ldflags 注入，本地开发构建往往还要包一层「没有注入值就返回可读占位」的逻辑；若在这里收成字符串，宿主就必须提前求值，本地构建的占位与输出重定向都会失效。
type VersionOptions struct {
	// Program 是程序名。默认渲染不输出它，保留该字段是为了让宿主能用同一份配置生成命令与自定义输出，避免版本来源在两处各写一份。
	Program string

	// Version 返回程序对外展示的版本号；为 nil 或返回空串时按本地开发构建处理。
	Version func() string

	// BuildTime 返回构建时间；为 nil 或返回空串时按 unknown 处理。
	BuildTime func() string
}

// EffectiveVersion 返回对外展示并用于升级比对的版本号。
//
// 展示与比对必须共用这一份映射：若只在 version 命令里补占位，升级检查会打印出「当前版本  不是受支持的版本号」这种版本位置为空的句子，用户看不出缺了什么（ggt 实际遇到过）。占位串本身不是合法版本号，升级器据此跳过比较，退化为列出目标通道中的最高版本。
func (o VersionOptions) EffectiveVersion() string {
	if o.Version == nil {
		return l10n.T("development build", nil)
	}
	if version := o.Version(); version != "" {
		return version
	}
	return l10n.T("development build", nil)
}

// effectiveBuildTime 返回用于展示的构建时间，把缺失值统一成 unknown。
//
// ggt 的 version 命令在构建时间为空或 unknown 时整行不输出（事实：ggt/cmd/version.go 的判断是 BuildTime != "" && BuildTime != "unknown"）。本包没有照搬该隐藏规则：eggokit 复用的是一条包含三行的单条译文（Version、Build time、Platform），隐藏其中一行就需要再增加并维护一条两行译文，而 flk 的本地开发构建本来就显示 Build time: unknown。把缺失值统一成同名的 unknown 既保持所有宿主的输出格式一致，也如实说明该值不可用，不会让人误以为构建信息被裁剪。
func (o VersionOptions) effectiveBuildTime() string {
	if o.BuildTime != nil {
		if buildTime := o.BuildTime(); buildTime != "" {
			return buildTime
		}
	}
	return "unknown"
}

// RenderVersion 把版本、构建时间与平台写入 w，返回写入错误。
//
// 平台标签与升级命令共用 upgradecli.PlatformLabel()，保证 version 与检查更新显示的名字永远一致（含 Windows 下的 (exe) 后缀）。该标签只用于展示，不要拿它替换按 GOOS/GOARCH 匹配发布资产名的逻辑，两者语义不同。
func RenderVersion(w io.Writer, opts VersionOptions) error {
	text := l10n.T("Version: {{.Version}}\nBuild time: {{.Time}}\nPlatform: {{.Platform}}", map[string]any{
		"Version":  opts.EffectiveVersion(),
		"Time":     opts.effectiveBuildTime(),
		"Platform": upgradecli.PlatformLabel(),
	})
	_, err := fmt.Fprintln(w, text)
	return err
}

// NewVersionCommand 生成 version 命令，别名为 ver。
//
// 命令不绑定任何全局状态：版本来源与输出目标都由宿主注入，writer 取 cobra 在解析阶段确定的 OutOrStdout()，因此宿主可以把输出重定向到缓冲区或其他流。
func NewVersionCommand(opts VersionOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Aliases: []string{"ver"},
		Short:   l10n.T("Display version information", nil),
		Long:    l10n.T("Display version information", nil),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RenderVersion(cmd.OutOrStdout(), opts)
		},
	}
}
