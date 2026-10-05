package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/eggokit/release"
	"github.com/jy-eggroll/eggokit/release/cmd/internal/relcli"
)

// testManifest 返回一份平台列表与 flk 完全一致的清单
// 用它渲染出的表格应当与既有发布说明逐字相同，因此这条清单本身就是"形态正确"的验收基准
func testManifest() *release.Manifest {
	return &release.Manifest{
		App:            "flk",
		Owner:          "Jy-EggRoll",
		Repo:           "flk",
		AssetPrefix:    "flk",
		LDFLAGSPackage: "github.com/jy-eggroll/flk/cmd",
		Platforms: []release.Platform{
			{OS: "windows", Arch: "386"},
			{OS: "windows", Arch: "amd64"},
			{OS: "windows", Arch: "arm64"},
			{OS: "linux", Arch: "386"},
			{OS: "linux", Arch: "amd64"},
			{OS: "linux", Arch: "arm", ARM: "7"},
			{OS: "linux", Arch: "arm64"},
			{OS: "darwin", Arch: "amd64"},
			{OS: "darwin", Arch: "arm64"},
			{OS: "freebsd", Arch: "amd64"},
			{OS: "freebsd", Arch: "arm64"},
		},
	}
}

// setLang 把进程语言切到指定值，供每个用例确定性地断言输出
// 语言是进程级全局状态，因此本文件的用例都不并行执行
func setLang(t *testing.T, lang string) {
	t.Helper()
	if err := relcli.InitL10n(lang); err != nil {
		t.Fatalf("初始化语言 %s 失败: %v", lang, err)
	}
}

// renderTo 渲染并返回文本
func renderTo(t *testing.T, o Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(&buf, o); err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	return buf.String()
}

// joinLines 把逐行切片拼成最终文本（统一以换行结尾），让 golden 用例可以直接按行书写
func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

// downloadURL 拼出某个资产的下载地址，避免在 golden 里反复手写同一前缀
func downloadURL(name string) string {
	return "https://github.com/Jy-EggRoll/flk/releases/download/1.2.3/" + name
}

