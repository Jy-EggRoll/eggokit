package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/release"
)

// 发布说明的两种类型
// 用常量而不是直接写字符串：这两个取值会出现在 flag 校验、分支判断与展示文案里，
// 各写一遍字面量迟早会有一处拼错，而拼错的后果是静默走到错误分支
const (
	typeDev     = "dev"
	typeRelease = "release"
)

// tableOS 是快速下载表格的列顺序，对应四个 GOOS
//
// 顺序即展示顺序（Windows、Linux、macOS、FreeBSD），与既有发布说明一致；
// 表头文案与它一一对应，改动这里必须同时改表头那一行
var tableOS = []string{"windows", "linux", "darwin", "freebsd"}

// tableArch 是快速下载表格的行顺序，对应四个 GOARCH
//
// 与 tableOS 一起构成"下载表格是一个固定 4x4 网格"这一事实：表格的行列是写死的，
// 清单里若有网格之外的平台（如 linux/mips64），能构建、能在 GitHub 上被下载，
// 却不会出现在表格里——这正是 needsOmissionWarning 要检测并告警的静默遗漏
var tableArch = []string{"386", "amd64", "arm64", "arm"}

// Options 是渲染一份发布说明所需的全部输入
//
// 刻意把 git 相关信息（PreviousTag、Branch、Commit）作为已解析好的入参传入，
// 而不是让渲染函数自己去调 git：渲染因此变成纯函数，单测可以直接构造输入，
// 不必依赖工作目录里一定存在一个真实仓库
type Options struct {
	// Manifest 是发布清单，提供仓库坐标、资产前缀等全部命名依据
	Manifest *release.Manifest

	// Version 是本版本的发布标签，同时用于下载地址、compare 链接与小节匹配
	Version string

	// Type 是发布类型，取 typeDev 或 typeRelease
	Type string

	// BuildTime 是构建时间，仅 dev 版展示；为空表示未提供，则整行省略
	BuildTime string

	// PreviousTag 是上一版本标签，为空表示首次发布
	PreviousTag string

	// Branch 与 Commit 是 dev 版展示用的分支名与提交短哈希，为空则整行省略
	Branch string
	Commit string

	// Root 是项目根目录，用于定位更新日志文件
	Root string

	// Warn 是渲染期告警的输出流（属于发布说明正文之外的提示，如清单里声明了下载表格
	// 网格之外的平台）；为空表示丢弃。刻意与正文流分开：告警是 stderr 语义，
	// 混进正文会污染被整体重定向进 GitHub Release 的内容
	Warn io.Writer
}

// emitter 负责按行拼装 markdown，并统一管理换行
//
// 之所以不直接在 Render 里到处 b.WriteString("...\n")：发布说明的形态对空白行很敏感
// （哪些标题前后有空行、分隔线前后是否留白），把"写一行"收成一个方法能显著降低手误，
// 也让 Render 的分支逻辑一眼可读
type emitter struct {
	b strings.Builder
}

// p 写入一行并补换行
func (e *emitter) p(s string) {
	e.b.WriteString(s)
	e.b.WriteByte('\n')
}

// blank 写入一个空行
func (e *emitter) blank() {
	e.b.WriteByte('\n')
}

