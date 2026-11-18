package wsl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// manifestServer 模擬 DistributionInfo.json 與發行版檔案（HEAD 回 Content-Length）。
func manifestServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/DistributionInfo.json":
			_, _ = w.Write([]byte(strings.ReplaceAll(`{
  "ModernDistributions": {
    "AzureLinux": [
      {"Name": "AzureLinux-4", "FriendlyName": "Azure Linux 4",
       "Amd64Url": {"Url": "BASE/az-x86_64.wsl", "Sha256": "x"},
       "Arm64Url": {"Url": "BASE/az-aarch64.wsl", "Sha256": "y"}}
    ]
  },
  "Distributions": [{"Name": "LegacyOnly", "FriendlyName": "Legacy"}]
}`, "BASE", srv.URL)))
		case "/az-x86_64.wsl":
			if r.Method != http.MethodHead {
				t.Errorf("file should be probed with HEAD, got %s", r.Method)
			}
			w.Header().Set("Content-Length", "176682312")
		case "/az-aarch64.wsl":
			w.Header().Set("Content-Length", "170000000")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadSize(t *testing.T) {
	srv := manifestServer(t)
	ctx := context.Background()
	manifest := srv.URL + "/DistributionInfo.json"

	n, err := DownloadSize(ctx, srv.Client(), manifest, "AzureLinux-4", "amd64")
	if err != nil || n != 176682312 {
		t.Fatalf("amd64 = %d, %v", n, err)
	}
	// 名稱比對不分大小寫（wsl.exe 亦然）
	n, err = DownloadSize(ctx, srv.Client(), manifest, "azurelinux-4", "arm64")
	if err != nil || n != 170000000 {
		t.Fatalf("arm64 = %d, %v", n, err)
	}
	// 只在舊式（appx）清單或不存在 → 錯誤，呼叫端改顯示不定進度
	if _, err = DownloadSize(ctx, srv.Client(), manifest, "LegacyOnly", "amd64"); err == nil {
		t.Fatal("legacy distro should not resolve a size")
	}
	if _, err = DownloadSize(ctx, srv.Client(), manifest, "Nope", "amd64"); err == nil {
		t.Fatal("unknown distro should error")
	}
	if _, err = DownloadSize(ctx, srv.Client(), srv.URL+"/missing.json", "AzureLinux-4", "amd64"); err == nil {
		t.Fatal("manifest 404 should error")
	}
}

func TestDownloadPath(t *testing.T) {
	// wsl.exe 下載 modern 發行版時寫入 %TEMP%\<distribution>.wsl（與 URL 檔名無關）
	got := DownloadPath(filepath.FromSlash("/tmp/x"), "AzureLinux-4")
	if want := filepath.Join(filepath.FromSlash("/tmp/x"), "AzureLinux-4.wsl"); got != want {
		t.Fatalf("DownloadPath = %q, want %q", got, want)
	}
}
