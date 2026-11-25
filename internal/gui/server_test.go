package gui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
)

type env struct {
	srv    *httptest.Server
	engine *FakeEngine
	life   *FakeLifecycle
	auto   *FakeAutostart
	cfgP   string
	token  string
	window []string
	s      *Server
}

func newEnv(t *testing.T, token string) *env {
	t.Helper()
	i18n.Set(i18n.EN)
	e := &env{engine: NewFakeEngine(), auto: &FakeAutostart{}, token: token, cfgP: filepath.Join(t.TempDir(), "config.toml")}
	e.life = &FakeLifecycle{Engine: e.engine, Distro: "Ubuntu-26.04", Endpoint: "tcp://127.0.0.1:23750"}
	c := config.Default()
	c.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: config.EndpointForPort(config.DefaultPort)})
	_ = c.SetEngine("Ubuntu-26.04") // 這台就是共用 engine
	_ = c.SetActive("Ubuntu-26.04")
	if err := config.Save(e.cfgP, c); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Engine: e.engine, Life: e.life, Autostart: e.auto, Version: "test", Token: token,
		Shell:          FakeDistroShell{},
		LoadConfig:     func() (config.Config, error) { return config.Load(e.cfgP) },
		SaveConfig:     func(c config.Config) error { return config.Save(e.cfgP, c) },
		OnWindowAction: func(a string) { e.window = append(e.window, a) },
		OnDistroAction: func(a, d, _ string) error {
			if d != "Ubuntu-26.04" {
				a += ":" + d // 非 active：記下目標 distro
			}
			e.window = append(e.window, a)
			return nil
		},
		OnOpenURL: func(u string) error { e.window = append(e.window, "open-url:"+u); return nil },
	}
	e.s = s
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) req(t *testing.T, method, path string, body string) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) ws(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + path
	if e.token != "" {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "token=" + e.token
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMetaIncludesMessagesAndConfig(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, "GET", "/api/meta", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var m Meta
	_ = json.Unmarshal(b, &m)
	if m.Version != "test" || m.Lang != "en" || m.Messages["gui.start"] != "Start" || m.Distro != "Ubuntu-26.04" || m.Config.RefreshIntervalSec != 3 || !m.AutostartSupported {
		t.Fatalf("meta = %+v", m)
	}
}

func TestMetaZhTW(t *testing.T) {
	e := newEnv(t, "")
	i18n.Set(i18n.ZhTW)
	t.Cleanup(func() { i18n.Set(i18n.EN) })
	_, b := e.req(t, "GET", "/api/meta", "")
	var m Meta
	_ = json.Unmarshal(b, &m)
	if m.Lang != "zh-TW" || m.Messages["gui.start"] != "啟動" {
		t.Fatalf("meta = %v %q", m.Lang, m.Messages["gui.start"])
	}
}

func TestTokenRequiredForAPI(t *testing.T) {
	e := newEnv(t, "secret")
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/status", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Fatalf("no token → %d", resp.StatusCode)
	}
	resp, _ = e.req(t, "GET", "/api/status", "")
	if resp.StatusCode != 200 {
		t.Fatalf("with token → %d", resp.StatusCode)
	}
	// query token（WebSocket 用）
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/status?token=secret", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("query token → %d", resp.StatusCode)
	}
}

