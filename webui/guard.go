package webui

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/jy-eggroll/eggokit/l10n"
)

// 本文件为「本机 WebUI 服务」提供统一的访问护栏
//
// 背景：这类服务的 HTTP 端点此前既无鉴权，也无 Host 校验、Origin/Referer 校验、
// CSRF 防护与 body 大小限制，绑定地址还能设成 0.0.0.0 而不给任何提示。
// 端点只接收 JSON 时，跨站请求难以伪造出有效载荷，风险还算可控；一旦 WebUI 具备
// 「真正操作文件系统」的能力（建立链接 / 修复 / 解除链接，或改写调用方的业务数据文件），
// DNS rebinding + CSRF 就等于任意文件删除，因此护栏属于必须先落地的前提
//
// 四道防线及其分工（外层先执行）：
//  1. Host 校验（hostGuard，对所有请求）：请求的 Host 必须落在显式白名单内——内置的回环地址、
//     服务实际绑定的地址（用户显式绑定即表示认可），以及调用方逐条列出的额外地址。
//     浏览器发出的 Host 由 URL 决定，攻击者域名（DNS 解析到自己机器）无法伪造成白名单里的
//     条目，因此这条同时挡住了 DNS rebinding 与直接跨站访问。
//     为什么是显式白名单而不是「绑定了哪个地址就允许哪个地址」：WebUI 能直接操作文件系统与
//     调用方的业务数据，属于高危入口，访问面应由用户逐条确认，而不是随绑定地址隐式扩张
//  2. token 门禁（tokenGate，仅 "/" 与 "/api/" 前缀）：见 tokenGate 的说明
//  3. Origin/Referer 校验（仅非 GET/HEAD）与 body 大小上限：见 writeGuard 的说明
//  4. 路由层拒绝 "/" + 首页文件名：见 Server.serveRoot 的说明
//
// 为什么第 1 道与第 3 道是两个独立函数：token 门禁必须夹在「Host 校验」与
// 「Origin/body 校验」之间——前者要挡所有请求，后者只对写请求有意义，token 判定
// 不该被写请求的特例绕过，因此这两道不能再写成同一个中间件

// maxServeBodyBytes 是 WebUI 服务允许的最大请求体（4 MiB）
// 取值理由：真正的载荷是一份调用方的业务数据文件，几千条记录也只有几十 KB，4 MiB 已留出
// 两个数量级的余量；同时它足够小，单个请求最多占用 4 MiB 内存，攻击者无法用超大 body 拖垮服务
const maxServeBodyBytes = 4 << 20

// tokenHeaderName 是前端 JS 在首屏之后携带 token 的请求头名
//
// 首屏只能靠查询参数（浏览器地址栏里的 URL 就是第一次请求），但把 token 留在地址栏里
// 会被写入浏览器历史、Referer 头与代理日志；因此前端拿到首屏后改用本请求头，
// 由 JS 从 location.search 里读出 token 再删掉地址栏里的痕迹（见调用方的前端实现约定）
const tokenHeaderName = "X-WebUI-Token"

// loopbackHosts 是始终允许的 Host 主机名（不含端口）
// [::1] 与 ::1 是同一个 IPv6 回环地址的两种写法：浏览器在 Host 头里带方括号，而
// net.SplitHostPort 会把方括号去掉，两种写法经 normalizeHost 后都会归一到 ::1，
// 这里两种都列出来只为让「允许清单」自解释
var loopbackHosts = []string{"127.0.0.1", "localhost", "::1", "[::1]"}

