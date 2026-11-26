package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
	"github.com/iridance/wsld/internal/lan"
)

// k8sNodePortsEvery：區網轉發每次同步都會經過，NodePort 清單最多每 30 秒查一次（要打 wsl.exe）。
const k8sNodePortsEvery = 30 * time.Second

// k8sReady 回傳可用的操作；未開啟、背景作業中或不支援時寫出回應並回 nil。
// empty 非 nil 時，未開啟就回 200 + empty（列表端點），否則回 409。
func (s *Server) k8sReady(w http.ResponseWriter, empty any) K8sOps {
	ops := s.k8sOps(K8sOptions{})
	if ops == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("kubernetes support unavailable"))
		return nil
	}
	if !s.cfg().K8s {
		if empty != nil {
			writeJSON(w, http.StatusOK, empty)
		} else {
			writeErr(w, http.StatusConflict, "not_ready", errors.New(i18n.T("k8s.not_running")))
		}
		return nil
	}
	return ops
}

// handleK8sPodDelete DELETE /api/k8s/pods/{ns}/{name}。
func (s *Server) handleK8sPodDelete(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !k8s.ValidName(ns) || !k8s.ValidName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("k8s.bad_name")))
		return
	}
	ops := s.k8sReady(w, nil)
	if ops == nil || s.k8sBusy(w) {
		return
	}
	if err := ops.DeletePod(r.Context(), ns, name); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleK8sServices GET /api/k8s/services（順便更新區網轉發用的 NodePort 清單）。
func (s *Server) handleK8sServices(w http.ResponseWriter, r *http.Request) {
	ops := s.k8sReady(w, []k8s.Service{})
	if ops == nil {
		return
	}
	svcs, err := ops.ListServices(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if svcs == nil {
		svcs = []k8s.Service{}
	}
	s.setNodePorts(k8s.NodePorts(svcs))
	writeJSON(w, http.StatusOK, svcs)
}

// handleK8sStats GET /api/k8s/stats：各 pod 的 CPU／記憶體（engine 的 docker stats 加總）。
func (s *Server) handleK8sStats(w http.ResponseWriter, r *http.Request) {
	ops := s.k8sReady(w, []k8s.PodStats{})
	if ops == nil {
		return
	}
	st, err := ops.ListPodStats(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if st == nil {
		st = []k8s.PodStats{}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleK8sForwards GET /api/k8s/forwards。
func (s *Server) handleK8sForwards(w http.ResponseWriter, r *http.Request) {
	ops := s.k8sReady(w, []k8s.PortForward{})
	if ops == nil {
		return
	}
	fws, err := ops.ListForwards(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if fws == nil {
		fws = []k8s.PortForward{}
	}
	writeJSON(w, http.StatusOK, fws)
}

// handleK8sForwardCreate POST /api/k8s/forwards：{namespace, kind: svc|pod, name, local_port, remote_port}。
func (s *Server) handleK8sForwardCreate(w http.ResponseWriter, r *http.Request) {
	var req k8s.ForwardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	if (req.Kind != "svc" && req.Kind != "pod") || !k8s.ValidName(req.Namespace) || !k8s.ValidName(req.Name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("k8s.bad_name")))
		return
	}
	if req.LocalPort < 1 || req.LocalPort > 65535 || req.RemotePort < 0 || req.RemotePort > 65535 {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("k8s.bad_port", strconv.Itoa(req.LocalPort))))
		return
	}
	ops := s.k8sReady(w, nil)
	if ops == nil || s.k8sBusy(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute) // 第一次要拉 socat 映像檔
	defer cancel()
	f, err := ops.Forward(ctx, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

// handleK8sForwardDelete DELETE /api/k8s/forwards/{port}。
func (s *Server) handleK8sForwardDelete(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("k8s.bad_port", r.PathValue("port"))))
		return
	}
	ops := s.k8sReady(w, nil)
	if ops == nil {
		return
	}
	if err := ops.StopForward(r.Context(), port); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setNodePorts(m map[int]string) {
	s.k8sMu.Lock()
	s.k8sNodePorts, s.k8sNodePortsAt = m, time.Now()
	s.k8sMu.Unlock()
}

func (s *Server) invalidateNodePorts() {
	s.k8sMu.Lock()
	s.k8sNodePortsAt = time.Time{}
	s.k8sMu.Unlock()
}

// k8sLANTargets：Kubernetes 開啟且 Ready 時，NodePort 也開放到區網（LoadBalancer 的 port 已是 docker published 埠）。
// 清單有快取；過期才在這裡重查。
func (s *Server) k8sLANTargets(ctx context.Context) []lan.Target {
	if !s.cfg().K8s || s.K8sFor == nil {
		return nil
	}
	s.k8sMu.Lock()
	ports, at, busy := s.k8sNodePorts, s.k8sNodePortsAt, s.k8sOp != ""
	last := s.k8sLast
	s.k8sMu.Unlock()
	if !busy && (at.IsZero() || time.Since(at) > k8sNodePortsEvery) && (last == nil || last.State == k8s.StateReady) {
		if svcs, err := s.k8sOps(K8sOptions{}).ListServices(ctx); err == nil {
			ports = k8s.NodePorts(svcs)
			s.setNodePorts(ports)
		}
	}
	out := make([]lan.Target, 0, len(ports))
	for p, name := range ports {
		out = append(out, lan.Target{Port: p, Container: "k8s:" + name})
	}
	return out
}

// mergeLANTargets 把 extra 中尚未出現的埠加進 base（依埠排序）。
func mergeLANTargets(base, extra []lan.Target) []lan.Target {
	seen := map[int]bool{}
	for _, t := range base {
		seen[t.Port] = true
	}
	for _, t := range extra {
		if !seen[t.Port] {
			seen[t.Port] = true
			base = append(base, t)
		}
	}
	sort.Slice(base, func(i, j int) bool { return base[i].Port < base[j].Port })
	return base
}
