package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeGitHubRepo(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"  ":                    "",
		"kong-hen/more-account": "kong-hen/more-account",
		"https://github.com/kong-hen/more-account":          "kong-hen/more-account",
		"https://github.com/kong-hen/more-account.git":      "kong-hen/more-account",
		"https://github.com/kong-hen/more-account/releases": "kong-hen/more-account",
		"github.com/org-1/app_2":                            "org-1/app_2",
	}
	for input, want := range cases {
		got, err := NormalizeGitHubRepo(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeGitHubRepo(%q) = (%q, %v), 期望 (%q, nil)", input, got, err, want)
		}
	}

	invalid := []string{
		"only-owner",
		"https://gitlab.com/user/repo",
		"../etc/passwd",
		"owner/..",
		"owner/re po",
	}
	for _, bad := range invalid {
		if _, err := NormalizeGitHubRepo(bad); err == nil {
			t.Fatalf("非法仓库输入 %q 应当被拦截", bad)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("1.0.0", "v1.0.0") != 0 {
		t.Fatal("1.0.0 与 v1.0.0 应视为相同版本")
	}
	if CompareVersions("1.0.0", "v1.0.1") <= 0 {
		t.Fatal("v1.0.1 应高于 1.0.0")
	}
	if CompareVersions("1.2.0", "v1.10.0") <= 0 {
		t.Fatal("v1.10.0 数值比较应高于 1.2.0")
	}
	if CompareVersions("1.0.0-beta.1", "v1.0.0") <= 0 {
		t.Fatal("正式版 v1.0.0 应高于预发布版 1.0.0-beta.1")
	}
	if CompareVersions("1.0.0", "v1.0.0-rc.1") >= 0 {
		t.Fatal("预发布版 v1.0.0-rc.1 应低于正式版 1.0.0")
	}
	if CompareVersions("2.0.0", "v1.9.9") >= 0 {
		t.Fatal("v1.9.9 应低于 2.0.0")
	}
}

func TestSelectRecommendedAsset(t *testing.T) {
	assets := []ReleaseAsset{
		{Name: "checksums.sha256", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/checksums.sha256"},
		{Name: "MoreAccount-macOS-universal.zip", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/MoreAccount-macOS-universal.zip"},
		{Name: "MoreAccount-windows-amd64.zip", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/MoreAccount-windows-amd64.zip"},
		{Name: "MoreAccount-setup-amd64.exe", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/MoreAccount-setup-amd64.exe"},
	}

	macPick := SelectRecommendedAsset(assets, "darwin", "arm64")
	if macPick == nil || macPick.Name != "MoreAccount-macOS-universal.zip" {
		t.Fatalf("macOS arm64 应匹配通用 zip 包，实际 %+v", macPick)
	}

	winPick := SelectRecommendedAsset(assets, "windows", "amd64")
	if winPick == nil || winPick.Name != "MoreAccount-setup-amd64.exe" {
		t.Fatalf("Windows amd64 应优先匹配安装器 exe，实际 %+v", winPick)
	}

	// 验证 installer 中的 "all" 子串不会被误识别为通用架构（universal/all）
	if hasStandaloneArchToken("moreaccount-macos-installer.dmg", "all") {
		t.Fatal("installer 不应命中独立架构词元 all")
	}
	if !hasStandaloneArchToken("moreaccount-macos-all.zip", "all") {
		t.Fatal("以分隔符独立出现的 all 应当命中")
	}
	macAssets := []ReleaseAsset{
		{Name: "MoreAccount-macOS-installer.dmg", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/MoreAccount-macOS-installer.dmg"},
		{Name: "MoreAccount-macOS-universal.zip", DownloadURL: "https://github.com/demo/r/releases/download/v1.1.0/MoreAccount-macOS-universal.zip"},
	}
	armPick := SelectRecommendedAsset(macAssets, "darwin", "arm64")
	if armPick == nil || armPick.Name != "MoreAccount-macOS-universal.zip" {
		t.Fatalf("macOS arm64 应优先匹配 universal.zip，而非误把 installer.dmg 当作 all，实际 %+v", armPick)
	}
}

func TestCheckLatestReleaseFullFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/demo/has-update/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v1.2.0",
				"name":         "v1.2.0 正式版",
				"body":         "- 新增 GitHub 自动检查更新\n- 优化跨平台体验",
				"html_url":     "https://github.com/demo/has-update/releases/tag/v1.2.0",
				"published_at": "2026-11-22T00:00:00Z",
				"assets": []map[string]any{
					{
						"name":                 "MoreAccount-macOS-universal.zip",
						"browser_download_url": "https://github.com/demo/has-update/releases/download/v1.2.0/MoreAccount-macOS-universal.zip",
						"size":                 13600000,
					},
					{
						"name":                 "MoreAccount-windows-amd64.zip",
						"browser_download_url": "https://github.com/demo/has-update/releases/download/v1.2.0/MoreAccount-windows-amd64.zip",
						"size":                 7200000,
					},
				},
			})

		case "/repos/demo/empty-repo/releases/latest":
			w.WriteHeader(http.StatusNotFound)
		case "/repos/demo/empty-repo/releases":
			_ = json.NewEncoder(w).Encode([]any{})

		case "/repos/demo/ratelimited/releases/latest":
			w.WriteHeader(http.StatusForbidden)
		case "/demo/ratelimited/releases/latest":
			w.Header().Set("Location", "https://github.com/demo/ratelimited/releases/tag/v1.3.5")
			w.WriteHeader(http.StatusFound)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origAPI, origWeb := GitHubAPIBaseURL, GitHubWebBaseURL
	GitHubAPIBaseURL = srv.URL
	GitHubWebBaseURL = srv.URL
	defer func() {
		GitHubAPIBaseURL = origAPI
		GitHubWebBaseURL = origWeb
	}()

	// 1. 正常检测到新版本
	res, err := CheckLatestRelease(context.Background(), srv.Client(), "https://github.com/demo/has-update", "1.0.0")
	if err != nil {
		t.Fatalf("检查有更新仓库失败: %v", err)
	}
	if !res.HasUpdate || res.LatestVersion != "1.2.0" || len(res.Assets) != 2 {
		t.Fatalf("期望检测到 1.2.0 新版本及 2 个资产，实际 %+v", res)
	}

	// 2. 仓库已创建但尚未发布任何 Release
	emptyRes, err := CheckLatestRelease(context.Background(), srv.Client(), "demo/empty-repo", "1.0.0")
	if err != nil {
		t.Fatalf("未发版公开仓库不应报错，实际: %v", err)
	}
	if !emptyRes.NoReleaseYet || emptyRes.HasUpdate {
		t.Fatalf("期望标记 NoReleaseYet=true 且 HasUpdate=false，实际 %+v", emptyRes)
	}

	// 3. GitHub REST API 403 限流时自动回退 Web 重定向探测成功
	fbRes, err := CheckLatestRelease(context.Background(), srv.Client(), "demo/ratelimited", "1.0.0")
	if err != nil {
		t.Fatalf("API 限流时 Web 重定向兜底应成功，实际: %v", err)
	}
	if !fbRes.HasUpdate || fbRes.LatestVersion != "1.3.5" {
		t.Fatalf("期望通过 Web 重定向识别到 1.3.5，实际 %+v", fbRes)
	}
}

func TestCheckLatestReleaseSkipsLeadingDraftRelease(t *testing.T) {
	var seenPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/demo/with-draft/releases/latest":
			w.WriteHeader(http.StatusNotFound)
		case "/repos/demo/with-draft/releases":
			seenPerPage = r.URL.Query().Get("per_page")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"tag_name": "v1.5.0-draft",
					"draft":    true,
				},
				{
					"tag_name": "v1.4.0-rc.1",
					"name":     "v1.4.0-rc.1",
					"html_url": "https://github.com/demo/with-draft/releases/tag/v1.4.0-rc.1",
					"draft":    false,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origAPI, origWeb := GitHubAPIBaseURL, GitHubWebBaseURL
	GitHubAPIBaseURL = srv.URL
	GitHubWebBaseURL = srv.URL
	defer func() {
		GitHubAPIBaseURL = origAPI
		GitHubWebBaseURL = origWeb
	}()

	res, err := CheckLatestRelease(context.Background(), srv.Client(), "demo/with-draft", "1.0.0")
	if err != nil {
		t.Fatalf("跳过首条 Draft 检测更新失败: %v", err)
	}
	if seenPerPage != "5" {
		t.Fatalf("期望列表回退查询 per_page=5，实际 %q", seenPerPage)
	}
	if res.NoReleaseYet || !res.HasUpdate || res.LatestVersion != "1.4.0-rc.1" {
		t.Fatalf("期望跳过 Draft 后识别出 v1.4.0-rc.1，实际 %+v", res)
	}
}
