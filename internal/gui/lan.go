package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

// LANUpdate 是 PUT /api/lan 的請求。
type LANUpdate struct {
	Enabled bool `json:"enabled"`
	// Restart 為 true 時執行 wsl --shutdown 並重新啟動 active machine，讓設定立即生效。
	Restart bool `json:"restart"`
}

func (s *Server) activeDistro() string {
	if m, ok := s.cfg().ActiveMachine(); ok {
		return m.Distro
	}
	return ""
}

func (s *Server) handleGetLAN(w http.ResponseWriter, r *http.Request) {
	if s.LAN == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("LAN access unavailable"))
		return
	}
	st, err := s.LAN.Status(r.Context(), s.activeDistro())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePutLAN(w http.ResponseWriter, r *http.Request) {
	if s.LAN == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("LAN access unavailable"))
		return
	}
	var in LANUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	// UAC 等待使用者確認，可能較久；不綁 request context（視窗關閉不中斷提權中的設定）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var err error
	if cur, cerr := s.LAN.Status(ctx, ""); cerr == nil && lanSettled(cur, in.Enabled) {
		// 已是目標狀態（例如只按「立即重啟 WSL」）：不再跳 UAC
	} else if in.Enabled {
		err = s.LAN.Enable(ctx)
	} else {
		err = s.LAN.Disable(ctx)
	}
	if errors.Is(err, lan.ErrUnsupported) {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	if in.Restart {
		if err := s.restartWSL(ctx); err != nil {
			writeEngineErr(w, err)
			return
		}
	}
	st, err := s.LAN.Status(ctx, s.activeDistro())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
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
	if err := wsl.Shutdown(ctx, s.WSL); err != nil {
		return err
	}
	if _, ok := s.cfg().ActiveMachine(); !ok {
		return nil
	}
	_, err := s.bringUpActive(ctx)
	return err
}

// lanSettled：.wslconfig 與防火牆規則都已符合目標狀態。
func lanSettled(st lan.Status, enabled bool) bool {
	if enabled {
		return st.Enabled && st.Firewall == lan.FirewallPresent
	}
	return !st.Enabled && st.Firewall == lan.FirewallMissing
}
