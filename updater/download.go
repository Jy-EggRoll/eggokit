package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jy-eggroll/eggokit/l10n"
)

// downloadChunkSize 是单次读取的字节数，32KB 兼顾吞吐与进度刷新频率
const downloadChunkSize = 32 * 1024

// 直连与代理使用两套超时策略
// 直连要求快速失败，以便尽早把"是否切换代理"的决定权交还给用户；
// 代理链路本身更慢，若沿用直连的短超时会导致正常但缓慢的下载被反复判定为失败
var (
	directDownloadClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
		},
	}

	proxyDownloadClient = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 15 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		},
	}
)

// transferResult 描述一次成功传输的规模与耗时
// 汇总信息不在这里输出：它必须等到进度展示结束之后才能打印，
// 否则文字会紧接在进度行末尾，与进度条挤在同一行
type transferResult struct {
	path    string
	bytes   int64
	elapsed time.Duration
}

// transferOutcome 在直连 goroutine 与等待结果的主流程之间传递一次尝试的成败
type transferOutcome struct {
	result transferResult
	err    error
}

// fetch 把目标资产下载到 dir 下的暂存文件并返回其路径
// 返回暂存文件而非最终路径，是为了让调用方在确认完整接收之后再执行替换，
// 使下载过程中的任何失败都不会触碰现有的可执行文件
func (u *Updater) fetch(info *UpdateInfo, dir string) (string, error) {
	result, err := u.transfer(info, dir)
	if err != nil {
		return "", err
	}

	// 速率按整体平均计算而不是瞬时值：慢速网络下瞬时速率抖动剧烈，
	// 换算出的数字会让用户误以为下载速度在反复变化
	speed := float64(0)
	if result.elapsed > 0 {
		speed = float64(result.bytes) / result.elapsed.Seconds()
	}
	u.cfg.Reporter.Info("%s", l10n.T("Download complete: {{.Size}}, took {{.Time}}, average speed {{.Speed}}",
		map[string]any{"Size": FormatSize(result.bytes), "Time": FormatDuration(result.elapsed), "Speed": FormatSpeed(speed)}))

	return result.path, nil
}

// transfer 按配置选择下载路径
// 未配置代理时不存在换源选项，直连就是唯一路径，失败即失败
func (u *Updater) transfer(info *UpdateInfo, dir string) (transferResult, error) {
	if u.cfg.ProxyPrefix == "" {
		return u.download(context.Background(), info.DownloadURL, info.Digest, dir, directDownloadClient)
	}
	return u.downloadWithProxyFallback(info, dir)
}

