// Package ui 把命令行工具共用的终端界面收敛成一份实现。
//
// 升级命令与 version 命令原先在 flk、ggt 里各写一份 pterm 适配代码，改一处文案或刷新节奏就要改多个仓库；本包把这两块独立出来，宿主只负责注入程序名、版本来源与输出流。
// 升级逻辑本身仍不依赖任何终端库（见 updater 的包注释），本包只是 updater.Reporter 的一个 pterm 实现，界面与流程的分离边界因此保持不变。
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/updater"
	"github.com/pterm/pterm"
	"golang.org/x/term"
)

// progressRefreshInterval 是进度重绘的最小间隔。
// 每收到一块数据都重绘会让终端闪烁，也会让速率与剩余时间的读数跳动到无法阅读。
const progressRefreshInterval = 100 * time.Millisecond

// Reporter 是升级器与终端之间的适配层，用 pterm 实现全部用户可见输出与交互。
//
// 状态输出与进度行都写入构造时传入的 writer，由宿主决定是 stderr 还是别的流，保证 stdout 只承载业务数据；交互询问受 pterm 组件限制写入 pterm 的全局输出（原因见 Confirm）。
// 进度绘制与交互确认共享同一个暂停标志，确认期间进度刷新自动停止，问题文本不会被进度行的回车覆盖。
type Reporter struct {
	writer io.Writer

	// paused 在交互确认期间置位，使仍在下载 goroutine 中运行的进度刷新停止绘制。
	// 读写分别发生在下载 goroutine 与命令主 goroutine 上，因此必须用原子类型而不是普通布尔。
	paused atomic.Bool

	// now 是时间来源，默认 time.Now；测试替换它来推进虚拟时钟，从而在不真实等待的前提下覆盖节流与速率计算。
	now func() time.Time

	// stdinIsTerminal 判断标准输入是否可交互，默认用 golang.org/x/term 检查 os.Stdin。
	// 抽成字段而不是直接调用包级函数，是为了让测试注入确定结论，避免依赖运行测试的环境是否连着终端。
	stdinIsTerminal func() bool

	// prompt 执行真正的交互询问，默认走 pterm 的确认组件。
	// 抽成字段的用途是让测试在无终端环境下验证「询问期间暂停进度」的时序，而不是伪造一个终端。
	prompt func(question string) (bool, error)
}

// NewReporter 创建写入 w 的终端上报器。
func NewReporter(w io.Writer) *Reporter {
	return &Reporter{
		writer:          w,
		now:             time.Now,
		stdinIsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		prompt: func(question string) (bool, error) {
			return pterm.DefaultInteractiveConfirm.WithDefaultValue(false).WithDefaultText(question).Show()
		},
	}
}

// Info 输出过程性状态，统一带 pterm 的 Info 前缀以便与业务数据区分。
func (r *Reporter) Info(format string, args ...any) {
	pterm.Info.WithWriter(r.writer).Printfln(format, args...)
}

// Warn 输出需要用户知晓但不阻断流程的问题。
func (r *Reporter) Warn(format string, args ...any) {
	pterm.Warning.WithWriter(r.writer).Printfln(format, args...)
}

// Success 输出成功结论。
func (r *Reporter) Success(format string, args ...any) {
	pterm.Success.WithWriter(r.writer).Printfln(format, args...)
}

// Confirm 提出一个问题并等待用户决定，返回 false 表示用户拒绝。
//
// --yes 的跳过逻辑由 upgradecli 负责，启用该开关时根本不会走到这里，因此本方法不再判断它。
// 非交互环境下 pterm 的确认会读到 EOF 或直接挂住，所以先判断标准输入是否为终端；不可交互时返回错误，让升级器走「用户未能确认」的保守分支，而不是把命令永久阻塞在等待输入上。
// 暂停标志在弹出前完成置位、在返回后复位，保证问题文本与进度行不会争用同一片终端区域。
// 交互组件本身只能写入 pterm 的全局输出（该组件没有按实例指定 writer 的入口），宿主应把自己的 stderr 设为 pterm 的默认输出，flk 与 ggt 在根命令里就是这么做的。
func (r *Reporter) Confirm(question string) (bool, error) {
	if !r.stdinIsTerminal() {
		return false, errors.New(l10n.T("Cannot ask for confirmation in a non-interactive environment; re-run with --yes", nil))
	}

	r.paused.Store(true)
	defer r.paused.Store(false)

	return r.prompt(question)
}