// hostGuard 施加第 1 道防线：请求的 Host 必须落在白名单内
//
// 传入 nil / 空切片时只保留回环地址，即「仅本机可访问」
//
// allowedHosts 由调用方组装，包含三部分：
//   - 服务实际绑定的地址：用户显式绑定 192.168.1.5 时，浏览器地址栏里输入的就是它，
//     不放行则用户自己都打不开页面
//   - 用户逐条列出的额外地址（本次运行的临时授权）
//   - 调用方设置文件里列出的地址（长期生效的授权）
//
// 这一道必须最先执行：Host 校验不通过时，后面的 token 判定与 Origin 判定都不该有观察面，
// 否则跨站方可以靠响应差异（401 还是 403）探测内网服务是否存在
func hostGuard(h http.Handler, allowedHosts []string) http.Handler {
	allowed := buildAllowedHosts(allowedHosts)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHost := normalizeHost(r.Host)
		if !allowed[requestHost] {
			http.Error(w, l10n.T("Forbidden: the request host is not allowed", nil), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// writeGuard 施加第 3 道防线：写请求的 Origin/Referer 同源校验与 body 大小上限
//
// 读请求不改状态，不参与 CSRF 模型，因此两项检查都只作用于非 GET/HEAD 请求
// 潜在影响点：受保护的只有「写请求」这一维度，与路径无关——静态资源上的写请求同样会被
// 同源校验拦下（浏览器不会对静态资源发写请求，拦下不误伤正常使用）
func writeGuard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// 第 2 道防线：Origin / Referer 同源校验
			if !sameOriginRequest(r) {
				http.Error(w, l10n.T("Forbidden: the request does not come from the same origin", nil), http.StatusForbidden)
				return
			}

			// 第 3 道防线：body 大小上限
			// 声明了 Content-Length 且超限时直接 413，不必等读满才失败，响应更快也更省资源
			// 未声明长度（分块传输，ContentLength 为 -1）的请求只能交给 MaxBytesReader：
			// 它限制的是 handler 能从 body 读到的字节数，因此上限只在 handler 真的读到那里
			// 时才生效——handler 提前返回（鉴权失败、路由不匹配）时这个上限形同虚设；
			// net/http 之后最多再丢弃 256 KiB 就关闭连接，所以不构成内存耗尽
			if r.ContentLength > maxServeBodyBytes {
				http.Error(w, l10n.T("Request body is too large", nil), http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxServeBodyBytes)
		}
		h.ServeHTTP(w, r)
	})
}

