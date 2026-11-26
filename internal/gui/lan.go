package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/lifecycle"
	"github.com/iridance/wsld/internal/wsl"
)

// LANStatus 是 GET/PUT /api/lan 的回應。
type LANStatus struct {
	// Enabled 對應 config.toml 的 lan_access。
	Enabled bool `json:"enabled"`
	// Addresses 是本機的內網 IPv4。
	Addresses []string `json:"addresses"`
	// Forwards 是目前的轉發（區網位址:埠 → 容器）。
	Forwards []lan.Forward `json:"forwards"`
	// Mirrored 表示 WSL 仍是 0.9.0 留下的 mirrored 模式（需還原 NAT 才能轉發）。
	Mirrored bool `json:"mirrored"`
	// Firewall 是 wsld-gui.exe 的防火牆對內授權狀態（allowed／missing／blocked／unknown）。
	// 授權屬於 app 層級，與區網存取開關無關，因此一律查詢。missing／blocked 時不在內網 IP 監聽。
	Firewall lan.FirewallState `json:"firewall"`
	// FirewallMethod 是授權時會提權的程式（netsh／helper），供說明對話框告訴使用者 UAC 會顯示什麼。
	FirewallMethod lan.FirewallMethod `json:"firewall_method,omitempty"`
	// Exe 是規則綁定的 wsld-gui.exe 路徑（說明對話框顯示）。
	Exe string `json:"exe,omitempty"`
	// FirewallPending 表示正在等使用者回應防火牆的 UAC（設定頁的授權按鈕）。
	FirewallPending bool `json:"firewall_pending"`
}

// fwCacheTTL 是背景同步沿用防火牆狀態的時間（讀取規則需逐條走 COM，較慢）。
const fwCacheTTL = 30 * time.Second

// firewall 回傳 wsld-gui.exe 的防火牆狀態；快取逾時（或已失效）時重新讀取。
func (s *Server) firewall(ctx context.Context) lan.FirewallInfo {
	s.fwCacheMu.Lock()
	defer s.fwCacheMu.Unlock()
	if s.fwCacheAt.IsZero() || time.Since(s.fwCacheAt) > fwCacheTTL {
		s.fwCache = s.LAN.FirewallInfo(ctx)
		s.fwCacheAt = time.Now()
	}
	return s.fwCache
}

// invalidateFirewall 讓下一次 firewall() 重新讀取規則。
func (s *Server) invalidateFirewall() {
	s.fwCacheMu.Lock()
	s.fwCacheAt = time.Time{}
	s.fwCacheMu.Unlock()
}

// firewallBlocksLAN：尚未授權（missing／blocked）時不在內網 IP 監聽，
// 避免 Windows 自己跳「安全性警訊」（在那裡按取消會產生封鎖規則）。查不到狀態（unknown）時照常監聽。
func (s *Server) firewallBlocksLAN(ctx context.Context) bool {
	if s.LAN == nil {
		return false
	}
	switch s.firewall(ctx).State {
	case lan.FirewallMissing, lan.FirewallBlocked:
		return true
	}
	return false
}

// SyncLAN 依設定與執行中容器更新轉發：關閉時全部停止；引擎查不到容器時維持現狀。
func (s *Server) SyncLAN(ctx context.Context) {
	if s.Forwarder == nil {
		return
	}
	s.lanMu.Lock()
	defer s.lanMu.Unlock()
	cfg := s.cfg()
	if !cfg.LANAccess || s.firewallBlocksLAN(ctx) {
		s.Forwarder.Close()
		return
	}
	addrs := s.lanAddresses()
	eng := s.engine()
	if eng == nil {
		return
	}
	cs, err := eng.ListContainers(ctx, false)
	if err != nil {
		return
	}
	apiPort, _ := cfg.APIPort()
	s.Forwarder.Sync(addrs, mergeLANTargets(lan.PublishedTargets(cs, apiPort), s.k8sLANTargets(ctx)))
}

// RunLAN 每 every 同步一次轉發，直到 ctx 結束（結束時關閉所有轉發）。
func (s *Server) RunLAN(ctx context.Context, every time.Duration) {
	if s.Forwarder == nil {
		return
	}
	defer s.Forwarder.Close()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		sctx, cancel := context.WithTimeout(ctx, every)
		s.SyncLAN(sctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) lanAddresses() []string {
	if s.LAN == nil {
		return []string{}
	}
	ifs, err := s.LAN.Host.Interfaces()
	if err != nil {
		return []string{}
	}
	return lan.LANAddresses(ifs)
}

func (s *Server) lanStatus(ctx context.Context) LANStatus {
	st := LANStatus{Enabled: s.cfg().LANAccess, Addresses: s.lanAddresses(), Forwards: []lan.Forward{}}
	if s.Forwarder != nil {
		st.Forwards = s.Forwarder.Forwards()
	}
	if s.LAN != nil {
		if l, err := s.LAN.Legacy(ctx); err == nil {
			st.Mirrored = l.Mirrored
		}
		fi := s.firewall(ctx)
		st.Firewall, st.FirewallMethod, st.Exe = fi.State, fi.Method, s.LAN.Exe
	}
	st.FirewallPending = s.fwPending.Load()
	return st
}

func (s *Server) lanAvailable(w http.ResponseWriter) bool {
	if s.LAN == nil || s.Forwarder == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("LAN access unavailable"))
		return false
	}
	return true
}

