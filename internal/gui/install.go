package gui

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/wsl"
)

// InstallProbe 觀察 `wsl --install` 的下載進度（wsl.exe 本身在 pipe 下不輸出進度）。
type InstallProbe interface {
	// TotalSize 回傳發行版下載檔大小；查不到（舊式 appx、離線）回錯誤。
	TotalSize(ctx context.Context, distribution string) (int64, error)
	// Downloaded 回傳目前暫存檔大小；檔案不存在時 ok=false。
	Downloaded(distribution string) (n int64, ok bool)
}

// WSLInstallProbe 以官方清單 + HEAD 取總大小，以 %TEMP%\<distribution>.wsl 的大小當已下載量。
type WSLInstallProbe struct {
	Client      *http.Client // nil → http.DefaultClient
	ManifestURL string       // 空 → wsl.DistributionInfoURL
	TempDir     string       // 空 → os.TempDir()
	Arch        string       // 空 → runtime.GOARCH
}

func (p WSLInstallProbe) TotalSize(ctx context.Context, distribution string) (int64, error) {
	u, arch := p.ManifestURL, p.Arch
	if u == "" {
		u = wsl.DistributionInfoURL
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	return wsl.DownloadSize(ctx, p.Client, u, distribution, arch)
}

func (p WSLInstallProbe) Downloaded(distribution string) (int64, bool) {
	dir := p.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	fi, err := os.Stat(wsl.DownloadPath(dir, distribution))
	if err != nil || fi.IsDir() {
		return 0, false
	}
	return fi.Size(), true
}

// InstallProgress 是 GET /api/machines/install/progress 的回應。
type InstallProgress struct {
	Active       bool   `json:"active"`
	Distribution string `json:"distribution,omitempty"`
	Name         string `json:"name,omitempty"`
	// Phase：preparing（尚未開始下載）／downloading／installing（下載完、匯入中，或無法追蹤下載）／joining（加入共用 engine 中）／bootstrapping（engine 內安裝 Docker 中）。
	Phase       string `json:"phase,omitempty"`
	Downloaded  int64  `json:"downloaded"`
	Total       int64  `json:"total"` // 0 = 未知
	BytesPerSec int64  `json:"bytes_per_sec"`
	ElapsedSec  int64  `json:"elapsed_sec"`
}

const speedWindow = 5 * time.Second

type sizeSample struct {
	at time.Time
	n  int64
}

type installState struct {
	dist, name string
	started    time.Time
	total      int64
	untracked  bool // 查不到總大小 → 沒有 .wsl 暫存檔可追
	sawFile    bool
	samples    []sizeSample
	phase      string // 強制階段：joining（加入共用 engine）、bootstrapping（建立 engine 時在 distro 內安裝 Docker）；空＝依下載狀況推算
}

// installTracker 記錄唯一一個進行中的安裝（wsl.exe 不支援同時安裝）。
type installTracker struct {
	probe InstallProbe
	now   func() time.Time

	mu  sync.Mutex
	cur *installState
}

func (t *installTracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// begin 開始追蹤；已有安裝進行中則回 false。
func (t *installTracker) begin(dist, name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur != nil {
		return false
	}
	t.cur = &installState{dist: dist, name: name, started: t.clock()}
	return true
}

// setJoining 標記進入「加入共用 engine」階段（進度條改顯示 joining，而不是停在下載完成的畫面）。
func (t *installTracker) setJoining() { t.setPhase("joining") }

// setPhase 強制目前階段（之後不再依下載狀況推算）。
func (t *installTracker) setPhase(p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur != nil {
		t.cur.phase = p
	}
}

func (t *installTracker) end() {
	t.mu.Lock()
	t.cur = nil
	t.mu.Unlock()
}

// setTotal 寫入總大小查詢結果；dist 與目前安裝不符（過期結果）則忽略。
func (t *installTracker) setTotal(dist string, n int64, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur == nil || t.cur.dist != dist {
		return
	}
	if err != nil || n <= 0 {
		t.cur.untracked = true
		return
	}
	t.cur.total = n
}

func (t *installTracker) snapshot() InstallProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cur
	if c == nil {
		return InstallProgress{}
	}
	now := t.clock()
	p := InstallProgress{
		Active: true, Distribution: c.dist, Name: c.name,
		Total: c.total, ElapsedSec: int64(now.Sub(c.started) / time.Second),
	}
	if c.phase != "" {
		p.Phase = c.phase
		return p
	}
	if t.probe == nil {
		p.Phase = "installing"
		return p
	}
	n, ok := t.probe.Downloaded(c.dist)
	switch {
	case ok && c.total > 0 && n >= c.total:
		p.Phase, p.Downloaded = "installing", c.total
	case ok:
		c.sawFile = true
		p.Phase, p.Downloaded = "downloading", n
		c.samples = append(c.samples, sizeSample{now, n})
		cut := 0
		for cut < len(c.samples)-1 && now.Sub(c.samples[cut].at) > speedWindow {
			cut++
		}
		c.samples = c.samples[cut:]
		first := c.samples[0]
		if dt := now.Sub(first.at); dt > 0 && n > first.n {
			p.BytesPerSec = int64(float64(n-first.n) / dt.Seconds())
		}
	case c.sawFile || c.untracked:
		p.Phase = "installing"
	default:
		p.Phase = "preparing"
	}
	return p
}

func (s *Server) installs() *installTracker {
	s.installOnce.Do(func() {
		s.install = &installTracker{probe: s.InstallProbe}
	})
	return s.install
}

func (s *Server) handleInstallProgress(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.installs().snapshot())
}

// trackInstall 開始追蹤並在背景查總大小；回傳的 done 於安裝結束時呼叫。ok=false 表示已有安裝進行中。
func (s *Server) trackInstall(ctx context.Context, dist, name string) (done func(), ok bool) {
	tr := s.installs()
	if !tr.begin(dist, name) {
		return nil, false
	}
	if tr.probe != nil {
		go func() {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			n, err := tr.probe.TotalSize(c, dist)
			tr.setTotal(dist, n, err)
		}()
	}
	return tr.end, true
}
