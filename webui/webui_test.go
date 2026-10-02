package webui

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
	"github.com/jy-eggroll/eggokit/locales"
)

// TestMain 先把语言初始化为默认语言
//
// 本包要断言的摘要与警告文案都带模板变量，而 l10n.T 只在 Init 之后才查语言文件。
// 虽然 l10n 的回退路径（renderSource）保证未 Init 时占位符也会被填上，这里依然 Init，
// 是为了让用例跑在与真实调用方一致的路径上——调用方总是先 Init 再起服务
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

// 本文件覆盖 Server 层面的行为：路由分派、token 门禁、静态资源的条件请求、端口策略与
// 几条对外字符串（URL / Summary / NonLoopbackWarning）
//
// 约定：一律用内存里的 httptest.ResponseRecorder 直接驱动 Server.handler()，不发起真实 HTTP
// 请求；只有端口策略的用例需要真的监听（那是被测对象本身），且都绑在回环地址上

// testAssets 是一份最小的前端资源集，用来验证托管、Content-Type 与 ETag
// 用 fstest.MapFS 而不是临时目录：它是纯内存的，用例之间互不影响，也不会留下任何文件
func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>index</h1>")},
		"style.css":  &fstest.MapFile{Data: []byte("body{color:red}")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log(1)")},
		"sub/p.svg":  &fstest.MapFile{Data: []byte("<svg/>")},
	}
}

// newTestServer 建一个绑在回环地址、端口由系统分配的服务，并在用例结束时关闭
// 所有 Server 层面的用例都从这里起步，保证不会漏掉 Close（否则测试进程会留下监听中的 socket）
func newTestServer(t *testing.T, mutate func(*Config)) *Server {
	t.Helper()

	cfg := Config{
		Host:        "127.0.0.1",
		Port:        Random(),
		Assets:      testAssets(),
		OpenBrowser: false,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	server, err := New(cfg)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close 失败: %v", err)
		}
	})
	return server
}

// tokenOf 从 URL 里取出 token 查询参数，取不到时让用例立刻失败
func tokenOf(t *testing.T, server *Server) string {
	t.Helper()

	parsed, err := url.Parse(server.URL())
	if err != nil {
		t.Fatalf("URL %q 解析失败: %v", server.URL(), err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("URL %q 里没有 token", server.URL())
	}
	return token
}

// call 构造一个 Host 合法的请求并驱动完整的处理链，返回 recorder
func call(server *Server, method, target, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.Host = server.Addr()
	if token != "" {
		query := request.URL.Query()
		query.Set("token", token)
		request.URL.RawQuery = query.Encode()
	}
	recorder := httptest.NewRecorder()
	server.handler().ServeHTTP(recorder, request)
	return recorder
}

// TestTokenGate 校验 token 门禁的三种凭据状态与两种携带方式
//
// 为什么需要正反两组：只测「正确 token 放行」无法发现门禁根本没生效，
// 只测「错误 token 拒绝」无法发现门禁把合法请求也挡住了
// 静态资源的放行单独一条：门禁若扩大到全部路径，页面会因为 css/js 被 401 而白屏
func TestTokenGate(t *testing.T) {
	apiHit := 0
	server := newTestServer(t, func(cfg *Config) {
		cfg.Auth = Auth{Enabled: true}
		cfg.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiHit++
			w.WriteHeader(http.StatusOK)
		})
	})
	token := tokenOf(t, server)

	tests := []struct {
		name       string
		method     string
		target     string
		token      string
		wantStatus int
	}{
		{name: "首页带正确 token 放行", method: http.MethodGet, target: "/", token: token, wantStatus: http.StatusOK},
		{name: "首页缺 token 拒绝", method: http.MethodGet, target: "/", wantStatus: http.StatusUnauthorized},
		{name: "首页 token 错误拒绝", method: http.MethodGet, target: "/", token: "wrong-token", wantStatus: http.StatusUnauthorized},
		{name: "API 带正确 token 放行", method: http.MethodGet, target: "/api/thing", token: token, wantStatus: http.StatusOK},
		{name: "API 缺 token 拒绝", method: http.MethodGet, target: "/api/thing", wantStatus: http.StatusUnauthorized},
		{name: "静态资源无 token 放行", method: http.MethodGet, target: "/style.css", wantStatus: http.StatusOK},
		{name: "静态资源带错误 token 仍然放行", method: http.MethodGet, target: "/app.js", token: "wrong-token", wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := call(server, tc.method, tc.target, tc.token)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d（响应体 %q）", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			// 401 响应体必须是极简的标准文案，不能回显 token 或内部判定细节
			if tc.wantStatus == http.StatusUnauthorized {
				if body := recorder.Body.String(); !strings.Contains(body, "Unauthorized") || strings.Contains(body, "token") {
					t.Fatalf("401 响应体 = %q，期望只含标准文案且不含细节", body)
				}
			}
		})
	}

	// 请求头携带是首屏之后的常规路径，必须是独立于查询参数的一条通路
	t.Run("请求头携带正确 token 放行", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/thing", nil)
		request.Host = server.Addr()
		request.Header.Set(tokenHeaderName, token)
		recorder := httptest.NewRecorder()

		server.handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", recorder.Code)
		}
	})

	if apiHit == 0 {
		t.Fatal("API handler 从未被调用，说明路由没有把 /api/ 前缀交给调用方")
	}
}