func (s *Server) handleGetLAN(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	s.invalidateFirewall() // 設定頁開啟／刷新時重新讀取規則（使用者可能在 Windows 端改過）
	s.SyncLAN(r.Context())
	writeJSON(w, http.StatusOK, s.lanStatus(r.Context()))
}

func (s *Server) handlePutLAN(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	c, err := s.LoadConfig()
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	c.LANAccess = in.Enabled
	if err := s.SaveConfig(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	// 只改設定並立即監聽（已授權時）；不提權：未授權時由前端先顯示說明對話框，使用者確認後才 POST /api/lan/firewall
	s.invalidateFirewall()
	s.SyncLAN(r.Context())
	writeJSON(w, http.StatusOK, s.lanStatus(r.Context()))
}

// LANForwards 是 GET /api/lan/forwards 的回應（容器頁每次刷新取用；不查 .wslconfig／防火牆）。
type LANForwards struct {
	Enabled  bool          `json:"enabled"`
	Forwards []lan.Forward `json:"forwards"`
}

func (s *Server) handleLANForwards(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	s.SyncLAN(r.Context())
	writeJSON(w, http.StatusOK, LANForwards{Enabled: s.cfg().LANAccess, Forwards: s.Forwarder.Forwards()})
}

// handleLANOpen 以系統瀏覽器開啟一個轉發位址；只接受目前成功轉發中的 http://<位址>:<埠>。
func (s *Server) handleLANOpen(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	allowed := false
	for _, f := range s.Forwarder.Forwards() {
		if f.Error == "" && in.URL == lan.ForwardURL(f) {
			allowed = true
			break
		}
	}
	if !allowed {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("not a forwarded LAN address"))
		return
	}
	if s.OnOpenURL != nil {
		if err := s.OnOpenURL(in.URL); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// allowFirewall 以 UAC 加入規則；同一時間只跑一個（等待期間 /api/lan 回報 firewall_pending）。
func (s *Server) allowFirewall(ctx context.Context) error {
	s.fwMu.Lock()
	defer s.fwMu.Unlock()
	// 排隊等待期間可能已由另一個請求授權完成
	s.invalidateFirewall()
	if s.firewall(ctx).State == lan.FirewallAllowed {
		return nil
	}
	s.fwPending.Store(true)
	defer s.fwPending.Store(false)
	defer s.invalidateFirewall()
	return s.LAN.AllowFirewall(ctx)
}

// handleAllowFirewall：以 UAC 替 wsld-gui.exe 加入對內允許規則（私人＋公用網路、TCP、僅同網段）。
// 只在使用者於說明對話框按「繼續」後呼叫（開啟區網存取或設定頁的授權按鈕）；wsld-gui 啟動時不提權。
func (s *Server) handleAllowFirewall(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	// 等使用者回應 UAC；不綁 request context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := s.allowFirewall(ctx); err != nil {
		if errors.Is(err, lan.ErrUACCanceled) {
			writeErr(w, http.StatusConflict, "canceled", errors.New(i18n.T("gui.lan.fw.canceled")))
			return
		}
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	s.SyncLAN(ctx) // 授權完成：立即開始轉發
	writeJSON(w, http.StatusOK, s.lanStatus(ctx))
}

// handleRestoreNAT：清掉 0.9.0 的 mirrored 設定（UAC 移除防火牆規則、改 .wslconfig），再重啟 WSL。
func (s *Server) handleRestoreNAT(w http.ResponseWriter, r *http.Request) {
	if !s.lanAvailable(w) {
		return
	}
	// UAC 與 WSL 重啟可能較久；不綁 request context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := s.LAN.RestoreNAT(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	if err := s.restartWSL(ctx); err != nil {
		writeEngineErr(w, err)
		return
	}
	s.SyncLAN(ctx)
	writeJSON(w, http.StatusOK, s.lanStatus(ctx))
}

// restartWSL：wsl --shutdown 後重新啟動 active machine（期間 /api/status 回報 starting）。
func (s *Server) restartWSL(ctx context.Context) error {
	if s.WSL == nil {
		return errors.New("wsl unavailable")
	}
	s.startMu.Lock()
	s.starting = true
	s.startMu.Unlock()
	defer func() {
		s.startMu.Lock()
		s.starting = false
		s.startMu.Unlock()
	}()
	// 用戶端先關 keep-alive 連線：避免伺服器端關閉時把 TIME_WAIT 留在 API 埠上
	if c, ok := s.engine().(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
	if err := wsl.Shutdown(ctx, s.WSL); err != nil {
		return err
	}
	cfg := s.cfg()
	am, ok := cfg.ActiveMachine()
	if !ok {
		return nil
	}
	// 網路模式切換後舊連線可能暫佔 API 埠（TIME_WAIT）；等埠釋放後再起 dockerd，否則 bind: address already in use
	if port, err := cfg.APIPort(); err == nil {
		s.LAN.WaitPortFree(ctx, port, lan.PortWaitTimeout)
	}
	if !am.Ready() {
		_, err := s.bringUpActive(ctx)
		return err
	}
	// 已 init：只重新啟動 docker.service（剛 shutdown 完 API 必然不通，不可當成需要重跑 bootstrap）
	if _, err := s.life().Start(ctx); err != nil {
		return err
	}
	if mgr, ok := s.life().(*lifecycle.Manager); ok {
		if s.SaveConfig != nil {
			if err := s.SaveConfig(mgr.Cfg); err != nil {
				return err
			}
		}
		s.BindActive(mgr.Cfg)
	}
	return nil
}
