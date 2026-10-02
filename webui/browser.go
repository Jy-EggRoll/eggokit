package webui

import (
	"os/exec"
	"runtime"
)

// tryOpenBrowser 尝试在默认浏览器中打开指定 URL，失败时静默忽略
//
// 三处刻意的取舍：
//   - 一律用 Start 而不是 Run：Run 会等浏览器进程退出，而用户往往在整个服务生命周期里
//     都开着那个页面，服务会被卡在启动步骤上。Start 只负责把 URL 交给桌面环境
//   - 不检查错误后重试或换命令：无桌面环境（服务器、SSH、容器）里没有 xdg-open / open，
//     这是正常情况而非故障，报错只会打扰用户。页面地址已经随 Summary 交给调用方打印，
//     用户手工打开即可
//   - 不打印任何东西：本包不做输出，浏览器打不开也不该由这里决定怎么告知用户
//
// 潜在影响点：URL 带 token 时会进入进程的 argv，而同机任何用户都能读 /proc/<pid>/cmdline。
// 暴露窗口不是「拉起的一瞬间」而是整个存活期：xdg-open / open / cmd start 都会把 URL 交给
// 浏览器进程本身，token 因此留在浏览器 argv 里直到那个窗口关闭。token 随机生成且只对本次
// 运行有效，所以风险限于「同机的其他用户」这一档；与不可信用户共享的机器上应让调用方
// 关掉 Config.OpenBrowser，由用户自己复制带 token 的地址
func tryOpenBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	case "windows":
		err = exec.Command("cmd", "/c", "start", url).Start()
	}
	// 这里刻意留空：调用方无从处理，也不该因为打不开浏览器而中断服务。
	// 显式赋值给 _ 是为了让「忽略错误」这个决定被读者看见，而不是被误认为漏写了检查
	_ = err
}
