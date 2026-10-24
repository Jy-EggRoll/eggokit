package upgradecli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jy-eggroll/eggokit/updater"
	"github.com/spf13/cobra"
)

// fakeReporter 记录命令层与升级器之间的全部交互，并可控制确认点的结果。
//
// 不直接复用 updater 包内的 recordingReporter：那是未导出类型，而这里要断言的是
// “命令层对用户说了什么”，两者的观测对象不同，耦合只会让两边都不敢改
type fakeReporter struct {
	mu sync.Mutex

	infos      []string
	warnings   []string
	successes  []string
	questions  []string
	progresses int

	confirmAnswer bool
	confirmErr    error
}

func (r *fakeReporter) Info(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, fmt.Sprintf(format, args...))
}

func (r *fakeReporter) Warn(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *fakeReporter) Success(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, fmt.Sprintf(format, args...))
}

func (r *fakeReporter) Confirm(question string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.questions = append(r.questions, question)
	return r.confirmAnswer, r.confirmErr
}

func (r *fakeReporter) Progress(string) updater.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progresses++
	return discardProgress{}
}

type discardProgress struct{}

func (discardProgress) Update(int64, int64) {}

func (discardProgress) Done() {}

// sawInfo 判断是否输出过包含指定子串的过程信息
func (r *fakeReporter) sawInfo(substring string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, info := range r.infos {
		if strings.Contains(info, substring) {
			return true
		}
	}
	return false
}

// sawSuccess 判断是否输出过包含指定子串的成功结论
func (r *fakeReporter) sawSuccess(substring string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, success := range r.successes {
		if strings.Contains(success, substring) {
			return true
		}
	}
	return false
}

// askedQuestions 返回被询问过的问题副本
func (r *fakeReporter) askedQuestions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.questions...)
}

// countingServer 是一个可观察的假 GitHub 接口：分别统计元信息请求与资产下载请求，
// 使“只检查不下载”这类约定可以被真正断言，而不是靠调用方自觉
type countingServer struct {
	*httptest.Server

	mu           sync.Mutex
	releaseCalls int
	assetCalls   int
}

func newCountingServer(t *testing.T, releases []updater.Release, alpha *updater.Release, status int) *countingServer {
	t.Helper()

	s := &countingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		isTag := strings.Contains(r.URL.Path, "/releases/tags/")
		if isTag {
			s.releaseCalls++
		} else if strings.HasSuffix(r.URL.Path, "/releases") {
			s.releaseCalls++
		} else {
			s.assetCalls++
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			if status != http.StatusOK || alpha == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(alpha)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(status)
			if status != http.StatusOK {
				return
			}
			_ = json.NewEncoder(w).Encode(releases)
		default:
			// 资产下载一律 404：测试不触碰可执行文件，只验证命令层是否走到下载这一步
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)

	return s
}

func (s *countingServer) calls() (release, asset int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseCalls, s.assetCalls
}

// testTarget 声明测试平台集合，覆盖当前测试进程所在的平台，使 Check 能匹配到资产
func testTarget() Target {
	return Target{
		Owner:       "owner",
		Repo:        "repo",
		AssetPrefix: "tool",
		Platforms: map[string]map[string]bool{
			"linux":   {"amd64": true, "arm64": true},
			"darwin":  {"amd64": true, "arm64": true},
			"windows": {"amd64": true},
		},
		UserAgent: "tool-updater",
	}
}

// assetNameForTest 返回当前进程平台对应的测试资产名
func assetNameForTest(t *testing.T) string {
	t.Helper()
	name, ok := testTarget().AssetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Skipf("测试平台 %s/%s 不在测试目标的发布集合内", runtime.GOOS, runtime.GOARCH)
	}
	return name
}

// stableReleases 构造一条高于本地版本的正式版发布
func stableReleases(t *testing.T, tag string) []updater.Release {
	t.Helper()
	name := assetNameForTest(t)
	return []updater.Release{{
		TagName: tag,
		Assets:  []updater.Asset{{Name: name, DownloadURL: "https://example.invalid/" + name}},
	}}
}