// TestRenderRelease 用全量 golden 锁定正式版发布说明的形态
func TestRenderRelease(t *testing.T) {
	setLang(t, "zh-CN")

	root := t.TempDir()
	writeChangelog(t, root, "# 更新日志\n\n## 1.2.3\n\n- 修复 A\n- 新增 B\n\n## 1.2.2\n\n- 旧内容\n")

	got := renderTo(t, Options{
		Manifest:    testManifest(),
		Version:     "1.2.3",
		Type:        typeRelease,
		PreviousTag: "1.2.2",
		Root:        root,
	})

	want := joinLines([]string{
		"## 快速下载",
		"",
		"| 架构 | Windows | Linux | macOS | FreeBSD |",
		"|------|---------|-------|-------|---------|",
		"| **x86 (32位)** | [x86](" + downloadURL("flk-windows-386.exe") + ") | [x86](" + downloadURL("flk-linux-386") + ") | 不支持 | 不支持 |",
		"| **x64 (64位)** | [x64](" + downloadURL("flk-windows-amd64.exe") + ") | [x64](" + downloadURL("flk-linux-amd64") + ") | [x64](" + downloadURL("flk-darwin-amd64") + ") | [x64](" + downloadURL("flk-freebsd-amd64") + ") |",
		"| **ARM64** | [ARM64](" + downloadURL("flk-windows-arm64.exe") + ") | [ARM64](" + downloadURL("flk-linux-arm64") + ") | [ARM64](" + downloadURL("flk-darwin-arm64") + ") | [ARM64](" + downloadURL("flk-freebsd-arm64") + ") |",
		"| **ARM (armv7)** | 不支持 | [ARM](" + downloadURL("flk-linux-arm") + ") | 不支持 | 不支持 |",
		"",
		"> **提示**：Windows 用户下载 .exe 文件，其他系统下载后需要添加执行权限 chmod +x flk-*",
		"",
		"---",
		"",
		"## 主要更新内容",
		"",
		"- 修复 A",
		"- 新增 B",
		"",
		"",
		"## 详细提交记录",
		"",
		"[查看 1.2.2 到 1.2.3 的完整变更](https://github.com/Jy-EggRoll/flk/compare/1.2.2...1.2.3)",
		"",
		"---",
		"",
		"**安装说明：**",
		"",
		"1. 从上方表格中选择并下载对应平台和架构的二进制文件",
		"2. Linux/macOS/BSD 用户需要赋予执行权限：`chmod +x flk-*`（建议直接将文件改名为 flk 方便调用和后续的升级）",
		"3. 运行程序（建议直接将文件改名为 flk 方便调用）`flk help`",
		"",
		"详细使用说明请参考 [README.md](https://github.com/Jy-EggRoll/flk/blob/main/README.md)",
	})

	if got != want {
		t.Fatalf("正式版输出与预期不符\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderDev 用全量 golden 锁定开发版发布说明的形态（含版本信息与版本信息外的分支/提交）
func TestRenderDev(t *testing.T) {
	setLang(t, "zh-CN")

	got := renderTo(t, Options{
		Manifest:    testManifest(),
		Version:     "1.2.3.dev.1",
		Type:        typeDev,
		BuildTime:   "2026-01-02T03:04:05Z",
		PreviousTag: "1.2.3.dev.0",
		Branch:      "main",
		Commit:      "abc1234",
		Root:        t.TempDir(),
	})

	// dev 版把链接里的版本换成 dev 版本，其余表格形态与正式版一致
	devURL := func(name string) string {
		return "https://github.com/Jy-EggRoll/flk/releases/download/1.2.3.dev.1/" + name
	}

	want := joinLines([]string{
		"## 开发版本 - 仅供测试",
		"",
		"这是一个开发版本，仅供测试使用。请谨慎使用，可能包含不稳定或实验性功能。",
		"",
		"## 快速下载",
		"",
		"| 架构 | Windows | Linux | macOS | FreeBSD |",
		"|------|---------|-------|-------|---------|",
		"| **x86 (32位)** | [x86](" + devURL("flk-windows-386.exe") + ") | [x86](" + devURL("flk-linux-386") + ") | 不支持 | 不支持 |",
		"| **x64 (64位)** | [x64](" + devURL("flk-windows-amd64.exe") + ") | [x64](" + devURL("flk-linux-amd64") + ") | [x64](" + devURL("flk-darwin-amd64") + ") | [x64](" + devURL("flk-freebsd-amd64") + ") |",
		"| **ARM64** | [ARM64](" + devURL("flk-windows-arm64.exe") + ") | [ARM64](" + devURL("flk-linux-arm64") + ") | [ARM64](" + devURL("flk-darwin-arm64") + ") | [ARM64](" + devURL("flk-freebsd-arm64") + ") |",
		"| **ARM (armv7)** | 不支持 | [ARM](" + devURL("flk-linux-arm") + ") | 不支持 | 不支持 |",
		"",
		"> **提示**：Windows 用户下载 .exe 文件，其他系统下载后需要添加执行权限 chmod +x flk-*",
		"",
		"---",
		"",
		"## 版本信息",
		"",
		"- 版本号: 1.2.3.dev.1",
		"- 构建时间: 2026-01-02T03:04:05Z",
		"- 分支: main",
		"- 提交: abc1234",
		"",
		"## 详细提交记录",
		"",
		"[查看 1.2.3.dev.0 到 1.2.3.dev.1 的完整变更](https://github.com/Jy-EggRoll/flk/compare/1.2.3.dev.0...1.2.3.dev.1)",
		"",
		"---",
		"",
		"## 注意事项",
		"",
		"仅通过 GitHub Release 提供下载",
		"可能包含未经充分测试的功能",
	})

	if got != want {
		t.Fatalf("开发版输出与预期不符\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderDevOmitsEmptyFields 锁定"构建时间/分支/提交为空则整行省略"的行为
func TestRenderDevOmitsEmptyFields(t *testing.T) {
	setLang(t, "zh-CN")

	got := renderTo(t, Options{
		Manifest: testManifest(),
		Version:  "1.2.3.dev.1",
		Type:     typeDev,
		Root:     t.TempDir(),
	})

	for _, unwanted := range []string{"- 构建时间:", "- 分支:", "- 提交:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("空的 %s 不应出现在输出中：\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "- 版本号: 1.2.3.dev.1") {
		t.Fatalf("版本号必须始终展示：\n%s", got)
	}
	// 首次发布（无上一标签）应给出明确措辞而不是留空
	if !strings.Contains(got, "首次发布") {
		t.Fatalf("无上一版本时应输出首次发布：\n%s", got)
	}
}

// TestRenderReleaseFirstRelease 锁定"无上一版本 tag 时写首次发布"的正式版分支
func TestRenderReleaseFirstRelease(t *testing.T) {
	setLang(t, "zh-CN")

	root := t.TempDir()
	writeChangelog(t, root, "## 1.0.0\n\n- 首个正式版\n")

	got := renderTo(t, Options{
		Manifest: testManifest(),
		Version:  "1.0.0",
		Type:     typeRelease,
		Root:     root,
	})

	if !strings.Contains(got, "首次发布") {
		t.Fatalf("无上一版本时应输出首次发布：\n%s", got)
	}
	if strings.Contains(got, "/compare/") {
		t.Fatalf("首次发布不应出现 compare 链接：\n%s", got)
	}
	if !strings.Contains(got, "- 首个正式版") {
		t.Fatalf("应包含小节内容：\n%s", got)
	}
}

// TestRenderReleaseMissingSection 锁定本次最重要的改进：找不到版本小节时不得输出空内容
// 这正是既有实现的真实缺陷（首个正式版 Release 正文为空）
func TestRenderReleaseMissingSection(t *testing.T) {
	setLang(t, "zh-CN")

	cases := []struct {
		name    string
		content string // 空串表示不创建文件
		version string
	}{
		{name: "文件存在但没有该版本小节", content: "## 1.0.0\n\n- 老内容\n", version: "1.0.1"},
		{name: "文件根本不存在", content: "", version: "1.0.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.content != "" {
				writeChangelog(t, root, tc.content)
			}

			got := renderTo(t, Options{
				Manifest: testManifest(),
				Version:  tc.version,
				Type:     typeRelease,
				Root:     root,
			})

			if !strings.Contains(got, "## 主要更新内容") {
				t.Fatalf("正式版必须带主要更新内容标题：\n%s", got)
			}
			// 默认提示必须点名版本与文件，读者据此才知道该去哪里补
			if !strings.Contains(got, tc.version) || !strings.Contains(got, "CHANGELOG.md") {
				t.Fatalf("默认提示应包含版本号与文件名：\n%s", got)
			}
			// 关键断言：标题下方不能只是空白
			if strings.Contains(got, "## 主要更新内容\n\n## 详细提交记录") {
				t.Fatalf("找不到小节时不应输出空内容：\n%s", got)
			}
		})
	}
}

// TestRenderEnglish 用英文源串跑一遍，确保语言切换对输出整体生效
func TestRenderEnglish(t *testing.T) {
	setLang(t, "en")

	root := t.TempDir()
	writeChangelog(t, root, "## 1.2.3\n\n- fix A\n")

	got := renderTo(t, Options{
		Manifest:    testManifest(),
		Version:     "1.2.3",
		Type:        typeRelease,
		PreviousTag: "1.2.2",
		Root:        root,
	})

	for _, want := range []string{"## Quick Download", "## Key Changes", "## Detailed Commit History", "**Installation:**", "Unsupported"} {
		if !strings.Contains(got, want) {
			t.Fatalf("英文输出缺少 %q：\n%s", want, got)
		}
	}
}

// writeManifest 在 root 下写出一份各项齐备的清单 JSON；extraFields 会原样并入顶层对象
//
// 用它构造清单而不是每个用例手抄一遍 JSON：新增 releaseNotesLang / changelogFiles 之类的字段时，
// 只需在关心它的用例里加一段 extraFields，其余用例不受影响，也就不会因误改基准而集体失真
func writeManifest(t *testing.T, root, extraFields string) string {
	t.Helper()
	fields := ""
	if strings.TrimSpace(extraFields) != "" {
		fields = extraFields + ","
	}
	path := filepath.Join(root, "release.json")
	writeFile(t, path, `{
	  "app": "flk",
	  "owner": "Jy-EggRoll",
	  "repo": "flk",
	  "assetPrefix": "flk",
	  "ldflagsPackage": "github.com/jy-eggroll/flk/cmd",`+fields+`
	  "platforms": [{"os": "linux", "arch": "amd64"}]
	}`)
	return path
}

// TestRunDefaultsToEnglish 锁定"命令行没给 --lang、清单也没写 releaseNotesLang"时回退内置英文
//
// 这是本次改动的核心行为：默认语言由 zh-CN 改为 en。跑完整 run 而非只测渲染，
// 才能覆盖"两个来源都缺省时最终落到的语言"这一整条链路
func TestRunDefaultsToEnglish(t *testing.T) {
	root := t.TempDir()
	writeChangelog(t, root, "## 1.0.0\n\n- first\n")
	writeManifest(t, root, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--manifest", "release.json",
		"--version", "1.0.0",
		"--type", "release",
		"--previous-tag", "0.9.0",
		"--root", root,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("期望成功，退出码 %d（stderr: %s）", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "## Quick Download") || !strings.Contains(out, "## Key Changes") {
		t.Fatalf("缺省语言应为英文：\n%s", out)
	}
	if strings.Contains(out, "快速下载") {
		t.Fatalf("缺省时不应输出中文：\n%s", out)
	}
}

// TestRunLanguagePriority 锁定语言优先级：命令行 --lang > 清单 releaseNotesLang > 内置默认 en
func TestRunLanguagePriority(t *testing.T) {
	cases := []struct {
		name       string
		manifest   string // 并入清单的附加字段
		langArg    string // 追加的 --lang 取值，空串表示不传
		wantMarker string // 输出里必须出现的语言标记
		unwant     string // 输出里不得出现的另一语言标记
	}{
		{
			name:       "命令行压过清单",
			manifest:   `"releaseNotesLang": "zh-CN"`,
			langArg:    "en",
			wantMarker: "## Quick Download",
			unwant:     "快速下载",
		},
		{
			name:       "无命令行时用清单的语言",
			manifest:   `"releaseNotesLang": "zh-CN"`,
			langArg:    "",
			wantMarker: "## 快速下载",
			unwant:     "## Quick Download",
		},
		{
			name:       "清单与命令行皆无则内置英文",
			manifest:   "",
			langArg:    "",
			wantMarker: "## Quick Download",
			unwant:     "快速下载",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeChangelog(t, root, "## 1.0.0\n\n- entry\n")
			writeManifest(t, root, tc.manifest)

			args := []string{"--manifest", "release.json", "--version", "1.0.0", "--type", "release", "--previous-tag", "0.9.0", "--root", root}
			if tc.langArg != "" {
				args = append(args, "--lang", tc.langArg)
			}

			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("期望成功，退出码 %d（stderr: %s）", code, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, tc.wantMarker) {
				t.Fatalf("输出应包含 %q：\n%s", tc.wantMarker, out)
			}
			if strings.Contains(out, tc.unwant) {
				t.Fatalf("输出不应包含 %q：\n%s", tc.unwant, out)
			}
		})
	}
}

// TestRenderChangelogFilesByLanguage 锁定 changelogFiles 的挑文件与回退
//
// 语言是进程级全局状态，setLang 在两段之间切换，正对应"同一份清单按不同语言渲染"的真实场景
func TestRenderChangelogFilesByLanguage(t *testing.T) {
	root := t.TempDir()
	// 两份日志都含目标版本，但正文语言不同，命中哪一份一目了然
	writeFile(t, filepath.Join(root, "CHANGELOG.md"), "## 1.2.3\n\n- 中文内容\n")
	writeFile(t, filepath.Join(root, "CHANGELOG.en.md"), "## 1.2.3\n\n- english content\n")

	m := testManifest()
	m.ChangelogFiles = map[string]string{"en": "CHANGELOG.en.md"}

	opts := func() Options {
		return Options{
			Manifest:    m,
			Version:     "1.2.3",
			Type:        typeRelease,
			PreviousTag: "1.2.2",
			Root:        root,
		}
	}

	setLang(t, "en")
	if got := renderTo(t, opts()); !strings.Contains(got, "english content") || strings.Contains(got, "中文内容") {
		t.Fatalf("英文下应取 CHANGELOG.en.md：\n%s", got)
	}

	// zh-CN 未在 changelogFiles 里配置，应回退既有的 changelogFile（默认 CHANGELOG.md）
	setLang(t, "zh-CN")
	if got := renderTo(t, opts()); !strings.Contains(got, "中文内容") {
		t.Fatalf("中文下应回退 CHANGELOG.md：\n%s", got)
	}
}

// TestRenderMissingSectionReportsActualFile 锁定默认提示必须点名"实际读取的那个文件"
//
// 多语言项目里日志文件名随语言而变，若固定报 CHANGELOG.md，用户会去改一个根本没被读到的文件
func TestRenderMissingSectionReportsActualFile(t *testing.T) {
	setLang(t, "en")

	root := t.TempDir()
	// 只提供英文日志，且其中没有目标版本，借此验证提示里报的是 CHANGELOG.en.md
	writeFile(t, filepath.Join(root, "CHANGELOG.en.md"), "## 1.0.0\n\n- old\n")

	m := testManifest()
	m.ChangelogFiles = map[string]string{"en": "CHANGELOG.en.md"}

	got := renderTo(t, Options{
		Manifest: m,
		Version:  "9.9.9",
		Type:     typeRelease,
		Root:     root,
	})

	if !strings.Contains(got, "CHANGELOG.en.md") {
		t.Fatalf("默认提示应点名实际读取的 CHANGELOG.en.md：\n%s", got)
	}
}

// TestOmittedPlatforms 锁定"网格外平台"的判定口径
//
// 网格就是 tableOS x tableArch 的全部组合，windows/arm 是其中一格（Windows 列 x ARM 行），
// 故不算遗漏；只有架构不在 tableArch、或系统不在 tableOS 的平台才算，这样才能真报出静默遗漏
func TestOmittedPlatforms(t *testing.T) {
	m := &release.Manifest{
		AssetPrefix: "x",
		Platforms: []release.Platform{
			{OS: "linux", Arch: "amd64"},  // 网格内
			{OS: "linux", Arch: "mips64"}, // 架构不在网格行内
			{OS: "plan9", Arch: "amd64"},  // 系统不在网格列内
			{OS: "windows", Arch: "arm"},  // 网格内（Windows 列 x ARM 行）
		},
	}

	got := omittedPlatforms(m)
	if len(got) != 2 {
		t.Fatalf("应检出 2 个网格外平台，实际 %d：%+v", len(got), got)
	}
	if got[0].String() != "linux/mips64" || got[1].String() != "plan9/amd64" {
		t.Fatalf("检出结果不符：%+v", got)
	}
}

// TestRenderWarnsOnOmittedPlatforms 锁定网格外平台的告警：逐个列出平台与资产名，且不进正文
func TestRenderWarnsOnOmittedPlatforms(t *testing.T) {
	setLang(t, "en")

	m := testManifest()
	m.Platforms = append(m.Platforms,
		release.Platform{OS: "linux", Arch: "mips64"},
		release.Platform{OS: "plan9", Arch: "amd64"},
	)

	var warn bytes.Buffer
	got := renderTo(t, Options{
		Manifest:    m,
		Version:     "1.2.3",
		Type:        typeRelease,
		PreviousTag: "1.2.2",
		Root:        t.TempDir(),
		Warn:        &warn,
	})

	w := warn.String()
	// 平台标识用于对照清单，资产名用于对照构建产物，缺一不可
	for _, want := range []string{"linux/mips64", "flk-linux-mips64", "plan9/amd64", "flk-plan9-amd64"} {
		if !strings.Contains(w, want) {
			t.Fatalf("告警应逐个列出平台与资产名，缺少 %q：\n%s", want, w)
		}
	}
	// 告警是给发布者看的，绝不能混进面向读者的正文
	if strings.Contains(got, "mips64") || strings.Contains(got, "plan9") {
		t.Fatalf("告警不应出现在发布说明正文：\n%s", got)
	}
}

// TestRenderNoWarnWhenAllPlatformsInGrid 锁定不产生误报：所有平台都在网格内时不得告警
func TestRenderNoWarnWhenAllPlatformsInGrid(t *testing.T) {
	setLang(t, "en")

	var warn bytes.Buffer
	_ = renderTo(t, Options{
		Manifest:    testManifest(),
		Version:     "1.2.3",
		Type:        typeRelease,
		PreviousTag: "1.2.2",
		Root:        t.TempDir(),
		Warn:        &warn,
	})

	if warn.Len() != 0 {
		t.Fatalf("所有平台都在网格内时不应告警：\n%s", warn.String())
	}
}

// TestExtractVersionSection 表驱动覆盖小节摘取的各种边界
func TestExtractVersionSection(t *testing.T) {
	cases := []struct {
		name    string
		content string
		version string
		want    string
		found   bool
	}{
		{
			name:    "普通小节",
			content: "# 日志\n\n## 1.2.3\n\n- a\n- b\n\n## 1.2.2\n\n- c\n",
			version: "1.2.3",
			want:    "\n- a\n- b\n",
			found:   true,
		},
		{
			name:    "标题带日期后缀也能命中",
			content: "## 1.2.3 - 2026-01-01\n- x\n",
			version: "1.2.3",
			want:    "- x\n",
			found:   true,
		},
		{
			name:    "小节位于文件末尾",
			content: "## 1.0.0\n- last\n",
			version: "1.0.0",
			want:    "- last\n",
			found:   true,
		},
		{
			name:    "三级标题也作为小节边界",
			content: "## 1.0.0\n- a\n### 子标题\n- b\n",
			version: "1.0.0",
			want:    "- a",
			found:   true,
		},
		{
			name:    "找不到版本",
			content: "## 2.0.0\n- a\n",
			version: "1.0.0",
			found:   false,
		},
		{
			name:    "空内容",
			content: "",
			version: "1.0.0",
			found:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := extractVersionSection(tc.content, tc.version)
			if found != tc.found {
				t.Fatalf("found = %v，期望 %v", found, tc.found)
			}
			if found && got != tc.want {
				t.Fatalf("小节内容 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestReleaseTagPattern 覆盖正式版标签的匹配规则，避免 dev 标签污染上一版本推断
func TestReleaseTagPattern(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"1.2.3", true},
		{"0.0.1", true},
		{"1.2.3.dev.1", false},
		{"v1.2.3", false},
		{"1.2", false},
		{"nightly", false},
	}

	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			if got := releaseTagPattern.MatchString(tc.tag); got != tc.want {
				t.Fatalf("releaseTagPattern.MatchString(%q) = %v，期望 %v", tc.tag, got, tc.want)
			}
		})
	}
}

// TestRunFlagErrors 覆盖参数解析与必填校验这些可离线断言的路径
func TestRunFlagErrors(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "缺少 manifest", args: []string{}, wantCode: 1, wantErr: "--manifest"},
		{name: "缺少 version", args: []string{"--manifest", "release.json"}, wantCode: 1, wantErr: "--version"},
		{name: "非法 type", args: []string{"--manifest", "release.json", "--version", "1.0.0", "--type", "beta"}, wantCode: 1, wantErr: "--type"},
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

// TestRunEndToEnd 用临时目录里的清单与日志跑通完整流程，且不触碰真实仓库
func TestRunEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeChangelog(t, root, "## 3.1.4\n\n- 修复登录\n")

	manifestPath := filepath.Join(root, "release.json")
	writeFile(t, manifestPath, `{
	  "app": "ggt",
	  "owner": "Jy-EggRoll",
	  "repo": "ggt",
	  "assetPrefix": "ggt",
	  "ldflagsPackage": "github.com/jy-eggroll/ggt/cmd",
	  "platforms": [{"os": "linux", "arch": "amd64"}]
	}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--manifest", "release.json",
		"--version", "3.1.4",
		"--type", "release",
		"--previous-tag", "3.1.3",
		"--root", root,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("期望成功，退出码 %d（stderr: %s）", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ggt-linux-amd64") {
		t.Fatalf("输出应包含资产名：\n%s", out)
	}
	if !strings.Contains(out, "修复登录") {
		t.Fatalf("输出应包含小节内容：\n%s", out)
	}
	if !strings.Contains(out, "3.1.3...3.1.4") {
		t.Fatalf("输出应包含 compare 链接：\n%s", out)
	}
}

// TestResolvePreviousTag 在临时 git 仓库里验证标签推断，避免依赖真实仓库
func TestResolvePreviousTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git，跳过标签推断用例")
	}

	root := t.TempDir()
	initGitRepo(t, root, "1.0.0", "1.0.9", "1.0.10")

	cases := []struct {
		name    string
		version string
		typ     string
		want    string
	}{
		{name: "正式版取最高的三段式标签", version: "2.0.0", typ: typeRelease, want: "1.0.10"},
		{name: "排除当前版本本身", version: "1.0.10", typ: typeRelease, want: "1.0.9"},
		{name: "dev 模式也按版本倒序取首个", version: "1.1.0.dev.1", typ: typeDev, want: "1.0.10"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePreviousTag(root, tc.version, tc.typ)
			if err != nil {
				t.Fatalf("推断失败: %v", err)
			}
			if got != tc.want {
				t.Fatalf("上一版本 = %q，期望 %q", got, tc.want)
			}
		})
	}

	t.Run("没有任何标签时返回空串", func(t *testing.T) {
		empty := t.TempDir()
		initGitRepo(t, empty) // 只建仓库不打标签
		got, err := resolvePreviousTag(empty, "1.0.0", typeRelease)
		if err != nil {
			t.Fatalf("推断失败: %v", err)
		}
		if got != "" {
			t.Fatalf("期望空串，实际 %q", got)
		}
	})
}

// writeChangelog 在 root 下写入 CHANGELOG.md
func writeChangelog(t *testing.T, root, content string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "CHANGELOG.md"), content)
}

// writeFile 写文件并在失败时终止用例
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

// initGitRepo 在 root 建一个带单个提交的仓库，并按需打上若干标签
// 提交显式带上 user.name/user.email，避免依赖机器上的全局 git 身份配置
func initGitRepo(t *testing.T, root string, tags ...string) {
	t.Helper()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, out)
		}
	}

	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	for _, tag := range tags {
		run("tag", tag)
	}
}
