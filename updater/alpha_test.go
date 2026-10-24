package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// testCommit 是测试中使用的完整提交号，恰好 40 位十六进制
const testCommit = "0123456789abcdef0123456789abcdef01234567"

// testBuildTime 是构建标记中的时间，保持 RFC3339 原样
const testBuildTime = "2026-10-20T09:30:00Z"

// alphaServer 是模拟按标签查询 Release 的测试服务器
// 与 releaseServer 分开，是因为它只接受单个对象，且需要断言请求路径确实带上了标签
type alphaServer struct {
	*httptest.Server

	mu    sync.Mutex
	paths []string
}

// newAlphaServer 构造返回指定 Release 的假接口，statusCode 用于模拟异常响应
func newAlphaServer(t *testing.T, release Release, statusCode int) *alphaServer {
	t.Helper()

	server := &alphaServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.paths = append(server.paths, r.URL.Path)
		server.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			if err := json.NewEncoder(w).Encode(release); err != nil {
				t.Errorf("写入假 Release 响应失败: %v", err)
			}
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// recordedPaths 返回收到的请求路径副本
func (s *alphaServer) recordedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// newAlphaUpdater 构造一个指向假接口、使用测试资产前缀的升级器
func newAlphaUpdater(t *testing.T, server *alphaServer, reporter Reporter) *Updater {
	t.Helper()

	updater, err := New(Config{
		Owner:       "owner",
		Repo:        "repo",
		APIEndpoint: server.URL,
		Reporter:    reporter,
		AssetName:   func(string, string) (string, bool) { return testPlatformPrefix, true },
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}
	return updater
}

// alphaRelease 构造一个带构建标记的滚动预发布
func alphaRelease(marker string) Release {
	return Release{
		TagName: "alpha",
		Body:    marker + "\n\n## 主要更新内容\n",
		Assets:  []Asset{platformAsset("alpha")},
	}
}

// TestCheckAlphaSameCommit 验证本地短提交是标记中完整提交的前缀时判定为已是最新
func TestCheckAlphaSameCommit(t *testing.T) {
	server := newAlphaServer(t, alphaRelease(fmt.Sprintf("<!-- alpha-build: %s %s -->", testCommit, testBuildTime)), http.StatusOK)
	updater := newAlphaUpdater(t, server, newRecordingReporter())

	info, err := updater.Check(testCommit[:7], "", ChannelAlpha)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info != nil {
		t.Fatalf("同一提交不应提示更新，实际得到 %q", info.LatestVersion)
	}

	paths := server.recordedPaths()
	if len(paths) != 1 || paths[0] != "/repos/owner/repo/releases/tags/alpha" {
		t.Fatalf("请求路径 = %v，期望单次请求按标签查询", paths)
	}
}

// TestCheckAlphaDifferentCommit 验证提交号不同时返回可升级信息，字段取自标记与资产
func TestCheckAlphaDifferentCommit(t *testing.T) {
	server := newAlphaServer(t, alphaRelease(fmt.Sprintf("<!-- alpha-build: %s %s -->", testCommit, testBuildTime)), http.StatusOK)
	updater := newAlphaUpdater(t, server, newRecordingReporter())

	info, err := updater.Check("f00dbabe", "", ChannelAlpha)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil {
		t.Fatal("提交号不同时应返回可升级信息，实际判定为已是最新")
	}
	if !info.CurrentComparable {
		t.Fatal("本地为提交号时应能完成比较，CurrentComparable 不应为 false")
	}
	if info.LatestVersion != testCommit[:7] {
		t.Fatalf("最新版本 = %q，期望 7 位短提交 %q", info.LatestVersion, testCommit[:7])
	}
	if info.LatestBuildTime != testBuildTime {
		t.Fatalf("构建时间 = %q，期望 %q", info.LatestBuildTime, testBuildTime)
	}
	if info.AssetName != testPlatformPrefix || info.Digest != platformAsset("alpha").Digest {
		t.Fatalf("资产未透传: %+v", info)
	}
}

// TestCheckAlphaVersionCurrent 验证本地为正式版本号时仍可取包，但声明不可比较
func TestCheckAlphaVersionCurrent(t *testing.T) {
	server := newAlphaServer(t, alphaRelease(fmt.Sprintf("<!-- alpha-build: %s %s -->", testCommit, testBuildTime)), http.StatusOK)
	updater := newAlphaUpdater(t, server, newRecordingReporter())

	info, err := updater.Check("1.2.3", "", ChannelAlpha)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil {
		t.Fatal("从正式版切到 alpha 时应能取到最新构建")
	}
	if info.CurrentComparable {
		t.Fatal("正式版本号无法与提交号比较，CurrentComparable 应为 false")
	}
}

// TestCheckAlphaNotFound 验证标签尚未创建时返回明确错误，而不是"已是最新"
func TestCheckAlphaNotFound(t *testing.T) {
	server := newAlphaServer(t, Release{}, http.StatusNotFound)
	updater := newAlphaUpdater(t, server, newRecordingReporter())

	info, err := updater.Check(testCommit[:7], "", ChannelAlpha)
	if err == nil {
		t.Fatal("404 应返回错误，实际返回 nil")
	}
	if info != nil {
		t.Fatalf("404 时不应返回升级信息，实际得到 %q", info.LatestVersion)
	}
}

// TestCheckAlphaMissingMarker 验证标记缺失时只告警，仍返回包但不宣称有更新
func TestCheckAlphaMissingMarker(t *testing.T) {
	server := newAlphaServer(t, alphaRelease("## 主要更新内容"), http.StatusOK)
	reporter := newRecordingReporter()
	updater := newAlphaUpdater(t, server, reporter)

	info, err := updater.Check(testCommit[:7], "", ChannelAlpha)
	if err != nil {
		t.Fatalf("标记缺失不应中断检查: %v", err)
	}
	if info == nil {
		t.Fatal("标记缺失时仍应返回可下载的构建")
	}
	if info.CurrentComparable {
		t.Fatal("没有标记就无法比较，CurrentComparable 应为 false")
	}
	if info.LatestVersion != "alpha" {
		t.Fatalf("标记不可用时 LatestVersion = %q，期望标签名", info.LatestVersion)
	}
	if !reporter.sawWarning("alpha") {
		t.Fatal("标记缺失时应告警说明无法比较")
	}
}

// TestCheckStableStillUsesReleaseList 验证新增通道没有改变正式版通道的查询方式
func TestCheckStableStillUsesReleaseList(t *testing.T) {
	server := newReleaseServer(t, []Release{
		{TagName: "v1.1.0", Assets: []Asset{platformAsset("v1.1.0")}},
	}, http.StatusOK)
	updater := newTestUpdater(t, server, newRecordingReporter())

	info, err := updater.Check("1.0.0", "", ChannelStable)
	if err != nil {
		t.Fatalf("检查更新失败: %v", err)
	}
	if info == nil || info.LatestVersion != "v1.1.0" {
		t.Fatalf("正式版通道行为发生变化: %+v", info)
	}
	if path := server.lastRequest().path; path != "/repos/owner/repo/releases" {
		t.Fatalf("请求路径 = %q，期望仍走 Release 列表", path)
	}
}

// TestNewDefaultsAlphaTag 验证未显式配置时使用默认的固定标签
func TestNewDefaultsAlphaTag(t *testing.T) {
	updater, err := New(Config{
		Owner:     "owner",
		Repo:      "repo",
		Reporter:  newRecordingReporter(),
		AssetName: func(string, string) (string, bool) { return testPlatformPrefix, true },
	})
	if err != nil {
		t.Fatalf("构造升级器失败: %v", err)
	}
	if updater.cfg.AlphaTag != "alpha" {
		t.Fatalf("AlphaTag = %q，期望 alpha", updater.cfg.AlphaTag)
	}
}

// TestCheckAlphaMarkerFormat 验证只有首行精确匹配的标记才会被采纳
func TestCheckAlphaMarkerFormat(t *testing.T) {
	cases := map[string]string{
		"缺少注释包裹":       fmt.Sprintf("alpha-build: %s %s", testCommit, testBuildTime),
		"提交号长度不足":      fmt.Sprintf("<!-- alpha-build: %s %s -->", testCommit[:12], testBuildTime),
		"时间不是 RFC3339": fmt.Sprintf("<!-- alpha-build: %s %s -->", testCommit, "2026-10-20 09:30:00"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseAlphaMarker(body + "\n\n说明"); ok {
				t.Fatalf("格式不合法的标记不应被采纳: %q", body)
			}
		})
	}

	commit, builtAt, ok := parseAlphaMarker(strings.Join([]string{
		"<!-- alpha-build: " + testCommit + " 2026-10-20T17:30:00+08:00 -->",
		"",
		"说明",
	}, "\r\n"))
	if !ok {
		t.Fatal("合法的标记应被采纳")
	}
	if commit != testCommit || builtAt != "2026-10-20T17:30:00+08:00" {
		t.Fatalf("解析结果 = %q, %q", commit, builtAt)
	}
}
