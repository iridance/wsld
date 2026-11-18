package wsl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// DistributionInfoURL 是 wsl.exe 用來列出／下載線上發行版的官方清單。
const DistributionInfoURL = "https://raw.githubusercontent.com/microsoft/WSL/master/distributions/DistributionInfo.json"

type distroURL struct {
	URL string `json:"Url"`
}

type modernDistro struct {
	Name     string     `json:"Name"`
	Amd64URL *distroURL `json:"Amd64Url"`
	Arm64URL *distroURL `json:"Arm64Url"`
}

// DownloadSize 從清單找出 distribution（modern／.wsl 格式）在 arch（amd64／arm64）下的檔案，
// 以 HEAD 取得 Content-Length。不在 modern 清單、或伺服器未回大小時回錯誤。
func DownloadSize(ctx context.Context, client *http.Client, manifestURL, distribution, arch string) (int64, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("distribution manifest: %s", resp.Status)
	}
	var m struct {
		ModernDistributions map[string][]modernDistro `json:"ModernDistributions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return 0, fmt.Errorf("distribution manifest: %w", err)
	}
	var u *distroURL
	for _, list := range m.ModernDistributions {
		for _, d := range list {
			if !strings.EqualFold(d.Name, distribution) {
				continue
			}
			if arch == "arm64" {
				u = d.Arm64URL
			} else {
				u = d.Amd64URL
			}
		}
	}
	if u == nil || u.URL == "" {
		return 0, fmt.Errorf("%s: no %s download in manifest", distribution, arch)
	}
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, u.URL, nil)
	if err != nil {
		return 0, err
	}
	hr, err := client.Do(head)
	if err != nil {
		return 0, err
	}
	hr.Body.Close()
	if hr.StatusCode != http.StatusOK || hr.ContentLength <= 0 {
		return 0, fmt.Errorf("%s: size unavailable (%s)", distribution, hr.Status)
	}
	return hr.ContentLength, nil
}

// DownloadPath 是 wsl.exe 下載 modern 發行版時的暫存檔：<tempDir>\<distribution>.wsl。
func DownloadPath(tempDir, distribution string) string {
	return filepath.Join(tempDir, distribution+".wsl")
}
