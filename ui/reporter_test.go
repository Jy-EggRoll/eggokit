package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// fixedClock 让进度用例用虚拟时间推进，既不真实等待，也能精确覆盖 100ms 节流边界。
type fixedClock struct {
	now time.Time
}

func (c *fixedClock) Now() time.Time { return c.now }

func (c *fixedClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// newTestReporter 返回写入 buf 的上报器与可推进的时钟，避免用例依赖真实终端。
func newTestReporter(buf *bytes.Buffer) (*Reporter, *fixedClock) {
	clock := &fixedClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	reporter := NewReporter(buf)
	reporter.now = clock.Now
	return reporter, clock
}

// TestReporterWritesToProvidedWriter 验证三类状态输出都落到构造时指定的流，而不是进程的标准输出。
func TestReporterWritesToProvidedWriter(t *testing.T) {
	var buf bytes.Buffer
	reporter, _ := newTestReporter(&buf)

	reporter.Info("阶段 %s", "下载")
	reporter.Warn("警告 %d", 1)
	reporter.Success("完成")

	output := buf.String()
	for _, want := range []string{"阶段 下载", "警告 1", "完成"} {
		if !strings.Contains(output, want) {
			t.Fatalf("输出未包含 %q，实际为 %q", want, output)
		}
	}
}

// TestProgressThrottlesRedraws 验证同一时间窗内只绘制一次，而传输完成时不受节流限制必须绘制。
func TestProgressThrottlesRedraws(t *testing.T) {
	var buf bytes.Buffer
	reporter, clock := newTestReporter(&buf)
	progress := reporter.Progress("下载").(*progress)

	progress.Update(1, 10)
	if frames := countFrames(buf.String()); frames != 1 {
		t.Fatalf("首帧后帧数 = %d，期望 1", frames)
	}

	progress.Update(2, 10)
	clock.Advance(50 * time.Millisecond)
	progress.Update(3, 10)
	if frames := countFrames(buf.String()); frames != 1 {
		t.Fatalf("节流窗口内的帧数 = %d，期望仍为 1", frames)
	}

	clock.Advance(60 * time.Millisecond)
	progress.Update(4, 10)
	if frames := countFrames(buf.String()); frames != 2 {
		t.Fatalf("超过节流窗口后的帧数 = %d，期望 2", frames)
	}

	progress.Update(10, 10)
	if frames := countFrames(buf.String()); frames != 3 {
		t.Fatalf("完成帧不受节流限制，帧数 = %d，期望 3", frames)
	}
}

// TestProgressDoneAddsNewline 验证收尾时补一个换行，且从未绘制过进度时不留下空行。
func TestProgressDoneAddsNewline(t *testing.T) {
	var buf bytes.Buffer
	reporter, _ := newTestReporter(&buf)

	drawn := reporter.Progress("下载").(*progress)
	drawn.Update(5, 10)
	drawn.Done()
	if output := buf.String(); !strings.HasSuffix(output, "\n") || strings.HasSuffix(output, "\n\n") {
		t.Fatalf("Done 后应恰好追加一个换行，实际为 %q", output)
	}

	var unknownBuf bytes.Buffer
	unknownReporter, _ := newTestReporter(&unknownBuf)
	unknown := unknownReporter.Progress("下载").(*progress)
	unknown.Update(5, 0)
	unknown.Done()
	if output := unknownBuf.String(); output != "" {
		t.Fatalf("总长未知时不应绘制进度，实际为 %q", output)
	}
}

// TestProgressNeverShowsFullPercentBeforeCompletion 验证未收到最后一块数据前不会显示 100.0%。
func TestProgressNeverShowsFullPercentBeforeCompletion(t *testing.T) {
	var buf bytes.Buffer
	reporter, _ := newTestReporter(&buf)
	progress := reporter.Progress("下载").(*progress)

	progress.Update(9999, 10000)

	output := buf.String()
	if strings.Contains(output, "100.0%") {
		t.Fatalf("未完成时不应显示 100.0%%，实际为 %q", output)
	}
	if !strings.Contains(output, "99.9%") {
		t.Fatalf("接近完成时应显示 99.9%%，实际为 %q", output)
	}
}

// TestProgressCompletionFrameOmitsSpeedAndRemaining 验证完成帧不再输出速率与剩余时间。
func TestProgressCompletionFrameOmitsSpeedAndRemaining(t *testing.T) {
	var buf bytes.Buffer
	reporter, clock := newTestReporter(&buf)
	progress := reporter.Progress("下载").(*progress)

	progress.Update(1, 10)
	clock.Advance(time.Second)
	progress.Update(10, 10)

	frames := strings.Split(buf.String(), "\r\x1b[K")
	last := frames[len(frames)-1]
	if !strings.Contains(last, "100.0%") {
		t.Fatalf("完成帧应显示 100.0%%，实际为 %q", last)
	}
	if strings.Contains(last, "/s") || strings.Contains(last, "remaining") {
		t.Fatalf("完成帧不应包含速率或剩余时间，实际为 %q", last)
	}
}

// TestConfirmRejectsNonInteractiveInput 验证非终端输入时返回错误而不是挂起，且错误文案提示改用 --yes。
func TestConfirmRejectsNonInteractiveInput(t *testing.T) {
	var buf bytes.Buffer
	reporter, _ := newTestReporter(&buf)
	reporter.stdinIsTerminal = func() bool { return false }
	reporter.prompt = func(string) (bool, error) {
		t.Fatal("非交互环境不应进入交互询问")
		return false, nil
	}

	confirmed, err := reporter.Confirm("继续吗")
	if confirmed {
		t.Fatal("非交互环境不应返回同意")
	}
	if err == nil {
		t.Fatal("非交互环境应返回错误")
	}
	if want := "Cannot ask for confirmation in a non-interactive environment; re-run with --yes"; err.Error() != want {
		t.Fatalf("错误文案 = %q，期望 %q", err.Error(), want)
	}
}

// TestConfirmPausesProgressDuringPrompt 验证询问期间进度停止绘制，返回后恢复绘制能力。
func TestConfirmPausesProgressDuringPrompt(t *testing.T) {
	var buf bytes.Buffer
	reporter, clock := newTestReporter(&buf)
	reporter.stdinIsTerminal = func() bool { return true }
	progress := reporter.Progress("下载").(*progress)

	progress.Update(1, 10)
	if frames := countFrames(buf.String()); frames != 1 {
		t.Fatalf("询问前的帧数 = %d，期望 1", frames)
	}

	reporter.prompt = func(string) (bool, error) {
		if !reporter.paused.Load() {
			t.Fatal("询问期间应置位暂停标志")
		}
		progress.Update(5, 10)
		return true, nil
	}

	confirmed, err := reporter.Confirm("继续吗")
	if err != nil || !confirmed {
		t.Fatalf("交互询问应返回同意，实际 confirmed = %v, err = %v", confirmed, err)
	}
	if reporter.paused.Load() {
		t.Fatal("询问返回后应复位暂停标志")
	}
	if frames := countFrames(buf.String()); frames != 1 {
		t.Fatalf("暂停期间的帧数 = %d，期望仍为 1", frames)
	}

	clock.Advance(2 * progressRefreshInterval)
	progress.Update(6, 10)
	if frames := countFrames(buf.String()); frames != 2 {
		t.Fatalf("询问结束后的帧数 = %d，期望 2", frames)
	}
}

func countFrames(output string) int {
	return strings.Count(output, "\r\x1b[K")
}
