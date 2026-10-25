package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jy-eggroll/eggokit/upgradecli"
	"github.com/spf13/cobra"
)

// TestEffectiveVersion 覆盖空版本、未注入版本来源与正常版本三条分支，确认占位规则只有一份。
func TestEffectiveVersion(t *testing.T) {
	cases := []struct {
		name      string
		version   func() string
		wantValue string
	}{
		{name: "正常版本", version: func() string { return "1.2.3" }, wantValue: "1.2.3"},
		{name: "空版本回退占位", version: func() string { return "" }, wantValue: "development build"},
		{name: "未提供版本来源", version: nil, wantValue: "development build"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := VersionOptions{Version: tc.version}
			if got := opts.EffectiveVersion(); got != tc.wantValue {
				t.Fatalf("EffectiveVersion() = %q，期望 %q", got, tc.wantValue)
			}
		})
	}
}

// TestRenderVersionWritesThreeLines 验证三行格式逐字稳定，平台标签与升级命令取同一份实现。
func TestRenderVersionWritesThreeLines(t *testing.T) {
	var buf bytes.Buffer
	opts := VersionOptions{
		Version:   func() string { return "1.2.3" },
		BuildTime: func() string { return "2026-01-02T03:04:05Z" },
	}

	if err := RenderVersion(&buf, opts); err != nil {
		t.Fatalf("RenderVersion 返回错误: %v", err)
	}

	want := "Version: 1.2.3\nBuild time: 2026-01-02T03:04:05Z\nPlatform: " + upgradecli.PlatformLabel() + "\n"
	if buf.String() != want {
		t.Fatalf("输出 = %q，期望 %q", buf.String(), want)
	}
}

// TestRenderVersionDevelopmentBuild 验证本地开发构建的占位输出：版本回退为可读占位，构建时间统一为 unknown。
func TestRenderVersionDevelopmentBuild(t *testing.T) {
	var buf bytes.Buffer
	opts := VersionOptions{}

	if err := RenderVersion(&buf, opts); err != nil {
		t.Fatalf("RenderVersion 返回错误: %v", err)
	}

	output := buf.String()
	for _, want := range []string{"Version: development build\n", "Build time: unknown\n", "Platform: " + upgradecli.PlatformLabel()} {
		if !strings.Contains(output, want) {
			t.Fatalf("输出未包含 %q，实际为 %q", want, output)
		}
	}
}

// TestNewVersionCommand 验证命令名、别名可被 cobra 解析，且输出写入命令自身的流。
func TestNewVersionCommand(t *testing.T) {
	cmd := NewVersionCommand(VersionOptions{
		Version:   func() string { return "2.0.0" },
		BuildTime: func() string { return "2026-02-03T04:05:06Z" },
	})
	if cmd.Use != "version" {
		t.Fatalf("Use = %q，期望 version", cmd.Use)
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "ver" {
		t.Fatalf("Aliases = %v，期望 [ver]", cmd.Aliases)
	}

	root := &cobra.Command{Use: "demo"}
	root.AddCommand(cmd)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"ver"})
	if err := root.Execute(); err != nil {
		t.Fatalf("执行 ver 别名失败: %v", err)
	}

	if want := "Version: 2.0.0\n"; !strings.Contains(buf.String(), want) {
		t.Fatalf("输出 = %q，未包含 %q", buf.String(), want)
	}
}
