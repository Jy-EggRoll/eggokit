// Package fileicon 把 VSCode 的 Seti 文件类型图标收敛成一个自带资源的包，
// 浏览器端只需要引入 fileicon/seti.js 与 fileicon/seti.css 即可使用。
//
// 包内资源（Assets 返回的根目录下）：
//
//	fileicon/seti.js                     自定位的图标解析器，暴露 window.fileicon
//	fileicon/seti.css                    @font-face 与 .seti 规则
//	fileicon/seti-icon-theme.json        图标主题：图标定义、三段查找表与 light 平行表
//	fileicon/vscode-language-map.json    语言 id 表（扩展名/文件名 -> 语言 id）
//	fileicon/seti.woff                   Seti 图标字体
//	fileicon/LICENSE-seti-ui.txt         上游许可（MIT）
//	fileicon/LICENSE-vscode.txt          上游许可（MIT）
//
// 出处与版本：
//   - seti-icon-theme.json 与 seti.woff 来自 jesseweed/seti-ui，其自带的 version 字段记录了
//     上游 commit 2d6c5e68b4ded73c92dac291845ee44e1182d511（许可见 LICENSE-seti-ui.txt）。
//   - vscode-language-map.json 来自 microsoft/vscode：语言 id 由各扩展的 package.json 里的
//     contributes.languages 声明，文件自带的 sourceCommit 为
//     7f20cdad4f4ab923272e91e330a7701c52706fc7（许可见 LICENSE-vscode.txt）。
//
// 之所以要带着这两份 JSON 而不是编译期静态映射：图标主题的前两级查找表（fileNames、
// fileExtensions）之外还有第三级 languageIds，它需要文件的语言 id，而语言 id 是 VSCode
// 由各语言扩展声明出来的，只能另存一张表，因此解析必须发生在运行时。
package fileicon

import (
	"embed"
	"errors"
	"io/fs"
)

// assets 与包放在一起、路径固定：资源在编译期打进二进制，调用方不需要额外分发文件
//
//go:embed assets
var assets embed.FS

// Assets 返回嵌入的图标资源，根目录下有一个 fileicon/ 前缀（即 fileicon/seti.js 等）。
//
// 用它接入静态资源服务时，fileicon/ 这一级的路径就是接入契约，不要改名。
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// 走不到这里：assets 目录由 go:embed 在编译期保证存在，出问题必然是构建期的事，
		// 运行期没有可补救的手段，程序性 panic 比返回一个残缺的 FS 更容易定位
		panic("fileicon: 嵌入资源缺少 assets 目录: " + err.Error())
	}
	return sub
}

// Overlay 把嵌入资源叠在调用方自己的静态资源之下：own 优先，own 缺的文件回退给嵌入资源。
//
// 为什么需要它：go:embed 不能跨模块取文件，而 webui 的 Config.Assets 只接受一个 fs.FS，
// 它只读这一层文件查找而不做目录合并——调用方目录里的枚举看不到 fileicon/ 下的文件。
// 也就是说：
//
//   - fs.ReadFile / Open 这类按名字取文件的调用是安全的，own 命中就用自己的；
//   - 但 fs.ReadDir 之类的目录枚举只反映 own 自己（或回退到嵌入资源）的那一层内容，
//     不会把两边同一目录下的条目合并起来。想对外暴露完整的目录清单，得自己合并。
//
// own 为 nil 时等价于直接使用 Assets()。
func Overlay(own fs.FS) fs.FS {
	if own == nil {
		return Assets()
	}
	return overlayFS{own: own}
}

type overlayFS struct {
	own fs.FS
}

// Open 先在 own 里找，只有 fs.ErrNotExist 才回退给嵌入资源；其余错误（例如权限）
// 原样上抛，避免把真实的读取失败伪装成“文件不存在”。
func (o overlayFS) Open(name string) (fs.File, error) {
	// 两个 FS 都要按 fs.ValidPath 校验名字：非法名（如 ../x）在这里就拒掉，
	// 不要往下传给实现，否则错误语义取决于具体 FS 实现
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := o.own.Open(name)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return Assets().Open(name)
}