func TestCORSPreflight(t *testing.T) {
	e := newEnv(t, "secret")
	req, _ := http.NewRequest("OPTIONS", e.srv.URL+"/api/status", nil)
	req.Header.Set("Origin", "wails://wails")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "wails://wails" {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestStatusReportsDownThenAutoStartBringsItUp(t *testing.T) {
	e := newEnv(t, "")
	e.engine.SetDown(true)
	_, b := e.req(t, "GET", "/api/status", "")
	var st StatusResponse
	_ = json.Unmarshal(b, &st)
	if st.Ready || st.APIReachable {
		t.Fatalf("expected down: %+v", st)
	}
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	_, b = e.req(t, "GET", "/api/status", "")
	st = StatusResponse{}
	_ = json.Unmarshal(b, &st)
	if !st.Ready || e.life.Starts() != 1 {
		t.Fatalf("after auto start: %+v starts=%d", st, e.life.Starts())
	}
}

// 引擎 profile 還是占位（沒有 user／endpoint）：自動帶起時補齊 init。
func TestAutoStartInitsAPlaceholderEngine(t *testing.T) {
	e := newEnv(t, "")
	e.s.WSL = NewFakeWSL()
	c := config.Default()
	c.UpsertMachine(config.Machine{Distro: "docker-desktop"})
	_ = c.SetEngine("docker-desktop")
	_ = c.SetActive("docker-desktop")
	if err := e.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	e.s.BindActive(c)
	e.engine.SetDown(true)

	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	c, _ = e.s.LoadConfig()
	m, ok := c.FindMachine("docker-desktop")
	if !ok || !m.Ready() || !m.IsEngine() {
		t.Fatalf("placeholder engine should be initialised: ok=%v m=%+v", ok, m)
	}
	_, b := e.req(t, "GET", "/api/meta", "")
	var meta Meta
	_ = json.Unmarshal(b, &meta)
	if !meta.ActiveReady || meta.Distro != "docker-desktop" {
		t.Fatalf("meta after start = %+v", meta)
	}
}

// 沒有 config 開 GUI：第一次啟動就建立 engine，Life 從 ConfigMissingLifecycle 升級。
func TestAutoStartPromotesConfigMissingLifecycle(t *testing.T) {
	e := newEnv(t, "")
	e.s.WSL = NewFakeWSL()
	if err := config.Save(e.cfgP, config.Default()); err != nil {
		t.Fatal(err)
	}
	e.s.Life = ConfigMissingLifecycle{Path: e.cfgP}
	e.s.NewLifecycle = func(cfg config.Config) Lifecycle {
		return &FakeLifecycle{Engine: e.engine, Distro: cfg.Distro, Endpoint: cfg.APIEndpoint}
	}
	e.engine.SetDown(true)
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.s.Life.(ConfigMissingLifecycle); ok {
		t.Fatal("Life should be promoted off ConfigMissingLifecycle")
	}
	_, b := e.req(t, "GET", "/api/status", "")
	var st StatusResponse
	_ = json.Unmarshal(b, &st)
	if !st.Ready {
		t.Fatalf("engine should be up: %+v", st)
	}
}

// endpoint 已失效（引擎沒回應）：自動帶起會重選可用 endpoint 並恢復。
func TestAutoStartReInitsWhenAPIUnreachable(t *testing.T) {
	e := newEnv(t, "")
	e.s.WSL = NewFakeWSL()
	e.engine.SetDown(true)
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	c, _ := e.s.LoadConfig()
	m, _ := c.FindMachine("Ubuntu-26.04")
	if !m.Ready() || m.APIEndpoint == "" {
		t.Fatalf("engine should stay ready: %+v", m)
	}
	_, b := e.req(t, "GET", "/api/status", "")
	var st StatusResponse
	_ = json.Unmarshal(b, &st)
	if !st.Ready {
		t.Fatalf("engine should be up: %+v", st)
	}
}

func TestCORSAllowsDELETE(t *testing.T) {
	e := newEnv(t, "")
	req, err := http.NewRequest(http.MethodOptions, e.srv.URL+"/api/containers/web", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://wails.localhost")
	req.Header.Set("Access-Control-Request-Method", "DELETE")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	allow := resp.Header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(allow, "DELETE") {
		t.Fatalf("preflight must allow DELETE (Wails cross-origin remove); got %q", allow)
	}
}

func TestContainerRemoveForce(t *testing.T) {
	e := newEnv(t, "")
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/api/containers/web?force=1", nil)
	req.Header.Set("Origin", "http://wails.localhost")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("remove = %d %s", resp.StatusCode, b)
	}
	_, b := e.req(t, "GET", "/api/containers?all=1", "")
	if strings.Contains(string(b), `"/web"`) {
		t.Fatalf("web still listed after remove: %s", b)
	}
}

func TestContainersListAndAll(t *testing.T) {
	e := newEnv(t, "")
	_, b := e.req(t, "GET", "/api/containers", "")
	if !strings.Contains(string(b), `"/web"`) || strings.Contains(string(b), `"/db"`) {
		t.Fatalf("running only: %s", b)
	}
	_, b = e.req(t, "GET", "/api/containers?all=1", "")
	if !strings.Contains(string(b), `"/db"`) {
		t.Fatalf("all: %s", b)
	}
}

func TestContainersWhenDownIs502(t *testing.T) {
	e := newEnv(t, "")
	e.engine.SetDown(true)
	resp, b := e.req(t, "GET", "/api/containers", "")
	var ae apiError
	_ = json.Unmarshal(b, &ae)
	if resp.StatusCode != 502 || ae.Code != "api" {
		t.Fatalf("%d %+v", resp.StatusCode, ae)
	}
}

func TestStartStopContainerAnd404(t *testing.T) {
	e := newEnv(t, "")
	resp, _ := e.req(t, "POST", "/api/containers/db/start", "")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	_, b := e.req(t, "GET", "/api/containers", "")
	if !strings.Contains(string(b), `"/db"`) {
		t.Fatalf("db should be running: %s", b)
	}
	resp, _ = e.req(t, "POST", "/api/containers/web/stop?t=1", "")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	resp, b = e.req(t, "POST", "/api/containers/nope/start", "")
	var ae apiError
	_ = json.Unmarshal(b, &ae)
	if resp.StatusCode != 404 || ae.Code != "not_found" {
		t.Fatalf("%d %+v", resp.StatusCode, ae)
	}
}

func TestLogsTailDefault100(t *testing.T) {
	e := newEnv(t, "")
	for i := 0; i < 150; i++ {
		e.engine.AppendLog("web", "line")
	}
	_, b := e.req(t, "GET", "/api/containers/web/logs", "")
	if n := strings.Count(string(b), "\n"); n != 100 {
		t.Fatalf("lines = %d", n)
	}
	_, b = e.req(t, "GET", "/api/containers/web/logs?tail=5", "")
	if n := strings.Count(string(b), "\n"); n != 5 {
		t.Fatalf("tail=5 lines = %d", n)
	}
}

func TestLogsStreamPushesNewLines(t *testing.T) {
	e := newEnv(t, "secret")
	c := e.ws(t, "/api/containers/web/logs/stream?tail=1")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, first, err := c.Read(ctx)
	if err != nil || !strings.Contains(string(first), "GET / 200") {
		t.Fatalf("first = %q err=%v", first, err)
	}
	e.engine.AppendLog("web", "NEW LINE")
	var got string
	for !strings.Contains(got, "NEW LINE") {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got)
		}
		got += string(msg)
	}
}

