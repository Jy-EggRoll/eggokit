// Command changelog 按发布清单生成一份 markdown 发布说明并输出到 stdout
//
// 用法：
//
//	changelog --manifest release.json --version 1.2.3 --type release|dev [--build-time T] [--previous-tag TAG] [--root .] [--lang en]
//
// 它替代的是既有项目 Taskfile 里那段用 shell + awk 拼发布说明的逻辑，目的是让多个项目的
// 发布说明形态收敛到一份实现：shell 版本每复制一次就多一处漂移点，而且首个正式版正文为空
// 这类缺陷只在个别的复制里被修过
//
// 输出只走 stdout，方便直接重定向进 GitHub Release 的正文；进度与告警走 stderr
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/release"
	"github.com/jy-eggroll/eggokit/release/cmd/internal/relcli"
)

// releaseTagPattern 是正式版标签的形态，与 updater 对正式版的判定保持一致
//
// dev 模式不设此限制（匹配任意标签），因为开发版之间往往用 1.2.3.dev.1 这类非正式标签互相比较
var releaseTagPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是命令的可测入口：返回退出码而不是直接退出，参数与输出流都可注入
//
// 之所以把 main 压到一行：flag 解析、git 推断、渲染都可在此被单测覆盖，
// 若逻辑写在 main 里，测试就只能靠起子进程，既慢又难断言
func run(args []string, stdout, stderr io.Writer) int {
	// 先把语言定下来再定义 flag：flag 的说明文案本身也要翻译，
	// 而它们是在定义时就求值的，若等到 Parse 之后才 Init，帮助信息只会是英文源串
	if err := relcli.InitL10n(relcli.PrescanLang(args)); err != nil {
		return relcli.Fail(stderr, err)
	}

	fs := flag.NewFlagSet("changelog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", l10n.T("Path to the release manifest (e.g. release.json)", nil))
	root := fs.String("root", ".", l10n.T("Project root; relative paths are resolved against it", nil))
	version := fs.String("version", "", l10n.T("Release tag for this build (e.g. 1.2.3)", nil))
	typeFlag := fs.String("type", "", l10n.T("Release type: dev or release", nil))
	buildTime := fs.String("build-time", "", l10n.T("Build time (optional, shown in dev builds)", nil))
	previousTag := fs.String("previous-tag", "", l10n.T("Previous release tag (optional; auto-detected from git when omitted)", nil))
	lang := fs.String("lang", "", l10n.T("Output language (e.g. zh-CN, en); defaults to en", nil))
	if err := fs.Parse(args); err != nil {
		// ErrHelp 表示用户主动查看帮助，属正常路径；其余解析错误 flag 包已打印原因
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if strings.TrimSpace(*manifestPath) == "" {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("--manifest is required", nil)))
	}
	if strings.TrimSpace(*version) == "" {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("--version is required (the release tag, e.g. 1.2.3)", nil)))
	}
	if *typeFlag != typeDev && *typeFlag != typeRelease {
		return relcli.Fail(stderr, fmt.Errorf("%s", l10n.T("Unsupported --type {{.Type}}; only dev and release are accepted", map[string]any{"Type": *typeFlag})))
	}

	m, err := release.Load(relcli.ResolveUnderRoot(*root, *manifestPath))
	if err != nil {
		return relcli.Fail(stderr, err)
	}

	// 语言优先级：--lang > 清单 releaseNotesLang > 内置默认 en（见 relcli.ResolveLanguage）
	// 最终语言必须等清单加载后才知道（releaseNotesLang 就在清单里），因此在这里才 Init——
	// 此前的报错（缺参数、清单本身有问题）用的是预扫描语言，属可接受的近似
	if err := relcli.InitL10n(relcli.ResolveLanguage(*lang, m.ReleaseNotesLang)); err != nil {
		return relcli.Fail(stderr, err)
	}

	opts := Options{
		Manifest:  m,
		Version:   *version,
		Type:      *typeFlag,
		BuildTime: *buildTime,
		Root:      *root,
		// 告警（如清单里声明了下载表格之外的平台）走 stderr，与正文流分开，
		// 避免污染被整体重定向进 Release 的发布说明
		Warn: stderr,
	}

	if strings.TrimSpace(*previousTag) != "" {
		opts.PreviousTag = *previousTag
	} else {
		tag, err := resolvePreviousTag(*root, *version, *typeFlag)
		if err != nil {
			// 取不到标签不构成失败：最坏是这次发布说明少了 compare 链接，
			// 仍应把说明正常产出去，否则一个 git 环境问题会阻断整条发布流程
			fmt.Fprintln(stderr, l10n.T("Warning: {{.Message}}", map[string]any{
				"Message": l10n.T("Could not read git tags ({{.Error}}); treating this as the first release", map[string]any{"Error": err.Error()}),
			}))
		} else {
			opts.PreviousTag = tag
		}
	}

	// 分支与提交短哈希只在 dev 版展示，且都是尽力而为：取不到就省略整行，绝不因此失败
	if *typeFlag == typeDev {
		opts.Branch = gitOutputBestEffort(*root, "rev-parse", "--abbrev-ref", "HEAD")
		opts.Commit = gitOutputBestEffort(*root, "rev-parse", "--short", "HEAD")
	}

	if err := Render(stdout, opts); err != nil {
		return relcli.Fail(stderr, err)
	}
	return 0
}

// resolvePreviousTag 推断上一版本标签
//
// 复用 git 自己的版本排序（--sort=-version:refname）而不是在 Go 里再实现一套 semver 比较：
// 排序规则是 git 的内建能力，重复实现只会多一处与 git 不一致的真源
//
// dev 模式匹配任意标签，release 模式只匹配纯数字三段式；两者都排除当前版本本身，
// 取排序后的第一个命中项，即为"版本号最大的既有标签"
func resolvePreviousTag(root, version, typ string) (string, error) {
	out, err := gitOutput(root, "tag", "-l", "--sort=-version:refname")
	if err != nil {
		return "", err
	}

	for _, tag := range strings.Split(out, "\n") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		// 同时排除带 v 前缀的等价写法：仓库里若既有 1.2.3 也有 v1.2.3 指向同一提交，
		// 不过滤掉会把"自己"当成上一个版本，生成一个 prev...version 的退化 compare 链接
		if tag == version || tag == "v"+version {
			continue
		}
		if typ == typeRelease && !releaseTagPattern.MatchString(tag) {
			continue
		}
		return tag, nil
	}
	return "", nil
}

// gitOutput 在 root 目录执行 git 并返回去除首尾空白后的标准输出
func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stdout, stderrBuf bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		// 把 git 自己的 stderr 带进错误里：否则"not a git repository"这类信息会被吞掉，
		// 用户只能看到一句笼统的失败
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// gitOutputBestEffort 执行 git 并忽略失败，失败时返回空串
func gitOutputBestEffort(root string, args ...string) string {
	out, err := gitOutput(root, args...)
	if err != nil {
		return ""
	}
	return out
}
