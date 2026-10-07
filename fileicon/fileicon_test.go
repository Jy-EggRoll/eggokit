package fileicon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// 资源清单同时是接入契约：fileicon/ 这一级路径与文件名被调用方的静态资源服务直接引用
var assetPaths = []string{
	"fileicon/seti.js",
	"fileicon/seti.css",
	"fileicon/seti-icon-theme.json",
	"fileicon/vscode-language-map.json",
	"fileicon/seti.woff",
	"fileicon/LICENSE-seti-ui.txt",
	"fileicon/LICENSE-vscode.txt",
}

type iconDef struct {
	FontCharacter string `json:"fontCharacter"`
	FontColor     string `json:"fontColor"`
}

// lookup 是图标主题里的一段查找表，深浅两套表结构相同，因此共用这一个类型
type lookup struct {
	File           string            `json:"file"`
	FileNames      map[string]string `json:"fileNames"`
	FileExtensions map[string]string `json:"fileExtensions"`
	LanguageIds    map[string]string `json:"languageIds"`
}

type iconTheme struct {
	lookup
	IconDefinitions map[string]iconDef `json:"iconDefinitions"`
	Light           *lookup            `json:"light"`
}

type langTable struct {
	ByFileName  map[string]string `json:"byFileName"`
	ByExtension map[string]string `json:"byExtension"`
}

func readAsset(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(Assets(), name)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return data
}

func loadTheme(t *testing.T) iconTheme {
	t.Helper()
	var theme iconTheme
	if err := json.Unmarshal(readAsset(t, "fileicon/seti-icon-theme.json"), &theme); err != nil {
		t.Fatalf("解析图标主题失败: %v", err)
	}
	if theme.Light == nil {
		t.Fatal("图标主题缺少 light 段，浅色主题没有可用的查找表")
	}
	return theme
}

// TestAssets 确认资源齐全，且字体与两份 JSON 的内容都是可用的
func TestAssets(t *testing.T) {
	for _, p := range assetPaths {
		if _, err := fs.Stat(Assets(), p); err != nil {
			t.Errorf("资源 %s 不可读: %v", p, err)
		}
	}

	woff := readAsset(t, "fileicon/seti.woff")
	if len(woff) < 4 {
		t.Fatalf("seti.woff 只有 %d 字节，不可能是有效字体", len(woff))
	}
	if got := string(woff[:4]); got != "wOFF" {
		t.Errorf("seti.woff 前 4 字节为 %q，期望 wOFF", got)
	}

	loadTheme(t)
	var langs langTable
	if err := json.Unmarshal(readAsset(t, "fileicon/vscode-language-map.json"), &langs); err != nil {
		t.Fatalf("解析语言映射失败: %v", err)
	}
	if len(langs.ByFileName) == 0 || len(langs.ByExtension) == 0 {
		t.Errorf("语言映射缺少 byFileName 或 byExtension 段: %d/%d", len(langs.ByFileName), len(langs.ByExtension))
	}
}

// TestThemeLookups 校验三段查找表引用的图标 id 都真实存在，
// 以及 light 段确实是深色段的平行表（file 与三段查找表齐备）
func TestThemeLookups(t *testing.T) {
	theme := loadTheme(t)
	if len(theme.IconDefinitions) == 0 {
		t.Fatal("iconDefinitions 为空")
	}

	// 深色与浅色两套表都要查：漏掉任一表，图标到运行期才变成空字形，
	// 而这种缺失在 CSS 与字体层面完全看不出来
	tables := []struct {
		name string
		l    lookup
	}{
		{"深色", theme.lookup},
		{"浅色", *theme.Light},
	}
	for _, tb := range tables {
		if tb.l.File == "" {
			t.Errorf("%s表缺少兜底图标 file", tb.name)
		}
		for _, part := range []struct {
			name string
			m    map[string]string
		}{
			{"fileNames", tb.l.FileNames},
			{"fileExtensions", tb.l.FileExtensions},
			{"languageIds", tb.l.LanguageIds},
		} {
			if len(part.m) == 0 {
				t.Errorf("%s表缺少 %s 段", tb.name, part.name)
			}
			for key, id := range part.m {
				if _, ok := theme.IconDefinitions[id]; !ok {
					t.Errorf("%s表 %s[%s] 指向不存在的图标 %q", tb.name, part.name, key, id)
				}
			}
		}
		if _, ok := theme.IconDefinitions[tb.l.File]; !ok {
			t.Errorf("%s表 file 指向不存在的图标 %q", tb.name, tb.l.File)
		}
	}

	// 语言映射里的语言 id 必须能在图标主题的 languageIds 里落到图标，
	// 否则第三级查找的部分条目永远查不到东西
	var langs langTable
	if err := json.Unmarshal(readAsset(t, "fileicon/vscode-language-map.json"), &langs); err != nil {
		t.Fatalf("解析语言映射失败: %v", err)
	}
	for _, part := range []struct {
		name string
		m    map[string]string
	}{
		{"byFileName", langs.ByFileName},
		{"byExtension", langs.ByExtension},
	} {
		missing := map[string]bool{}
		for key, lang := range part.m {
			if _, ok := theme.LanguageIds[lang]; !ok {
				missing[lang] = true
				t.Errorf("语言映射 %s[%s] 的语言 id %q 在图标主题的 languageIds 里不存在", part.name, key, lang)
			}
		}
	}
}