// TestAuthDisabledNoToken 校验关闭 Auth 时不生成 token、URL 也不带查询参数
//
// 防的回归：仍然生成 token 只是不校验——凭据会白留在内存与摘要输出里，
// 徒增泄露面，且会让「URL 里有没有 token」不再能反映门禁是否生效
func TestAuthDisabledNoToken(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.Auth = Auth{Enabled: false}
	})

	if strings.Contains(server.URL(), "token=") {
		t.Fatalf("Auth 未启用时 URL 不应带 token，实际为 %q", server.URL())
	}
	// 门禁关闭时首页直接可读，且静态资源照旧
	if recorder := call(server, http.MethodGet, "/", ""); recorder.Code != http.StatusOK {
		t.Fatalf("Auth 未启用时首页状态码 = %d，期望 200", recorder.Code)
	}
	if recorder := call(server, http.MethodGet, "/style.css", ""); recorder.Code != http.StatusOK {
		t.Fatalf("Auth 未启用时静态资源状态码 = %d，期望 200", recorder.Code)
	}
}

// TestIndexNamePathIsRejected 校验第 4 道防线："/" + IndexName 必须 404，且有无 token 都是 404
//
// 防的回归：静态资源一律放行，如果首页文件能被它自己的真实文件名取到，
// token 门禁就被完全绕过——首页里往往被调用方注入了运行期状态，这条旁路一旦成立，
// 日后任何「往首页塞数据」的改动都会同时变成「无凭据可读」
// 自定义 IndexName 也单独守一条：规则必须跟着配置走，而不是写死 index.html
func TestIndexNamePathIsRejected(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.Auth = Auth{Enabled: true}
	})
	token := tokenOf(t, server)

	for _, target := range []string{"/index.html", "/./index.html"} {
		t.Run("无 token "+target, func(t *testing.T) {
			if recorder := call(server, http.MethodGet, target, ""); recorder.Code != http.StatusNotFound {
				t.Fatalf("状态码 = %d，期望 404", recorder.Code)
			}
		})
		t.Run("带 token "+target, func(t *testing.T) {
			if recorder := call(server, http.MethodGet, target, token); recorder.Code != http.StatusNotFound {
				t.Fatalf("状态码 = %d，期望 404", recorder.Code)
			}
		})
	}

	t.Run("自定义 IndexName 同样被拒", func(t *testing.T) {
		custom := newTestServer(t, func(cfg *Config) {
			cfg.IndexName = "main.html"
			cfg.Assets = fstest.MapFS{
				"main.html": &fstest.MapFile{Data: []byte("<h1>main</h1>")},
			}
		})
		if recorder := call(custom, http.MethodGet, "/main.html", ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404", recorder.Code)
		}
		if recorder := call(custom, http.MethodGet, "/", ""); recorder.Code != http.StatusOK {
			t.Fatalf("首页状态码 = %d，期望 200", recorder.Code)
		}
	})
}

