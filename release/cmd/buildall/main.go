// Command buildall 按发布清单把当前模块交叉编译成各平台的发布产物
//
// 用法：
//
//	buildall --manifest release.json [--version X] [--build-time T] [--out-dir build] [--package .] [--jobs N] [--root .] [--lang en] [--log-level info]
//
// 它替代的是既有项目 Taskfile 里那十几个 build:* 任务：那些任务把同一段 go build 命令
// 按平台抄了十几遍，增删平台时要人肉同步，漏一处就会漏一个产物。改为由清单驱动后，
// "有哪些平台"只有一处真源（release.json），增删平台只改清单
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/release"
	"github.com/jy-eggroll/eggokit/release/cmd/internal/relcli"
)

// defaultOutDir 是产物输出目录的默认值，与既有项目的习惯保持一致
const defaultOutDir = "build"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是命令的可测入口：返回退出码而不是直接退出，参数与输出流都可注入
func run(args []string, stdout, stderr io.Writer) int {
	// 先把语言定下来再定义 flag，理由见 relcli.PrescanLang
	if err := relcli.InitL10n(relcli.PrescanLang(args)); err != nil {
		return relcli.Fail(stderr, err)
	}

	fs := flag.NewFlagSet("buildall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", l10n.T("Path to the release manifest (e.g. release.json)", nil))
	version := fs.String("version", "", l10n.T("Version to inject into the binary (default: a local timestamp)", nil))
	buildTime := fs.String("build-time", "", l10n.T("Build time to inject (default: the current UTC time)", nil))
	outDir := fs.String("out-dir", defaultOutDir, l10n.T("Output directory for the built assets", nil))
	packagePath := fs.String("package", ".", l10n.T("Package path to build (e.g. . or ./cmd/foo)", nil))
	jobs := fs.Int("jobs", 0, l10n.T("Maximum number of concurrent builds (default: half of the CPU cores)", nil))
	root := fs.String("root", ".", l10n.T("Project root; relative paths are resolved against it", nil))
	lang := fs.String("lang", "", l10n.T("Output language (e.g. zh-CN, en); defaults to en", nil))
	logLevel := fs.String("log-level", "info", l10n.T("Log level: trace, debug, info, warn, or error", nil))
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if err := relcli.InitL10n(*lang); err != nil {
		return relcli.Fail(stderr, err)
	}

	level, ok := parseLogLevel(*logLevel)
	if !ok {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("Unsupported --log-level {{.Level}}; expected one of trace, debug, info, warn, error", map[string]any{"Level": *logLevel})))
	}
	log := newLogger(stdout, stderr, level)

	if strings.TrimSpace(*manifestPath) == "" {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("--manifest is required", nil)))
	}
	if *jobs < 0 {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("Invalid --jobs {{.Jobs}}: must not be negative", map[string]any{"Jobs": *jobs})))
	}

	// 版本与构建时间允许留空，但都要给出可读的缺省值并明确告知用户它是缺省值——
	// 否则用户会以为产物带的是真实发布版本，直到升级器报"版本不可比"才发现
	effectiveVersion := strings.TrimSpace(*version)
	if effectiveVersion == "" {
		// 采用既有实现的时间戳形态，仅用于本地构建，不代表任何发布版本
		effectiveVersion = time.Now().Format("2006-01-02-15-04-05")
		log.Info(l10n.T("No --version given; using local build default {{.Version}} (a timestamp, not a real release version)", map[string]any{"Version": effectiveVersion}))
	}
	effectiveBuildTime := strings.TrimSpace(*buildTime)
	if effectiveBuildTime == "" {
		effectiveBuildTime = time.Now().UTC().Format(time.RFC3339)
		log.Info(l10n.T("No --build-time given; using the current UTC time {{.BuildTime}}", map[string]any{"BuildTime": effectiveBuildTime}))
	}

	m, err := release.Load(relcli.ResolveUnderRoot(*root, *manifestPath))
	if err != nil {
		return relcli.Fail(stderr, err)
	}

	absoluteOutDir := relcli.ResolveUnderRoot(*root, *outDir)
	if err := os.MkdirAll(absoluteOutDir, 0o755); err != nil {
		return relcli.Fail(stderr, fmt.Errorf("%s: %w", l10n.T("Failed to create the output directory", nil), err))
	}

	// 规划阶段与执行阶段分离：产物名与落点先算好，构建阶段只负责执行
	// 这样"资产命名"这部分的正确性可以脱离真实编译被单测覆盖
	plans := planBuilds(m, absoluteOutDir)
	concurrency := resolveConcurrency(*jobs, len(plans))

	log.Info(l10n.T("Building {{.Count}} platform(s) with {{.Jobs}} concurrent job(s)", map[string]any{
		"Count": len(plans),
		"Jobs":  concurrency,
	}))
	log.Info(l10n.T("Output directory: {{.Dir}}", map[string]any{"Dir": absoluteOutDir}))
	// 打印构建的包路径：main 包不在根目录的项目（如 cmd/ 布局）必须显式传 --package，
	// 明确回显它才能让用户确认"构建的到底是不是我想构建的那个包"
	log.Info(l10n.T("Package: {{.Package}}", map[string]any{"Package": *packagePath}))

	startAll := time.Now()
	results := runBuilds(log, plans, m.LDFLAGSPackage, *packagePath, effectiveVersion, effectiveBuildTime, *root, concurrency)
	total := time.Since(startAll)

	// 汇总：逐一列出失败平台而不只是报"有失败"，让调用方一眼知道该重跑哪个平台
	var failed []string
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r.plan.Platform.String())
		}
	}
	if len(failed) > 0 {
		log.Error(l10n.T("{{.Count}} platform(s) failed: {{.List}}", map[string]any{
			"Count": len(failed),
			"List":  strings.Join(failed, ", "),
		}))
		return 1
	}
	log.Info(l10n.T("All {{.Count}} platform(s) built successfully in {{.Total}}", map[string]any{
		"Count": len(results),
		"Total": total.Round(time.Millisecond).String(),
	}))
	return 0
}

