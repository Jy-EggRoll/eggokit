// Package upgradecli 把命令行工具的“自升级”命令收敛成一份实现。
//
// 升级本身（查 Release、比版本、下载、校验摘要、原子替换可执行文件）由 updater 负责，
// 而各个工具此前各自写一份命令层：读 flag、选通道、组织提示语、决定确认时机。
// 那部分在多个仓库里逐行重复，改一处文案就要改好几遍，因此独立成包。
//
// 本包只承担“与用户对话”的那一半，不内置任何宿主取值：程序名、仓库坐标、资产命名、
// 版本与构建时间、界面实现、令牌来源全部由 Options 注入。
package upgradecli

import (
	"fmt"
	"runtime"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/updater"
	"github.com/spf13/cobra"
)

// Target 描述一个宿主项目的发布目标。
//
// 这些取值来自各项目自己的发布约定（仓库、产物文件名前缀、发布平台），
// 与 updater 的通用能力无关，因此由宿主填写而不是写进 updater。
type Target struct {
	// Owner 与 Repo 指向承载 Release 的仓库，必须是实际发布产物的仓库
	Owner string
	Repo  string

	// AssetPrefix 是发布产物的固定文件名前缀，必须与宿主的构建输出名一致
	AssetPrefix string

	// Platforms 声明宿主实际发布产物的系统与架构组合：外层 GOOS，内层 GOARCH。
	// 少列组合会让对应平台的用户收到“不提供该平台产物”的提示，
	// 多列不存在的组合则会被判为无可用资产而继续向前寻找旧版本——
	// 两种偏差都会让用户拿不到本该能装上的新版本，因此必须与构建脚本保持同步
	Platforms map[string]map[string]bool

	// ProxyPrefix 是直连不稳定时供用户选择的备用下载代理前缀，与资产原始地址直接拼接。
	// 是否启用完全由用户在升级过程中的确认决定，程序不会静默换源——
	// 下载结果随后会被直接执行，用户必须清楚二进制来自哪个镜像
	ProxyPrefix string

	// UserAgent 是请求标识，建议设为宿主程序名以便上游识别流量来源
	UserAgent string
}

// AssetName 返回指定平台对应的资产文件名前缀。
//
// ok 为 false 表示宿主不为该平台发布产物，updater 会据此给出明确提示，
// 而不会因为找不到资产就误报“已是最新”
func (t Target) AssetName(goos, goarch string) (string, bool) {
	architectures, known := t.Platforms[goos]
	if !known || !architectures[goarch] {
		return "", false
	}

	prefix := fmt.Sprintf("%s-%s-%s", t.AssetPrefix, goos, goarch)
	if goos == "windows" {
		// 把扩展名纳入前缀，使匹配结果精确到 Windows 产物本身，
		// 避免将来同时存在带与不带扩展名的同类文件时命中错误的目标
		prefix += ".exe"
	}
	return prefix, true
}

// PlatformLabel 产出面向用户展示的当前平台标签，形如 linux-amd64。
//
// 它只用于展示，不要用它去替换 Target.AssetName 中按 GOOS/GOARCH 匹配资产名的逻辑：
// 那里是“选哪个发布产物”的构建坐标，与这里“给用户看的名字”语义不同，合并会破坏资产匹配
func PlatformLabel() string {
	platform := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		platform += " (exe)"
	}
	return platform
}

// Options 是生成升级命令所需的全部宿主信息。
//
// 版本、构建时间与界面用函数而不是字符串，是因为它们的取值时机与来源都属于宿主：
// 版本与构建时间由发布构建通过 -ldflags 注入，是编译期常量，宿主往往还有一个
// “本地开发构建时补一个可读占位”的包装函数；界面则可能要到命令真正执行时才知道
// 该写入哪个流（例如 cobra 在解析阶段才确定输出目标）。若在这里收成字符串，
// 宿主就必须提前求值，本地构建占位与输出重定向都会失效。
type Options struct {
	// Program 是程序名，仅用于文案（如“重启 ggt 后生效”）
	Program string

	// Target 是宿主的发布目标
	Target Target

	// Version 返回宿主当前版本，通常在本地开发构建下返回可读占位而非空串
	Version func() string

	// BuildTime 返回宿主构建时间，用于展示
	BuildTime func() string

	// Reporter 在命令执行时返回承接全部用户可见输出与确认的界面实现。
	// 为空时使用 updater.DiscardReporter，便于宿主在测试中免于实现界面
	Reporter func() updater.Reporter

	// TokenProvider 返回 GitHub 令牌；为空时按环境变量查找
	TokenProvider func() string

	// apiEndpoint 仅供包内测试把升级器指向假接口，宿主无法设置它。
	// 对外的 APIEndpoint 属于 upgradecli 无需暴露的细节：宿主只提供仓库坐标，
	// 联系方式（GitHub 公共 API）由 updater 的默认值决定
	apiEndpoint string

	// AssumeYes 返回是否跳过确认。为空表示从不跳过。
	//
	// 是否提供“跳过确认”的开关由宿主决定：ggt 把它做成升级命令的局部参数，
	// flk 则把它做成全局持久参数。共享命令因此不注册 --yes——
	// 注册会与宿主的定义冲突（cobra 同一命令上重复注册同名 flag 会 panic），
	// 也会把“全局开关”降级成“只对升级生效”，两种语义都无法兼顾。
	// 这里只接收判断结果，把参数怎么声明留给宿主
	AssumeYes func() bool
}