// Progress 开启一次下载进度展示。
func (r *Reporter) Progress(label string) updater.Progress {
	return &progress{reporter: r, label: label}
}

// progress 用就地覆盖的单行文本展示下载进度、已传输量与剩余时间。
//
// 这里刻意不使用 pterm 自带的进度条组件：它会自行接管光标与刷新节奏，与确认弹窗同时出现时会争夺同一片终端区域，而此处的绘制完全由 Reporter 的暂停标志统一调度。
type progress struct {
	reporter *Reporter
	label    string

	// drawn 记录是否真正绘制过：总长未知时不会绘制，结束时也就无需补换行。
	drawn atomic.Bool

	// mu 串行化节流字段的读写：下载 goroutine 在被取消的瞬间仍可能刷新一次进度，而主流程此时可能已在启动新的下载。
	mu          sync.Mutex
	startedAt   time.Time
	lastDrawnAt time.Time
}

// Update 汇报当前进度，total 小于等于 0 表示总长未知，此时只记录而不绘制。
func (p *progress) Update(done, total int64) {
	if total <= 0 || p.reporter.paused.Load() {
		return
	}

	now := p.reporter.now()

	p.mu.Lock()
	if p.startedAt.IsZero() {
		p.startedAt = now
	}
	// 传输完成时必须强制绘制一次：否则受节流影响进度会停在 99% 之类的数字上，用户会以为下载没有收尾。
	if done < total && now.Sub(p.lastDrawnAt) < progressRefreshInterval {
		p.mu.Unlock()
		return
	}
	p.lastDrawnAt = now
	startedAt := p.startedAt
	p.mu.Unlock()

	percent := float64(done) / float64(total) * 100
	// 最后一块数据到达前不能显示 100.0%：浮点除法会把 99.99% 四舍五入成 100.0%，让用户以为下载已经收尾。
	// 判断依据必须是保留一位小数后的显示值，而不是原始值是否达到 100：99.95% 以上的值在 %.1f 下同样会显示成 100.0%，只拦住 >= 100 的原始值并不够。
	if done < total && percent >= 99.95 {
		percent = 99.9
	}

	// 每帧以「回车 + 清到行尾」开头，而不是只回车（ANSI 的 EL 序列 \x1b[K）。
	//
	// 为什么必须清行：下载完成的那一帧不再输出速率与剩余时间（见下面的 done < total 判断），整帧会明显短于上一帧；只回车的覆盖写会把上一帧多出来的尾巴留在行尾，用户看到的就是「剩余 3s2s」这种由两帧叠加出来的伪内容。帧长变化不止发生在收尾：速率、剩余时间与百分比的字符数都会随进度变化。
	// 不支持 ANSI 的终端会把 \x1b[K 当普通字符显示，但这类终端连进度条本身也渲染不对（\r 就地覆盖的表现同样不可靠）；项目已经依赖 pterm，它本身就在用 ANSI 序列，因此这里不额外做终端能力探测。
	line := "\r\x1b[K" + fmt.Sprintf("%s: %.1f%% (%s/%s)", p.label, percent, updater.FormatSize(done), updater.FormatSize(total))

	// 仅在未完成时给出速率与剩余时间，完成时这两项已无意义。
	if done < total {
		// 速率取整体平均值而非瞬时值：瞬时值在抖动网络下会让剩余时间剧烈跳动。
		if elapsed := now.Sub(startedAt); elapsed > 0 {
			if speed := float64(done) / elapsed.Seconds(); speed > 0 {
				line += fmt.Sprintf("  %s/s", updater.FormatSize(int64(speed)))
				// 不足一秒的剩余时间显示出来只是噪音，反而让人以为卡住了。
				if remaining := time.Duration(float64(total-done) / speed * float64(time.Second)); remaining >= time.Second {
					line += "  " + l10n.T("remaining {{.Time}}", map[string]any{"Time": updater.FormatDuration(remaining)})
				}
			}
		}
	}

	_, _ = fmt.Fprint(p.reporter.writer, line)
	p.drawn.Store(true)
}

// Done 在绘制过进度后补一个换行，使后续输出从新行开始。
func (p *progress) Done() {
	if p.drawn.Load() {
		_, _ = fmt.Fprintln(p.reporter.writer)
	}
}
