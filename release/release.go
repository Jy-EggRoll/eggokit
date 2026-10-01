// Package release 收敛各命令行项目「发版」环节里重复度最高的两类知识：平台清单与产物命名
//
// 背景：flk、ggt 这类项目各自维护一份 Taskfile，用十几个 build:* 任务交叉编译、用一段
// shell 生成更新日志、再用手写表格描述"哪些平台有产物"。这些内容在多个仓库里被复制了
// 三四遍，任一处漏改都会制造"发布了但升级器认不出产物"这种极难排查的问题
//
// 本包把「有哪些平台」「产物叫什么名字」抽成单一事实源 Manifest：构建、发布说明、以及
// 下游的升级器都可以从同一份清单推导，因此三者不会再各说各话
//
// 本包刻意只做纯数据处理——不读 git、不执行 go build、不产生任何输出，
// 也不决定用哪种语言展示。命令行工具负责把 Manifest 翻译成具体的构建命令与展示文案
package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/locales"
)

// DefaultChangelogFile 是 Manifest.ChangelogFile 未配置时的默认文件名
//
// 之所以在这里声明而不是散落在命令行工具里：默认值是"清单的语义"的一部分，
// 让清单类型自己给出，才能保证 buildall 与 changelog 两个工具用的是同一个缺省
const DefaultChangelogFile = "CHANGELOG.md"

// Manifest 是项目发布配置的单一事实源，各项目的 release.json 由它描述
//
// 字段之所以用 json 小驼峰而不是下划线：这份文件是给人读写的项目配置，与 Go 结构体字段
// 一一对应；同时它也是跨仓库共享的约定，命名一旦改动就要同步所有项目的 release.json
type Manifest struct {
	// App 是程序名，如 flk，必填。用于生成资产名、安装提示里的可执行文件名等
	App string `json:"app"`

	// Owner 与 Repo 指向承载 Release 的 GitHub 仓库，均必填
	// 二者拼出下载地址与 compare 链接，因此大小写敏感——GitHub 的仓库路径就是这样用的
	Owner string `json:"owner"`
	Repo  string `json:"repo"`

	// AssetPrefix 是资产名前缀（不含系统与架构），必填，如 flk
	// 与 updater.Config.AssetName 的产物前缀是同一个值：升级器按它匹配资产，
	// 构建按它命名产物，两处一旦不一致，用户会下载到"不存在的资产"
	AssetPrefix string `json:"assetPrefix"`

	// LDFLAGSPackage 是版本注入目标的包路径，必填，如 github.com/jy-eggroll/flk/cmd
	// 构建时通过 -X <包>.Version=... 与 -X <包>.BuildTime=... 注入
	//
	// 有两条容易踩的坑：包路径写错不会报错，只会让版本号静默停留在零值；
	// 且它必须指向声明 Version/BuildTime 的**那个包**——若这两个变量声明在 main 包里，
	// 链接器符号名是 "main.Version" 而非模块路径，写全导入路径同样会静默失效，
	// 因此惯例是把它们放在一个非 main 的包（如 cmd）里再引用
	LDFLAGSPackage string `json:"ldflagsPackage"`

	// ProxyPrefix 是备用下载代理前缀，可空
	// 本包只负责携带这个配置，具体用途见 updater.Config.ProxyPrefix（部分网络环境下直连不稳）
	ProxyPrefix string `json:"proxyPrefix,omitempty"`

	// ChangelogFile 是更新日志文件的文件名，相对项目根，留空时用 DefaultChangelogFile
	// 保留可配置是为了兼容把日志放在 docs/ 之类位置的仓库，但绝大多数项目直接用默认值
	//
	// 它是 ChangelogFiles 各语言的兜底：某个语言在 ChangelogFiles 里没有专属文件时用这一份
	ChangelogFile string `json:"changelogFile,omitempty"`

	// ChangelogFiles 是按语言的更新日志文件映射（语言标签 -> 相对项目根的文件路径），可空
	//
	// 用途：多语言项目常维护多份 CHANGELOG（如 CHANGELOG.md 与 CHANGELOG.en.md），
	// 当发布说明选英文时，正文就该取英文那份，否则会出现"英文说明里嵌着中文更新内容"的割裂
	// 查表用的键是"当前语言"（即发布说明实际生效的语言，与 l10n 归一化后的标签一致），
	// 未命中时回退 ChangelogFile，因此只维护单一日志的项目无需关心这个字段
	ChangelogFiles map[string]string `json:"changelogFiles,omitempty"`

	// ReleaseNotesLang 是发布说明的默认语言，可空；留空时命令行工具回退内置默认（en）
	//
	// 之所以放进清单：发布说明用哪种语言发版是"项目级"的稳定约定，写在这里后每个发布命令
	// 都不必重复指定；命令行 --lang 仍可临时覆盖它（优先级见 relcli.ResolveLanguage）
	//
	// 取值必须是 eggokit 自带 locales 支持的语言（当前为 en、zh-CN），否则校验失败：
	// 静默接受一个拼错的语言，会让 l10n 悄悄回退到默认语言，用户以为切换生效、实则完全没有
	ReleaseNotesLang string `json:"releaseNotesLang,omitempty"`

	// Platforms 是本项目实际发布的平台列表，必填且非空
	// 它同时约束构建产物与发布说明里的下载表格：清单里没有的组合一律视为"不支持"，
	// 因此这份列表就是"本项目支持哪些平台"的唯一真相，不要再在别处手写一份
	Platforms []Platform `json:"platforms"`
}

