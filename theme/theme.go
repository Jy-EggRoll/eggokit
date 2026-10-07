// Package theme 解析 VSCode 格式的主题文档，把它合并成一份可直接取用的颜色表。
//
// 为什么选 VSCode 的主题格式：它是配色文档事实上的通用格式，官方自带的十几套、以及
// Catppuccin 这类第三方主题都是这个格式。与其自创一套再要求别人转换，不如直接读它们。
//
// 一套主题的解析顺序与 VSCode 一致，三层，后面的覆盖前面的：
//
//  1. 注册表默认值（defaults.json，本库收录的令牌子集，取值逐条照抄 VSCode 源码）
//  2. 主题文件的 include 链，从最底层往上覆盖（2026-dark -> dark_modern -> dark_plus -> dark_vs）
//  3. 主题文件自身的 colors
//
// 第 1 层是“与 VSCode 视觉一致”的关键：gitDecoration.*、diffEditor.* 这些颜色本来就不在主题
// 文件里，而在颜色注册表的默认值里——官方主题没写它们，VSCode 用的就是那些默认值。
//
// 内置的主题文件是逐字内嵌的上游文件（含 MIT 许可，见 builtin/*/LICENSE.txt），与调用方自己
// 放进来的主题走同一条解析路径，唯一的区别是“从哪儿读字节”。
//
// 本包与调用方的分界：
//   - 本包负责：解析机制、VSCode 颜色注册表的默认值、内置主题文件
//   - 调用方负责：它自己要哪些颜色 id、id 到自家变量名的映射，以及 VSCode 里没有对应物的
//     自有令牌（调用方自己的视觉概念）的取值
//
// 因此本包里不该出现任何一个具体页面的视觉概念——出现即说明这条分界被打破了。
package theme

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultDarkID / DefaultLightID 是“跟随系统深浅”时用的两套内置主题。
//
// 取的是 VSCode 当前的默认主题——vscode 的 ThemeSettingDefaults.COLOR_THEME_DARK 值为
// "Dark 2026"、"Light 2026"（源码 src/vs/workbench/services/themes/common/workbenchThemeService.ts），
// 对应的就是这两个文件
const (
	DefaultDarkID  = builtinPrefix + "vscode/2026-dark.json"
	DefaultLightID = builtinPrefix + "vscode/2026-light.json"
)

// builtinPrefix 是内置主题 id 的前缀。
//
// 为什么是“前缀 + 来源目录”：外部主题的 id 是它的绝对路径，而文件名可能与内置主题重名
// （有人会把 dark_vs.json 复制出来改），带上前缀与来源目录就不存在“谁遮住谁”的歧义
const builtinPrefix = "builtin:"

// builtinGroups 是内嵌主题的来源分组，顺序即主题选择器里的顺序。
//
// Label 是分组标题，直接用来源的名字、不翻译——它们是各自的品牌名（VSCode、Catppuccin），
// 与“要不要翻译”无关；Dir 是 builtin/ 下的子目录，同时也进主题 id，
// 用来保证不同来源之间不会因为同名文件而打架
var builtinGroups = []struct {
	Label string
	Dir   string
	// Files 列出的都是上游文件的原样拷贝，顺序即同组内的展示顺序
	Files []string
}{
	{
		Label: "VSCode",
		Dir:   "vscode",
		// 四套深色、四套浅色。它们互为 include 链的成员，这里各自算一套，也是因为 VSCode
		// 自己就把它们并列成可选主题（Dark+/Light+ 与 Dark Modern/Light Modern 都在菜单里）
		Files: []string{
			"2026-dark.json", "dark_modern.json", "dark_plus.json", "dark_vs.json",
			"2026-light.json", "light_modern.json", "light_plus.json", "light_vs.json",
		},
	},
	{
		Label: "Catppuccin",
		Dir:   "catppuccin",
		// 四套口味，浅色的 Latte 在前。它们都是自包含的（没有 include），且自带 type 字段
		Files: []string{"latte.json", "frappe.json", "macchiato.json", "mocha.json"},
	},
}

//go:embed builtin
var builtinFS embed.FS

