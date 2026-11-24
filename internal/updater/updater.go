package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	// AppVersion 当前客户端语义化版本号（可通过 -ldflags "-X more-account/internal/updater.AppVersion=x.y.z" 覆盖）。
	AppVersion = "1.0.1"

	// DefaultGitHubRepo 默认检查更新的 GitHub 仓库（格式 "owner/repo"）。
	// 支持在客户端「设置」中覆盖，或在构建时通过 -ldflags "-X more-account/internal/updater.DefaultGitHubRepo=owner/repo" 注入。
	DefaultGitHubRepo = "kong-hen/more-account"

	// GitHubAPIBaseURL 与 GitHubWebBaseURL 支持在单元测试中替换为本地 httptest 服务。
	GitHubAPIBaseURL = "https://api.github.com"
	GitHubWebBaseURL = "https://github.com"
)

// ReleaseAsset 描述 GitHub Release 下的单个可下载安装包资产。
type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"downloadUrl"`
	Size        int64  `json:"size"`
}

// UpdateCheckResult 表示一次版本更新检查的完整结果。
type UpdateCheckResult struct {
	CurrentVersion   string         `json:"currentVersion"`
	LatestVersion    string         `json:"latestVersion"`
	HasUpdate        bool           `json:"hasUpdate"`
	NoReleaseYet     bool           `json:"noReleaseYet,omitempty"`
	Repo             string         `json:"repo"`
	ReleaseName      string         `json:"releaseName,omitempty"`
	ReleaseNotes     string         `json:"releaseNotes,omitempty"`
	ReleaseURL       string         `json:"releaseUrl"`
	PublishedAt      string         `json:"publishedAt,omitempty"`
	RecommendedAsset *ReleaseAsset  `json:"recommendedAsset,omitempty"`
	Assets           []ReleaseAsset `json:"assets,omitempty"`
	CheckedAt        int64          `json:"checkedAt"`
}

type githubReleaseResp struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// NormalizeGitHubRepo 将用户输入的 "owner/repo" 或完整 GitHub 仓库链接规范化为安全的 "owner/repo" 格式。
func NormalizeGitHubRepo(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}

	// 支持完整 URL 或带域名写法：https://github.com/owner/repo(.git)/releases...
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		parsed, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("无效的 GitHub 仓库链接: %w", err)
		}
		host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		if host != "github.com" && host != "www.github.com" {
			return "", fmt.Errorf("仅支持 github.com 仓库地址，当前域名为 %q", parsed.Hostname())
		}
		s = parsed.Path
	} else if strings.HasPrefix(lower, "github.com/") {
		s = s[len("github.com/"):]
	} else if strings.HasPrefix(lower, "www.github.com/") {
		s = s[len("www.github.com/"):]
	}

	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("GitHub 仓库格式应为 owner/repo（例如 yourname/more-account）")
	}

	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSuffix(strings.TrimSpace(parts[1]), ".git")
	if !isValidGitHubSegment(owner) || !isValidGitHubSegment(repo) {
		return "", fmt.Errorf("GitHub 仓库名称包含非法字符: %s/%s", owner, repo)
	}
	return owner + "/" + repo, nil
}

func isValidGitHubSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." || len(seg) > 100 {
		return false
	}
	for _, r := range seg {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlphaNum && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// ResolveEffectiveRepo 解析优先使用的 GitHub 仓库：临时参数 > 用户设置 > 环境变量 > 编译期默认仓库。
func ResolveEffectiveRepo(overrideRepo, savedRepo string) (string, error) {
	for _, candidate := range []string{
		overrideRepo,
		savedRepo,
		os.Getenv("MORE_ACCOUNT_GITHUB_REPO"),
		DefaultGitHubRepo,
	} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		return NormalizeGitHubRepo(candidate)
	}
	return "", nil
}

type parsedSemver struct {
	nums [3]int
	pre  string
}

func parseSemver(v string) parsedSemver {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	// 剥离 +build 元数据
	if plusIdx := strings.IndexByte(s, '+'); plusIdx >= 0 {
		s = s[:plusIdx]
	}
	var pre string
	if dashIdx := strings.IndexByte(s, '-'); dashIdx >= 0 {
		pre = strings.TrimSpace(s[dashIdx+1:])
		s = s[:dashIdx]
	}
	segs := strings.Split(s, ".")
	var out parsedSemver
	out.pre = pre
	for i := 0; i < 3 && i < len(segs); i++ {
		digits := segs[i]
		for j, ch := range digits {
			if ch < '0' || ch > '9' {
				digits = digits[:j]
				break
			}
		}
		if n, err := strconv.Atoi(digits); err == nil {
			out.nums[i] = n
		}
	}
	return out
}

// CompareVersions 比较两个语义化版本号：
// 返回 1 表示 latest > current（有新版本），0 表示相等，-1 表示 latest < current。
func CompareVersions(current, latest string) int {
	if strings.TrimSpace(latest) == "" {
		return -1
	}
	c := parseSemver(current)
	l := parseSemver(latest)
	for i := 0; i < 3; i++ {
		if l.nums[i] > c.nums[i] {
			return 1
		}
		if l.nums[i] < c.nums[i] {
			return -1
		}
	}
	// 正式版高于同版本号的预发布版（例如 1.0.0 > 1.0.0-beta.1）
	if c.pre != "" && l.pre == "" {
		return 1
	}
	if c.pre == "" && l.pre != "" {
		return -1
	}
	if c.pre == l.pre {
		return 0
	}
	return comparePrerelease(c.pre, l.pre)
}

func comparePrerelease(curPre, latPre string) int {
	cParts := strings.Split(curPre, ".")
	lParts := strings.Split(latPre, ".")
	limit := len(cParts)
	if len(lParts) > limit {
		limit = len(lParts)
	}
	for i := 0; i < limit; i++ {
		if i >= len(cParts) {
			return 1
		}
		if i >= len(lParts) {
			return -1
		}
		cn, cErr := strconv.Atoi(cParts[i])
		ln, lErr := strconv.Atoi(lParts[i])
		if cErr == nil && lErr == nil {
			if ln > cn {
				return 1
			}
			if ln < cn {
				return -1
			}
			continue
		}
		if lParts[i] > cParts[i] {
			return 1
		}
		if lParts[i] < cParts[i] {
			return -1
		}
	}
	return 0
}

func isSafeHTTPSURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" {
		return true
	}
	// 仅允许本机测试服务使用 http
	host := strings.ToLower(u.Hostname())
	return scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")
}

// SelectRecommendedAsset 根据给定操作系统（darwin / windows / linux）与芯片架构（arm64 / amd64）挑选最匹配的安装包资产。
func SelectRecommendedAsset(assets []ReleaseAsset, goos, goarch string) *ReleaseAsset {
	if len(assets) == 0 {
		return nil
	}
	var best *ReleaseAsset
	bestScore := -1

	for i := range assets {
		a := &assets[i]
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if name == "" || !isSafeHTTPSURL(a.DownloadURL) {
			continue
		}
		// 跳过校验和、签名、源码块映射等非安装包附属文件
		if strings.HasSuffix(name, ".sha256") ||
			strings.HasSuffix(name, ".sha512") ||
			strings.HasSuffix(name, ".sig") ||
			strings.HasSuffix(name, ".asc") ||
			strings.HasSuffix(name, ".txt") ||
			strings.HasSuffix(name, ".yml") ||
			strings.HasSuffix(name, ".yaml") ||
			strings.HasSuffix(name, ".blockmap") {
			continue
		}

		score := scoreAssetForPlatform(name, goos, goarch)
		if score > bestScore {
			bestScore = score
			best = a
		}
	}

	if bestScore <= 0 {
		return nil
	}
	copied := *best
	return &copied
}

func scoreAssetForPlatform(lowerName, goos, goarch string) int {
	isMacAsset := strings.Contains(lowerName, "macos") ||
		strings.Contains(lowerName, "darwin") ||
		strings.Contains(lowerName, "osx") ||
		strings.HasSuffix(lowerName, ".dmg") ||
		strings.HasSuffix(lowerName, ".pkg") ||
		strings.HasSuffix(lowerName, ".app.zip")
	isWinAsset := strings.Contains(lowerName, "windows") ||
		strings.Contains(lowerName, "win64") ||
		strings.Contains(lowerName, "win32") ||
		strings.Contains(lowerName, "-win-") ||
		strings.HasSuffix(lowerName, ".exe") ||
		strings.HasSuffix(lowerName, ".msi")
	isLinuxAsset := strings.Contains(lowerName, "linux") ||
		strings.HasSuffix(lowerName, ".appimage") ||
		strings.HasSuffix(lowerName, ".deb") ||
		strings.HasSuffix(lowerName, ".rpm")

	score := 0
	switch goos {
	case "darwin":
		if isWinAsset || isLinuxAsset {
			return -1
		}
		if !isMacAsset {
			return 0
		}
		score += 50
		if strings.HasSuffix(lowerName, ".dmg") {
			score += 15
		} else if strings.HasSuffix(lowerName, ".zip") {
			score += 12
		} else if strings.HasSuffix(lowerName, ".pkg") {
			score += 10
		}
	case "windows":
		if isMacAsset || isLinuxAsset {
			return -1
		}
		if !isWinAsset {
			return 0
		}
		score += 50
		if strings.Contains(lowerName, "installer") || strings.Contains(lowerName, "setup") {
			score += 14
		} else if strings.HasSuffix(lowerName, ".exe") {
			score += 13
		} else if strings.HasSuffix(lowerName, ".zip") {
			score += 11
		}
	default:
		if isMacAsset || isWinAsset {
			return -1
		}
		if !isLinuxAsset {
			return 0
		}
		score += 50
	}

	hasArm := strings.Contains(lowerName, "arm64") || strings.Contains(lowerName, "aarch64")
	hasAmd := strings.Contains(lowerName, "amd64") || strings.Contains(lowerName, "x86_64") || strings.Contains(lowerName, "x64")
	// 仅匹配 universal 或按分隔符独立存在的 "all" 词元，避免误命中 "installer" 中的 "all" 子串。
	hasUniversal := strings.Contains(lowerName, "universal") || hasStandaloneArchToken(lowerName, "all")

	switch goarch {
	case "arm64":
		if hasArm {
			score += 30
		} else if hasUniversal {
			score += 25
		} else if hasAmd {
			score -= 20
		} else {
			score += 10
		}
	default: // amd64
		if hasAmd {
			score += 30
		} else if hasUniversal {
			score += 25
		} else if hasArm {
			score -= 30
		} else {
			score += 15
		}
	}
	return score
}

// hasStandaloneArchToken 按常见文件名分隔符切分词元，精确判断是否存在独立架构标识（如 -all.zip / _all.dmg）。
func hasStandaloneArchToken(lowerName, token string) bool {
	tokens := strings.FieldsFunc(lowerName, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, t := range tokens {
		if t == token {
			return true
		}
	}
	return false
}

// CheckLatestRelease 检查指定 GitHub 仓库的最新版本。
// 兼容：1) 正常正式版 Release 2) 仅存在预发布版或尚未发版的仓库 3) GitHub REST API 匿名限流时自动回退 Web 重定向探测。
func CheckLatestRelease(ctx context.Context, httpClient *http.Client, rawRepo string, currentVersion string) (*UpdateCheckResult, error) {
	repo, err := NormalizeGitHubRepo(rawRepo)
	if err != nil {
		return nil, err
	}
	if repo == "" {
		return nil, fmt.Errorf("尚未配置 GitHub 仓库地址，请先在「设置 → 版本与更新」中填写 owner/repo")
	}
	if strings.TrimSpace(currentVersion) == "" {
		currentVersion = AppVersion
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	apiBase := strings.TrimRight(GitHubAPIBaseURL, "/")
	webBase := strings.TrimRight(GitHubWebBaseURL, "/")
	latestURL := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo)

	rel, status, apiErr := fetchSingleRelease(ctx, httpClient, latestURL, currentVersion)
	if apiErr == nil && rel != nil {
		return buildCheckResult(repo, currentVersion, *rel, webBase), nil
	}

	// 若 /releases/latest 返回 404，可能是仓库尚未发布正式 Release（或仅有预发布版本），进一步查询最近 5 条 releases 跳过草稿（Draft）进行区分
	if status == http.StatusNotFound {
		listURL := fmt.Sprintf("%s/repos/%s/releases?per_page=5", apiBase, repo)
		firstRel, listStatus, listErr := fetchFirstReleaseFromList(ctx, httpClient, listURL, currentVersion)
		if listErr == nil {
			if firstRel != nil {
				return buildCheckResult(repo, currentVersion, *firstRel, webBase), nil
			}
			return &UpdateCheckResult{
				CurrentVersion: currentVersion,
				LatestVersion:  currentVersion,
				HasUpdate:      false,
				NoReleaseYet:   true,
				Repo:           repo,
				ReleaseURL:     fmt.Sprintf("%s/%s/releases", webBase, repo),
				CheckedAt:      time.Now().Unix(),
			}, nil
		}
		if listStatus == http.StatusNotFound {
			return nil, fmt.Errorf("暂未找到公开的 GitHub 仓库 %q（若仓库当前为私有 Private，待后续公开并发布 Release 后即可在线检测更新）", repo)
		}
	}

	// 若触发 GitHub API 匿名速率限制 (403 / 429)，回退通过 Web /releases/latest 302 Location 解析最新 Tag
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		if fallbackRes, fbErr := fallbackCheckViaWebRedirect(ctx, httpClient, webBase, repo, currentVersion); fbErr == nil {
			return fallbackRes, nil
		}
	}

	if apiErr != nil {
		return nil, apiErr
	}
	return nil, fmt.Errorf("检查 GitHub 更新失败 (HTTP %d)", status)
}

func buildCheckResult(repo, currentVersion string, rel githubReleaseResp, webBase string) *UpdateCheckResult {
	tag := strings.TrimSpace(rel.TagName)
	cleanLatest := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	if cleanLatest == "" {
		cleanLatest = currentVersion
	}

	assets := make([]ReleaseAsset, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		if strings.TrimSpace(a.Name) == "" || !isSafeHTTPSURL(a.BrowserDownloadURL) {
			continue
		}
		assets = append(assets, ReleaseAsset{
			Name:        strings.TrimSpace(a.Name),
			DownloadURL: strings.TrimSpace(a.BrowserDownloadURL),
			Size:        a.Size,
		})
	}

	releaseURL := strings.TrimSpace(rel.HTMLURL)
	if !isSafeHTTPSURL(releaseURL) {
		releaseURL = fmt.Sprintf("%s/%s/releases/latest", webBase, repo)
	}

	hasUpdate := CompareVersions(currentVersion, tag) > 0
	rec := SelectRecommendedAsset(assets, runtime.GOOS, runtime.GOARCH)

	return &UpdateCheckResult{
		CurrentVersion:   currentVersion,
		LatestVersion:    cleanLatest,
		HasUpdate:        hasUpdate,
		Repo:             repo,
		ReleaseName:      strings.TrimSpace(rel.Name),
		ReleaseNotes:     strings.TrimSpace(rel.Body),
		ReleaseURL:       releaseURL,
		PublishedAt:      strings.TrimSpace(rel.PublishedAt),
		RecommendedAsset: rec,
		Assets:           assets,
		CheckedAt:        time.Now().Unix(),
	}
}

func fetchSingleRelease(ctx context.Context, client *http.Client, reqURL, currentVer string) (*githubReleaseResp, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("构建更新检查请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "MoreAccount/"+currentVer)
	if ghToken := strings.TrimSpace(os.Getenv("MORE_ACCOUNT_GITHUB_TOKEN")); ghToken != "" {
		req.Header.Set("Authorization", "Bearer "+ghToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 GitHub 更新接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("GitHub API 返回状态码 %d", resp.StatusCode)
	}

	var rel githubReleaseResp
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("解析 GitHub Release 响应失败: %w", err)
	}
	return &rel, resp.StatusCode, nil
}

func fetchFirstReleaseFromList(ctx context.Context, client *http.Client, reqURL, currentVer string) (*githubReleaseResp, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "MoreAccount/"+currentVer)
	if ghToken := strings.TrimSpace(os.Getenv("MORE_ACCOUNT_GITHUB_TOKEN")); ghToken != "" {
		req.Header.Set("Authorization", "Bearer "+ghToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var list []githubReleaseResp
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, resp.StatusCode, err
	}
	for i := range list {
		if !list[i].Draft {
			return &list[i], resp.StatusCode, nil
		}
	}
	return nil, resp.StatusCode, nil
}

func fallbackCheckViaWebRedirect(ctx context.Context, baseClient *http.Client, webBase, repo, currentVersion string) (*UpdateCheckResult, error) {
	noRedirectClient := &http.Client{
		Transport: baseClient.Transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	target := fmt.Sprintf("%s/%s/releases/latest", webBase, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MoreAccount/"+currentVersion)

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()

	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if (resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently) && strings.Contains(loc, "/releases/tag/") {
		idx := strings.Index(loc, "/releases/tag/")
		rawTag := loc[idx+len("/releases/tag/"):]
		if qIdx := strings.IndexAny(rawTag, "?#"); qIdx >= 0 {
			rawTag = rawTag[:qIdx]
		}
		tag, _ := url.PathUnescape(strings.TrimSpace(rawTag))
		if tag != "" {
			cleanLatest := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
			releaseURL := loc
			if strings.HasPrefix(releaseURL, "/") {
				releaseURL = webBase + releaseURL
			}
			if !isSafeHTTPSURL(releaseURL) {
				releaseURL = fmt.Sprintf("%s/%s/releases/latest", webBase, repo)
			}
			return &UpdateCheckResult{
				CurrentVersion: currentVersion,
				LatestVersion:  cleanLatest,
				HasUpdate:      CompareVersions(currentVersion, tag) > 0,
				Repo:           repo,
				ReleaseName:    tag,
				ReleaseURL:     releaseURL,
				CheckedAt:      time.Now().Unix(),
			}, nil
		}
	}
	return nil, fmt.Errorf("Web 重定向未返回有效 Release Tag")
}