// Platform 描述一个发布目标平台，与 Go 交叉编译的三个变量一一对应
type Platform struct {
	// OS 是 GOOS，必填，如 windows、linux、darwin、freebsd
	OS string `json:"os"`

	// Arch 是 GOARCH，必填，如 386、amd64、arm、arm64
	Arch string `json:"arch"`

	// ARM 是 GOARM，仅 linux/arm 需要（如 "7"）
	// 其余平台留空；填错不会让构建失败，只会悄悄改变生成的指令集，因此只在 arm 上使用
	ARM string `json:"arm,omitempty"`
}

// String 返回 "os/arch" 形态的平台标识，用于报错与日志
//
// 刻意不含 GOARM：同一 os/arch 只允许出现一次（见 Validate 的去重规则），
// 因此 "linux/arm" 足以唯一定位一个平台，把 armv7 拼进去反而会让标识冗长且难对照
func (p Platform) String() string {
	return p.OS + "/" + p.Arch
}

// Load 读取并校验一份发布清单
//
// 校验放在 Load 里而不是留给调用方：清单是后续所有构建与展示的输入，带着错误继续走下去
// 只会产出命名错乱的资产或空白的发布说明；在入口一次拦下，代价最低
func Load(path string) (*Manifest, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to read the release manifest", nil), err)
	}

	var m Manifest
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to parse the release manifest", nil), err)
	}
	if err := m.Validate(); err != nil {
		// 带上文件路径：命令行工具的报错里必须能看出"是哪份清单有问题"，否则多项目切换时无从下手
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// Validate 校验清单的必填项、平台列表与发布说明语言
//
// 只做"结构性"与"取值白名单"校验（必填、非空、去重、releaseNotesLang 是否受支持），
// 不校验 OS/Arch 是否属于 Go 支持的取值：那属于 go build 的职责，
// 且 Go 的支持列表会随版本变化，在库里再维护一份必然漂移
func (m *Manifest) Validate() error {
	// 逐个检查而不是一句大错误：报错要能直接指出缺的是哪个字段，
	// 否则用户得反复对照文档才能定位
	required := []struct {
		field string
		value string
	}{
		{"app", m.App},
		{"owner", m.Owner},
		{"repo", m.Repo},
		{"assetPrefix", m.AssetPrefix},
		{"ldflagsPackage", m.LDFLAGSPackage},
	}
	for _, r := range required {
		// 纯空白等同于未填：配置里一个空格是很常见的误输入，放行只会把问题推后
		if strings.TrimSpace(r.value) == "" {
			return errors.New(l10n.T("Manifest field {{.Field}} is required", map[string]any{"Field": r.field}))
		}
	}

	if len(m.Platforms) == 0 {
		return errors.New(l10n.T("The manifest must declare at least one platform", nil))
	}

	// releaseNotesLang 若填写，必须是受支持的语言：拼错的语言会被 l10n 静默回退到默认语言，
	// 用户对这种"看起来生效了"最没防备，因此在入口直接拦下并把合法取值一并列出
	if strings.TrimSpace(m.ReleaseNotesLang) != "" {
		supported := locales.Supported()
		if !l10n.IsSupported(m.ReleaseNotesLang, supported) {
			return errors.New(l10n.T("releaseNotesLang {{.Value}} is not a supported language; supported languages are: {{.Supported}}", map[string]any{
				"Value":     m.ReleaseNotesLang,
				"Supported": strings.Join(supported, ", "),
			}))
		}
	}

	// 按 os/arch 去重：资产名由 <前缀>-<os>-<arch> 组成，不含 GOARM，
	// 因此同一 os/arch 出现两次会生成同名产物，后构建的会覆盖先构建的——
	// 表面上"成功了"，实际少了一个平台的文件
	seen := make(map[string]bool, len(m.Platforms))
	for i, p := range m.Platforms {
		if strings.TrimSpace(p.OS) == "" || strings.TrimSpace(p.Arch) == "" {
			return errors.New(l10n.T("Platform #{{.Index}} must provide both os and arch", map[string]any{"Index": i + 1}))
		}
		if seen[p.String()] {
			return errors.New(l10n.T("Duplicate platform {{.Platform}} in the manifest; asset names would collide", map[string]any{"Platform": p.String()}))
		}
		seen[p.String()] = true
	}
	return nil
}

// AssetName 返回某平台对应发布产物的文件名
//
// 规则是 <AssetPrefix>-<os>-<arch>，windows 追加 .exe，与 flk 等项目的既有产物完全一致：
// 升级器按同一个前缀在 Release 资产里找文件，命名一旦变化，老版本用户就再也升不上来，
// 因此这里刻意不做任何"优化命名"的空间
func (m *Manifest) AssetName(p Platform) string {
	name := fmt.Sprintf("%s-%s-%s", m.AssetPrefix, p.OS, p.Arch)
	if p.OS == "windows" {
		// 扩展名纳入名称，使匹配结果精确到 Windows 产物本身，
		// 避免同目录同时存在带与不带扩展名的同类文件时命中错误目标
		name += ".exe"
	}
	return name
}

// Supports 报告清单里是否存在给定 os/arch 的发布目标
//
// 发布说明与升级器都依赖它判断"这个组合该不该给出下载链接"：清单里没有的组合会显示为
// "不支持"，而不是给出一个必然 404 的链接
func (m *Manifest) Supports(goos, goarch string) bool {
	for _, p := range m.Platforms {
		if p.OS == goos && p.Arch == goarch {
			return true
		}
	}
	return false
}

// ChangelogPath 返回更新日志相对项目根的文件名，未配置时回退默认值
//
// 做成方法而不是在 Load 里改写字段：默认值属于"读取时的语义"，直接写回结构体会让
// 调用方无法分辨"清单里明确写了 CHANGELOG.md"与"清单没写、由本包兜底"
func (m *Manifest) ChangelogPath() string {
	if strings.TrimSpace(m.ChangelogFile) == "" {
		return DefaultChangelogFile
	}
	return m.ChangelogFile
}

// ChangelogPathFor 返回指定语言下应读取的更新日志文件
//
// 解析规则：先看 ChangelogFiles[lang]，命中且非空白即用它；否则回退 ChangelogPath()（含默认值）
//
// 之所以把"按语言挑文件"收进本类型而不是留给调用方：文件选择与当前语言强绑定，
// 散在调用方会让"取哪个文件"和"渲染成哪种语言"各说各话，最终产出正文与语言对不上的说明
func (m *Manifest) ChangelogPathFor(lang string) string {
	if p, ok := m.ChangelogFiles[lang]; ok && strings.TrimSpace(p) != "" {
		return p
	}
	return m.ChangelogPath()
}