// defaultsJSON 是颜色注册表的默认值，即本库收录的那部分令牌。
//
// 取值逐条照抄 VSCode 自己的注册表定义（本机 /tmp/vscode-src 的浅克隆，提交 45373f0）：
//
//	editor.background                                    src/vs/platform/theme/common/colors/baseColors.ts
//	foreground / descriptionForeground                   src/vs/platform/theme/common/colors/baseColors.ts
//	focusBorder                                          src/vs/platform/theme/common/colors/baseColors.ts
//	list.hoverBackground                                 src/vs/platform/theme/common/colors/listColors.ts
//	badge.background / badge.foreground                  src/vs/platform/theme/common/colors/miscColors.ts
//	button.* / dropdown.* / input.*                      src/vs/platform/theme/common/colors/inputColors.ts
//	editorWidget.background                              src/vs/platform/theme/common/colors/editorColors.ts
//	widget.shadow                                        src/vs/platform/theme/common/colors/editorColors.ts
//	diffEditor.{inserted,removed}LineBackground          src/vs/platform/theme/common/colors/editorColors.ts
//	diffEditor.{inserted,removed}TextBackground          src/vs/platform/theme/common/colors/editorColors.ts
//	                                                     前者铺整行、后者盖行内变化的字符，两者都是
//	                                                     半透明色，页面里叠在一起用
//	sideBar.background                                   src/vs/workbench/common/theme.ts
//	scmGraph.foreground1..5                              src/vs/workbench/contrib/scm/browser/scmHistory.ts 的 colorRegistry
//	scmGraph.historyItemRefColor / RemoteRefColor        scmHistory.ts 里引用 charts.blue / charts.purple，
//	                                                     最终落在 editorInfo.foreground（baseColors.ts）与
//	                                                     charts.purple（chartsColors.ts）两个字面值上
//	gitDecoration.*                                      extensions/git/package.json 的 contributes.colors[].defaults
//	scrollbarSlider.*                                    src/vs/platform/theme/common/colors/miscColors.ts
//	                                                     上游写作 fromHex(...).transparent(0.4/0.7/0.4)，是
//	                                                     “字面色 + 固定透明度”，换算成 8 位十六进制后仍可逐位核对
//	                                                     （与 keybindingLabel.* 同一情形，不属于下面那条排除）
//
// 主题文件里写了同名令牌时以主题为准，这份只负责“主题没写的那些”——而 VSCode 的官方主题
// 恰好就没写 gitDecoration.* 与 diffEditor.*，它们一直用的就是这里的默认值
//
// 收录标准是“上游注册表给得出字面值”：button.hoverBackground 这类在上游是 lighten()/darken()
// 算出来的，这里不收（收了就得把换算结果固化成字面量，日后与上游脱钩且无法逐条核对）。
// 调用方若需要这张表以外的令牌，自行覆盖本表、或在它自己的样式表里用 CSS 变量回退——
// 本库不预设谁需要哪些颜色
//
//go:embed defaults.json
var defaultsJSON []byte

// Type 主题的明暗类型。
//
// 只有深/浅两种：VSCode 另有高对比（hcDark/hcLight）两个类型，但那是为无障碍场景专门设计的
// 另一套视觉（取值与深/浅差别很大），支持它得连视觉设计一起做，不是换个色值就行，故暂不支持
type Type string

const (
	Dark  Type = "dark"
	Light Type = "light"
)

// Theme 一套可用的主题。
type Theme struct {
	// ID 选中它时用的标识：内置主题是 builtin: 前缀加官方文件名，外部主题是文件的绝对路径
	ID string
	// Name 展示名，取自主题文件的 name 字段；没有就退回文件名（不含扩展名）
	Name string
	// Type 明暗类型。外部主题大多不写 type（VSCode 把它放在扩展的 package.json 里），
	// 这时按背景色的亮度推断，见 inferType
	Type Type
	// Builtin 是否为内嵌的主题
	Builtin bool
	// Group 是来源分组标题：内置主题取来源名（VSCode、Catppuccin），
	// 用户放进来的为空——空表示“调用方自己的”，标题由调用方按当前语言给出
	Group string
}

// Resolved 是解析完成的主题。
//
// Colors 以 VSCode 的颜色 id 为键（形如 gitDecoration.modifiedResourceForeground），
// 含主题链里写到的全部颜色，以及主题没写、但注册表里有默认值的那些。
// 使用方按自己要用的 id 去取——本包不预设谁需要哪些颜色
type Resolved struct {
	Theme
	Colors map[string]string
}

// Available 列出全部可用主题：先是内置的官方主题，再是用户放进来的（按名字排序）。
//
// dirs 是用户放主题的目录（调用方通常传配置目录本身加上它的 themes 子目录，两个都看）。
// 目录不存在、或某个文件解析不了，都只是跳过它——用户还没放主题是正常状态，一份手抄错的
// JSON 也不该让整个主题列表消失
func Available(dirs []string) []Theme {
	out := make([]Theme, 0, 16)
	for _, g := range builtinGroups {
		for _, f := range g.Files {
			if t, err := peek(embedSource{}, path.Join(g.Dir, f), g.Label); err == nil {
				out = append(out, t)
			}
		}
	}

	var user []Theme
	seen := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// 只收 .json：VSCode 主题文件都是这个扩展名。同目录里调用方自己的配置文件虽然
			// 也是 .json，但会被 peek 判成“不是主题”而跳过
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if seen[full] {
				continue // 配置目录与 themes 子目录都在扫描范围内，同一个文件别列两次
			}
			seen[full] = true
			if t, err := peek(diskSource{}, full, ""); err == nil {
				user = append(user, t)
			}
		}
	}
	sort.Slice(user, func(i, j int) bool { return user[i].Name < user[j].Name })
	return append(out, user...)
}

