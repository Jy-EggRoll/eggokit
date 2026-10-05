// webui 提供「本机 WebUI 服务」的公共基础设施：监听与端口策略、Host/Origin/token 三道访问校验、
// 静态资源托管（内容哈希 ETag）、可选自动拉起浏览器，以及一份可供调用方自行打印的启动摘要
//
// 为什么值得单独成一个包：这类服务在多个命令行工具里各写了一遍，校验细节各自漂移（有的没有
// Host 校验、有的用字符串比较 token、有的用 http.FileServer 托管 embed.FS 于是条件请求悄悄
// 失效），而风险完全一致——WebUI 通常能操作文件系统或改写业务数据，DNS rebinding + CSRF
// 就等于任意文件删除
//
// 本包不做输出、不做业务：需要展示的内容（服务地址、白名单、非回环警告）由 Summary 与
// NonLoopbackWarning 以字符串返回，打印位置留给调用方——它们属于必须默认可见的启动摘要，
// 而日志默认级别往往会把它过滤掉；业务路由由调用方经 Config.API 注入
package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
)

// defaultHost 是 Config.Host 为空时使用的绑定地址
//
// 默认值必须选安全的那一侧：绑定 0.0.0.0 意味着同网段任何人都能打开这个 WebUI，
// 而它通常能直接改写调用方的数据文件；需要暴露时由调用方显式写出地址
const defaultHost = "127.0.0.1"

// defaultIndexName 是 Config.IndexName 为空时使用的首页文件名
const defaultIndexName = "index.html"

// tokenBytes 是 token 的随机字节数（32 字节 = 256 位熵）
//
// 用 crypto/rand 而不是 math/rand：后者可预测（同一进程的序列能被反推），
// 一旦 token 可预测，整套门禁形同虚设
const tokenBytes = 32

// Config 描述一个 WebUI 服务的全部可变部分
//
// 字段都是标准库类型（fs.FS / http.Handler）：调用方不必为托管一个内嵌页面改写构建方式，
// 本包也不必依赖任何具体框架
type Config struct {
	// Host 是绑定地址，空串按 127.0.0.1 处理
	// 传 "0.0.0.0" / "::" 表示监听全部网卡，此时务必让用户看到 NonLoopbackWarning
	Host string

	// Port 是端口策略，nil 等价于 Random()
	// 接口不导出方法，因此调用方只能从 Sequential 与 Random 两个构造函数获得取值
	Port PortMode

	// Assets 是静态资源根，可为 nil（表示没有静态资源）
	// 传 embed.FS 是推荐用法：它天然只读、不会随工作目录变化，也不涉及符号链接
	Assets fs.FS

	// IndexName 是静态资源里首页的文件名，空串按 "index.html" 处理
	// 它同时是被 `/` + IndexName 这条路径拒绝规则所针对的对象，理由见 Server.serveRoot
	IndexName string

	// Index 非 nil 时用它生成 "/" 的响应，为 nil 时从 Assets 读 IndexName
	// 调用方靠它在首页里注入运行期状态（例如当前语言、内嵌的翻译表）
	Index func() []byte

	// API 是业务 API，挂在 "/api/" 前缀下，可为 nil
	// 为 nil 时该前缀下的请求一律 404，不会回落到静态资源，避免把 API 路径当成资源名去查
	API http.Handler

	// AllowHosts 是回环地址之外额外放行的 authority（可带端口，比较时会归一化掉）
	//
	// 它对应调用方的命令行开关与设置文件两处授权来源（例如 --allow-host 与设置文件的 allowHosts），
	// 本包不关心它们各自来自哪里，只要求调用方把并集传进来
	AllowHosts []string

	// OpenBrowser 表示是否在 Serve 开始阻塞前自动拉起浏览器（尽力而为，失败静默忽略）
	//
	// 打开的是带 token 的地址，token 会随之进入浏览器进程的 argv（同机其他用户可读
	// /proc/<pid>/cmdline）。因此与不可信用户共享的机器上应当关掉本开关，
	// 让用户自己复制 Summary 里的地址
	OpenBrowser bool

	// Auth 是 token 门禁的开关，详见 Auth
	Auth Auth
}