// Render 把发布说明写入 w
//
// 整体顺序逐字复刻既有发布说明（先快速下载表格，再版本信息/更新内容，再提交记录，
// 最后结尾说明）：这份形态已经在多个项目里验证过，发布说明的读者是靠固定位置找东西的，
// 顺序变化会直接增加阅读成本
func Render(w io.Writer, o Options) error {
	m := o.Manifest
	e := &emitter{}

	if o.Type == typeDev {
		e.p(l10n.T("## Development Build - For Testing Only", nil))
		e.blank()
		e.p(l10n.T("This is a development build intended for testing only; it may contain unstable or experimental features, please use it with caution", nil))
		e.blank()
	}

	// 快速下载表格：每一格是否给出链接，完全由清单里的平台决定，
	// 清单没有组合显示为"不支持"，而不是给一个必然 404 的地址
	e.p(l10n.T("## Quick Download", nil))
	e.blank()
	e.p(l10n.T("| Architecture | Windows | Linux | macOS | FreeBSD |", nil))
	// 分隔行由纯符号构成，不含任何需要翻译的文字，故不进语言文件
	e.p("|------|---------|-------|-------|---------|")
	e.p(buildRow(m, o.Version, l10n.T("**x86 (32-bit)**", nil), "x86", "386"))
	e.p(buildRow(m, o.Version, l10n.T("**x64 (64-bit)**", nil), "x64", "amd64"))
	e.p(buildRow(m, o.Version, l10n.T("**ARM64**", nil), "ARM64", "arm64"))
	e.p(buildRow(m, o.Version, l10n.T("**ARM (armv7)**", nil), "ARM", "arm"))
	e.blank()
	e.p(l10n.T("> **Note**: on Windows download the .exe file; on other systems grant the execute permission with {{.Command}} after downloading", map[string]any{"Command": "chmod +x " + m.AssetPrefix + "-*"}))
	e.blank()
	e.p("---")
	e.blank()

	// 下载表格是固定 4x4 网格，清单里若有网格之外的平台，构建会照常产出但说明里完全不出现，
	// 属静默遗漏，因此在此点名告警。告警走 stderr 而非正文：它面向的是发布者而不是读者
	warnOmittedPlatforms(o, m)

	if o.Type == typeDev {
		e.p(l10n.T("## Version Info", nil))
		e.blank()
		e.p(l10n.T("- Version: {{.Version}}", map[string]any{"Version": o.Version}))
		// 构建时间/分支/提交都是"有则展示"：拿不到时省略整行，好过留一个空值占位，
		// 空值会让读者误以为"这个信息就是空的"，而实际只是当前环境取不到
		if o.BuildTime != "" {
			e.p(l10n.T("- Build time: {{.BuildTime}}", map[string]any{"BuildTime": o.BuildTime}))
		}
		if o.Branch != "" {
			e.p(l10n.T("- Branch: {{.Branch}}", map[string]any{"Branch": o.Branch}))
		}
		if o.Commit != "" {
			e.p(l10n.T("- Commit: {{.Commit}}", map[string]any{"Commit": o.Commit}))
		}
		e.blank()
	}

	if o.Type == typeRelease {
		e.p(l10n.T("## Key Changes", nil))
		// 按"当前语言"挑日志文件：选了英文就取英文那份 CHANGELOG，避免正文语言与说明语言割裂
		// 语言取自 l10n.Current()，即本次渲染实际生效的语言，与上方正文文案用的是同一门语言
		changelogFile := m.ChangelogPathFor(l10n.Current())
		text, found, err := readVersionSection(o.Root, changelogFile, o.Version)
		if err != nil {
			return err
		}
		if found {
			// 小节内容整体写入（可能多行），再补一个空行，与既有实现的空行位置一致
			e.p(text)
		} else {
			// 找不到小节时绝不能输出空内容：空白的"主要更新内容"正是既有实现里
			// 首个正式版 Release 正文为空这一缺陷的根因，这里改为明确提示并点名文件
			// 点名的必须是**实际读取的那个文件**：多语言项目里日志文件随语言而变，
			// 若固定报 CHANGELOG.md，用户会去改一个根本没被读到的文件
			e.p(l10n.T("Section for {{.Version}} was not found in {{.File}}; the release notes would be empty, please add this section to the file", map[string]any{
				"Version": o.Version,
				"File":    changelogFile,
			}))
		}
		e.blank()
	}

	e.p(l10n.T("## Detailed Commit History", nil))
	e.blank()
	if o.PreviousTag == "" {
		e.p(l10n.T("First release", nil))
	} else {
		e.p(l10n.T("[View the full changes from {{.Previous}} to {{.Version}}]({{.URL}})", map[string]any{
			"Previous": o.PreviousTag,
			"Version":  o.Version,
			"URL":      compareURL(m, o.PreviousTag, o.Version),
		}))
		e.blank()
	}
	e.p("---")
	e.blank()

	if o.Type == typeDev {
		e.p(l10n.T("## Notes", nil))
		e.blank()
		e.p(l10n.T("Only distributed via GitHub Release", nil))
		e.p(l10n.T("May contain features that have not been fully tested", nil))
	} else {
		e.p(l10n.T("**Installation:**", nil))
		e.blank()
		e.p(l10n.T("1. Choose and download the binary for your platform and architecture from the table above", nil))
		e.p(l10n.T("2. Linux/macOS/BSD users need to grant the execute permission: `{{.Command}}` (renaming the file to {{.App}} is recommended, which makes invocation and future upgrades easier)", map[string]any{
			"Command": "chmod +x " + m.AssetPrefix + "-*",
			"App":     m.App,
		}))
		e.p(l10n.T("3. Run the program: `{{.App}} help` (renaming the file to {{.App}} is recommended)", map[string]any{"App": m.App}))
		e.blank()
		e.p(l10n.T("See [README.md]({{.URL}}) for detailed usage", map[string]any{"URL": readmeURL(m)}))
	}

	_, err := io.WriteString(w, e.b.String())
	return err
}

// buildRow 生成快速下载表格的一行
//
// rowLabel 与 linkText 分开是因为二者用途不同：rowLabel 是行首的架构名（需要翻译），
// linkText 是超链接里的文字（x86/x64/ARM64/ARM 这类跨语言通用的技术标识，不翻译）
func buildRow(m *release.Manifest, version, rowLabel, linkText, arch string) string {
	cells := make([]string, 0, len(tableOS))
	for _, goos := range tableOS {
		p, ok := findPlatform(m, goos, arch)
		if !ok {
			cells = append(cells, l10n.T("Unsupported", nil))
			continue
		}
		cells = append(cells, fmt.Sprintf("[%s](%s)", linkText, assetURL(m, version, p)))
	}
	return "| " + rowLabel + " | " + strings.Join(cells, " | ") + " |"
}