// downloadWithProxyFallback 先直连下载，慢于阈值或直接失败时询问用户是否改用代理
// 无论超时还是报错都交由用户裁决，绝不静默换源：
// 自动回退会让用户失去对"二进制究竟来自哪个镜像"的判断，而下载结果随后会被直接执行
func (u *Updater) downloadWithProxyFallback(info *UpdateInfo, dir string) (transferResult, error) {
	u.cfg.Reporter.Info("%s", l10n.T("Download mode: direct (will ask whether to switch to the proxy if not finished within {{.Threshold}})", map[string]any{"Threshold": u.cfg.SlowThreshold}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 缓冲为 1：被取消的直连 goroutine 也必须能把结果写出去，否则它会永久阻塞在发送上
	results := make(chan transferOutcome, 1)

	go func() {
		result, err := u.download(ctx, info.DownloadURL, info.Digest, dir, directDownloadClient)
		results <- transferOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-results:
		if outcome.err == nil {
			return outcome.result, nil
		}
		return u.switchToProxy(info, dir, outcome.err)

	case <-time.After(u.cfg.SlowThreshold):
		confirmed, confirmErr := u.cfg.Reporter.Confirm(l10n.T("Direct download is slow; switch to the proxy?", nil))
		if confirmErr != nil {
			// 询问失败时保持直连，等价于用户选择继续等待；
			// 但必须说明询问本身失败的原因，否则用户无从理解为什么没有走代理方案
			u.cfg.Reporter.Info("%s", l10n.T("Could not ask about switching to the proxy ({{.Err}}); continuing with direct download", map[string]any{"Err": confirmErr.Error()}))
			confirmed = false
		}
		if !confirmed {
			// 直连仍是首选路径，不能因为一次询问未获同意就丢弃正在进行的下载
			u.cfg.Reporter.Info("%s", l10n.T("Continuing to wait for the direct download to finish...", nil))
			outcome := <-results
			if outcome.err == nil {
				return outcome.result, nil
			}
			return transferResult{}, outcome.err
		}

		// 切换前必须等直连彻底退出：否则两路下载会同时刷新同一个进度，暂存文件也会互相干扰
		u.cfg.Reporter.Info("%s", l10n.T("Switched to proxy download", nil))
		cancel()
		<-results
		return u.download(context.Background(), u.cfg.ProxyPrefix+info.DownloadURL, info.Digest, dir, proxyDownloadClient)
	}
}

// switchToProxy 在直连失败后询问用户是否改用代理
// 用户拒绝或询问失败时返回直连的原始错误而非询问错误：
// 用户真正需要知道的是下载为什么失败，而不是弹窗本身出了什么问题
func (u *Updater) switchToProxy(info *UpdateInfo, dir string, directErr error) (transferResult, error) {
	confirmed, confirmErr := u.cfg.Reporter.Confirm(l10n.T("Direct download failed; switch to the proxy?", nil))
	if confirmErr != nil {
		// 必须说明询问失败的原因：否则用户只会看到一条网络错误，
		// 完全不知道程序本来准备了代理方案，也就失去了自行重试的线索
		u.cfg.Reporter.Info("%s", l10n.T("Could not ask about switching to the proxy ({{.Err}})", map[string]any{"Err": confirmErr.Error()}))
		return transferResult{}, directErr
	}
	if !confirmed {
		return transferResult{}, directErr
	}

	u.cfg.Reporter.Info("%s", l10n.T("Switched to proxy download", nil))
	return u.download(context.Background(), u.cfg.ProxyPrefix+info.DownloadURL, info.Digest, dir, proxyDownloadClient)
}

// download 执行一次完整下载，成功时返回暂存文件路径与本次传输的规模耗时
// 汇总信息刻意不在这里输出：它必须等进度展示结束之后才能打印，详见 fetch
// 任何失败路径都会删除暂存文件，保证不会在安装目录里留下半截内容；
// 下载地址来自 Release 接口响应或宿主配置的代理前缀，属于既定信任边界，不在此做额外目标校验
//
// expectedDigest 是 Release 接口给出的内容摘要（形如 "sha256:<hex>"），边收边算 sha256 并核对，
// 它比 Content-Length 可靠：长度相同但内容被替换的资产只能靠它拦下；为空则退化为仅长度校验并向用户告警
func (u *Updater) download(ctx context.Context, url, expectedDigest, dir string, client *http.Client) (transferResult, error) {
	// 进度展示的生命周期与单次传输严格绑定：从直连切换到代理属于两次独立传输，
	// 各自重新计量才能算出正确的速率与剩余时间，
	// 否则会把两段传输的字节数与跨越两段的总耗时混在一起，得出毫无意义的数字
	progress := u.cfg.Reporter.Progress(l10n.T("Download progress", nil))
	defer progress.Done()

	u.cfg.Reporter.Info("%s", l10n.T("Starting download: {{.URL}}", map[string]any{"URL": url}))
	started := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to build the download request", nil), err)
	}
	req.Header.Set("User-Agent", u.cfg.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to request the download URL", nil), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return transferResult{}, fmt.Errorf("%s", l10n.T("Download failed, status code: {{.Code}}", map[string]any{"Code": resp.StatusCode}))
	}

	// 暂存文件名由 CreateTemp 随机生成而不采用上游返回的资产名：
	// 资产名来自外部响应，直接当作文件名会把路径穿越风险引入安装目录
	// 前缀取自 cleanup.go 的 stagingPrefix：残留清理按同一个前缀识别中断下载留下的文件
	staged, err := os.CreateTemp(dir, stagingPrefix+"*")
	if err != nil {
		return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to create a staging file in the install directory (write permission on {{.Dir}} may be required)", map[string]any{"Dir": dir}), err)
	}

	// 失败清理集中在此处，避免每个返回点都要手写一遍关闭与删除；
	// 成功时保留暂存文件交给替换步骤，重复的 Close 只会返回 ErrClosed，可以安全忽略
	succeeded := false
	defer func() {
		_ = staged.Close()
		if !succeeded {
			_ = os.Remove(staged.Name())
		}
	}()

	total := resp.ContentLength
	written := int64(0)
	buffer := make([]byte, downloadChunkSize)

	// 边写盘边算摘要，避免下载完成后再把整份文件重读一遍
	hasher := sha256.New()
	sink := io.MultiWriter(staged, hasher)

	for {
		select {
		case <-ctx.Done():
			return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Download cancelled", nil), ctx.Err())
		default:
		}

		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := sink.Write(buffer[:n]); writeErr != nil {
				return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to write the staging file", nil), writeErr)
			}
			written += int64(n)
			progress.Update(written, total)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to read the download data", nil), readErr)
		}
	}

	if err := staged.Close(); err != nil {
		return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to close the staging file", nil), err)
	}

	// Content-Length 是弱校验：分块传输时它缺失，只能接受已接收的字节；
	// 一旦存在长度信息而实际字节数不符，必然意味着连接中途断开，此时绝不能把不完整内容当作新版本安装
	if total > 0 && written != total {
		return transferResult{}, fmt.Errorf("%s", l10n.T("Download incomplete: received {{.Got}} bytes, expected {{.Want}} bytes", map[string]any{"Got": written, "Want": total}))
	}

	// 强校验：核对 Release 接口给出的 SHA-256。三种情况都不能混为一谈——
	// 摘要可用且不符 → 硬失败；摘要缺失或非 sha256 形式 → 放行但必须告警，
	// 绝不能让"没能校验"静默等同于"校验通过"，否则用户会误以为这次替换有信任锚
	switch want, ok := parseSHA256(expectedDigest); {
	case ok:
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, want) {
			return transferResult{}, fmt.Errorf("%s", l10n.T("Downloaded file failed the integrity check: expected SHA-256 {{.Want}}, got {{.Got}}", map[string]any{"Want": want, "Got": got}))
		}
	case expectedDigest == "":
		u.cfg.Reporter.Warn("%s", l10n.T("The release provides no SHA-256 digest; integrity could not be verified", nil))
	default:
		u.cfg.Reporter.Warn("%s", l10n.T("The asset digest {{.Digest}} uses an unsupported format; integrity could not be verified", map[string]any{"Digest": expectedDigest}))
	}

	if err := os.Chmod(staged.Name(), 0o755); err != nil {
		return transferResult{}, fmt.Errorf("%s: %w", l10n.T("Failed to set executable permissions", nil), err)
	}

	succeeded = true
	return transferResult{path: staged.Name(), bytes: written, elapsed: time.Since(started)}, nil
}