// TestFontCharacters 等价于 JS 侧 iconGlyph 的语义：fontCharacter 是反斜杠加 1~6 位十六进制，
// 必须能解析成码位，否则前端只能拿到空字形
func TestFontCharacters(t *testing.T) {
	theme := loadTheme(t)
	re := regexp.MustCompile(`^\\([0-9A-Fa-f]{1,6})$`)
	for id, def := range theme.IconDefinitions {
		m := re.FindStringSubmatch(def.FontCharacter)
		if m == nil {
			t.Errorf("图标 %s 的 fontCharacter %q 不是反斜杠加 1~6 位十六进制", id, def.FontCharacter)
			continue
		}
		var cp int64
		for _, c := range []byte(m[1]) {
			cp = cp*16 + int64(strings.IndexByte("0123456789abcdef", c|0x20))
		}
		if cp <= 0 {
			t.Errorf("图标 %s 解析出的码位为 %d", id, cp)
		}
	}
}

// TestOverlay 覆盖叠层的四种结果：own 命中、回退到嵌入资源、两边都没有、非法路径名
func TestOverlay(t *testing.T) {
	own := fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("own index")},
		"fileicon/seti.js":  &fstest.MapFile{Data: []byte("own seti.js")},
		"fileicon/seti.css": &fstest.MapFile{Data: []byte("own seti.css")},
	}
	overlay := Overlay(own)

	// own 命中优先：同名文件必须取调用方自己那份，不能被嵌入资源盖掉
	for _, p := range []string{"index.html", "fileicon/seti.js", "fileicon/seti.css"} {
		data, err := fs.ReadFile(overlay, p)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", p, err)
		}
		if !strings.HasPrefix(string(data), "own ") {
			t.Errorf("%s 取到的是 %q，期望 own 优先", p, data)
		}
	}

	// own 缺的文件回退给嵌入资源
	data, err := fs.ReadFile(overlay, "fileicon/seti.woff")
	if err != nil {
		t.Fatalf("回退读取 fileicon/seti.woff 失败: %v", err)
	}
	if len(data) == 0 || string(data[:4]) != "wOFF" {
		t.Errorf("回退读取的 woff 内容不对: %d 字节", len(data))
	}

	// 两边都没有时错误要能被 errors.Is 识别成 fs.ErrNotExist
	if _, err := overlay.Open("fileicon/nope.js"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("缺失文件的错误为 %v，期望 fs.ErrNotExist", err)
	}

	// 非法路径名直接拒掉，不往下传给任何一个 FS
	for _, bad := range []string{"../x", "/abs", "fileicon/../x", ""} {
		if _, err := overlay.Open(bad); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("非法路径 %q 的错误为 %v，期望 fs.ErrInvalid", bad, err)
		}
	}

	// own 为 nil 时等价于 Assets()
	bare := Overlay(nil)
	if _, err := fs.ReadFile(bare, "fileicon/seti.js"); err != nil {
		t.Errorf("Overlay(nil) 读取嵌入资源失败: %v", err)
	}
	if _, err := bare.Open("../x"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Overlay(nil) 未拒绝非法路径: %v", err)
	}
}
