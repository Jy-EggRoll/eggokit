package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jy-eggroll/eggokit/release"
)

// TestPlanBuilds 锁定"清单 -> 产物名与落点"这一步
// 产物名必须与升级器匹配的命名完全一致，因此在脱离真实编译的层面单独固定
func TestPlanBuilds(t *testing.T) {
	m := &release.Manifest{
		App:         "ggt",
		AssetPrefix: "ggt",
		Platforms: []release.Platform{
			{OS: "windows", Arch: "amd64"},
			{OS: "linux", Arch: "arm", ARM: "7"},
		},
	}

	plans := planBuilds(m, filepath.Join("/out"))
	if len(plans) != 2 {
		t.Fatalf("计划数 = %d，期望 2", len(plans))
	}

	// windows 产物带 .exe 后缀，落点由 outDir 与产物名拼出
	if plans[0].Asset != "ggt-windows-amd64.exe" {
		t.Fatalf("windows 产物名 = %q", plans[0].Asset)
	}
	if plans[0].Output != filepath.Join("/out", "ggt-windows-amd64.exe") {
		t.Fatalf("windows 落点 = %q", plans[0].Output)
	}
	// linux/arm 的产物名不含 GOARM
	if plans[1].Asset != "ggt-linux-arm" {
		t.Fatalf("linux/arm 产物名 = %q", plans[1].Asset)
	}
}

// TestLdflagsString 锁定注入版本信息的 -ldflags 形态
// 拼错不会让构建失败，只会让版本号静默为零值，因此必须用测试固定下来
func TestLdflagsString(t *testing.T) {
	got := ldflagsString("github.com/jy-eggroll/flk/cmd", "1.2.3", "2026-01-02T03:04:05Z")
	want := "-s -w -X github.com/jy-eggroll/flk/cmd.Version=1.2.3 -X github.com/jy-eggroll/flk/cmd.BuildTime=2026-01-02T03:04:05Z"
	if got != want {
		t.Fatalf("ldflags = %q，期望 %q", got, want)
	}
}

// TestResolveConcurrency 覆盖并发度的缺省与上限
func TestResolveConcurrency(t *testing.T) {
	halfCPU := runtime.NumCPU() / 2
	if halfCPU < 1 {
		halfCPU = 1
	}

	cases := []struct {
		name  string
		jobs  int
		plans int
		want  int
	}{
		{name: "显式指定按指定值", jobs: 2, plans: 5, want: 2},
		{name: "指定值超过计划数时被收敛到计划数", jobs: 10, plans: 3, want: 3},
		{name: "指定 1 时只用 1 个", jobs: 1, plans: 5, want: 1},
		{name: "缺省取 CPU 核数一半（计划数足够大时）", jobs: 0, plans: 100000, want: halfCPU},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveConcurrency(tc.jobs, tc.plans); got != tc.want {
				t.Fatalf("resolveConcurrency(%d, %d) = %d，期望 %d", tc.jobs, tc.plans, got, tc.want)
			}
		})
	}

	t.Run("计划数少于缺省并发时被收敛", func(t *testing.T) {
		if got := resolveConcurrency(0, 1); got != 1 {
			t.Fatalf("resolveConcurrency(0, 1) = %d，期望 1", got)
		}
	})
}

// TestParseLogLevel 覆盖级别文本解析，含比 debug 更细的 trace
func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		text string
		ok   bool
	}{
		{"trace", true},
		{"debug", true},
		{"info", true},
		{"warn", true},
		{"error", true},
		{"verbose", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			level, ok := parseLogLevel(tc.text)
			if ok != tc.ok {
				t.Fatalf("parseLogLevel(%q) ok = %v，期望 %v", tc.text, ok, tc.ok)
			}
			if tc.text == "trace" && level >= slog.LevelDebug {
				t.Fatalf("trace 应低于 debug，实际 %v", level)
			}
		})
	}
}

// TestLoggerLevelsAndStreams 验证分级过滤与 stdout/stderr 分流
// 两个流都用 bytes.Buffer，因此这里同样覆盖了"注入非终端时不着色"的分支
func TestLoggerLevelsAndStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	log := newLogger(&stdout, &stderr, slog.LevelInfo)

	log.Debug("debug line") // 低于阈值，应被过滤
	log.Info("info line")   // 进 stdout
	log.Warn("warn line")   // 进 stderr
	log.Error("error line") // 进 stderr

	if strings.Contains(stdout.String(), "debug line") {
		t.Fatalf("debug 应被过滤：%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "info line") {
		t.Fatalf("info 应进 stdout：%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "info line") {
		t.Fatalf("info 不应进 stderr：%s", stderr.String())
	}
	for _, want := range []string{"warn line", "error line"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr 缺少 %q：%s", want, stderr.String())
		}
	}
	// 非终端不得出现 ANSI 转义
	if strings.Contains(stdout.String(), "\x1b[") || strings.Contains(stderr.String(), "\x1b[") {
		t.Fatalf("非终端输出不应着色")
	}
}

