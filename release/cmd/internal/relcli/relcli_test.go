package relcli

import "testing"

// TestDefaultLanguageIsEnglish 锁定内置默认语言为英文
//
// 这是本次行为的核心改动之一：默认语言从 zh-CN 改为 en。此处单独固定，
// 避免日后有人"顺手改回中文"而没有任何测试拦下
func TestDefaultLanguageIsEnglish(t *testing.T) {
	if DefaultLanguage != "en" {
		t.Fatalf("DefaultLanguage = %q，期望 %q", DefaultLanguage, "en")
	}
}

// TestResolveLanguage 锁定语言优先级：命令行 > 清单 > 内置默认
//
// 三个来源都可能留空，优先级一旦写反就会出现"命令行指定了语言却不生效"这类难查问题，
// 因此把全部分支用表驱动一次固定
func TestResolveLanguage(t *testing.T) {
	cases := []struct {
		name         string
		flagLang     string
		manifestLang string
		want         string
	}{
		{name: "命令行优先于清单", flagLang: "en", manifestLang: "zh-CN", want: "en"},
		{name: "无命令行时用清单", flagLang: "", manifestLang: "zh-CN", want: "zh-CN"},
		{name: "两者皆空回退内置默认", flagLang: "", manifestLang: "", want: DefaultLanguage},
		{name: "仅有命令行时用命令行", flagLang: "zh-CN", manifestLang: "", want: "zh-CN"},
		// 全空白等同于未填：否则会把一个空格当成合法语言传下去，最终静默回退默认语言
		{name: "命令行全空白视为未填", flagLang: "   ", manifestLang: "zh-CN", want: "zh-CN"},
		{name: "清单全空白视为未填", flagLang: "", manifestLang: "   ", want: DefaultLanguage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveLanguage(tc.flagLang, tc.manifestLang); got != tc.want {
				t.Fatalf("ResolveLanguage(%q, %q) = %q，期望 %q", tc.flagLang, tc.manifestLang, got, tc.want)
			}
		})
	}
}
