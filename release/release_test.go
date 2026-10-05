package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/locales"
)

// TestMain 先把语言初始化为默认语言
//
// 这不是可选项：l10n.T 在未 Init 时按契约返回"未渲染的源串"（模板占位符不会被替换），
// 于是错误文案会变成 "Manifest field {{.Field}} is required" 这种带花括号的形态，
// 断言字段名之类的用例就失去意义。先 Init 才能观察到真实用户会看到的报错
func TestMain(m *testing.M) {
	layer := locales.Layer()
	if err := l10n.Init("en", l10n.Options{
		Default:   locales.Default,
		Supported: locales.Supported(),
		FS:        layer.FS,
		Dir:       layer.Dir,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "初始化语言失败:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// validManifest 返回一份各项齐备的清单，供各用例在此基础上做单点改动
// 之所以用构造函数而不是共享变量：用例之间会改动它，共享变量会让执行顺序影响结果
func validManifest() *Manifest {
	return &Manifest{
		App:            "flk",
		Owner:          "Jy-EggRoll",
		Repo:           "flk",
		AssetPrefix:    "flk",
		LDFLAGSPackage: "github.com/jy-eggroll/flk/cmd",
		Platforms: []Platform{
			{OS: "windows", Arch: "386"},
			{OS: "windows", Arch: "amd64"},
			{OS: "linux", Arch: "arm", ARM: "7"},
			{OS: "linux", Arch: "arm64"},
			{OS: "darwin", Arch: "amd64"},
			{OS: "freebsd", Arch: "amd64"},
		},
	}
}

// TestValidate 表驱动地覆盖必填项、空平台与重复平台的各类非法形态
// 用 mutate 逐个制造"只坏一处"的清单，保证失败原因可归因到被改动的那一个字段
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr bool
		// wantSub 非空时进一步断言错误文案里包含它，避免"报错但报错内容没法定位问题"
		wantSub string
	}{
		{name: "完整清单通过校验"},
		{
			name:    "缺 app",
			mutate:  func(m *Manifest) { m.App = "" },
			wantErr: true,
			wantSub: "app",
		},
		{
			name:    "app 只有空白等同于未填",
			mutate:  func(m *Manifest) { m.App = "   " },
			wantErr: true,
			wantSub: "app",
		},
		{
			name:    "缺 owner",
			mutate:  func(m *Manifest) { m.Owner = "" },
			wantErr: true,
			wantSub: "owner",
		},
		{
			name:    "缺 repo",
			mutate:  func(m *Manifest) { m.Repo = "" },
			wantErr: true,
			wantSub: "repo",
		},
		{
			name:    "缺 assetPrefix",
			mutate:  func(m *Manifest) { m.AssetPrefix = "" },
			wantErr: true,
			wantSub: "assetPrefix",
		},
		{
			name:    "缺 ldflagsPackage",
			mutate:  func(m *Manifest) { m.LDFLAGSPackage = "" },
			wantErr: true,
			wantSub: "ldflagsPackage",
		},
		{
			name:   "releaseNotesLang 为受支持语言时通过",
			mutate: func(m *Manifest) { m.ReleaseNotesLang = "zh-CN" },
		},
		{
			name:    "releaseNotesLang 不受支持时报错",
			mutate:  func(m *Manifest) { m.ReleaseNotesLang = "fr" },
			wantErr: true,
			wantSub: "fr",
		},
		{
			name:    "平台列表为空",
			mutate:  func(m *Manifest) { m.Platforms = nil },
			wantErr: true,
		},
		{
			name:    "平台缺 os",
			mutate:  func(m *Manifest) { m.Platforms = []Platform{{Arch: "amd64"}} },
			wantErr: true,
		},
		{
			name:    "平台缺 arch",
			mutate:  func(m *Manifest) { m.Platforms = []Platform{{OS: "linux"}} },
			wantErr: true,
		},
		{
			name: "平台重复（含 GOARM 不同也算重复，因为产物同名）",
			mutate: func(m *Manifest) {
				m.Platforms = []Platform{
					{OS: "linux", Arch: "arm", ARM: "7"},
					{OS: "linux", Arch: "arm", ARM: "5"},
				}
			},
			wantErr: true,
			wantSub: "linux/arm",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			if tc.mutate != nil {
				tc.mutate(m)
			}

			err := m.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("期望校验失败，实际通过")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望校验通过，实际失败: %v", err)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误文案 %q 未包含 %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestAssetName 锁定产物命名规则：<前缀>-<os>-<arch>，windows 追加 .exe
// 这是与既有项目（flk）反复核对过的形态，改动会让老用户升级失败，因此单独用表驱动固定
func TestAssetName(t *testing.T) {
	m := validManifest()

	cases := []struct {
		name string
		p    Platform
		want string
	}{
		{name: "windows/386 带 exe 后缀", p: Platform{OS: "windows", Arch: "386"}, want: "flk-windows-386.exe"},
		{name: "windows/amd64 带 exe 后缀", p: Platform{OS: "windows", Arch: "amd64"}, want: "flk-windows-amd64.exe"},
		{name: "linux/amd64 无后缀", p: Platform{OS: "linux", Arch: "amd64"}, want: "flk-linux-amd64"},
		{name: "linux/arm 的 GOARM 不进入文件名", p: Platform{OS: "linux", Arch: "arm", ARM: "7"}, want: "flk-linux-arm"},
		{name: "darwin/arm64", p: Platform{OS: "darwin", Arch: "arm64"}, want: "flk-darwin-arm64"},
		{name: "freebsd/amd64", p: Platform{OS: "freebsd", Arch: "amd64"}, want: "flk-freebsd-amd64"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.AssetName(tc.p); got != tc.want {
				t.Fatalf("AssetName(%s) = %q，期望 %q", tc.p, got, tc.want)
			}
		})
	}
}

// TestSupports 锁定"清单里没有的组合一律不支持"这一语义
func TestSupports(t *testing.T) {
	m := validManifest()

	cases := []struct {
		goos   string
		goarch string
		want   bool
	}{
		{"windows", "386", true},
		{"windows", "arm64", false}, // 清单里只有 386/amd64，arm64 未声明
		{"linux", "arm", true},      // 只看 os/arch，不看 GOARM
		{"linux", "mips", false},
		{"darwin", "amd64", true},
		{"darwin", "386", false},
		{"plan9", "amd64", false},
	}

	for _, tc := range cases {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			if got := m.Supports(tc.goos, tc.goarch); got != tc.want {
				t.Fatalf("Supports(%s, %s) = %v，期望 %v", tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

// TestPlatformString 锁定平台标识形态，报错与日志都依赖它
func TestPlatformString(t *testing.T) {
	if got := (Platform{OS: "linux", Arch: "arm", ARM: "7"}).String(); got != "linux/arm" {
		t.Fatalf("Platform.String() = %q，期望 %q", got, "linux/arm")
	}
}

// TestChangelogPath 锁定未配置时的默认值
func TestChangelogPath(t *testing.T) {
	if got := (&Manifest{}).ChangelogPath(); got != DefaultChangelogFile {
		t.Fatalf("未配置时 ChangelogPath() = %q，期望 %q", got, DefaultChangelogFile)
	}
	if got := (&Manifest{ChangelogFile: "docs/NEWS.md"}).ChangelogPath(); got != "docs/NEWS.md" {
		t.Fatalf("已配置时 ChangelogPath() = %q，期望 %q", got, "docs/NEWS.md")
	}
}

// TestValidateReleaseNotesLang 单独锁定 releaseNotesLang 的校验口径
//
// 与上表的通用用例分开：这里要连同"报错是否点名了非法取值与合法取值"一起断言，
// 光判"有错"不够——用户拿不到合法取值就无从修改
func TestValidateReleaseNotesLang(t *testing.T) {
	cases := []struct {
		name    string
		lang    string
		wantErr bool
	}{
		{name: "留空通过", lang: ""},
		{name: "受支持语言 en 通过", lang: "en"},
		{name: "受支持语言 zh-CN 通过", lang: "zh-CN"},
		{name: "地区变体视为受支持", lang: "en-US"},
		{name: "不受支持语言报错", lang: "fr", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			m.ReleaseNotesLang = tc.lang

			err := m.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望校验失败，实际通过")
				}
				// 报错必须同时点名非法取值与实际支持的取值，否则用户不知道能填什么
				if !strings.Contains(err.Error(), "fr") {
					t.Fatalf("错误文案应点名非法取值 fr，实际: %v", err)
				}
				if !strings.Contains(err.Error(), "zh-CN") {
					t.Fatalf("错误文案应列出合法取值，实际: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望校验通过，实际失败: %v", err)
			}
		})
	}
}

// TestChangelogPathFor 锁定"按语言取日志文件"的解析规则与默认值
func TestChangelogPathFor(t *testing.T) {
	m := &Manifest{
		ChangelogFile: "docs/NEWS.md",
		ChangelogFiles: map[string]string{
			"en": "CHANGELOG.en.md",
			// 值全为空白视为未配置，应回退 changelogFile，避免"配了个空串就再也读不到日志"
			"zh-CN": "   ",
		},
	}

	// 命中的语言取专属文件
	if got := m.ChangelogPathFor("en"); got != "CHANGELOG.en.md" {
		t.Fatalf("ChangelogPathFor(en) = %q，期望 %q", got, "CHANGELOG.en.md")
	}
	// 命中但值为空白的语言回退 changelogFile
	if got := m.ChangelogPathFor("zh-CN"); got != "docs/NEWS.md" {
		t.Fatalf("ChangelogPathFor(zh-CN) = %q，期望回退 %q", got, "docs/NEWS.md")
	}
	// 未配置的语言同样回退 changelogFile
	if got := m.ChangelogPathFor("fr"); got != "docs/NEWS.md" {
		t.Fatalf("ChangelogPathFor(fr) = %q，期望回退 %q", got, "docs/NEWS.md")
	}
	// 既无 changelogFiles 也无 changelogFile 时回退内置默认值
	if got := (&Manifest{}).ChangelogPathFor("en"); got != DefaultChangelogFile {
		t.Fatalf("未配置任何日志时 ChangelogPathFor = %q，期望 %q", got, DefaultChangelogFile)
	}
}

// TestLoad 覆盖"读文件 + 解析 + 校验"三个阶段各自的失败形态
// 全部在 t.TempDir 里造文件，不触碰真实仓库
func TestLoad(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("准备文件失败: %v", err)
		}
		return p
	}

	validPath := write("valid.json", `{
	  "app": "ggt",
	  "owner": "Jy-EggRoll",
	  "repo": "ggt",
	  "assetPrefix": "ggt",
	  "ldflagsPackage": "github.com/jy-eggroll/ggt/cmd",
	  "releaseNotesLang": "en",
	  "changelogFiles": {"en": "CHANGELOG.en.md"},
	  "platforms": [
	    {"os": "linux", "arch": "amd64"},
	    {"os": "windows", "arch": "amd64"}
	  ]
	}`)
	invalidJSON := write("broken.json", `{ "app": `)
	missingField := write("incomplete.json", `{"owner": "x", "repo": "y", "assetPrefix": "z", "ldflagsPackage": "p", "platforms": [{"os":"linux","arch":"amd64"}]}`)

	t.Run("读取并解析成功", func(t *testing.T) {
		m, err := Load(validPath)
		if err != nil {
			t.Fatalf("期望成功，实际: %v", err)
		}
		if m.App != "ggt" || len(m.Platforms) != 2 {
			t.Fatalf("解析结果不符: %+v", m)
		}
		// 未配置 changelogFile 时应回退默认值，且不改写结构体字段
		if m.ChangelogFile != "" || m.ChangelogPath() != DefaultChangelogFile {
			t.Fatalf("默认 changelog 处理不符: field=%q path=%q", m.ChangelogFile, m.ChangelogPath())
		}
		// 新增的两个字段应被正确解析：releaseNotesLang 直接读出，changelogFiles 按键命中
		if m.ReleaseNotesLang != "en" {
			t.Fatalf("releaseNotesLang = %q，期望 %q", m.ReleaseNotesLang, "en")
		}
		if got := m.ChangelogPathFor("en"); got != "CHANGELOG.en.md" {
			t.Fatalf("ChangelogPathFor(en) = %q，期望 %q", got, "CHANGELOG.en.md")
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		if _, err := Load(filepath.Join(dir, "nope.json")); err == nil {
			t.Fatalf("期望失败，实际通过")
		}
	})

	t.Run("JSON 语法错误", func(t *testing.T) {
		if _, err := Load(invalidJSON); err == nil {
			t.Fatalf("期望失败，实际通过")
		}
	})

	t.Run("校验失败时带上文件路径", func(t *testing.T) {
		_, err := Load(missingField)
		if err == nil {
			t.Fatalf("期望失败，实际通过")
		}
		if !strings.Contains(err.Error(), missingField) {
			t.Fatalf("错误文案应包含清单路径，实际: %v", err)
		}
	})
}