// NewCommand 构造升级命令：Use 为 upgrade，别名为 update 与 up。
func NewCommand(opts Options) *cobra.Command {
	c := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update", "up"},
		Short:   l10n.T("Check for and upgrade to the latest version", nil),
		Long:    l10n.T("Check for and upgrade {{.Program}} to the latest version", map[string]any{"Program": opts.Program}),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, opts)
		},
	}

	c.Flags().Bool("check", false, l10n.T("Only check the version, do not upgrade", nil))
	c.Flags().Bool("force", false, l10n.T("Force upgrade", nil))
	c.Flags().Bool("dev", false, l10n.T("Check for development versions", nil))
	// --alpha 与 --dev 都表示“抢先验证尚未正式发布的构建”，同时指定时无法判断用户想要哪一个
	// 通道，静默择一执行只会让人以为命令没按预期工作，因此声明互斥交给 cobra 拦下
	c.Flags().Bool("alpha", false, l10n.T("Check for builds from the rolling alpha prerelease", nil))
	c.MarkFlagsMutuallyExclusive("dev", "alpha")

	return c
}

// run 完成版本检查、用户确认与升级执行。
//
// 业务失败只返回带上下文的错误交由宿主的根命令统一打印一次，命令内部绝不调用 os.Exit，
// 以免绕过宿主的退出码边界
func run(cmd *cobra.Command, opts Options) error {
	// GetBool 读取 flag 的真实布尔值，避免 --check=false 这类显式 false 被 Changed 误判为启用；
	// 任一读取失败都必须沿 RunE 返回，不能以零值继续执行而掩盖命令定义或测试注入错误
	checkOnly, err := cmd.Flags().GetBool("check")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --check argument", nil), err)
	}
	forceUpdate, err := cmd.Flags().GetBool("force")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --force argument", nil), err)
	}
	checkDev, err := cmd.Flags().GetBool("dev")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --dev argument", nil), err)
	}
	checkAlpha, err := cmd.Flags().GetBool("alpha")
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to read the --alpha argument", nil), err)
	}

	assumeYes := opts.AssumeYes != nil && opts.AssumeYes()

	// 通道决定候选版本的取舍：正式版用户永远不会被投递开发版，开发版与 alpha 通道供希望
	// 提前验证的用户主动选择。两个通道已在 flag 层声明互斥，因此这里的分支先后不影响结果，
	// 保留默认走 stable 的分支形态是为了让新增通道时改动只落在一处
	channel := updater.ChannelStable
	channelLabel := l10n.T("stable", nil)
	switch {
	case checkAlpha:
		channel = updater.ChannelAlpha
		channelLabel = l10n.T("alpha", nil)
	case checkDev:
		channel = updater.ChannelDev
		channelLabel = l10n.T("dev", nil)
	}

	// reporter 承接升级器的全部用户可见输出与确认；令牌查找结果被升级器与下面的提示共用，只会真正查找一次
	reporter := updater.Reporter(updater.DiscardReporter())
	if opts.Reporter != nil {
		reporter = opts.Reporter()
	}
	tokenProvider := opts.TokenProvider
	if tokenProvider == nil {
		tokenProvider = updater.EnvTokenProvider("")
	}

	// 升级器只接收注入的仓库坐标、资产规则与界面，不感知任何宿主的发布约定
	upgrader, err := updater.New(updater.Config{
		Owner:         opts.Target.Owner,
		Repo:          opts.Target.Repo,
		AssetName:     opts.Target.AssetName,
		Reporter:      reporter,
		TokenProvider: tokenProvider,
		ProxyPrefix:   opts.Target.ProxyPrefix,
		UserAgent:     opts.Target.UserAgent,
		APIEndpoint:   opts.apiEndpoint,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to initialize the upgrader", nil), err)
	}

	reporter.Info("%s", l10n.T("Checking for updates ({{.Channel}})...", map[string]any{"Channel": channelLabel}))
	reporter.Info("%s", l10n.T("Current platform: {{.Platform}}", map[string]any{"Platform": PlatformLabel()}))

	// 令牌状态提示属于宿主的运维建议而非升级器职责，因此留在命令层；
	// 只在缺失时提示，配置正确的用户无需被无关信息打扰
	if tokenProvider() == "" {
		reporter.Info("%s", l10n.T("No GitHub token configured; API requests are subject to anonymous rate limits. Set GITHUB_TOKEN to raise the quota", nil))
	}

	// 版本与构建时间来自宿主构建期注入的变量，本地开发构建下可能只有可读占位。
	// 此时升级器无法解析版本，会跳过比较，退化为“列出目标通道中的最高版本”，
	// 由展示层避免使用“更新”这类断言
	var currentVersion, currentBuildTime string
	if opts.Version != nil {
		currentVersion = opts.Version()
	}
	if opts.BuildTime != nil {
		currentBuildTime = opts.BuildTime()
	}

	info, err := upgrader.Check(currentVersion, currentBuildTime, channel)
	if err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Failed to check for updates", nil), err)
	}
	if info == nil {
		// alpha 通道没有版本号，不能沿用“已是最新版本”，否则用户会以为存在一个叫“最新版本”的东西
		if checkAlpha {
			reporter.Success("%s", l10n.T("Already on the latest alpha build", nil))
			return nil
		}
		reporter.Success("%s", l10n.T("Already on the latest version", nil))
		return nil
	}

	reporter.Info("%s", l10n.T("Current version: {{.Version}} (build: {{.Build}})", map[string]any{"Version": info.CurrentVersion, "Build": info.CurrentBuildTime}))
	switch {
	case checkAlpha:
		// 滚动预发布只能按提交号与构建时间描述候选构建。标记缺失或格式不符时升级器拿不到构建时间，
		// 这里如实说明时间不可得，而不是留空或补一个看起来正常的日期
		if info.LatestBuildTime == "" {
			reporter.Info("%s", l10n.T("Latest alpha build: {{.Commit}} (build time unavailable)", map[string]any{"Commit": info.LatestVersion}))
		} else {
			reporter.Info("%s", l10n.T("Latest alpha build: {{.Commit}} (built at {{.Time}})", map[string]any{"Commit": info.LatestVersion, "Time": info.LatestBuildTime}))
		}
	case info.CurrentComparable:
		reporter.Info("%s", l10n.T("Latest version: {{.Version}}", map[string]any{"Version": info.LatestVersion}))
	default:
		// 本地版本无法比较时不能宣称“最新”：这里给出的只是通道内的最高版本，
		// 是否比本地构建新需要用户自行判断
		reporter.Info("%s", l10n.T("Highest version in the {{.Channel}} channel: {{.Version}}", map[string]any{"Channel": channelLabel, "Version": info.LatestVersion}))
	}

	// 只检查模式到此为止：不触碰可执行文件，也不进入确认环节
	if checkOnly {
		return nil
	}

	// AssumeYes 为真表示宿主已经获得用户授权（例如显式给出 --yes），
	// 非交互环境下无法询问，此时跳过确认而不是让询问失败
	if !forceUpdate && !assumeYes {
		confirmed, confirmErr := reporter.Confirm(l10n.T("Upgrade to {{.Version}}?", map[string]any{"Version": info.LatestVersion}))
		if confirmErr != nil {
			return fmt.Errorf("%s: %w", l10n.T("Failed to read the upgrade confirmation", nil), confirmErr)
		}
		if !confirmed {
			reporter.Info("%s", l10n.T("Upgrade cancelled", nil))
			return nil
		}
	}

	reporter.Info("%s", l10n.T("Starting upgrade...", nil))
	if err := upgrader.Apply(info); err != nil {
		return fmt.Errorf("%s: %w", l10n.T("Upgrade failed", nil), err)
	}

	// 替换完成后当前进程仍运行旧版本，必须明确告知用户何时生效，
	// 避免用户在当前会话中反复执行命令却看不到版本变化
	reporter.Success("%s", l10n.T("Upgrade complete; it takes effect after restarting {{.Program}}", map[string]any{"Program": opts.Program}))
	return nil
}