// Auth 控制 token 门禁
//
// 为什么用 struct 而不是直接一个 bool：门禁将来若要加「有效期」「per-session」这类维度，
// 加字段就是非破坏性变更，而把 bool 换成 struct 会破坏所有调用点
type Auth struct {
	// Enabled 表示是否启用 token 门禁
	//
	// 为 false 时完全不生成 token，URL 也不带查询参数。这一点是刻意的：
	// 「生成了但不用」等于把凭据白留在内存与地址栏里，徒增泄露面
	Enabled bool
}

// PortMode 是端口选取策略，不导出方法形成封闭集合，调用方只能取 Sequential 或 Random 的返回值
type PortMode interface {
	// listen 按策略建立监听，返回实际使用的端口
	listen(host string) (net.Listener, int, error)
}

// listenOn 监听 host:port，返回 listener 与**实际绑定**的端口
//
// 必须回读 listener.Addr() 而不能直接用传入的 port：端口 0 的含义是「任意可用端口」，
// 此时内核分配的端口与传入值不同，用传入值会产出一个指向死链的地址
func listenOn(host string, port int) (net.Listener, int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, 0, err
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, 0, fmt.Errorf("%s", l10n.T("Failed to determine the listening port", nil))
	}
	return listener, addr.Port, nil
}

// sequentialMode 从 start 开始逐个尝试端口，最多 attempts 次
type sequentialMode struct {
	start    int
	attempts int
}

// listen 从 start 开始递增重试
//
// 为什么需要重试：固定端口的服务重启时，上一个进程的 socket 可能还处于 TIME_WAIT
// （内核尚未回收），直接监听同一个端口会失败，而用户只是重启了一次服务，不该报错
func (m sequentialMode) listen(host string) (net.Listener, int, error) {
	if m.attempts < 1 {
		m.attempts = 1
	}
	for i := 0; i < m.attempts; i++ {
		listener, port, err := listenOn(host, m.start+i)
		if err == nil {
			return listener, port, nil
		}
	}
	return nil, 0, fmt.Errorf("%s", l10n.T("Ports {{.From}}-{{.To}} are all in use", map[string]any{
		"From": m.start,
		"To":   m.start + m.attempts - 1,
	}))
}

// randomMode 把端口选取交给系统（监听 0 端口）
type randomMode struct{}

// listen 交给系统分配可用端口
//
// 为什么把它作为默认：WebUI 是临时打开的辅助界面，用户并不关心端口号（地址会被自动打开
// 或打印出来），而固定端口意味着「同时开两个实例」或「端口被别人占用」都会直接失败
func (randomMode) listen(host string) (net.Listener, int, error) {
	return listenOn(host, 0)
}

// Sequential 返回「从 start 开始、最多尝试 attempts 个端口」的端口策略
//
// attempts 小于 1 会被当作 1
// start 为 0 时 net.Listen 会分配任意可用端口（端口 0 的定义如此），返回的仍是内核
// 实际分配的端口；负值被 net.Listen 拒绝。本包不在这里额外校验取值范围：
// 端口值来自调用方的输入解析，属于调用方的校验职责，在这里悄悄改写它反而更难排查
func Sequential(start, attempts int) PortMode {
	return sequentialMode{start: start, attempts: attempts}
}

// Random 返回「交给系统分配可用端口」的端口策略
func Random() PortMode {
	return randomMode{}
}

// Server 是一个已建立监听、但尚未开始服务的 WebUI 服务
//
// 字段一律不导出：监听器、token、白名单缓存都是「构造后不可变」的内部状态，
// 放出去只会让调用方有机会把三者改成互相矛盾的状态（例如改了白名单却忘了重建 map）
type Server struct {
	host       string
	port       int
	token      string
	allowHosts []string

	cfg      Config
	listener net.Listener
	http     *http.Server

	// assetMu 保护 assetCache。静态资源的请求是并发的，而缓存是懒加载的
	assetMu    sync.RWMutex
	assetCache map[string]assetEntry
}

// assetEntry 是一份静态资源的缓存条目：内容、内容哈希 ETag、算好的 Content-Type，
// 以及它对应的文件状态（修改时间与大小）
//
// 带上文件状态是为了让缓存只按「路径」这一个维度存放：缓存键若把修改时间也算进去，
// 磁盘上的资源每被替换一次就多留一条永不释放的旧条目（开发期反复改页面时内存只增不减），
// 而按路径存放只需在命中时比对这两个字段，变了就整条替换，缓存大小始终等于资源数
type assetEntry struct {
	body    []byte
	etag    string
	ctype   string
	modTime time.Time
	size    int64
}