// tokenGate 施加第 2 道防线：token 门禁
//
// 保护范围按路径分类，而不是全部请求："/" 与 "/api/" 前缀受保护（前者是页面入口，加上
// token 才能让用户在地址栏里一次带走凭据；后者是真正读写业务数据的端点），其余路径
// （静态资源）一律放行——它们是公开的前端代码，若也要求 token，页面加载 <link>/<script>
// 时无法附加查询参数，前端必须把 token 写进每个资源 URL，徒增耦合且没有安全收益
//
// 读取顺序是「查询参数优先、请求头兜底」：首屏是浏览器地址栏直接发起的导航请求，
// 除了 URL 没有别的地方能携带凭据；首屏之后前端改用请求头（见 tokenHeaderName）
//
// 比较必须用 crypto/subtle.ConstantTimeCompare：普通字符串比较会在第一个不同的字节处
// 提前返回，攻击者能用大量请求的耗时差异逐字节猜出 token
//
// 失败时返回极简 401：不回显任何细节（不区分「没带 token」「token 不对」「路径受保护」），
// 也不写 WWW-Authenticate，避免给探测者提供可区分状态的信号
func tokenGate(h http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenProtectedPath(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}

		presented := r.URL.Query().Get("token")
		if presented == "" {
			presented = r.Header.Get(tokenHeaderName)
		}

		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// tokenProtectedPath 判断某个路径是否落在 token 门禁的保护范围内
//
// 刻意只认 "/" 与 "/api/" 前缀：
//   - "/" 是精确匹配，页面入口本身受保护
//   - 只有 "/api/" 带斜杠的形式才算 API；裸 "/api" 不是任何已注册的路由（会落到静态资源
//     查找或 404），把它纳入保护只会让「静态资源一律放行」这条规则出现例外
//
// 潜在影响点：路径用 path.Clean 归一之后再判定，因此 "/./api/config"、"/api/../api/config"
// 这类写法会先被规整成 "/api/config" 再判定，无法用等价写法绕过门禁
func tokenProtectedPath(rawPath string) bool {
	cleaned := normalizePath(rawPath)
	return cleaned == "/" || strings.HasPrefix(cleaned, "/api/")
}

// buildAllowedHosts 把回环地址与调用方传入的地址合并成允许集合，元素已归一化
// 空字符串（未指定 host、监听全部网卡）会被跳过：它不是一个可出现在 Host 头里的主机名
func buildAllowedHosts(allowedHosts []string) map[string]bool {
	allowed := make(map[string]bool, len(loopbackHosts)+len(allowedHosts))
	for _, host := range loopbackHosts {
		allowed[normalizeHost(host)] = true
	}
	for _, host := range allowedHosts {
		normalized := normalizeHost(host)
		if normalized == "" {
			continue
		}
		allowed[normalized] = true
	}
	return allowed
}

// allowedHostDisplay 返回当前生效白名单的可读展示列表（已归一化、去重、按字典序排序），
// 供 Server.AllowedHosts 交给调用方打印
//
// 刻意复用 buildAllowedHosts 而不是自己遍历一遍入参：展示结果与实际判定必须来自同一份实现，
// 否则「打印一套、放行另一套」的偏差会直接误导用户——这类问题在安全相关的白名单上尤其致命
// 排序是为了让输出稳定可读：map 的遍历顺序随机，同一份配置每次启动打印的顺序都不同
// 会让人误以为配置变了
func allowedHostDisplay(allowedHosts []string) []string {
	allowed := buildAllowedHosts(allowedHosts)
	hosts := make([]string, 0, len(allowed))
	for host := range allowed {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// normalizeHost 把 Host 头或 URL 里的主机部分归一成「小写、无端口、无 IPv6 方括号」的形式，便于比较
//
// 归一化的必要性：同一个回环地址在 Host 头里有多种合法写法（127.0.0.1:8999、[::1]:8999、
// [::1]、LOCALHOST），逐字比较会把等价写法判成伪造，同时大写写法又可能绕过检查，
// 因此两边（允许清单与请求）都走本函数
// 潜在影响点：Host 头缺失或为空时返回空串，而允许集合里不存在空串，所以空 Host 一律被拒
func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return ""
	}
	// 带端口时 net.SplitHostPort 会顺手去掉 IPv6 的方括号（[::1]:8999 → ::1）
	// 不带端口时它返回错误（missing port），此时沿用原值，再由下面的 Trim 处理 [::1] 这种写法
	if withoutPort, _, err := net.SplitHostPort(host); err == nil {
		host = withoutPort
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// isLoopbackHost 判断服务绑定的主机是否是回环地址，用于 Server.LoopbackBound 与启动警告
//
// 只认 localhost 字面量与回环 IP，刻意不做 DNS 解析：解析结果依赖运行环境的 hosts 与 DNS，
// 启动阶段的判断不该引入网络查询，更不能因为解析失败而误判成安全
// 潜在影响点：返回 false 不一定代表绑定到了公网（例如自定义域名的 hosts 别名），
// 属于「宁可多警告」的取向
func isLoopbackHost(host string) bool {
	normalized := normalizeHost(host)
	if normalized == "" {
		return false
	}
	if normalized == "localhost" {
		return true
	}
	if ip := net.ParseIP(normalized); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// sameOriginRequest 判断写请求是否来自同源页面
//
// 判定顺序与理由：
//  1. 有 Origin 就以它为准：浏览器对跨站写请求必定带上 Origin，页面脚本无法伪造它
//  2. Origin 缺失但有 Referer 时用 Referer 兜底：少数老旧客户端不发 Origin
//  3. 两者都没有则放行：非浏览器客户端（curl / 脚本）不适用 CSRF 模型，
//     也不携带任何浏览器凭据，挡下来只会误伤本地工具
//
// 比较「主机 + 端口」（含 IPv6 归一）而不比 scheme：
//   - 必须比端口：只比主机名会放行 http://127.0.0.1:1234 这类页面发起的写请求，
//     本机上任意一个别的服务都能借此操作调用方的数据文件
//   - 不必比 scheme：本服务只提供明文 HTTP，不存在「同主机同端口的 https 页面」
//
// 潜在影响点：Origin 为 "null"（沙箱 iframe、file:// 页面）时解析出的 Host 为空，
// 与任何合法 authority 都不相等，会被判为跨站而拒绝——这正是期望行为
func sameOriginRequest(r *http.Request) bool {
	requestAuthority := normalizeAuthority(r.Host)
	if origin := r.Header.Get("Origin"); origin != "" {
		return authorityOfURL(origin) == requestAuthority
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return authorityOfURL(referer) == requestAuthority
	}
	return true
}

// normalizeAuthority 把 Host 头或 URL 的主机部分归一成「小写主机 + 端口」，供同源比较使用
//
// 与 normalizeHost 的唯一区别是端口：normalizeHost 刻意丢掉端口，因为白名单是按主机名授权的
// （用户不会因为换了个端口就重新授权一次）；而同源比较必须保留端口，否则本机另一个端口上的页面
// 会被判成与自己同源。两者不能合并成一份实现，这一点是刻意的
// 不带端口时退回 normalizeHost：裸 IPv6 写法（[::1]）会走这条路径，方括号在那里被去掉
func normalizeAuthority(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return normalizeHost(trimmed)
	}
	// JoinHostPort 会为 IPv6 补回方括号，因此两端（请求头与 URL）归一后的形态一致
	return net.JoinHostPort(normalizeHost(host), port)
}

// authorityOfURL 取出 URL 字符串里的「主机 + 端口」（已归一化），解析失败时返回空串
// 返回空串而不是原值：任何无法解析的输入都不应该碰巧等于某个合法请求的 authority 而被放行
func authorityOfURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return normalizeAuthority(parsed.Host)
}