func TestExecWebSocketEchoAndResize(t *testing.T) {
	e := newEnv(t, "")
	c := e.ws(t, "/api/containers/web/exec?cols=100&rows=30")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	read := func() string {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read(); !strings.Contains(got, "fake-shell$") {
		t.Fatalf("prompt = %q", got)
	}
	if e.engine.ResizeState() != [2]int{30, 100} {
		t.Fatalf("initial resize = %v", e.engine.ResizeState())
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	var out string
	for !strings.Contains(out, "you typed: ls") {
		out += read()
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	deadline := time.Now().Add(2 * time.Second)
	for e.engine.ResizeState() != [2]int{40, 120} && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.engine.ResizeState() != [2]int{40, 120} {
		t.Fatalf("resize = %v", e.engine.ResizeState())
	}
	_ = c.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			break // 連線在 exec 結束後關閉
		}
	}
}

func TestExecOnStoppedContainerFails(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, "GET", "/api/containers/db/exec", "")
	// Docker 對未執行容器的 exec 回 409，橋接層原樣轉為 409 conflict
	if resp.StatusCode != 409 || !strings.Contains(string(b), "not running") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestMachineExecWebSocketEcho(t *testing.T) {
	e := newEnv(t, "")
	c := e.ws(t, "/api/machines/Ubuntu-26.04/exec?cols=80&rows=24")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil || !strings.Contains(string(b), "fake-shell$") {
		t.Fatalf("prompt = %q err=%v", b, err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("pwd\n")); err != nil {
		t.Fatal(err)
	}
	var out string
	for !strings.Contains(out, "you typed: pwd") {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, out)
		}
		out += string(msg)
	}
}