// New 建立监听、按需生成 token，返回可直接 Serve 的服务实例
//
// 本函数不阻塞、不打印、不打开浏览器：进程的生命周期（什么时候输出摘要、什么时候进入
// 阻塞）由调用方掌握。唯一的副作用是占用一个端口，因此在返回错误前会确保 listener 已关闭，
// 避免调用方重试时连「端口被自己上一次失败占着」都排查不到
func New(cfg Config) (*Server, error) {
	if cfg.Host == "" {
		cfg.Host = defaultHost
	}
	if cfg.IndexName == "" {
		cfg.IndexName = defaultIndexName
	}
	if cfg.Port == nil {
		cfg.Port = Random()
	}

	listener, port, err := cfg.Port.listen(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to start the WebUI service", nil), err)
	}

	s := &Server{
		host:       cfg.Host,
		port:       port,
		cfg:        cfg,
		listener:   listener,
		assetCache: make(map[string]assetEntry),
	}

	// 绑定地址本身必须放行：用户显式绑定 192.168.1.5 时，浏览器地址栏里输入的就是它，
	// 不放行则用户自己都打不开页面
	//
	// 注意绑定通配地址（0.0.0.0 / ::）时它不是一个能出现在 Host 头里的主机名，
	// 因此不会被任何请求命中——要通过局域网访问必须显式绑定具体 IP，或把该 IP 写进 AllowHosts，
	// 这是刻意收紧的取向：访问面应由用户逐条确认，而不是随绑定地址隐式扩张
	s.allowHosts = append(append([]string{}, cfg.AllowHosts...), cfg.Host)

	if cfg.Auth.Enabled {
		token, tokenErr := newToken()
		if tokenErr != nil {
			_ = listener.Close()
			return nil, tokenErr
		}
		s.token = token
	}

	s.http = &http.Server{Handler: s.handler()}
	return s, nil
}

// newToken 用 crypto/rand 生成 32 字节随机 token，并编码成 URL 安全的字符串
//
// 三条硬约束，缺一不可：
//   - 必须是 crypto/rand：math/rand 的序列可预测，token 一旦可预测，门禁形同虚设
//   - 32 字节：256 位熵，暴力枚举在任何现实时间尺度内都不可行
//   - base64.RawURLEncoding：token 要出现在 URL 的查询参数里，标准 base64 的 "+" "/" "="
//     需要再转义一次，RawURLEncoding 直接可用，也避免不同客户端对转义处理不一致
//
// 只存在于内存，绝不接受调用方从外部注入：命令行参数与环境变量都会经 /proc/<pid>/cmdline
// 或 /proc/<pid>/environ 泄露给同机其他用户，也容易被打进日志与 shell history
func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%s: %w", l10n.T("Failed to generate the access token", nil), err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// handler 组装完整的处理链
//
// 包装顺序与执行顺序相反：最后包装的在最外层、最先执行，因此这里的执行顺序是
// Host 校验（第 1 道）→ token 门禁（第 2 道）→ 写请求校验（第 3 道）→ 路由（含第 4 道）
//
// token 门禁必须夹在中间：它要挡的不只是写请求，而是所有对 "/" 与 "/api/" 的请求
// （读接口同样会吐数据）；而写请求校验只对非 GET/HEAD 有意义，放在最内层不会漏掉任何请求
// 未启用 Auth 时整层不挂：不生成 token 也就不需要判定，少一层就少一处可能被改错的地方
func (s *Server) handler() http.Handler {
	var h http.Handler = http.HandlerFunc(s.route)
	h = writeGuard(h)
	if s.token != "" {
		h = tokenGate(h, s.token)
	}
	return hostGuard(h, s.allowHosts)
}