// TestRootResponseHeaders 校验首页的两个响应头
//
// Cache-Control 必须是 no-store 而不是 no-cache：首页可能被调用方注入运行期状态（例如当前语言），
// 下游靠「切换后整页重载」对齐；no-cache 只要求回源校验，一旦校验器命中（内容恰好没变），
// 浏览器仍会复用旧副本，用户会看到「切换没生效」。no-store 要求任何一层都不留副本
// Content-Type 必须显式带 charset：HTML 里含中文时缺少字符集会被按本地编码猜，直接乱码
func TestRootResponseHeaders(t *testing.T) {
	server := newTestServer(t, nil)

	recorder := call(server, http.MethodGet, "/", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q，期望 no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q，期望 text/html; charset=utf-8", got)
	}
}

// TestIndexFuncOverridesAssets 校验 Config.Index 优先于 Assets 里的文件
// 调用方靠它注入运行期状态，如果仍去读静态文件，注入的内容会被静默丢弃
func TestIndexFuncOverridesAssets(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.Index = func() []byte { return []byte("<p>dynamic</p>") }
	})

	recorder := call(server, http.MethodGet, "/", "")
	if body := recorder.Body.String(); body != "<p>dynamic</p>" {
		t.Fatalf("首页内容 = %q，期望注入的内容", body)
	}
}

// TestAssetServingAndConditionalRequest 校验静态资源的 Content-Type、ETag 与 304
//
// 为什么专门守 304：embed.FS 的 ModTime 是零值，若改用 http.FileServerFS，
// 服务端给不出可用的校验器，浏览器每次都要重传整份 css/js；而且 FileServer 会把
// /index.html 301 重定向到 /，正好绕过第 4 道防线。基于内容哈希的 ETag 是这两点的共同答案
func TestAssetServingAndConditionalRequest(t *testing.T) {
	server := newTestServer(t, nil)

	t.Run("css 的 Content-Type 与缓存头", func(t *testing.T) {
		recorder := call(server, http.MethodGet, "/style.css", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
			t.Fatalf("Content-Type = %q，期望 text/css; charset=utf-8", got)
		}
		// 静态资源与首页刻意不同：资源不含运行期状态，走回源校验即可
		if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q，期望 no-cache", got)
		}
		if recorder.Header().Get("ETag") == "" {
			t.Fatal("静态资源必须带 ETag")
		}
	})

	t.Run("js 的 Content-Type", func(t *testing.T) {
		recorder := call(server, http.MethodGet, "/app.js", "")
		if got := recorder.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
			t.Fatalf("Content-Type = %q，期望 application/javascript; charset=utf-8", got)
		}
	})

	t.Run("子目录资源可访问", func(t *testing.T) {
		recorder := call(server, http.MethodGet, "/sub/p.svg", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Fatalf("Content-Type = %q，期望 image/svg+xml", got)
		}
	})

	t.Run("If-None-Match 命中返回 304 且无 body", func(t *testing.T) {
		first := call(server, http.MethodGet, "/style.css", "")
		etag := first.Header().Get("ETag")

		request := httptest.NewRequest(http.MethodGet, "/style.css", nil)
		request.Host = server.Addr()
		request.Header.Set("If-None-Match", etag)
		recorder := httptest.NewRecorder()
		server.handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNotModified {
			t.Fatalf("状态码 = %d，期望 304", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("304 不应带 body，实际 %q", recorder.Body.String())
		}
		// 304 也必须带上 ETag 与缓存头，否则浏览器下次仍无法校验
		if recorder.Header().Get("ETag") != etag {
			t.Fatalf("304 的 ETag = %q，期望 %q", recorder.Header().Get("ETag"), etag)
		}
	})

	t.Run("不同内容的 ETag 不同", func(t *testing.T) {
		css := call(server, http.MethodGet, "/style.css", "").Header().Get("ETag")
		js := call(server, http.MethodGet, "/app.js", "").Header().Get("ETag")
		if css == js {
			t.Fatalf("两份内容不同的资源 ETag 相同（%q），说明 ETag 不是内容哈希", css)
		}
	})

	t.Run("目录请求不被托管理", func(t *testing.T) {
		if recorder := call(server, http.MethodGet, "/sub/", ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("状态码 = %d，期望 404", recorder.Code)
		}
	})
}

