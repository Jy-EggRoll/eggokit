package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
)

// releasePageSize 是单次拉取的 Release 条数
// Release 列表按创建时间倒序返回，目标通道的最新版本必然落在首页，
// 因此只取首页即可，既省流量也避免为几百个历史版本做无谓的解析
const releasePageSize = 50

// Release 是 Release 接口中与升级相关的字段子集
// 只声明必要字段，使上游新增字段时不会影响解析
type Release struct {
	TagName string  `json:"tag_name"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
}

// Asset 是 Release 附件中的一个可下载文件
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	// Digest 是 GitHub 为附件计算的内容摘要，形如 "sha256:<64 位十六进制>"。
	// 下载完成后据此校验内容完整性；为空表示上游未提供（旧数据或自建实例），
	// 此时只能退化为 Content-Length 的长度校验，并会向用户明确告警
	Digest string `json:"digest"`
}

// UpdateInfo 描述一次可用的更新
type UpdateInfo struct {
	// CurrentVersion 与 CurrentBuildTime 是发起检查时传入的本地版本信息，仅用于展示，不参与比较
	CurrentVersion   string
	CurrentBuildTime string

	// CurrentComparable 表示本地版本是否成功解析并与候选版本完成了比较
	// 为 false 时 LatestVersion 只代表目标通道中的最高版本，不能据此断言本地版本更旧，
	// 展示层必须据此调整措辞，避免把未经比较的结果说成"有更新的版本"
	CurrentComparable bool

	// LatestVersion 是候选版本的标签原文
	// alpha 通道的标签没有版本号语义，这里改为填构建标记中的 7 位短提交号；
	// 标记不可用时退回标签原文，因为此时确实没有任何可展示的构建身份
	LatestVersion string

	// LatestBuildTime 是 alpha 构建标记中的构建时间（RFC3339）
	// 其它通道的 Release 没有这个约定，保持为空
	LatestBuildTime string

	// DownloadURL 与 AssetName 描述当前平台应当下载的资产
	DownloadURL string
	AssetName   string

	// Digest 是目标资产的内容摘要（形如 "sha256:<64 位十六进制>"），下载后据此校验完整性。
	// 为空表示 Release 接口未随资产给出摘要，升级器会跳过强校验并告警，
	// 而不是把"没有校验"当成"校验通过"
	Digest string
}

// Check 查询目标通道中是否存在比 current 更新的版本
// 返回值约定：info 为 nil 且 err 为 nil 表示没有可升级的目标
//
// current 无法解析时（典型情形是本地源码构建用时间戳充当版本号）不会中断检查，
// 而是警告后退化为"列出目标通道中的最高版本"：
// 用户依然能看到最新发布并决定是否升级，只是没有比较基准，
// 因此 UpdateInfo.CurrentComparable 会被置为 false，由展示层避免使用"更新"这类断言
func (u *Updater) Check(current, buildTime string, channel Channel) (*UpdateInfo, error) {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	assetPrefix, supported := u.cfg.AssetName(goos, goarch)
	if !supported {
		return nil, fmt.Errorf("%s", l10n.T("No release artifact is available for {{.OS}}/{{.Arch}}", map[string]any{"OS": goos, "Arch": goarch}))
	}

	// alpha 通道的标签固定且会被覆盖，版本号比较在这里没有意义，
	// 必须在解析 current 之前分流，否则提交号会被当成无法解析的版本号而多报一次警告
	if channel == ChannelAlpha {
		return u.checkAlpha(current, buildTime, assetPrefix)
	}

	currentVersion, comparable := ParseVersion(current)
	if !comparable {
		u.cfg.Reporter.Warn("%s", l10n.T("Current version {{.Version}} is not a supported version number (expected x.y.z or x.y.z.dev.n); skipping version comparison", map[string]any{"Version": current}))
	}

	releases, err := u.fetchReleases()
	if err != nil {
		return nil, err
	}

	latest, found := pickLatest(releases, currentVersion, channel, assetPrefix, comparable)
	if !found {
		return nil, nil
	}

	return &UpdateInfo{
		CurrentVersion:    current,
		CurrentBuildTime:  buildTime,
		CurrentComparable: comparable,
		LatestVersion:     latest.TagName,
		DownloadURL:       latest.Assets[0].DownloadURL,
		AssetName:         latest.Assets[0].Name,
		Digest:            latest.Assets[0].Digest,
	}, nil
}

// alphaMarkerPattern 匹配滚动预发布正文首行的构建标记
// 格式由 CI 固定写出，只有完整提交号与构建时间两个字段可供比较；
// 刻意只认这一行而非在全文里搜索，避免把发布说明中恰好出现的示例当成真标记
var alphaMarkerPattern = regexp.MustCompile(`^<!-- alpha-build: ([0-9a-f]{40}) (\S+) -->$`)

// commitPattern 表示本地版本是一个短提交号
// 长度取 7 到 40 位，是因为 git 默认的短提交号是 7 位，而 CI 也可能直接注入完整提交号
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// checkAlpha 查询固定标签下的滚动预发布，并按提交号判断是否需要升级
// 与列表通道不同，这里只发一次按标签查询的请求：标签固定，没有在多个候选之间挑选的必要
func (u *Updater) checkAlpha(current, buildTime, assetPrefix string) (*UpdateInfo, error) {
	release, err := u.fetchReleaseByTag()
	if err != nil {
		return nil, err
	}

	asset, ok := matchAsset(release.Assets, assetPrefix)
	if !ok {
		return nil, fmt.Errorf("%s", l10n.T("No release artifact is available for {{.OS}}/{{.Arch}}", map[string]any{"OS": runtime.GOOS, "Arch": runtime.GOARCH}))
	}

	info := &UpdateInfo{
		CurrentVersion:   current,
		CurrentBuildTime: buildTime,
		// 标签固定，标记不可用时唯一能展示的身份就是标签本身
		LatestVersion: u.cfg.AlphaTag,
		DownloadURL:   asset.DownloadURL,
		AssetName:     asset.Name,
		Digest:        asset.Digest,
	}

	commit, builtAt, ok := parseAlphaMarker(release.Body)
	if !ok {
		// 标记缺失时不猜新旧：仍然返回可下载的包，但把结论交给展示层，
		// 否则一旦 CI 改了标记格式，用户会被永久提示"有更新"
		u.cfg.Reporter.Warn("%s", l10n.T("The alpha build marker is missing or malformed; the local build cannot be compared with the latest build", nil))
		return info, nil
	}

	info.LatestVersion = commit[:7]
	info.LatestBuildTime = builtAt
	info.CurrentComparable = commitPattern.MatchString(current)
	if info.CurrentComparable && (strings.HasPrefix(commit, current) || strings.HasPrefix(current, commit)) {
		// 本地可能就是同一提交的短号或完整号，两个方向都比一次，
		// 避免较长的一方被判成不同提交而永远提示有更新
		return nil, nil
	}
	return info, nil
}

// parseAlphaMarker 解析 Release 正文首行的构建标记
// 时间必须能按 RFC3339 解析，否则整条标记视为不可用：
// 只取到提交号却展示一个解析不了的时间，会让用户以为构建时间是坏的
func parseAlphaMarker(body string) (commit, builtAt string, ok bool) {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSuffix(line, "\r")

	matched := alphaMarkerPattern.FindStringSubmatch(line)
	if matched == nil {
		return "", "", false
	}
	parsed, err := time.Parse(time.RFC3339, matched[2])
	if err != nil {
		return "", "", false
	}
	return matched[1], parsed.Format(time.RFC3339), true
}

// fetchReleaseByTag 查询固定标签对应的单个 Release
// 404 是 alpha 通道的正常初始状态，单独给出可读错误，
// 而不是让用户从裸状态码里猜测"还没有构建过"
func (u *Updater) fetchReleaseByTag() (Release, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases/tags/%s",
		u.cfg.APIEndpoint, u.cfg.Owner, u.cfg.Repo, url.PathEscape(u.cfg.AlphaTag),
	)

	req, err := u.newReleaseRequest(endpoint)
	if err != nil {
		return Release{}, err
	}

	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("%s: %w", l10n.T("Failed to query the Release", nil), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("%s", l10n.T("No alpha build has been published yet: the Release tagged {{.Tag}} does not exist", map[string]any{"Tag": u.cfg.AlphaTag}))
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("%s", l10n.T("Failed to query the Release, status code: {{.Code}}", map[string]any{"Code": resp.StatusCode}))
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("%s: %w", l10n.T("Failed to parse the Release response", nil), err)
	}
	return release, nil
}

// fetchReleases 拉取仓库最近的 Release 列表
// 认证令牌只影响限速配额：匿名调用每小时额度很低，配置令牌后不影响其他行为
func (u *Updater) fetchReleases() ([]Release, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases?per_page=%d",
		u.cfg.APIEndpoint, u.cfg.Owner, u.cfg.Repo, releasePageSize,
	)

	req, err := u.newReleaseRequest(endpoint)
	if err != nil {
		return nil, err
	}

	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to query the Release", nil), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", l10n.T("Failed to query the Release, status code: {{.Code}}", map[string]any{"Code": resp.StatusCode}))
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to parse the Release response", nil), err)
	}
	return releases, nil
}

// newReleaseRequest 构造带认证与请求标识的 Release 查询请求
// 按标签查询与列表查询的请求头完全一致，集中在这里可以避免两处认证逻辑各自漂移
func (u *Updater) newReleaseRequest(endpoint string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l10n.T("Failed to build the Release query request", nil), err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", u.cfg.UserAgent)
	if u.cfg.TokenProvider != nil {
		if token := u.cfg.TokenProvider(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return req, nil
}

// pickLatest 在候选列表中挑选版本最高、且为当前平台提供资产的那一个
// 筛选规则依次为：
//  1. 通道过滤：正式版通道忽略开发版标签，开发版通道忽略正式版标签
//  2. 只看更新：本地版本可比时，候选必须严格高于本地版本，相等或更低一律跳过
//  3. 平台可用：缺少当前平台资产的版本视为不提供该平台，继续向前寻找
//  4. 无法解析的标签直接跳过，绝不猜测其含义
//
// comparable 为 false 表示本地版本无法解析，此时缺少比较基准，规则 2 被跳过，
// 结果是返回通道内版本最高的可用版本
//
// 其中"正式版用户不会被带到同号开发版"由 Version.Compare 内建的
// "同号开发版低于正式版"规则自然覆盖，无需在此额外特判
func pickLatest(releases []Release, current Version, channel Channel, assetPrefix string, comparable bool) (Release, bool) {
	var best Release
	var bestVersion Version
	var found bool

	for _, release := range releases {
		candidate, ok := ParseVersion(release.TagName)
		if !ok {
			continue
		}
		if channel == ChannelDev && !candidate.IsDev {
			continue
		}
		if channel == ChannelStable && candidate.IsDev {
			continue
		}
		if comparable && candidate.Compare(current) <= 0 {
			continue
		}
		if found && candidate.Compare(bestVersion) <= 0 {
			continue
		}

		asset, ok := matchAsset(release.Assets, assetPrefix)
		if !ok {
			continue
		}

		// 只保留命中的资产，使调用方无需再关心同一 Release 中的其他平台产物
		best = Release{TagName: release.TagName, Assets: []Asset{asset}}
		bestVersion = candidate
		found = true
	}

	return best, found
}

// matchAsset 按前缀匹配资产名
// 采用前缀而非全等匹配，是为了容忍发布产物在平台标识之后附加的额外后缀，
// 而前缀本身由宿主构造并已包含系统与架构，因此不会误匹配到其他平台
func matchAsset(assets []Asset, prefix string) (Asset, bool) {
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, prefix) {
			return asset, true
		}
	}
	return Asset{}, false
}