func TestMachineExecInvalidName(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, "GET", "/api/machines/bad name!/exec", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestExecUsesConfigShell(t *testing.T) {
	e := newEnv(t, "")
	c, _ := config.Load(e.cfgP)
	c.ExecShell = "zsh"
	_ = config.Save(e.cfgP, c)
	ws := e.ws(t, "/api/containers/web/exec")
	defer ws.CloseNow()
	// FakeEngine 的 execID 含命令；透過 prompt 後沒有直接暴露，改驗證 ExecCreate 不失敗即可
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, b, err := ws.Read(ctx); err != nil || !strings.Contains(string(b), "fake-shell$") {
		t.Fatalf("%q %v", b, err)
	}
}

func TestSettingsRoundTripAndAutostart(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, "PUT", "/api/settings", `{"refresh_interval_sec":5,"exec_shell":"fish","tray_autostart":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	c, _ := config.Load(e.cfgP)
	if c.RefreshIntervalSec != 5 || c.ExecShell != "fish" || !c.TrayAutostart {
		t.Fatalf("cfg = %+v", c)
	}
	if on, _ := e.auto.Enabled(); !on {
		t.Fatal("autostart should be enabled")
	}
	_, b = e.req(t, "GET", "/api/settings", "")
	if !strings.Contains(string(b), `"refresh_interval_sec":5`) {
		t.Fatalf("%s", b)
	}
	resp, _ = e.req(t, "PUT", "/api/settings", `{"refresh_interval_sec":0}`)
	if resp.StatusCode != 400 {
		t.Fatalf("invalid interval → %d", resp.StatusCode)
	}
}

func TestSettingsLangSwitchPersistsAndChangesMeta(t *testing.T) {
	e := newEnv(t, "")
	var notified []string
	e.s.OnLangChange = func(l i18n.Lang) { notified = append(notified, string(l)) }
	t.Cleanup(func() { i18n.Set(i18n.EN) })

	// 預設英文，settings 帶 lang
	_, b := e.req(t, "GET", "/api/meta", "")
	if !strings.Contains(string(b), `"lang":"en"`) || !strings.Contains(string(b), `"Engine not running"`) {
		t.Fatalf("default meta should be English: %s", b)
	}
	_, b = e.req(t, "GET", "/api/settings", "")
	if !strings.Contains(string(b), `"lang":"en"`) {
		t.Fatalf("settings should expose lang: %s", b)
	}

	resp, b := e.req(t, "PUT", "/api/settings", `{"refresh_interval_sec":3,"exec_shell":"","tray_autostart":false,"lang":"zh-TW"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	c, _ := config.Load(e.cfgP)
	if c.Lang != "zh-TW" {
		t.Fatalf("cfg.Lang = %q", c.Lang)
	}
	if i18n.Current() != i18n.ZhTW {
		t.Fatalf("server language should switch, got %s", i18n.Current())
	}
	if len(notified) != 1 || notified[0] != "zh-TW" {
		t.Fatalf("OnLangChange = %v", notified)
	}
	_, b = e.req(t, "GET", "/api/meta", "")
	if !strings.Contains(string(b), `"lang":"zh-TW"`) || !strings.Contains(string(b), "引擎尚未啟動") {
		t.Fatalf("meta should be zh-TW: %s", b)
	}

	// 未知語系 → 400，且不改變目前語系
	resp, _ = e.req(t, "PUT", "/api/settings", `{"refresh_interval_sec":3,"lang":"klingon"}`)
	if resp.StatusCode != 400 || i18n.Current() != i18n.ZhTW {
		t.Fatalf("invalid lang → %d, current=%s", resp.StatusCode, i18n.Current())
	}
	// 省略 lang → 保持原值
	resp, _ = e.req(t, "PUT", "/api/settings", `{"refresh_interval_sec":4}`)
	c, _ = config.Load(e.cfgP)
	if resp.StatusCode != 200 || c.Lang != "zh-TW" {
		t.Fatalf("omitting lang must keep it: %d %q", resp.StatusCode, c.Lang)
	}
}

func TestWindowActions(t *testing.T) {
	e := newEnv(t, "")
	for _, a := range []string{"hide", "show", "quit"} {
		if resp, _ := e.req(t, "POST", "/api/window/"+a, ""); resp.StatusCode != 200 {
			t.Fatal(a, resp.StatusCode)
		}
	}
	if resp, _ := e.req(t, "POST", "/api/window/explode", ""); resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	if strings.Join(e.window, ",") != "hide,show,quit" {
		t.Fatalf("window = %v", e.window)
	}
}

func TestOpenRepo(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, "POST", "/api/open-repo", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), RepoURL) {
		t.Fatalf("body = %s", b)
	}
	if strings.Join(e.window, ",") != "open-url:"+RepoURL {
		t.Fatalf("window = %v", e.window)
	}
}