// TestUnknownPathAndNilAPI 校验未知路径 404、API 为 nil 时 /api/ 前缀 404 且不回落静态资源
//
// 不回落是刻意的：静态资源来自调用方，若把 "api" 当成资源名去查，
// 一个恰好叫 api 的文件就会在 API 未启用时被当成端点响应，语义完全错位
func TestUnknownPathAndNilAPI(t *testing.T) {
	server := newTestServer(t, nil)

	tests := []string{"/nope", "/nope/deep", "/api/config", "/api/"}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			if recorder := call(server, http.MethodGet, target, ""); recorder.Code != http.StatusNotFound {
				t.Fatalf("状态码 = %d，期望 404", recorder.Code)
			}
		})
	}
}

// TestNoAssetsOrIndex 校验 Assets 与 Index 都为 nil 时首页 404 而不是 panic 或空 200
// 空 200 会让调用方以为配置没问题，排查时只能看到一片空白页面
func TestNoAssetsOrIndex(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.Assets = nil
	})

	if recorder := call(server, http.MethodGet, "/", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", recorder.Code)
	}
	if recorder := call(server, http.MethodGet, "/style.css", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", recorder.Code)
	}
}

// TestPortModes 校验两种端口策略各自都能拿到可用端口
//
// Sequential 的用例先让系统分配一个空闲端口再立刻释放，把那号码交给 Sequential：
// 这样断言的是「真的用了用户要求的端口」，而不是「随便拿到了一个端口」
// Random 的用例断言端口有效且确实处于监听状态（能连上）
func TestPortModes(t *testing.T) {
	t.Run("Sequential 使用指定端口", func(t *testing.T) {
		free := freePort(t)

		server, err := New(Config{Host: "127.0.0.1", Port: Sequential(free, 3)})
		if err != nil {
			t.Fatalf("New 失败: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })

		if server.Port() != free {
			t.Fatalf("端口 = %d，期望 %d", server.Port(), free)
		}
		assertListening(t, server)
	})

	t.Run("Sequential 端口被占用时顺延到下一个", func(t *testing.T) {
		blocker, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("预占端口失败: %v", err)
		}
		t.Cleanup(func() { _ = blocker.Close() })
		occupied := blocker.Addr().(*net.TCPAddr).Port

		server, err := New(Config{Host: "127.0.0.1", Port: Sequential(occupied, 2)})
		if err != nil {
			t.Fatalf("New 失败: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })

		if server.Port() != occupied+1 {
			t.Fatalf("端口 = %d，期望顺延到 %d", server.Port(), occupied+1)
		}
	})

	t.Run("Sequential 全部被占用时报错", func(t *testing.T) {
		blocker, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("预占端口失败: %v", err)
		}
		t.Cleanup(func() { _ = blocker.Close() })
		occupied := blocker.Addr().(*net.TCPAddr).Port

		server, err := New(Config{Host: "127.0.0.1", Port: Sequential(occupied, 1)})
		if err == nil {
			_ = server.Close()
			t.Fatal("端口全被占用时 New 应当报错")
		}
	})

	t.Run("Sequential 传 0 时返回内核实际分配的端口", func(t *testing.T) {
		// 端口 0 的含义是「任意可用端口」，此时请求值与实际绑定值不同。
		// 必须断言 Port() 与 URL() 用的是实际端口：若沿用请求值，Summary 与自动打开的
		// 地址都会指向 :0，用户拿到的是一个死链
		server, err := New(Config{Host: "127.0.0.1", Port: Sequential(0, 1)})
		if err != nil {
			t.Fatalf("New 失败: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })

		if server.Port() <= 0 {
			t.Fatalf("端口 = %d，期望内核实际分配的端口", server.Port())
		}
		if !strings.Contains(server.URL(), fmt.Sprintf(":%d", server.Port())) {
			t.Fatalf("URL = %q，应包含实际端口 %d", server.URL(), server.Port())
		}
		assertListening(t, server)
	})

	t.Run("Random 由系统分配可用端口", func(t *testing.T) {
		server := newTestServer(t, nil)

		if server.Port() <= 0 {
			t.Fatalf("端口 = %d，期望有效端口", server.Port())
		}
		assertListening(t, server)
	})

	t.Run("Port 为 nil 等价于 Random", func(t *testing.T) {
		server, err := New(Config{Host: "127.0.0.1", Assets: testAssets()})
		if err != nil {
			t.Fatalf("New 失败: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })

		if server.Port() <= 0 {
			t.Fatalf("端口 = %d，期望有效端口", server.Port())
		}
	})
}

// freePort 让系统分配一个空闲端口后立刻释放，返回端口号
// 存在极小的竞态窗口（释放到重新绑定之间可能被别的进程抢走），本地测试环境下可接受
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("释放空闲端口失败: %v", err)
	}
	return port
}

// assertListening 断言服务的端口确实处于监听状态，避免「拿到了端口号但其实没在听」
func assertListening(t *testing.T, server *Server) {
	t.Helper()

	connection, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatalf("连接 %s 失败: %v", server.Addr(), err)
	}
	_ = connection.Close()
}

// TestServeAndClose 校验 Serve 真的在服务、Close 能让 Serve 正常返回
//
// 断言的落点：Close 之后 Serve 必须返回 nil 而不是 http.ErrServerClosed——
// 后者会让调用方把每次正常退出都当成故障上报
func TestServeAndClose(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.Auth = Auth{Enabled: true}
	})

	done := make(chan error, 1)
	go func() { done <- server.Serve() }()

	response, err := http.Get(server.URL())
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", server.URL(), err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", response.StatusCode)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Close 之后 Serve 返回 %v，期望 nil", err)
	}
}