// newTestCommand 构造指向假接口的升级命令
func newTestCommand(t *testing.T, server *countingServer, reporter *fakeReporter, version string, mutate func(*Options)) *Options {
	t.Helper()

	opts := Options{
		Program:       "tool",
		Target:        testTarget(),
		Version:       func() string { return version },
		BuildTime:     func() string { return "2026-01-01T00:00:00Z" },
		Reporter:      func(*cobra.Command) updater.Reporter { return reporter },
		TokenProvider: func() string { return "test-token" },
		apiEndpoint:   server.URL,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return &opts
}

// runCommand 执行升级命令并返回其错误，输出丢弃以免污染测试日志
func runCommand(t *testing.T, opts *Options, args ...string) error {
	t.Helper()

	c := NewCommand(*opts)
	c.SetArgs(args)
	c.SetOut(new(strings.Builder))
	c.SetErr(new(strings.Builder))
	return c.Execute()
}

// TestCheckOnlyDoesNotDownloadOrConfirm 断言 --check 只查询元信息：
// 既不下载资产，也不进入确认环节
func TestCheckOnlyDoesNotDownloadOrConfirm(t *testing.T) {
	server := newCountingServer(t, stableReleases(t, "v9.9.9"), nil, http.StatusOK)
	reporter := &fakeReporter{confirmAnswer: true}
	opts := newTestCommand(t, server, reporter, "1.0.0", nil)

	if err := runCommand(t, opts, "--check"); err != nil {
		t.Fatalf("--check 不应失败: %v", err)
	}

	releaseCalls, assetCalls := server.calls()
	if releaseCalls == 0 {
		t.Fatal("--check 应至少查询一次 Release 元信息")
	}
	if assetCalls != 0 {
		t.Fatalf("--check 不应下载资产，实际下载请求 %d 次", assetCalls)
	}
	if questions := reporter.askedQuestions(); len(questions) != 0 {
		t.Fatalf("--check 不应询问确认，实际询问 %q", questions)
	}
	if !reporter.sawInfo("Latest version: v9.9.9") {
		t.Fatalf("--check 应展示候选版本，实际输出 %q", reporter.infos)
	}
	if reporter.progresses != 0 {
		t.Fatalf("--check 不应开启进度展示，实际 %d 次", reporter.progresses)
	}
}

// TestNoCandidateMessages 断言没有候选时按通道给出不同提示：
// 正式版通道说“已是最新版本”，alpha 通道说“已是 alpha 通道最新构建”
func TestNoCandidateMessages(t *testing.T) {
	t.Run("stable", func(t *testing.T) {
		// 只有开发版标签，正式版通道下没有候选
		releases := []updater.Release{{TagName: "v9.9.9-dev.1", Assets: stableReleases(t, "x")[0].Assets}}
		server := newCountingServer(t, releases, nil, http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, "1.0.0", nil)

		if err := runCommand(t, opts, "--check"); err != nil {
			t.Fatalf("没有候选不应失败: %v", err)
		}
		if !reporter.sawSuccess("Already on the latest version") {
			t.Fatalf("正式版通道应提示已是最新版本，实际 %q", reporter.successes)
		}
		if reporter.sawSuccess("alpha") {
			t.Fatalf("正式版通道不应提及 alpha，实际 %q", reporter.successes)
		}
	})

	t.Run("alpha", func(t *testing.T) {
		// alpha 通道返回与本地相同的提交，检查结果为“没有更新”
		commit := strings.Repeat("a", 40)
		server := newCountingServer(t, nil, alphaRelease(t, commit), http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, commit[:7], nil)

		if err := runCommand(t, opts, "--check", "--alpha"); err != nil {
			t.Fatalf("没有候选不应失败: %v", err)
		}
		if !reporter.sawSuccess("Already on the latest alpha build") {
			t.Fatalf("alpha 通道应提示已是最新构建，实际 %q", reporter.successes)
		}
	})
}

// alphaRelease 构造一条带构建标记的滚动预发布
func alphaRelease(t *testing.T, commit string) *updater.Release {
	t.Helper()
	name := assetNameForTest(t)
	return &updater.Release{
		TagName: "alpha",
		Body:    fmt.Sprintf("<!-- alpha-build: %s 2026-02-03T04:05:06Z -->\n", commit),
		Assets:  []updater.Asset{{Name: name, DownloadURL: "https://example.invalid/" + name}},
	}
}

// TestAlphaChannelMessages 覆盖 alpha 通道的四种情形：
// 可比较、不可比较、标记缺失、接口 404
func TestAlphaChannelMessages(t *testing.T) {
	latestCommit := strings.Repeat("b", 40)

	t.Run("comparable", func(t *testing.T) {
		// 本地是另一个提交的短号，可以与候选比较
		server := newCountingServer(t, nil, alphaRelease(t, latestCommit), http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, strings.Repeat("c", 7), nil)

		if err := runCommand(t, opts, "--check", "--alpha"); err != nil {
			t.Fatalf("alpha 检查不应失败: %v", err)
		}
		want := fmt.Sprintf("Latest alpha build: %s (built at 2026-02-03T04:05:06Z)", latestCommit[:7])
		if !reporter.sawInfo(want) {
			t.Fatalf("应展示短提交号与构建时间，实际输出 %q", reporter.infos)
		}
	})

	t.Run("not comparable", func(t *testing.T) {
		// 本地是正式版本号，无法与提交号比较，但仍应展示候选构建
		server := newCountingServer(t, nil, alphaRelease(t, latestCommit), http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, "1.0.0", nil)

		if err := runCommand(t, opts, "--check", "--alpha"); err != nil {
			t.Fatalf("alpha 检查不应失败: %v", err)
		}
		if !reporter.sawInfo("Latest alpha build: " + latestCommit[:7]) {
			t.Fatalf("不可比较时仍应展示候选构建，实际输出 %q", reporter.infos)
		}
	})

	t.Run("marker missing", func(t *testing.T) {
		// 正文没有构建标记：拿不到提交号与时间，应如实说明构建时间不可用
		release := alphaRelease(t, latestCommit)
		release.Body = "no marker here\n"
		server := newCountingServer(t, nil, release, http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, "1.0.0", nil)

		if err := runCommand(t, opts, "--check", "--alpha"); err != nil {
			t.Fatalf("标记缺失不应让检查失败: %v", err)
		}
		if !reporter.sawInfo("build time unavailable") {
			t.Fatalf("标记缺失时应说明构建时间不可用，实际输出 %q", reporter.infos)
		}
		if len(reporter.warnings) == 0 {
			t.Fatal("标记缺失时应由升级器给出告警")
		}
	})

	t.Run("not found", func(t *testing.T) {
		server := newCountingServer(t, nil, nil, http.StatusOK)
		reporter := &fakeReporter{confirmAnswer: true}
		opts := newTestCommand(t, server, reporter, "1.0.0", nil)

		err := runCommand(t, opts, "--check", "--alpha")
		if err == nil {
			t.Fatal("alpha 标签不存在时应返回错误")
		}
		if !strings.Contains(err.Error(), "Failed to check for updates") {
			t.Fatalf("错误应带上下文，实际 %q", err)
		}
	})
}

// TestRejectsDevAndAlphaTogether 断言 --dev 与 --alpha 同时出现会被拒
func TestRejectsDevAndAlphaTogether(t *testing.T) {
	server := newCountingServer(t, stableReleases(t, "v9.9.9"), nil, http.StatusOK)
	reporter := &fakeReporter{confirmAnswer: true}
	opts := newTestCommand(t, server, reporter, "1.0.0", nil)

	err := runCommand(t, opts, "--dev", "--alpha")
	if err == nil {
		t.Fatal("同时指定 --dev 与 --alpha 应被拒绝")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("错误应点明冲突的两个参数，实际 %q", err)
	}
	if releaseCalls, _ := server.calls(); releaseCalls != 0 {
		t.Fatalf("参数冲突时不应发出任何网络请求，实际 %d 次", releaseCalls)
	}
}

// TestAssumeYesSkipsConfirmation 断言 AssumeYes 为真时不再询问确认
func TestAssumeYesSkipsConfirmation(t *testing.T) {
	server := newCountingServer(t, stableReleases(t, "v9.9.9"), nil, http.StatusOK)
	reporter := &fakeReporter{confirmAnswer: true}
	opts := newTestCommand(t, server, reporter, "1.0.0", func(o *Options) {
		o.AssumeYes = func() bool { return true }
	})

	// 资产下载被假接口拒绝，因此命令以失败结束；这里只关心“有没有问过用户”
	_ = runCommand(t, opts, "upgrade")
	if questions := reporter.askedQuestions(); len(questions) != 0 {
		t.Fatalf("AssumeYes 为真时不应询问确认，实际询问 %q", questions)
	}
	if reporter.progresses == 0 {
		t.Fatal("跳过确认后应直接进入升级流程")
	}
}

// TestAutoYesReporterConfirmsEverything 断言 --yes 的装饰层覆盖升级器内部的其它确认点。
//
// 直连下载失败后“是否换用代理”的询问发生在 updater 内部，不经过命令层的跳过判断。
// 若只在那里判断 --yes，用户会看到“请加 --yes 后重试”，而他明明已经加了——实测遇到过。
// 这里直接断言装饰层：内层无论返回什么，一律视为同意，且问题不会被转给内层
func TestAutoYesReporterConfirmsEverything(t *testing.T) {
	inner := &fakeReporter{confirmErr: errors.New("非交互环境下无法确认")}
	wrapped := autoYesReporter{Reporter: inner}

	confirmed, err := wrapped.Confirm("直连下载失败，是否切换到代理下载？")
	if err != nil {
		t.Fatalf("--yes 下确认不应失败，实际 %v", err)
	}
	if !confirmed {
		t.Fatal("--yes 下确认应返回同意")
	}
	if questions := inner.askedQuestions(); len(questions) != 0 {
		t.Fatalf("装饰层不应把问题转给内层，实际 %q", questions)
	}
}

// TestConfirmErrorAborts 断言确认环节出错时流程中止，且不会进入升级
func TestConfirmErrorAborts(t *testing.T) {
	server := newCountingServer(t, stableReleases(t, "v9.9.9"), nil, http.StatusOK)
	reporter := &fakeReporter{confirmAnswer: true, confirmErr: errors.New("终端不可交互")}
	opts := newTestCommand(t, server, reporter, "1.0.0", nil)

	err := runCommand(t, opts)
	if err == nil {
		t.Fatal("确认失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "Failed to read the upgrade confirmation") {
		t.Fatalf("错误应带上下文，实际 %q", err)
	}
	if _, assetCalls := server.calls(); assetCalls != 0 {
		t.Fatalf("确认失败后不应继续下载，实际下载请求 %d 次", assetCalls)
	}
	if reporter.progresses != 0 {
		t.Fatalf("确认失败后不应进入升级流程，实际开启进度 %d 次", reporter.progresses)
	}
}

// TestAssetNameAndPlatformLabel 校验资产命名与平台标签这两处纯函数的边界
func TestAssetNameAndPlatformLabel(t *testing.T) {
	target := testTarget()

	if name, ok := target.AssetName("windows", "amd64"); !ok || name != "tool-windows-amd64.exe" {
		t.Fatalf("windows 资产名应带 .exe 扩展名，实际 %q（ok=%v）", name, ok)
	}
	if name, ok := target.AssetName("linux", "amd64"); !ok || name != "tool-linux-amd64" {
		t.Fatalf("linux 资产名不应带扩展名，实际 %q（ok=%v）", name, ok)
	}
	if _, ok := target.AssetName("linux", "ppc64"); ok {
		t.Fatal("未声明的架构应返回 false")
	}
	if _, ok := target.AssetName("plan9", "amd64"); ok {
		t.Fatal("未声明的系统应返回 false")
	}

	label := PlatformLabel()
	if !strings.HasPrefix(label, runtime.GOOS+"-"+runtime.GOARCH) {
		t.Fatalf("平台标签应以 GOOS-GOARCH 开头，实际 %q", label)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(label, " (exe)") {
		t.Fatalf("windows 平台标签应带 (exe)，实际 %q", label)
	}
}

// TestCommandShape 断言命令的对外形态：名称、别名与参数集合
func TestCommandShape(t *testing.T) {
	server := newCountingServer(t, nil, nil, http.StatusOK)
	opts := newTestCommand(t, server, &fakeReporter{confirmAnswer: true}, "1.0.0", nil)

	c := NewCommand(*opts)
	if c.Use != "upgrade" {
		t.Fatalf("Use = %q，期望 upgrade", c.Use)
	}
	if strings.Join(c.Aliases, ",") != "update,up" {
		t.Fatalf("Aliases = %v，期望 update 与 up", c.Aliases)
	}
	for _, name := range []string{"check", "force", "dev", "alpha"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatalf("应注册 --%s 参数", name)
		}
	}
	// --yes 由宿主注册，共享命令不得自带，否则会与宿主的定义冲突或改变其作用域
	if c.Flags().Lookup("yes") != nil {
		t.Fatal("共享命令不应注册 --yes，该参数的作用域由宿主决定")
	}
}

// TestReporterReceivesCommand 断言界面回调拿到的是正在执行的命令，
// 宿主可以据此用 cmd.ErrOrStderr() 作为输出目标：测试替换了错误流，
// 回调必须看到同一个流，否则宿主只能退回 os.Stderr 而破坏可测性
func TestReporterReceivesCommand(t *testing.T) {
	server := newCountingServer(t, stableReleases(t, "v9.9.9"), nil, http.StatusOK)
	reporter := &fakeReporter{confirmAnswer: true}
	opts := newTestCommand(t, server, reporter, "1.0.0", nil)

	var gotErrStream, wantErrStream any
	opts.Reporter = func(cmd *cobra.Command) updater.Reporter {
		gotErrStream = cmd.ErrOrStderr()
		return reporter
	}

	c := NewCommand(*opts)
	errStream := new(strings.Builder)
	c.SetOut(new(strings.Builder))
	c.SetErr(errStream)
	c.SetArgs([]string{"--check"})
	wantErrStream = errStream

	if err := c.Execute(); err != nil {
		t.Fatalf("--check 不应失败: %v", err)
	}
	if gotErrStream != wantErrStream {
		t.Fatalf("界面回调应拿到命令配置的错误流，实际 %v，期望 %v", gotErrStream, wantErrStream)
	}
}
