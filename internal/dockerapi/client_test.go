package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, 2*time.Second), srv
}

func TestPingOK(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Api-Version", "1.47")
		_, _ = w.Write([]byte("OK"))
	}))
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPingBadBody(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not docker</html>"))
	}))
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected error for non-OK ping body")
	}
}

func TestPingUnreachableIsErrUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1", 500*time.Millisecond) // port 1: 幾乎必定拒絕
	err := c.Ping(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestNewAcceptsTCPScheme(t *testing.T) {
	c := New("tcp://127.0.0.1:23750", time.Second)
	if c.base != "http://127.0.0.1:23750" {
		t.Fatalf("base = %q", c.base)
	}
}

func TestListContainers(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "1" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[
		  {"Id":"abc123def456","Names":["/web"],"Image":"nginx:latest","State":"running","Status":"Up 2 hours","Created":1700000000,
		   "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web"},
		   "Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]},
		  {"Id":"fff","Names":["/db"],"Image":"postgres:16","State":"exited","Status":"Exited (0) 1 day ago","Created":1600000000,"Ports":[]}
		]`)
	}))
	got, err := c.ListContainers(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name() != "web" || got[0].ShortID() != "abc123def456" || got[0].State != "running" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[0].Ports[0].PublicPort != 8080 || got[0].Ports[0].PrivatePort != 80 {
		t.Fatalf("ports = %+v", got[0].Ports)
	}
	if got[1].Name() != "db" {
		t.Fatalf("got[1].Name = %q", got[1].Name())
	}
	if p, s := got[0].ComposeProject(), got[0].ComposeService(); p != "shop" || s != "web" {
		t.Fatalf("compose labels = %q/%q", p, s)
	}
	if p, s := got[1].ComposeProject(), got[1].ComposeService(); p != "" || s != "" {
		t.Fatalf("non-compose container must have empty project/service, got %q/%q", p, s)
	}
}

func TestShortIDTruncatesTo12(t *testing.T) {
	ct := Container{ID: "0123456789abcdef0123"}
	if ct.ShortID() != "0123456789ab" {
		t.Fatalf("ShortID = %q", ct.ShortID())
	}
}

// docker stop 會等到寬限期 t 秒才 SIGKILL（PID 1 忽略 SIGTERM 時很常見）；
// 這段等待不可被一般 API timeout 誤判成 ErrUnreachable。
func TestStopContainerWaitsBeyondAPITimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/slow/stop":
			time.Sleep(300 * time.Millisecond) // 比 API timeout 長，比 t + timeout 短
			w.WriteHeader(http.StatusNoContent)
		case "/_ping":
			time.Sleep(300 * time.Millisecond)
			_, _ = io.WriteString(w, "OK")
		}
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, 100*time.Millisecond)
	ctx := context.Background()

	if err := c.StopContainer(ctx, "slow", 1); err != nil {
		t.Fatalf("stop within grace period must succeed, got %v", err)
	}
	// 對照：一般請求仍受 API timeout 約束
	if err := c.Ping(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("ping should still time out with ErrUnreachable, got %v", err)
	}
}

func TestStopContainerHonoursGraceDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, 50*time.Millisecond)
	start := time.Now()
	err := c.StopContainer(context.Background(), "hang", 0) // t=0 → 只剩 API timeout 當緩衝
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stop should give up after t+timeout, took %s", time.Since(start))
	}
}

func TestStartStopContainerStatusHandling(t *testing.T) {
	var lastPath, lastQuery string
	status := http.StatusNoContent
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastQuery = r.URL.Path, r.URL.RawQuery
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if status >= 400 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"No such container: nope"}`)
			return
		}
		w.WriteHeader(status)
	}))
	ctx := context.Background()

	if err := c.StartContainer(ctx, "abc"); err != nil || lastPath != "/containers/abc/start" {
		t.Fatalf("start: %v path=%s", err, lastPath)
	}
	status = http.StatusNotModified // already started
	if err := c.StartContainer(ctx, "abc"); err != nil {
		t.Fatalf("304 should be nil, got %v", err)
	}
	status = http.StatusNoContent
	if err := c.StopContainer(ctx, "abc", 5); err != nil || lastPath != "/containers/abc/stop" || lastQuery != "t=5" {
		t.Fatalf("stop: %v path=%s q=%s", err, lastPath, lastQuery)
	}
	status = http.StatusNotFound
	err := c.StopContainer(ctx, "nope", 5)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || !strings.Contains(apiErr.Message, "No such container") {
		t.Fatalf("want APIError 404, got %v", err)
	}
	if !IsNotFound(err) {
		t.Fatal("IsNotFound should be true")
	}
}