// TestURLAndAddr 校验对外地址的拼装：回环绑定、通配绑定回退 localhost、显式绑定沿用原值
//
// 通配地址（0.0.0.0 / ::）不能直接放进浏览器地址栏，必须回退到 localhost，
// 否则用户点开摘要里的链接只会得到一个无法导航的地址
func TestURLAndAddr(t *testing.T) {
	t.Run("回环绑定", func(t *testing.T) {
		server := newTestServer(t, nil)
		want := fmt.Sprintf("http://127.0.0.1:%d", server.Port())
		if server.URL() != want {
			t.Fatalf("URL = %q，期望 %q", server.URL(), want)
		}
		if server.Addr() != fmt.Sprintf("127.0.0.1:%d", server.Port()) {
			t.Fatalf("Addr = %q 与 Port %d 不一致", server.Addr(), server.Port())
		}
		if !server.LoopbackBound() {
			t.Fatal("回环绑定应当判定为回环")
		}
	})

	t.Run("通配绑定回退 localhost", func(t *testing.T) {
		// 直接构造而不真的绑定 0.0.0.0：用例只需要地址拼装逻辑，不该把服务暴露到所有网卡
		server := &Server{host: "0.0.0.0", port: 8999}
		if got := server.URL(); got != "http://localhost:8999" {
			t.Fatalf("URL = %q，期望 http://localhost:8999", got)
		}
		if server.LoopbackBound() {
			t.Fatal("0.0.0.0 不应判定为回环")
		}
	})

	t.Run("显式绑定沿用原地址", func(t *testing.T) {
		server := &Server{host: "192.168.1.5", port: 8999}
		if got := server.URL(); got != "http://192.168.1.5:8999" {
			t.Fatalf("URL = %q，期望 http://192.168.1.5:8999", got)
		}
	})
}