// route 是路由层：按归一化后的路径分派，并落实第 4 道防线
//
// 分派规则（顺序即优先级）：
//   - "/"：首页，由 Config.Index 生成，或从 Assets 读 IndexName
//   - "/api/" 前缀：交给 Config.API；为 nil 时 404，且刻意不回落静态资源，
//     免得把 "api" 这样的路径名当成资源名去查
//   - "/" + IndexName：一律 404，理由见 serveRoot
//   - 其余：静态资源
//
// 先用 normalizePath 归一："/./x"、"//x"、"a/../x" 都会先被规整，避免同一份内容被
// 多个等价路径命中而绕过某条按路径编写的规则（尤其是第 4 道）
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := normalizePath(r.URL.Path)

	switch {
	case p == "/":
		s.serveRoot(w, r)
	case strings.HasPrefix(p, "/api/"):
		if s.cfg.API == nil {
			http.NotFound(w, r)
			return
		}
		s.cfg.API.ServeHTTP(w, r)
	case p == "/"+s.cfg.IndexName:
		// 第 4 道防线：显式拒绝首页文件本身
		//
		// 为什么必须单独一条：首页文件在静态资源里还有一个真实名字（通常是 index.html），
		// 而静态资源一律放行，于是 /index.html 会绕过 token 把首页交出去。首页通常不含机密，
		// 但它往往带着运行期状态，这条旁路一旦成立，日后任何「往首页写运行期数据」的改动
		// 都会变成无凭据可读。按 404 而不是重定向到 "/"：重定向会把请求送回受保护路径，
		// 等于用 302 告诉探测者「这个文件存在且受保护」
		http.NotFound(w, r)
	default:
		s.serveAsset(w, r, p)
	}
}

// serveRoot 生成首页响应
//
// 两个响应头是刻意区分的：
//   - Content-Type 显式带 charset：缺少 BOM 时浏览器会按本地编码猜，中文会乱码
//   - Cache-Control: no-store：首页可能被注入运行期状态（例如当前语言），而下游靠
//     「切换语言后整页重载」对齐，一旦被缓存，重载拿到的还是旧内容。no-store 要求任何
//     一层缓存都不留副本，比静态资源用的 no-cache 更强（no-cache 允许缓存但必须回源校验）
//
// Assets 与 Index 都为 nil 时返回 404：这是调用方配置错误，但不该 panic，也不该返回
// 空 200（空页面更难排查）
func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if s.cfg.Index != nil {
		body = s.cfg.Index()
	} else {
		// 先挡 nil：fs.ReadFile 对 nil 的 fs.FS 会直接 panic（它在方法调用上不做判空），
		// 而「既没给 Index 也没给 Assets」是调用方很容易踩到的配置组合，
		// 一个 404 远比整个进程崩掉便于排查
		if s.cfg.Assets == nil {
			http.NotFound(w, r)
			return
		}
		content, err := fs.ReadFile(s.cfg.Assets, s.cfg.IndexName)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = content
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveAsset 从 Assets 里托管一份静态资源，带内容哈希 ETag 的条件请求支持
//
// 为什么不用 http.FileServerFS：它靠 fs.FileInfo.ModTime 做条件请求，而 embed.FS 的
// ModTime 是零值，服务端等于不提供任何校验器，浏览器每次都要重下整份资源；它还会把
// "/index.html" 301 重定向到 "/"，正好绕过第 4 道防线。内容哈希 ETag 对 embed.FS 与磁盘
// fs.FS 都成立，也不必要求调用方的资源带时间戳
//
// 缓存：按路径缓存内容与哈希，命中时比对修改时间与大小（见 assetEntry）
//
// 响应头：ETag 取内容哈希的十六进制（强校验器，内容哈希能保证字节级一致）；
// Cache-Control 用 no-cache（允许缓存但每次使用前必须回源校验），与首页的 no-store 刻意
// 不同——静态资源不含数据、不随运行期状态变化，走校验省下的是整份文件的传输
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, cleanedPath string) {
	name := strings.TrimPrefix(cleanedPath, "/")
	// fs.FS 的路径规则比 URL 严：不接受前导斜杠、"." 与 ".."。
	// 归一后的 URL 路径理论上已经安全，但这里再按 fs.FS 的规则校验一次，
	// 让「能不能被读取」只由 fs.FS 说了算
	if name == "" || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}

	entry, err := s.asset(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("ETag", entry.etag)
	w.Header().Set("Cache-Control", "no-cache")

	// 按 RFC 9110，If-None-Match 只需包含当前 ETag（可能是列表）即算命中
	if etagMatches(r.Header.Get("If-None-Match"), entry.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Content-Type 刻意放在 304 判断之后：304 的语义是「用你已有的那份」，
	// 它不该携带表示元数据，否则等于向浏览器声明这次响应有一种新的媒体类型
	w.Header().Set("Content-Type", entry.ctype)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(entry.body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.body)
}

// asset 取出一份静态资源的内容、ETag 与 Content-Type，优先命中缓存
func (s *Server) asset(name string) (assetEntry, error) {
	assets := s.assets()
	if assets == nil {
		return assetEntry{}, fs.ErrNotExist
	}

	file, err := assets.Open(name)
	if err != nil {
		return assetEntry{}, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return assetEntry{}, err
	}
	// 目录不参与托管：列目录会把资源清单交出去，而调用方从未打算公开它。
	// 索引页（"/"）由 serveRoot 单独负责，因此这里没有任何需要放行的目录
	if info.IsDir() {
		return assetEntry{}, fs.ErrNotExist
	}

	// 缓存按路径存放，命中与否取决于文件状态是否与缓存条目一致：
	// embed.FS 的修改时间恒为零值、大小也不变，因此每个路径只会读一次；
	// 磁盘 fs.FS 上的资源被替换后（修改时间或大小变化）会自动重新读取
	s.assetMu.RLock()
	cached, ok := s.assetCache[name]
	s.assetMu.RUnlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached, nil
	}

	body, err := fs.ReadFile(assets, name)
	if err != nil {
		return assetEntry{}, err
	}

	entry := assetEntry{
		body:    body,
		etag:    fmt.Sprintf("\"%x\"", sha256.Sum256(body)),
		ctype:   contentTypeFor(name),
		modTime: info.ModTime(),
		size:    info.Size(),
	}

	s.assetMu.Lock()
	s.assetCache[name] = entry
	s.assetMu.Unlock()

	return entry, nil
}