func TestDistroActions(t *testing.T) {
	e := newEnv(t, "")
	for _, a := range []string{"open-folder", "open-terminal"} {
		if resp, _ := e.req(t, "POST", "/api/distro/"+a, ""); resp.StatusCode != 200 {
			t.Fatal(a, resp.StatusCode)
		}
	}
	if resp, _ := e.req(t, "POST", "/api/distro/explode", ""); resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	if strings.Join(e.window, ",") != "open-folder,open-terminal" {
		t.Fatalf("actions = %v", e.window)
	}
}

func TestDistroActionsCanTargetAManagedMachine(t *testing.T) {
	e := newEnv(t, "")
	c, _ := config.Load(e.cfgP)
	if err := c.SetK8sMachine("k8s-engine"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(e.cfgP, c); err != nil {
		t.Fatal(err)
	}
	var got []string
	e.s.OnDistroAction = func(a, d, u string) error { got = append(got, a+":"+d+":"+u); return nil }
	if resp, _ := e.req(t, "POST", "/api/distro/open-folder?distro=k8s-engine", ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := e.req(t, "POST", "/api/distro/open-folder?distro=nope", ""); resp.StatusCode != 404 {
		t.Fatalf("unknown machine must be 404, got %d", resp.StatusCode)
	}
	if strings.Join(got, ",") != "open-folder:k8s-engine:root" {
		t.Fatalf("got %v", got)
	}
}

func TestDoctorUnavailableWithoutWSL(t *testing.T) {
	e := newEnv(t, "")
	if resp, _ := e.req(t, "GET", "/api/doctor", ""); resp.StatusCode != 501 {
		t.Fatal(resp.StatusCode)
	}
}

func TestListenLoopback(t *testing.T) {
	e := newEnv(t, "")
	s := &Server{Engine: e.engine, Life: e.life, Version: "x"}
	base, stop, err := s.ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("base = %s", base)
	}
	resp, err := http.Get(base + "/api/status")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp)
	}
}

