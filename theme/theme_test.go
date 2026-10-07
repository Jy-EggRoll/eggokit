package theme

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestSanitizeJSONC 覆盖 JSONC 规整的四种情形。前两种是 VSCode 官方主题文件里真实存在的，
// 后两种是“看着像注释”的问题：字符串里的 // 绝不能被当成注释
func TestSanitizeJSONC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"行注释", "{\n// 注释\n\"a\": 1\n}", "{\n\n\"a\": 1\n}"},
		{"块注释", "{\"a\": /* 注释 */ 1}", "{\"a\":  1}"},
		{"尾逗号", "{\"a\": [1,2,],\"b\": 2,}", "{\"a\": [1,2],\"b\": 2}"},
		{"尾逗号与注释之间", "{\n\"a\": 1, // 说明\n}", "{\n\"a\": 1 \n}"},
		{"字符串里的双斜杠不是注释", "{\"a\": \"https://x/y\"}", "{\"a\": \"https://x/y\"}"},
		{"字符串里的尾逗号不动", "{\"a\": \"x,}\"}", "{\"a\": \"x,}\"}"},
		{"转义引号不结束字符串", "{\"a\": \"x\\\"//y\"}", "{\"a\": \"x\\\"//y\"}"},
		{"BOM", "\xEF\xBB\xBF{\"a\": 1}", "{\"a\": 1}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(sanitizeJSONC([]byte(tc.in))); got != tc.want {
				t.Errorf("sanitizeJSONC(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// allBuiltins 返回全部内置主题的 id，顺序与 Available 一致
func allBuiltins() []string {
	var out []string
	for _, g := range builtinGroups {
		for _, f := range g.Files {
			out = append(out, builtinPrefix+path.Join(g.Dir, f))
		}
	}
	return out
}

// TestOfficialThemesResolve 把内嵌的主题逐套解析一遍。
//
// 这是本包最重要的一条测试：官方文件本身就带注释与尾逗号（dark_vs.json 的 colors 末尾就有一个），
// 它们能解析成功，说明 JSONC 规整与 include 链是真的按 VSCode 的语义在走，而不是只对
// 我们手写的干净 JSON 有效
func TestOfficialThemesResolve(t *testing.T) {
	for _, id := range allBuiltins() {
		t.Run(id, func(t *testing.T) {
			r, err := Resolve(id)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if r.Name == "" {
				t.Error("取不到主题名")
			}
			if r.Group == "" {
				t.Error("内置主题应当带上来源分组")
			}
			// 注册表默认值里登记的每个令牌都必须解析出值：缺一个，使用方取到的就是空字符串
			base, err := defaultsFor(r.Type)
			if err != nil {
				t.Fatal(err)
			}
			for tok := range base {
				if r.Colors[tok] == "" {
					t.Errorf("令牌 %s 没有解析出值", tok)
				}
			}
			// 文件名里就写着深浅，解析出来的类型必须与它一致——这正是 type 字段/亮度推断那条链的验收
			if want := Type("dark"); strings.Contains(id, "dark") && r.Type != want {
				t.Errorf("%s 的类型应为 %s，实际 %s", id, want, r.Type)
			}
			if want := Type("light"); strings.Contains(id, "light") && r.Type != want {
				t.Errorf("%s 的类型应为 %s，实际 %s", id, want, r.Type)
			}
		})
	}
}

// TestResolveFallsBackToRegistryDefaults 主题只写了一个令牌时，其余全部回落到注册表默认值。
// 这条同时锁住“官方主题没写 gitDecoration.*，靠的就是回落”这件事
func TestResolveFallsBackToRegistryDefaults(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "only-bg.json")
	write(t, file, `{"name":"Only BG","colors":{"editor.background":"#010203"}}`)

	r, err := Resolve(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Colors["editor.background"]; got != "#010203" {
		t.Errorf("主题里写的值没生效：%s", got)
	}
	if r.Type != Dark {
		t.Errorf("#010203 是深色，推断结果却是 %s", r.Type)
	}
	want, err := defaultsFor(Dark)
	if err != nil {
		t.Fatal(err)
	}
	for id := range want {
		if id == "editor.background" {
			continue // 这个令牌主题自己写了，不参与回落
		}
		if got := r.Colors[id]; got != want[id] {
			t.Errorf("令牌 %s 应回落到默认值 %s，实际 %s", id, want[id], got)
		}
	}
}

// TestResolveLightInference 没写 type 时按背景亮度推断为浅色
func TestResolveLightInference(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bright.json")
	write(t, file, `{"name":"Bright","colors":{"editor.background":"#ffffff"}}`)

	r, err := Resolve(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != Light {
		t.Fatalf("白色背景应推断为浅色，实际 %s", r.Type)
	}
	// 回落的是浅色那一套
	if got, want := r.Colors["gitDecoration.addedResourceForeground"], "#587c0c"; got != want {
		t.Errorf("应回落浅色默认值 %s，实际 %s", want, got)
	}
}

// TestResolveIncludeChain include 的合并方向：底层先、本层覆盖
func TestResolveIncludeChain(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "base.json"), `{"name":"Base","type":"light","colors":{"editor.background":"#111111","foreground":"#222222"}}`)
	write(t, filepath.Join(dir, "child.json"), `{
  // 子主题自己声明 include，路径是相对当前文件的
  "name": "Child",
  "include": "./base.json",
  "colors": {"foreground": "#333333"}
}`)

	r, err := Resolve(filepath.Join(dir, "child.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Colors["editor.background"]; got != "#111111" {
		t.Errorf("被 include 的那层没生效：%s", got)
	}
	if got := r.Colors["foreground"]; got != "#333333" {
		t.Errorf("本层应覆盖底层：%s", got)
	}
	// type 也沿链继承：本层没写，取底层的
	if r.Type != Light {
		t.Errorf("应从 include 链继承 light，实际 %s", r.Type)
	}
	if r.Name != "Child" {
		t.Errorf("名字应取本层的：%s", r.Name)
	}
}

// TestResolveIncludeCycle include 成环时必须报错而不是递归到栈溢出
func TestResolveIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.json"), `{"name":"A","include":"./b.json","colors":{}}`)
	write(t, filepath.Join(dir, "b.json"), `{"name":"B","include":"./a.json","colors":{}}`)

	if _, err := Resolve(filepath.Join(dir, "a.json")); err == nil {
		t.Fatal("成环的 include 应当报错")
	} else if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("错误信息应当点明是 include 成环：%v", err)
	}
}