// assets 返回调用方给的静态资源根，nil 安全的访问器
func (s *Server) assets() fs.FS {
	return s.cfg.Assets
}

// contentTypeFor 按扩展名给出静态资源的 Content-Type
//
// 常见类型写死在表里而不是全靠 mime.TypeByExtension：后者读的是系统 mime.types，
// 在最小化的容器镜像里可能缺失，届时 css 会退化成 text/plain。这里只对调用方最常用的
// 类型做保证，其余交给 mime 库判断，最后再退到二进制流
// 显式带上 charset 是刻意的：css 与 js 里若含中文，缺少 charset 时浏览器会按本地编码猜
func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if byExt := mime.TypeByExtension(filepath.Ext(name)); byExt != "" {
		return byExt
	}
	return "application/octet-stream"
}

// etagMatches 判断 If-None-Match 头是否命中给定 ETag
//
// 按 RFC 9110 处理两种形态：单值精确匹配，以及逗号分隔的列表（浏览器在缓存多份变体时
// 会带上列表）。"*" 表示「任何表示都算命中」，但静态资源总是有具体内容，这里不把它当作命中
// 潜在影响点：弱校验器前缀 "W/" 被忽略（比较的是弱比较语义），因为强 ETag 与弱 ETag
// 指向的内容一致，误判只会多传一次 body，不会拿到错内容
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for candidate := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(strings.TrimPrefix(candidate, "W/")) == etag {
			return true
		}
	}
	return false
}