// FormatSize 以人类可读形式表示字节数，进位使用 1024 而非 1000 以贴近文件管理器显示
// 导出是因为展示层的进度条需要同一套刻度，重复实现会让两处显示出现不一致的进位口径
func FormatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// FormatDuration 输出秒级或分秒级时长，分钟以上才拆出分钟段，避免出现无意义的 0m 前缀
// 该函数同时用于传输耗时与界面上的剩余时间展示
func FormatDuration(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// FormatSpeed 输出平均下载速率，保留一位小数以便观察带宽量级
func FormatSpeed(bytesPerSecond float64) string {
	switch {
	case bytesPerSecond >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", bytesPerSecond/(1024*1024))
	case bytesPerSecond >= 1024:
		return fmt.Sprintf("%.1f KB/s", bytesPerSecond/1024)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSecond)
	}
}

// sha256DigestPrefix 是 GitHub Release 接口给出的摘要串前缀
const sha256DigestPrefix = "sha256:"

// parseSHA256 从 GitHub 的摘要串（形如 "sha256:<64 位十六进制>"）中取出小写的十六进制部分。
//
// 只认 sha256：其它算法（如未来可能出现的 sha512）本包不做校验，返回 false 交由调用方
// 放行并告警，而不是假装校验过。长度与字符集一并校验，避免把残缺的摘要当成有效值比对
func parseSHA256(digest string) (string, bool) {
	if !strings.HasPrefix(digest, sha256DigestPrefix) {
		return "", false
	}
	hexPart := strings.TrimPrefix(digest, sha256DigestPrefix)
	if len(hexPart) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", false
	}
	return strings.ToLower(hexPart), true
}