// TestResolveIncludeEscape include 不许指到主题文件自己目录之外：
// 主题文件是从别处粘进来的第三方文件，放它读系统任意文件没有任何好处
func TestResolveIncludeEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "secret.json")
	write(t, outside, `{"name":"Secret","colors":{"foreground":"#000000"}}`)
	themes := filepath.Join(dir, "themes")
	if err := os.MkdirAll(themes, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(themes, "evil.json"), `{"name":"Evil","include":"../secret.json","colors":{}}`)

	if _, err := Resolve(filepath.Join(themes, "evil.json")); err == nil {
		t.Fatal("越过目录的 include 应当被拒绝")
	}
}

// TestResolveRejectsNonPath 相对路径不是合法的主题标识：内置主题要带前缀、外部主题给绝对路径，
// 含糊的值一律拒绝，免得“某个看起来像名字的东西”被当成路径去读文件
func TestResolveRejectsNonPath(t *testing.T) {
	if _, err := Resolve("some-theme.json"); err == nil {
		t.Fatal("相对路径应当被拒绝")
	}
}

// TestAvailable 主题列表：内置的八套排前面，用户目录里的按名字排在后面；
// 认不出是主题的 JSON（同目录里调用方自己的配置文件）必须被跳过
func TestAvailable(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ggt-config.json"), `{"language":"zh-CN","parent_paths":["/x"]}`)
	write(t, filepath.Join(dir, "zebra.json"), `{"name":"Zebra","type":"dark","colors":{"foreground":"#fff"}}`)
	write(t, filepath.Join(dir, "alpha.json"), `{"name":"Alpha","type":"light","colors":{"foreground":"#000"}}`)
	write(t, filepath.Join(dir, "broken.json"), `{"name": `)

	list := Available([]string{dir, filepath.Join(dir, "does-not-exist")})
	builtins := allBuiltins()
	if len(list) != len(builtins)+2 {
		t.Fatalf("应有 %d 套主题，实际 %d：%+v", len(builtins)+2, len(list), list)
	}
	for i, id := range builtins {
		if !list[i].Builtin || list[i].ID != id {
			t.Errorf("第 %d 个应是内置主题 %s，实际 %+v", i, id, list[i])
		}
	}
	user := list[len(builtins):]
	if user[0].Name != "Alpha" || user[1].Name != "Zebra" {
		t.Errorf("用户主题应按名字排序，实际 %s / %s", user[0].Name, user[1].Name)
	}
	if user[0].Type != Light {
		t.Errorf("Alpha 的 type 应取自文件：%s", user[0].Type)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