// normalizePath 把 URL 路径归一成以 "/" 开头、不含 "." / ".." / 重复斜杠的规范形式
//
// 归一化的目的是让「按路径编写的规则」无法被等价写法绕过：token 门禁认 "/api/" 前缀，
// 第 4 道防线认 "/" + IndexName，两者都必须建立在同一份归一化结果上，否则
// "/./api/config" 这类写法就成了绕过门禁的入口
// path.Clean 会清掉尾部的 "/"（"/api/" 变成 "/api"），这一点对前缀判定是有利的：
// 判定前先补一次，见调用点
func normalizePath(raw string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(raw, "/"))
	if strings.HasSuffix(raw, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// AllowedHosts 返回本次生效的完整白名单（已归一化、去重、按字典序排序）
//
// 内容包含内置回环地址、绑定地址与调用方传入的 AllowHosts。调用方打印它的唯一目的是
// 让用户确认「我加的那个地址到底被读到了没有」（字段名写错、主机名拼错都不会报错）；
// 展示与实际判定共用 buildAllowedHosts，因此不会出现「打印一套、实际放行另一套」
func (s *Server) AllowedHosts() []string {
	return allowedHostDisplay(s.allowHosts)
}

// LoopbackBound 返回绑定地址是否是回环地址，供调用方决定是否展示安全警告
func (s *Server) LoopbackBound() bool {
	return isLoopbackHost(s.host)
}

// Addr 返回监听地址的 "host:port" 形式
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Port 返回实际使用的端口
//
// 与 Addr 里解析出来的端口是同一个值：Random 模式下端口由系统决定，调用方必须能拿到它，
// 否则无法把地址告诉用户，也无法在自己的日志里留下可复现的记录
func (s *Server) Port() int {
	return s.port
}

// URL 返回面向用户的地址；启用 token 门禁时带上 token 查询参数
//
// 主机名的选择与绑定地址解耦：绑定 "0.0.0.0" / "::" 时这些地址不能直接放进浏览器的地址栏
// （它们不是可导航的主机），回退到 localhost 才能在用户自己的机器上打开；
// 显式绑定具体地址（例如 192.168.1.5）时沿用绑定值，因为那正是用户会从别的机器访问的地址
//
// token 放查询参数是「首屏」的唯一可行方式：第一次请求由浏览器地址栏发起，无法附加请求头
// 潜在影响点：URL 一旦被打印或复制，token 就随之泄露，因此调用方不该把 URL 写进长期日志
// 或上报数据；前端应在首屏后改用 X-WebUI-Token 请求头。地址栏里是否保留 token 交给调用方
// 决定：清掉更干净，代价是刷新与复制链接会失去凭据
func (s *Server) URL() string {
	authority := net.JoinHostPort(s.urlHost(), fmt.Sprintf("%d", s.port))
	url := "http://" + authority
	if s.token != "" {
		url += "/?token=" + s.token
	}
	return url
}

// urlHost 给出面向用户的地址里该用的主机名
func (s *Server) urlHost() string {
	normalized := normalizeHost(s.host)
	// 通配地址与空值都无法在浏览器里导航，一律回退到 localhost
	if normalized == "" || normalized == "0.0.0.0" || normalized == "::" {
		return "localhost"
	}
	return s.host
}

// Summary 返回启动摘要：服务地址（含 token）与本次生效的完整白名单
//
// 为什么与白名单同段：白名单有多个来源（绑定地址、命令行开关、设置文件），用户只能靠它
// 确认设置文件里的条目是否真的被读到
// 为什么返回字符串而不是直接打印：本包不引入彩色输出依赖，打印位置留给调用方；
// 这类信息属于必须默认可见的启动摘要，而日志默认级别往往会把它过滤掉
func (s *Server) Summary() string {
	return l10n.T("Service started: {{.URL}}", map[string]any{"URL": s.URL()}) + "\n" +
		l10n.T("Allowed hosts for this session: {{.Hosts}}", map[string]any{
			"Hosts": strings.Join(s.AllowedHosts(), ", "),
		})
}

// NonLoopbackWarning 返回绑定非回环地址时的警告文案，绑定回环时返回空串
//
// 为什么必须存在：非回环绑定意味着同网段任何人都能打开这个 WebUI，而它通常能直接改写
// 调用方的数据文件；默认值（回环）下保持安静，只有真正扩大了访问面才警告
// 返回空串而不是让调用方自己判断回环：判断依据属于本包的内部知识，放到调用方会漂移
// （例如把 0.0.0.0 误判成回环，用户对暴露毫不知情）
func (s *Server) NonLoopbackWarning() string {
	if s.LoopbackBound() {
		return ""
	}
	return l10n.T("The WebUI is bound to {{.Host}}, a non-loopback address; anyone who can reach this port can access the data served by this service",
		map[string]any{"Host": s.host})
}

// Serve 开始服务并阻塞，直到 Close 被调用或出错
//
// 返回 nil 表示「被正常关闭」：Close 会让 http.Server.Serve 返回 http.ErrServerClosed，
// 那是关闭流程的正常一步，不该被调用方当成故障上报（否则每次正常退出都会打印一条错误）
//
// OpenBrowser 的拉起放在这里而不是 New：New 刻意不产生任何用户可见的副作用，
// 浏览器应当在服务真正开始监听请求之后才打开
func (s *Server) Serve() error {
	if s.cfg.OpenBrowser {
		tryOpenBrowser(s.URL())
	}
	if err := s.http.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("%s: %w", l10n.T("Service failed to run", nil), err)
	}
	return nil
}

// Close 关闭服务并释放端口
//
// 用 http.Server.Close 而不是 Shutdown：本服务只有本机用户，没有需要优雅等待的长连接
// （SSE 之类的长连接由调用方自己决定要不要在关闭前通知客户端），立即关闭能保证
// 调用方在 Close 返回后立刻可以重新绑定同一个端口，这对「重启服务」的场景最实用
// 幂等：重复调用返回 nil，不报错——关闭流程里出现「关了两次」是常事
func (s *Server) Close() error {
	err := s.http.Close()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