// Resolve 按 id 解析一套主题。id 为内置主题的 id 或外部主题文件的绝对路径。
//
// 返回的颜色表里既有主题链写到的颜色，也有主题没写、但注册表里有默认值的那些；本包不做筛选，
// 因为预设不了调用方需要哪些令牌，全量返回由调用方按 id 自取
func Resolve(id string) (*Resolved, error) {
	var src fsys
	var rel string
	builtin := strings.HasPrefix(id, builtinPrefix)
	if builtin {
		src, rel = embedSource{}, strings.TrimPrefix(id, builtinPrefix)
	} else {
		if !filepath.IsAbs(id) {
			return nil, fmt.Errorf("theme id must be a builtin id or an absolute path: %q", id)
		}
		src, rel = diskSource{}, id
	}

	colors, declared, name, err := loadChain(src, rel, map[string]bool{})
	if err != nil {
		return nil, err
	}

	t := Type(declared)
	if t != Dark && t != Light {
		t = inferType(colors)
	}
	base, err := defaultsFor(t)
	if err != nil {
		return nil, err
	}
	// 以注册表默认值为准，主题链里写的覆盖它——这是“与 VSCode 视觉一致”的关键一步：
	// 例如 2026-light 就没有写 diffEditor.*LineBackground，VSCode 用的正是注册表默认值
	merged := make(map[string]string, len(base)+len(colors))
	for id, v := range base {
		merged[id] = v
	}
	for id, v := range colors {
		merged[id] = v
	}

	if name == "" {
		name = strings.TrimSuffix(filepath.Base(rel), ".json")
	}
	group := ""
	if builtin {
		group = builtinGroupOf(rel)
	}
	return &Resolved{
		Theme:  Theme{ID: id, Name: name, Type: t, Builtin: builtin, Group: group},
		Colors: merged,
	}, nil
}

// builtinGroupOf 从内置主题的相对路径（形如 vscode/2026-dark.json）里取出分组标题
func builtinGroupOf(rel string) string {
	dir, _, _ := strings.Cut(rel, "/")
	for _, g := range builtinGroups {
		if g.Dir == dir {
			return g.Label
		}
	}
	return ""
}

// withinDir 判断 inc 是否落在 base 目录之内。两者用的是同一套坐标：内嵌主题是斜杠分隔的
// 相对路径，外部主题是文件系统路径
func withinDir(base, inc string) bool {
	rel, err := filepath.Rel(filepath.FromSlash(base), filepath.FromSlash(inc))
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}

// fsys 是读主题文件的口子：内置主题读内嵌 FS，外部主题读真实文件系统。
// 抽出这一层，是因为两者的差别只有“从哪儿读字节”，include 链的解析逻辑完全共用
type fsys interface {
	Read(rel string) ([]byte, error)
}

type embedSource struct{}

func (embedSource) Read(rel string) ([]byte, error) {
	return fs.ReadFile(builtinFS, path.Join("builtin", rel))
}

type diskSource struct{}

func (diskSource) Read(rel string) ([]byte, error) {
	return os.ReadFile(rel)
}

// themeDoc 是主题文件里本包关心的那几个字段。其余字段（tokenColors 等语法高亮配置）
// 本包一概不处理，直接忽略——json 解码天然如此，不需要额外声明
type themeDoc struct {
	Name    string            `json:"name"`
	Include string            `json:"include"`
	Type    string            `json:"type"`
	Colors  map[string]string `json:"colors"`
}