// TestSummaryAndNonLoopbackWarning 校验两条对外文案的内容
//
// Summary 必须同时含服务地址与完整白名单：白名单有多个来源，用户只能靠这一行确认
// 设置文件里的条目是否真的被读到（字段名写错、主机名拼错都不会报错）
// 非回环警告在回环绑定下必须是空串：调用方靠「空串即无需展示」来保持默认路径安静
func TestSummaryAndNonLoopbackWarning(t *testing.T) {
	t.Run("回环绑定的摘要", func(t *testing.T) {
		server := newTestServer(t, func(cfg *Config) {
			cfg.AllowHosts = []string{"192.168.1.5"}
		})

		summary := server.Summary()
		if !strings.Contains(summary, fmt.Sprintf("127.0.0.1:%d", server.Port())) {
			t.Fatalf("摘要里缺少服务地址: %q", summary)
		}
		if !strings.Contains(summary, "192.168.1.5") {
			t.Fatalf("摘要里缺少白名单条目: %q", summary)
		}
		if warning := server.NonLoopbackWarning(); warning != "" {
			t.Fatalf("回环绑定的警告 = %q，期望空串", warning)
		}
	})

	t.Run("非回环绑定的警告", func(t *testing.T) {
		server := &Server{host: "0.0.0.0"}
		warning := server.NonLoopbackWarning()
		if warning == "" {
			t.Fatal("非回环绑定必须给出警告")
		}
		if !strings.Contains(warning, "0.0.0.0") {
			t.Fatalf("警告里应包含绑定地址: %q", warning)
		}
	})
}

// TestAllowedHostsOnServer 校验 Server.AllowedHosts 的内容与排序
// 展示与实际判定必须同源，否则会出现「打印一套、实际放行另一套」
func TestAllowedHostsOnServer(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.AllowHosts = []string{"LOCALHOST:8999", "", "192.168.1.5", "192.168.1.5"}
	})

	got := server.AllowedHosts()
	want := []string{"127.0.0.1", "192.168.1.5", "::1", "localhost"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedHosts = %#v，期望 %#v", got, want)
	}
}

// TestAllowedHostsPassThrough 校验调用方传入的额外白名单真的放行了请求
// 只断言列表内容不够：列表对了但 guard 没收到它，功能仍是坏的
func TestAllowedHostsPassThrough(t *testing.T) {
	server := newTestServer(t, func(cfg *Config) {
		cfg.AllowHosts = []string{"192.168.1.5"}
	})

	request := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	request.Host = "192.168.1.5:8999"
	recorder := httptest.NewRecorder()

	server.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("白名单内的 Host 被拒，状态码 = %d，期望 200", recorder.Code)
	}
}

// TestDiskAssets 校验从磁盘目录托管资源（fs.FS 的另一种常见来源）
//
// 这条用例同时覆盖缓存键里的「修改时间 + 大小」分支：磁盘文件的 ModTime 非零，
// 替换内容后应当重新读取；embed.FS 则靠零值 ModTime 退化为按路径缓存
func TestDiskAssets(t *testing.T) {
	dir := t.TempDir()
	cssPath := filepath.Join(dir, "style.css")
	if err := os.WriteFile(cssPath, []byte("body{color:blue}"), 0o644); err != nil {
		t.Fatalf("写入临时资源失败: %v", err)
	}

	server := newTestServer(t, func(cfg *Config) {
		cfg.Assets = os.DirFS(dir)
	})

	first := call(server, http.MethodGet, "/style.css", "")
	if first.Code != http.StatusOK || first.Body.String() != "body{color:blue}" {
		t.Fatalf("状态码 = %d，内容 = %q", first.Code, first.Body.String())
	}

	// 内容变了，ETag 必须跟着变，否则浏览器会一直用旧副本（no-cache 回源校验也救不回来）
	// 等待一小段时间确保 ModTime 走字，避免同毫秒内判定为未变化
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(cssPath, []byte("body{color:green}"), 0o644); err != nil {
		t.Fatalf("覆盖临时资源失败: %v", err)
	}

	second := call(server, http.MethodGet, "/style.css", "")
	if second.Body.String() != "body{color:green}" {
		t.Fatalf("替换后的内容 = %q，期望重新读取", second.Body.String())
	}
	if first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Fatal("内容变化后 ETag 不应相同")
	}
}

// TestCloseIsIdempotent 校验重复 Close 不报错
// 关闭流程里「关了两次」很常见（defer 加显式关闭），返回错误会逼调用方到处写忽略逻辑
func TestCloseIsIdempotent(t *testing.T) {
	server := newTestServer(t, nil)

	if err := server.Close(); err != nil {
		t.Fatalf("第一次 Close 失败: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("第二次 Close 返回 %v，期望 nil", err)
	}
}