func TestInspectTTY(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/abc/json" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"Id":"abc","Name":"/web","Config":{"Tty":true,"Image":"nginx"},"State":{"Status":"running","Running":true}}`)
	}))
	d, err := c.Inspect(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Config.Tty || d.State.Status != "running" || d.Name != "/web" {
		t.Fatalf("detail = %+v", d)
	}
}

func frame(stream byte, payload string) []byte {
	var b bytes.Buffer
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	b.Write(hdr)
	b.WriteString(payload)
	return b.Bytes()
}

func TestLogsDemuxesNonTTYStream(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/abc/json":
			_, _ = io.WriteString(w, `{"Id":"abc","Config":{"Tty":false}}`)
		case "/containers/abc/logs":
			q := r.URL.Query()
			if q.Get("stdout") != "1" || q.Get("stderr") != "1" || q.Get("tail") != "100" || q.Get("follow") != "0" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
			_, _ = w.Write(frame(1, "hello\n"))
			_, _ = w.Write(frame(2, "oops\n"))
			_, _ = w.Write(frame(1, "world\n"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	rc, err := c.Logs(context.Background(), "abc", LogsOptions{Tail: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	out, _ := io.ReadAll(rc)
	if string(out) != "hello\noops\nworld\n" {
		t.Fatalf("demuxed = %q", out)
	}
}

func TestLogsPassesThroughTTYStream(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/abc/json":
			_, _ = io.WriteString(w, `{"Id":"abc","Config":{"Tty":true}}`)
		case "/containers/abc/logs":
			_, _ = io.WriteString(w, "raw tty output\n")
		}
	}))
	rc, err := c.Logs(context.Background(), "abc", LogsOptions{Tail: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	out, _ := io.ReadAll(rc)
	if string(out) != "raw tty output\n" {
		t.Fatalf("got %q", out)
	}
}

func TestLogsFollowAndTailAll(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/abc/json":
			_, _ = io.WriteString(w, `{"Id":"abc","Config":{"Tty":true}}`)
		case "/containers/abc/logs":
			q := r.URL.Query()
			if q.Get("follow") != "1" || q.Get("tail") != "all" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
		}
	}))
	rc, err := c.Logs(context.Background(), "abc", LogsOptions{Follow: true, Tail: 0})
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}

func TestDemuxReaderHandlesSplitFrames(t *testing.T) {
	data := append(frame(1, strings.Repeat("a", 5000)), frame(2, "b")...)
	// 以極小 chunk 餵資料，確保跨讀取邊界正確
	r := NewDemuxReader(iotest1(data, 7))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5001 || out[5000] != 'b' {
		t.Fatalf("len = %d", len(out))
	}
}

// iotest1 每次最多回傳 n bytes 的 reader。
func iotest1(b []byte, n int) io.Reader {
	return &chunkReader{r: bytes.NewReader(b), n: n}
}

type chunkReader struct {
	r *bytes.Reader
	n int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(p) > c.n {
		p = p[:c.n]
	}
	return c.r.Read(p)
}

func TestExecCreateAndStartHijack(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/abc/exec":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["Tty"] != true || body["AttachStdin"] != true {
				t.Errorf("exec body = %v", body)
			}
			cmd, _ := body["Cmd"].([]any)
			if len(cmd) != 1 || cmd[0] != "bash" {
				t.Errorf("Cmd = %v", cmd)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"exec1"}`)
		case r.URL.Path == "/exec/exec1/start":
			_, _ = io.Copy(io.Discard, r.Body) // 如真 dockerd 一樣先消耗 request body
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("no hijacker")
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			// echo 一行
			line, _ := rw.ReadString('\n')
			_, _ = rw.WriteString("echo:" + line)
			_ = rw.Flush()
		case r.URL.Path == "/exec/exec1/resize":
			if r.URL.Query().Get("h") != "24" || r.URL.Query().Get("w") != "80" {
				t.Errorf("resize query = %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	ctx := context.Background()
	id, err := c.ExecCreate(ctx, "abc", []string{"bash"}, true)
	if err != nil || id != "exec1" {
		t.Fatalf("ExecCreate = %q, %v", id, err)
	}
	conn, err := c.ExecStart(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "ls\n"); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || got != "echo:ls\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := c.ExecResize(ctx, id, 24, 80); err != nil {
		t.Fatal(err)
	}
}

func TestExecStartRejectsNonUpgrade(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such exec instance"}`)
	}))
	_, err := c.ExecStart(context.Background(), "nope", true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("want 404 APIError, got %v", err)
	}
}