// buildPlan 描述一次构建要做什么：平台、产物名、产物落点
type buildPlan struct {
	Platform release.Platform
	Asset    string
	Output   string
}

// planBuilds 把清单翻译成一组构建计划
//
// 产物名完全交给 release.Manifest.AssetName 决定，本函数不重复实现命名规则：
// 命名是升级器与构建之间唯一的契约，只能有一处真源
func planBuilds(m *release.Manifest, outDir string) []buildPlan {
	plans := make([]buildPlan, 0, len(m.Platforms))
	for _, p := range m.Platforms {
		asset := m.AssetName(p)
		plans = append(plans, buildPlan{
			Platform: p,
			Asset:    asset,
			Output:   filepath.Join(outDir, asset),
		})
	}
	return plans
}

// resolveConcurrency 决定并发构建数
//
// 缺省取 CPU 核数的一半：交叉编译是 CPU 密集型，占满所有核心会让同机的其它任务（编辑器、
// 浏览器）明显卡顿，而构建本身并不会因为多占几个核就快多少。--jobs 可显式覆盖，
// 但无论如何不会超过计划数——多于计划数的并发没有任何意义，只是白白占着调度槽
func resolveConcurrency(jobs, plans int) int {
	concurrency := jobs
	if concurrency <= 0 {
		concurrency = runtime.NumCPU() / 2
		if concurrency < 1 {
			concurrency = 1
		}
	}
	if concurrency > plans {
		concurrency = plans
	}
	return concurrency
}

// buildResult 承载单个平台的构建结果，按计划下标写回，避免并发结果乱序
type buildResult struct {
	plan buildPlan
	err  error
}

// runBuilds 以受限并发执行全部构建，并逐个打印耗时与成败
func runBuilds(log *buildLogger, plans []buildPlan, ldflagsPkg, pkg, version, buildTime, root string, concurrency int) []buildResult {
	results := make([]buildResult, len(plans))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, plan := range plans {
		wg.Add(1)
		go func(i int, plan buildPlan) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			log.Info(l10n.T("Building {{.Platform}} -> {{.Asset}}", map[string]any{
				"Platform": plan.Platform.String(),
				"Asset":    plan.Asset,
			}))

			start := time.Now()
			err := buildOne(root, plan.Output, ldflagsPkg, pkg, version, buildTime, plan.Platform)
			elapsed := time.Since(start)
			results[i] = buildResult{plan: plan, err: err}

			if err != nil {
				log.Error(l10n.T("Build failed for {{.Platform}}: {{.Error}}", map[string]any{
					"Platform": plan.Platform.String(),
					"Error":    err.Error(),
				}))
				return
			}
			log.Info(l10n.T("Built {{.Platform}} in {{.Duration}}", map[string]any{
				"Platform": plan.Platform.String(),
				"Duration": elapsed.Round(time.Millisecond).String(),
			}))
		}(i, plan)
	}

	wg.Wait()
	return results
}

