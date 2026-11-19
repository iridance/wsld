package lan

import (
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/dockerapi"
)

// Target 是一個要開放給區網的 published 埠。
type Target struct {
	Port      int    `json:"port"`
	Container string `json:"container"`
}

// Forward 是一個轉發（區網位址:埠 → 127.0.0.1:埠）的目前狀態。
type Forward struct {
	Address   string `json:"address"`
	Port      int    `json:"port"`
	Container string `json:"container"`
	// Error 非空表示無法在該位址監聽（例如埠已被其他程式佔用）。
	Error string `json:"error,omitempty"`
}

// ForwardURL 回傳轉發位址的 http URL（IPv6 會加中括號）。
func ForwardURL(f Forward) string { return "http://" + key(f.Address, f.Port) }

// Forwarder 在區網 IP 上監聽容器 published 埠，並把連線轉到 Windows 的 127.0.0.1（WSL localhost 轉發）。
// 由 Windows 程式監聽非 loopback 位址，第一次時 Windows 防火牆會跳出「允許私人／公用網路」的確認視窗。
type Forwarder struct {
	// TargetHost 是轉發目的地（預設 127.0.0.1）。
	TargetHost string
	// Listen 可替換（測試）；nil = net.Listen。
	Listen func(network, addr string) (net.Listener, error)
	// DialTimeout 連到目的地的逾時（預設 5 秒）。
	DialTimeout time.Duration

	mu    sync.Mutex
	state map[string]*fwd // key = address:port
}

type fwd struct {
	Forward
	ln    net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  chan struct{}
}

func key(addr string, port int) string { return net.JoinHostPort(addr, strconv.Itoa(port)) }

// Sync 讓轉發與 addresses × targets 一致：開啟缺少的、關閉多餘的；先前監聽失敗的會重試。
func (f *Forwarder) Sync(addresses []string, targets []Target) {
	want := map[string]Target{}
	wantAddr := map[string]string{}
	for _, a := range addresses {
		for _, t := range targets {
			k := key(a, t.Port)
			want[k] = t
			wantAddr[k] = a
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]*fwd{}
	}
	for k, cur := range f.state {
		if _, ok := want[k]; !ok {
			cur.stop()
			delete(f.state, k)
		}
	}
	for k, t := range want {
		if cur, ok := f.state[k]; ok {
			cur.Container = t.Container
			if cur.ln != nil {
				continue
			}
			delete(f.state, k) // 上次失敗：重試
		}
		f.state[k] = f.open(wantAddr[k], t)
	}
}

// Close 關閉所有轉發。
func (f *Forwarder) Close() { f.Sync(nil, nil) }

// Forwards 回傳目前的轉發（依位址、埠排序）。
func (f *Forwarder) Forwards() []Forward {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Forward, 0, len(f.state))
	for _, s := range f.state {
		out = append(out, s.Forward)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Address != out[j].Address {
			return out[i].Address < out[j].Address
		}
		return out[i].Port < out[j].Port
	})
	return out
}

func (f *Forwarder) open(addr string, t Target) *fwd {
	s := &fwd{Forward: Forward{Address: addr, Port: t.Port, Container: t.Container}, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
	listen := f.Listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", key(addr, t.Port))
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.ln = ln
	target := key(f.targetHost(), t.Port)
	timeout := f.DialTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	go s.serve(target, timeout)
	return s
}

func (f *Forwarder) targetHost() string {
	if f.TargetHost == "" {
		return "127.0.0.1"
	}
	return f.TargetHost
}

func (s *fwd) serve(target string, timeout time.Duration) {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		go s.pipe(c, target, timeout)
	}
}

func (s *fwd) pipe(c net.Conn, target string, timeout time.Duration) {
	up, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		_ = c.Close()
		return
	}
	if !s.track(c, up) {
		_ = c.Close()
		_ = up.Close()
		return
	}
	defer s.untrack(c, up)
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(up, c)
	go cp(c, up)
	<-done
	<-done
	_ = c.Close()
	_ = up.Close()
}

func (s *fwd) track(cs ...net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return false
	default:
	}
	for _, c := range cs {
		s.conns[c] = struct{}{}
	}
	return true
}

func (s *fwd) untrack(cs ...net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cs {
		delete(s.conns, c)
	}
}

// stop 關閉監聽與進行中的連線（埠不再 published 時）。
func (s *fwd) stop() {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
	}
	close(s.done)
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// PublishedTargets 從容器清單挑出要開放的 TCP published 埠：只看執行中、綁所有介面（0.0.0.0／::／空）的埠，
// 明確綁 127.0.0.1 的不開放；exclude（例如 Docker API 埠）一律略過。同一埠只取第一個容器名。
func PublishedTargets(cs []dockerapi.Container, exclude ...int) []Target {
	seen := map[int]bool{}
	for _, p := range exclude {
		seen[p] = true
	}
	var out []Target
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		name := c.ID
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		for _, p := range c.Ports {
			if p.PublicPort <= 0 || (p.Type != "" && p.Type != "tcp") {
				continue
			}
			switch p.IP {
			case "", "0.0.0.0", "::":
			default:
				continue
			}
			if seen[int(p.PublicPort)] {
				continue
			}
			seen[int(p.PublicPort)] = true
			out = append(out, Target{Port: int(p.PublicPort), Container: name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