// findPlatform 在清单里查找指定 os/arch 的平台，找不到时 ok 为 false
// 需要返回完整的 Platform 而不只是布尔值：下载地址里的资产名依赖 GOARM 等信息，
// 只有拿到清单里那份原始定义才能算出与构建产物完全一致的名称
func findPlatform(m *release.Manifest, goos, goarch string) (release.Platform, bool) {
	for _, p := range m.Platforms {
		if p.OS == goos && p.Arch == goarch {
			return p, true
		}
	}
	return release.Platform{}, false
}

// inDownloadTable 判断某平台是否落在快速下载表格的 4x4 网格内
//
// 判定只看 os/arch 是否分别属于 tableOS 与 tableArch：表格的行列是固定的，
// GOARM 不改变它占哪一格（linux/arm 的 v7 与 v5 都落在同一格），因此无需参与判断
func inDownloadTable(p release.Platform) bool {
	return containsString(tableOS, p.OS) && containsString(tableArch, p.Arch)
}

// omittedPlatforms 返回清单里声明了、却落在下载表格网格之外的平台
//
// 这类平台会被 buildall 正常构建、也会被上传到 Release，但发布说明的表格里压根没有它，
// 用户只能靠猜地址才能下载——是"发布了却没人知道"的静默遗漏，必须让发布者看到
func omittedPlatforms(m *release.Manifest) []release.Platform {
	var out []release.Platform
	for _, p := range m.Platforms {
		if !inDownloadTable(p) {
			out = append(out, p)
		}
	}
	return out
}

// warnOmittedPlatforms 把网格外平台的告警写到 o.Warn（通常为 stderr）
//
// 逐个列出平台与资产名，而不是只报一个数量：发布者要据此判断"这是有意为之还是漏配了表格列"，
// 只有具体到平台与产物名才能一眼对照清单。o.Warn 为空时静默跳过，便于在不需要告警的场合调用
func warnOmittedPlatforms(o Options, m *release.Manifest) {
	if o.Warn == nil {
		return
	}
	missing := omittedPlatforms(m)
	if len(missing) == 0 {
		return
	}
	items := make([]string, 0, len(missing))
	for _, p := range missing {
		// 平台标识用于对照清单，资产名用于对照 build 目录与 Release 资产，两者都要给出
		items = append(items, fmt.Sprintf("%s (%s)", p.String(), m.AssetName(p)))
	}
	fmt.Fprintln(o.Warn, l10n.T("Warning: {{.Message}}", map[string]any{
		"Message": l10n.T("{{.Count}} platform(s) declared in the manifest are not shown in the download table and would be silently missing from the release notes: {{.List}}", map[string]any{
			"Count": len(missing),
			"List":  strings.Join(items, ", "),
		}),
	}))
}

// containsString 判断字符串切片里是否含某个字符串
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// assetURL 拼出某个平台发布产物的下载地址
func assetURL(m *release.Manifest, version string, p release.Platform) string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		m.Owner, m.Repo, version, m.AssetName(p))
}

// compareURL 拼出两版本之间的 GitHub compare 链接
func compareURL(m *release.Manifest, previous, version string) string {
	return fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s", m.Owner, m.Repo, previous, version)
}

// readmeURL 拼出仓库 README 的地址
func readmeURL(m *release.Manifest) string {
	return fmt.Sprintf("https://github.com/%s/%s/blob/main/README.md", m.Owner, m.Repo)
}

// readVersionSection 读取更新日志并摘出指定版本的小节
//
// 文件不存在不是错误而是"没有可摘内容"：很多仓库在首个版本发布前还没有 CHANGELOG，
// 此时应走"未找到小节"的默认提示，而不是让整个命令失败
func readVersionSection(root, file, version string) (string, bool, error) {
	buf, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("%s: %w", l10n.T("Failed to read the changelog file", nil), err)
	}
	text, found := extractVersionSection(string(buf), version)
	return text, found, nil
}

// extractVersionSection 从更新日志全文里摘出某版本小节的内容
//
// 规则复刻既有实现：遇到以 "##" 开头的行时，若已经进入目标小节则结束摘取；
// 否则判断该行是否包含版本串，命中即从下一行开始摘。二号及以上标题（"##"/"###"）都算分节，
// 这样小节内容里若再出现四级标题也不会被误当成分节边界
func extractVersionSection(content, version string) (string, bool) {
	var section []string
	found := false

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "##") {
			if found {
				break
			}
			// 用"包含"而不是"等于"：日志标题常写成 "## 1.2.3 - 2026-01-01" 这类带后缀的形式
			if strings.Contains(line, version) {
				found = true
			}
			// 标题行本身不进入小节内容
			continue
		}
		if found {
			section = append(section, line)
		}
	}

	if !found {
		return "", false
	}
	return strings.Join(section, "\n"), true
}
