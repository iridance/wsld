package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

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
}

// SyncLAN 依設定與執行中容器更新轉發：關閉時全部停止；引擎查不到容器時維持現狀。
func (s *Server) SyncLAN(ctx context.Context) {
	if s.Forwarder == nil {
		return
	}
	s.lanMu.Lock()
	defer s.lanMu.Unlock()
	cfg := s.cfg()
	if !cfg.LANAccess {
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
	s.Forwarder.Sync(addrs, lan.PublishedTargets(cs, apiPort))
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
	}
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
	// 立即監聽：Windows 防火牆的「允許私人／公用網路」視窗會在第一次監聽時跳出
	s.SyncLAN(r.Context())
	writeJSON(w, http.StatusOK, s.lanStatus(r.Context()))
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