// TestRunFlagErrors 覆盖参数解析与校验这些可离线断言的路径
func TestRunFlagErrors(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "缺少 manifest", args: []string{}, wantCode: 1, wantErr: "--manifest"},
		{name: "非法日志级别", args: []string{"--manifest", "release.json", "--log-level", "loud"}, wantCode: 1, wantErr: "--log-level"},
		{name: "负数并发", args: []string{"--manifest", "release.json", "--jobs", "-1"}, wantCode: 1, wantErr: "--jobs"},
		{name: "帮助正常退出", args: []string{"--help"}, wantCode: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("退出码 = %d，期望 %d（stderr: %s）", code, tc.wantCode, stderr.String())
			}
			if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr 应包含 %q，实际: %s", tc.wantErr, stderr.String())
			}
		})
	}
}

// TestRunManifestMissing 覆盖清单文件不存在时的失败路径（不需真实编译）
func TestRunManifestMissing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--manifest", "does-not-exist.json", "--root", t.TempDir()}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("退出码 = %d，期望 1", code)
	}
	if !strings.Contains(stderr.String(), "does-not-exist.json") {
		t.Fatalf("错误应点名清单路径：%s", stderr.String())
	}
}

// TestBuildAllSmoke 是唯一的真实编译用例：在一个临时模块上跑通完整构建
// 只构建宿主机平台，既保证产物可执行以便校验版本注入，又把耗时压到最短
// 用 testing.Short 作为回退，需要跳过时可用 go test -short
//
// main 包刻意放在 cmd/hello 而非项目根：这样必须靠 --package ./cmd/hello 才能构建成功，
// 若该参数没有真正传给 go build（退回固定构建根目录的旧行为），构建会因"根目录没有 main 包"而失败——
// 因此本用例同时是 --package 生效的端到端证据
func TestBuildAllSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实交叉编译冒烟用例")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("未安装 go，跳过构建冒烟用例")
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/hello\n\ngo 1.21\n")
	// 版本变量刻意放在非 main 的 version 包里再被 main 引用：一旦声明在 main 包，
	// 链接器符号名会是 main.Version，此时 -X 写全模块路径会静默失效（实测确认过），
	// 因此这里复刻真实项目的惯例，才能真实验证 -X 注入
	if err := os.MkdirAll(filepath.Join(root, "version"), 0o755); err != nil {
		t.Fatalf("创建 version 目录失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "hello"), 0o755); err != nil {
		t.Fatalf("创建 cmd/hello 目录失败: %v", err)
	}
	writeFile(t, filepath.Join(root, "version", "version.go"), `package version

var Version = "dev"
var BuildTime = "none"
`)
	writeFile(t, filepath.Join(root, "cmd", "hello", "main.go"), `package main

import (
	"fmt"

	"example.com/hello/version"
)

func main() {
	fmt.Println(version.Version, version.BuildTime)
}
`)
	writeFile(t, filepath.Join(root, "release.json"), `{
  "app": "hello",
  "owner": "example",
  "repo": "hello",
  "assetPrefix": "hello",
  "ldflagsPackage": "example.com/hello/version",
  "platforms": [{"os": "`+runtime.GOOS+`", "arch": "`+runtime.GOARCH+`"}]
}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--manifest", "release.json",
		"--version", "9.9.9",
		"--build-time", "2026-01-02T03:04:05Z",
		"--out-dir", "build",
		"--package", "./cmd/hello",
		"--root", root,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("构建失败，退出码 %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	asset := "hello-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	binPath := filepath.Join(root, "build", asset)
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("未生成预期产物 %s: %v", binPath, err)
	}

	// 执行产物，验证版本与构建时间确实被注入（这是 -ldflags 正确性的端到端证据）
	if runtime.GOOS != "windows" {
		out, err := exec.Command(binPath).Output()
		if err != nil {
			t.Fatalf("执行产物失败: %v", err)
		}
		got := strings.TrimSpace(string(out))
		if got != "9.9.9 2026-01-02T03:04:05Z" {
			t.Fatalf("产物输出 = %q，期望注入的版本与构建时间", got)
		}
	}
}

// writeFile 写文件并在失败时终止用例
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}