// loadChain 沿 include 链把颜色合并成一份：先递归取被 include 的，再用本文件覆盖它。
// 返回合并后的颜色、本层声明的类型（可能为空）、本层的 name。
//
// seen 用来挡循环 include：主题文件之间互相 include（哪怕间接）会让递归停不下来，
// 而这类错误光看堆栈很难看出是主题文件的问题
func loadChain(src fsys, rel string, seen map[string]bool) (map[string]string, string, string, error) {
	if seen[rel] {
		return nil, "", "", fmt.Errorf("theme %s: include cycle detected", rel)
	}
	seen[rel] = true

	raw, err := src.Read(rel)
	if err != nil {
		return nil, "", "", fmt.Errorf("read theme %s: %w", rel, err)
	}
	doc, err := parseDoc(raw)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse theme %s: %w", rel, err)
	}

	colors := map[string]string{}
	declared, name := "", doc.Name
	if doc.Include != "" {
		inc := doc.Include
		if !path.IsAbs(inc) && !filepath.IsAbs(inc) {
			// include 是相对路径，相对的是当前文件所在目录
			inc = path.Join(path.Dir(rel), inc)
		}
		// include 只允许落在主题文件自己所在的目录树里：主题文件是从别处粘进来的第三方文件，
		// 里面的 include 若是 ../../x.json 这种路径，就等于让本包按它去读系统上任意文件。
		// 官方与 Catppuccin 的主题 include 的都是同目录下的兄弟文件，这条限制不会误伤
		if !withinDir(path.Dir(rel), inc) {
			return nil, "", "", fmt.Errorf("theme %s: include escapes its own directory: %q", rel, doc.Include)
		}
		base, baseType, _, err := loadChain(src, inc, seen)
		if err != nil {
			return nil, "", "", err
		}
		for k, v := range base {
			colors[k] = v
		}
		declared = baseType
	}
	// 本层的 colors 覆盖被 include 的；本层声明的 type 也覆盖底层声明的
	for k, v := range doc.Colors {
		colors[k] = v
	}
	if doc.Type != "" {
		declared = doc.Type
	}
	return colors, declared, name, nil
}

// peek 只读到“够列进选择器”的程度：名字、类型与来源分组。
// 解析失败时返回错误，由调用方跳过它。group 是来源分组的标题（内置主题才有）
func peek(src fsys, rel, group string) (Theme, error) {
	raw, err := src.Read(rel)
	if err != nil {
		return Theme{}, err
	}
	doc, err := parseDoc(raw)
	if err != nil {
		return Theme{}, err
	}
	// 认不出是主题的文件不算主题：调用方自己的配置文件常常就放在同一个目录里，
	// 它既没有 name 也没有 colors
	if doc.Name == "" && doc.Include == "" && len(doc.Colors) == 0 {
		return Theme{}, errors.New("not a theme file (no name/include/colors)")
	}

	builtin := false
	id := rel
	if _, ok := src.(embedSource); ok {
		builtin = true
		id = builtinPrefix + rel
	}
	name := doc.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(rel), ".json")
	}
	t := Type(doc.Type)
	if t != Dark && t != Light {
		// 主题文件大多不写 type，这里只能按它自己声明的背景色推断；
		// 连背景色都没有的（比如只写了 include 的那几层）就等真正解析时再定
		t = inferType(doc.Colors)
	}
	return Theme{ID: id, Name: name, Type: t, Builtin: builtin, Group: group}, nil
}

// inferType 在主题没有声明 type 时按背景色亮度推断深浅。
//
// 为什么需要推断：VSCode 主题文件几乎都不写 type，它写在扩展的 package.json 的 uiTheme 里，
// 而我们拿到的只是一个孤立的主题文件。推断只看 editor.background（没有就看 sideBar.background），
// 用 sRGB 加权亮度与 0.5 比较——这是业界常见的做法，对深/浅两类的判别足够可靠
func inferType(colors map[string]string) Type {
	bg := colors["editor.background"]
	if bg == "" {
		bg = colors["sideBar.background"]
	}
	// 复用 contrast.go 的颜色解析：同一个包里不该有两份解析规则（它们对 #RGBA 的支持就不一样，
	// 曾各自存在过，迟早会给出不同答案）
	c, ok := parseColor(bg)
	if !ok {
		return Dark
	}
	lum := (0.2126*c.r + 0.7152*c.g + 0.0722*c.b) / 255
	if lum > 0.5 {
		return Light
	}
	return Dark
}

// parseHex 已并入 contrast.go 的 parseColor，本包只保留一份颜色解析规则

// defaultsFor 取出某个类型的注册表默认值
func defaultsFor(t Type) (map[string]string, error) {
	var all map[string]map[string]string
	if err := json.Unmarshal(defaultsJSON, &all); err != nil {
		return nil, fmt.Errorf("parse defaults.json: %w", err)
	}
	base, ok := all[string(t)]
	if !ok {
		return nil, fmt.Errorf("defaults.json has no section for type %q", t)
	}
	return base, nil
}

// parseDoc 把主题文件解成 themeDoc。
// 先过一遍 JSONC 规整（VSCode 的主题文件带注释与尾逗号，官方那几套本身就带），
// 否则官方的主题文件一个都读不了
func parseDoc(raw []byte) (themeDoc, error) {
	var doc themeDoc
	if err := json.Unmarshal(sanitizeJSONC(raw), &doc); err != nil {
		return themeDoc{}, err
	}
	return doc, nil
}