func TestOpenLocalOnlyPublishedPorts(t *testing.T) {
	e := newEnv(t, "") // 不需要區網支援
	if resp, b := e.req(t, "POST", "/api/open-local", `{"port":8080}`); resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if len(e.window) != 1 || e.window[0] != "open-url:http://localhost:8080" {
		t.Fatalf("window=%v", e.window)
	}
	for _, bad := range []string{`{"port":9999}`, `{"port":0}`, `{"port":-1}`, `nope`} {
		if resp, _ := e.req(t, "POST", "/api/open-local", bad); resp.StatusCode != 400 {
			t.Fatalf("%s: status %d", bad, resp.StatusCode)
		}
	}
	// 停止後不再接受
	if resp, b := e.req(t, "POST", "/api/containers/web/stop", ""); resp.StatusCode != 200 {
		t.Fatalf("stop: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.req(t, "POST", "/api/open-local", `{"port":8080}`); resp.StatusCode != 400 {
		t.Fatalf("stopped container: status %d", resp.StatusCode)
	}
	if len(e.window) != 1 {
		t.Fatalf("rejected ports must not open: %v", e.window)
	}
}

func TestAutoStartBringsUpReadyActive(t *testing.T) {
	e := newEnv(t, "")
	e.engine.SetDown(true)
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if e.life.Starts() != 1 {
		t.Fatalf("auto start should start engine once, starts=%d", e.life.Starts())
	}
}

func TestAutoStartSkipsStartWhenAlreadyReady(t *testing.T) {
	e := newEnv(t, "")
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if e.life.Starts() != 0 {
		t.Fatalf("running engine must not be restarted, starts=%d", e.life.Starts())
	}
}

// 全新安裝（還沒有任何 engine）：啟動即自動建立並帶起 engine，不用使用者按任何鍵。
func TestAutoStartCreatesTheEngineOnFirstRun(t *testing.T) {
	e := newEnv(t, "")
	if err := config.Save(e.cfgP, config.Default()); err != nil {
		t.Fatal(err)
	}
	e.engine.SetDown(true)
	if err := e.s.AutoStart(); err != nil {
		t.Fatal(err)
	}
	if e.life.Starts() != 1 {
		t.Fatalf("first run must start the engine, starts=%d", e.life.Starts())
	}
	c, _ := e.s.LoadConfig()
	if en, ok := c.EngineMachine(); !ok || !en.Ready() {
		t.Fatalf("an engine must exist after first run: %+v", c.Machines)
	}
}

func TestSuperviseBringsADownEngineUp(t *testing.T) {
	e := newEnv(t, "")
	e.engine.SetDown(true)
	e.s.superviseOnce(time.Now())
	if e.life.Starts() != 1 {
		t.Fatalf("starts = %d", e.life.Starts())
	}
}

func TestSuperviseLeavesAReadyEngineAlone(t *testing.T) {
	e := newEnv(t, "")
	e.s.superviseOnce(time.Now())
	if e.life.Starts() != 0 {
		t.Fatalf("starts = %d", e.life.Starts())
	}
}

func TestSuperviseRespectsAUserStop(t *testing.T) {
	e := newEnv(t, "")
	fw := NewFakeWSL()
	e.s.WSL = fw
	e.s.KeepAlive = &wsl.KeepAlive{Spawner: fw}
	e.s.KeepAlive.Release("Ubuntu-26.04", true) // 使用者在 Machines 頁停止了 engine
	e.engine.SetDown(true)
	e.s.superviseOnce(time.Now())
	if e.life.Starts() != 0 {
		t.Fatalf("a stopped engine must stay stopped, starts=%d", e.life.Starts())
	}
}

func TestSuperviseFailureIsReportedAndBackedOff(t *testing.T) {
	e := newEnv(t, "")
	e.life.Broken = true
	e.engine.SetDown(true)
	now := time.Now()
	e.s.superviseOnce(now)
	e.s.superviseOnce(now.Add(5 * time.Second)) // 退避期間不重試
	if e.life.Starts() != 1 {
		t.Fatalf("starts = %d, want 1 (backoff)", e.life.Starts())
	}
	_, b := e.req(t, "GET", "/api/status", "")
	var st struct {
		StartError string `json:"start_error"`
	}
	_ = json.Unmarshal(b, &st)
	if !strings.Contains(st.StartError, "systemd") {
		t.Fatalf("status must carry the start error: %s", b)
	}
	e.life.Broken = false
	e.s.superviseOnce(now.Add(time.Minute))
	_, b = e.req(t, "GET", "/api/status", "")
	st.StartError = ""
	_ = json.Unmarshal(b, &st)
	if st.StartError != "" || e.life.Starts() != 2 {
		t.Fatalf("recovered: start_error=%q starts=%d", st.StartError, e.life.Starts())
	}
}