// buildOne 构建单个平台
//
// 参数与 flk 等项目的既有任务逐项对齐：关闭 CGO（保证静态链接、可跨发行版运行）、
// -trimpath（去掉本机绝对路径，产物可复现）、-s -w（去符号与调试信息，缩小体积）、
// 以及通过 -X 注入版本与构建时间
//
// pkg 是待构建的包路径，默认 "."（项目根即 main 包）；main 包位于 cmd/ 等子目录的项目
// 必须由调用方显式给出，否则 go build 会在根目录找不到 main 包而失败
func buildOne(root, output, ldflagsPkg, pkg, version, buildTime string, p release.Platform) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflagsString(ldflagsPkg, version, buildTime), "-o", output, pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+p.OS,
		"GOARCH="+p.Arch,
	)
	// GOARM 只在 arm 上设置：给别的架构设它会被 Go 忽略，但显式只在需要时设置更不容易误导
	if p.ARM != "" {
		cmd.Env = append(cmd.Env, "GOARM="+p.ARM)
	}

	// 合并 stdout 与 stderr 一起捕获：编译错误可能出现在任一路，只捕获一路会丢关键信息
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s: %w", l10n.T("Failed to run 'go build' (is the Go toolchain installed?)", nil), err)
		}
		if msg := strings.TrimSpace(combined.String()); msg != "" {
			// 优先回报编译器的原始输出：它是定位问题的唯一有效信息
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// ldflagsString 拼出注入版本信息所用的 -ldflags 取值
//
// 单独成函数以便单测钉死其形态：这段字符串是"版本号真的进到二进制里"的关键，
// 少一个 -X、包路径拼错、缺空格，都不会让构建报错，只会让版本号静默停留在零值
func ldflagsString(ldflagsPkg, version, buildTime string) string {
	return fmt.Sprintf("-s -w -X %s.Version=%s -X %s.BuildTime=%s", ldflagsPkg, version, ldflagsPkg, buildTime)
}

// parseLogLevel 把文本级别转成 slog 级别，额外接受一个比 debug 更细的 trace
func parseLogLevel(text string) (slog.Level, bool) {
	switch text {
	case "trace":
		return slog.LevelDebug - 4, true
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// newLogger 构造分级输出器
func newLogger(stdout, stderr io.Writer, level slog.Level) *buildLogger {
	return &buildLogger{stdout: stdout, stderr: stderr, level: level}
}

// buildLogger 是 buildall 的极简分级输出器
//
// 刻意不复用 eggokit/logger：logger 输出的是 slog 的 key=value 文本，适合诊断与审计；
// 而构建工具的进度提示是面向用户的过程信息，两者受众不同（这条分工在 logger 的包注释里也写明了）
//
// 带锁是因为构建是并发的：多个 goroutine 会同时写同一个输出流，若不串行化，
// 轻则输出交错，重则在测试用的 bytes.Buffer 上直接触发数据竞争
type buildLogger struct {
	mu     sync.Mutex
	stdout io.Writer
	stderr io.Writer
	level  slog.Level
}

// Trace/Debug/Info 输出到 stdout，Warn/Error 输出到 stderr
//
// 分流的意义：正常进度留在 stdout 可被整体重定向进日志文件，
// 警告与错误进 stderr，在管道里能被单独捕获
func (l *buildLogger) Trace(msg string) { l.emit(slog.LevelDebug-4, msg) }
func (l *buildLogger) Debug(msg string) { l.emit(slog.LevelDebug, msg) }
func (l *buildLogger) Info(msg string)  { l.emit(slog.LevelInfo, msg) }
func (l *buildLogger) Warn(msg string)  { l.emit(slog.LevelWarn, msg) }
func (l *buildLogger) Error(msg string) { l.emit(slog.LevelError, msg) }

// emit 按级别过滤后写出；文案已由调用方用 l10n.T 翻译好，这里只加级别前缀与着色
func (l *buildLogger) emit(level slog.Level, msg string) {
	if level < l.level {
		return
	}

	out := l.stdout
	if level >= slog.LevelWarn {
		out = l.stderr
	}

	tag := levelTag(level)
	// 只有直接面向终端时才着色：重定向到文件或管道时保留 ANSI 转义会把日志弄脏
	// （是否终端由输出流自身决定，测试注入的 bytes.Buffer 自然走无色分支）
	if isTerminal(out) {
		tag = colorFor(level) + tag + "\x1b[0m"
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(out, "["+tag+"] "+msg+"\n")
}

// levelTag 返回级别的短标签，与 slog 的名称一致，便于对照
func levelTag(level slog.Level) string {
	switch {
	case level < slog.LevelDebug:
		return "TRACE"
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// colorFor 返回某级别的终端 ANSI 颜色前缀
func colorFor(level slog.Level) string {
	switch {
	case level < slog.LevelDebug:
		return "\x1b[90m" // 灰
	case level < slog.LevelInfo:
		return "\x1b[36m" // 青
	case level < slog.LevelWarn:
		return "\x1b[32m" // 绿
	case level < slog.LevelError:
		return "\x1b[33m" // 黄
	default:
		return "\x1b[31m" // 红
	}
}

// isTerminal 判断输出流是否为字符设备（即终端）
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
