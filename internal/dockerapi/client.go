// Package dockerapi 是對 Docker Engine HTTP API 的最小 client，
// 只涵蓋 wsld／wsld-gui 需要的端點（SPEC §5.4／§5.5）。
package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnreachable 表示 TCP 連線層失敗（引擎未啟動／埠不對）。
var ErrUnreachable = errors.New("docker api unreachable")

// APIError 是引擎回傳的非 2xx 回應。
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker api: %d %s", e.Status, e.Message)
}

// IsNotFound 判斷是否 404。
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Client 綁定單一 base URL。
type Client struct {
	base    string       // http://127.0.0.1:23750
	hc      *http.Client // 一般請求：整體 timeout
	longHC  *http.Client // 長時間請求（stop 寬限期、串流）：只靠 ctx deadline
	timeout time.Duration
}

// New 建立 client。endpoint 可為 tcp://host:port 或 http://host:port。
func New(endpoint string, timeout time.Duration) *Client {
	base := strings.TrimSuffix(endpoint, "/")
	if strings.HasPrefix(base, "tcp://") {
		base = "http://" + strings.TrimPrefix(base, "tcp://")
	}
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSHandshakeTimeout: timeout,
	}
	return &Client{
		base:    base,
		timeout: timeout,
		hc:      &http.Client{Timeout: timeout, Transport: tr},
		longHC:  &http.Client{Transport: tr},
	}
}

// BaseURL 回傳 http base。
func (c *Client) BaseURL() string { return c.base }

// hostPort 回傳 base 的 host:port（供 hijack 用）。
func (c *Client) hostPort() (string, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	return c.doWith(ctx, c.hc, method, path, q, body)
}

// doWith 同 do，但可指定 http.Client（longHC 用於必須超過一般 timeout 的請求）。
func (c *Client) doWith(ctx context.Context, hc *http.Client, method, path string, q url.Values, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, wrapNetErr(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func wrapNetErr(err error) error {
	var ne net.Error
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%w: %v", ErrUnreachable, ue.Err)
	}
	return err
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

// Ping GET /_ping，期望 body 為 "OK"。
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if strings.TrimSpace(string(b)) != "OK" {
		return fmt.Errorf("unexpected ping response %q (is this really the Docker API?)", truncate(string(b), 40))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Version 對應 GET /version 的子集。
type Version struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Os         string `json:"Os"`
	Arch       string `json:"Arch"`
}

// ServerVersion GET /version。
func (c *Client) ServerVersion(ctx context.Context) (Version, error) {
	var v Version
	resp, err := c.do(ctx, http.MethodGet, "/version", nil, nil)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	return v, json.NewDecoder(resp.Body).Decode(&v)
}

// Port 對應容器埠對應。
type Port struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Container 對應 GET /containers/json 的項目子集。
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Ports   []Port            `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
}

// Compose 標籤（docker compose 為每個容器打上）。
const (
	LabelComposeProject = "com.docker.compose.project"
	LabelComposeService = "com.docker.compose.service"
)

// ComposeProject 回傳 compose 專案名；非 compose 容器為空字串。
func (c Container) ComposeProject() string { return c.Labels[LabelComposeProject] }

// ComposeService 回傳 compose 服務名；非 compose 容器為空字串。
func (c Container) ComposeService() string { return c.Labels[LabelComposeService] }

// Name 回傳第一個名稱（去掉前導 "/"）。
func (ct Container) Name() string {
	if len(ct.Names) == 0 {
		return ct.ShortID()
	}
	return strings.TrimPrefix(ct.Names[0], "/")
}

// ShortID 回傳前 12 碼。
func (ct Container) ShortID() string {
	if len(ct.ID) > 12 {
		return ct.ID[:12]
	}
	return ct.ID
}

// ListContainers GET /containers/json。
func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/json", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Container
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	return out, nil
}

// StartContainer POST /containers/{id}/start（304 已啟動視為成功）。
func (c *Client) StartContainer(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// StopContainer POST /containers/{id}/stop?t=（304 已停止視為成功）。
func (c *Client) StopContainer(ctx context.Context, id string, timeoutSec int) error {
	q := url.Values{}
	if timeoutSec > 0 {
		q.Set("t", strconv.Itoa(timeoutSec))
	}
	// dockerd 會等到寬限期 t 秒後才 SIGKILL 再回應；deadline = t + 一般 API timeout，
	// 否則 PID 1 忽略 SIGTERM 的容器會被誤判為「API 無法連線」。
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second+c.timeout)
	defer cancel()
	resp, err := c.doWith(ctx, c.longHC, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// RemoveContainer DELETE /containers/{id}?force=1（force 可刪 running）。
func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ContainerDetail 對應 GET /containers/{id}/json 的子集。
type ContainerDetail struct {
	ID      string   `json:"Id"`
	Name    string   `json:"Name"`
	Created string   `json:"Created"`
	Path    string   `json:"Path"`
	Args    []string `json:"Args"`
	Config  struct {
		Tty    bool              `json:"Tty"`
		Image  string            `json:"Image"`
		Cmd    []string          `json:"Cmd"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	HostConfig struct {
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports    map[string][]PortBinding            `json:"Ports"`
		Networks map[string]ContainerNetworkEndpoint `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []ContainerMount `json:"Mounts"`
}

// PortBinding 是 inspect 的 HostPort 綁定。
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// ContainerNetworkEndpoint 是單一網路端點。
type ContainerNetworkEndpoint struct {
	IPAddress string `json:"IPAddress"`
	NetworkID string `json:"NetworkID"`
}

// ContainerMount 是掛載點。
type ContainerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

// Inspect GET /containers/{id}/json。
func (c *Client) Inspect(ctx context.Context, id string) (ContainerDetail, error) {
	var d ContainerDetail
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil)
	if err != nil {
		return d, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return d, fmt.Errorf("decode inspect: %w", err)
	}
	return d, nil
}

// LogsOptions：Tail<=0 代表 all；Follow 為串流。
type LogsOptions struct {
	Tail   int
	Follow bool
}

// Logs GET /containers/{id}/logs。非 TTY 容器回傳的多工串流會被解多工成純文字。
// Follow=true 時 timeout 不適用（呼叫端以 ctx 取消）。
func (c *Client) Logs(ctx context.Context, id string, opts LogsOptions) (io.ReadCloser, error) {
	detail, err := c.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("stdout", "1")
	q.Set("stderr", "1")
	if opts.Tail > 0 {
		q.Set("tail", strconv.Itoa(opts.Tail))
	} else {
		q.Set("tail", "all")
	}
	if opts.Follow {
		q.Set("follow", "1")
	} else {
		q.Set("follow", "0")
	}
	u := c.base + "/containers/" + url.PathEscape(id) + "/logs?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hc := c.hc
	if opts.Follow {
		hc = c.longHC // 串流不可有整體 timeout
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, wrapNetErr(err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	if detail.Config.Tty {
		return resp.Body, nil
	}
	return &readCloser{Reader: NewDemuxReader(resp.Body), Closer: resp.Body}, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// DemuxReader 把 Docker 多工串流（8 byte header + payload）合併為單一位元流。
type DemuxReader struct {
	r       io.Reader
	remain  int // 目前 frame 尚未讀完的 payload bytes
	hdr     [8]byte
	hdrRead int
}

// NewDemuxReader 建立解多工 reader。
func NewDemuxReader(r io.Reader) *DemuxReader { return &DemuxReader{r: r} }

func (d *DemuxReader) Read(p []byte) (int, error) {
	for d.remain == 0 {
		// 讀 header（可能跨多次 Read）
		n, err := d.r.Read(d.hdr[d.hdrRead:])
		d.hdrRead += n
		if d.hdrRead == 8 {
			d.remain = int(uint32(d.hdr[4])<<24 | uint32(d.hdr[5])<<16 | uint32(d.hdr[6])<<8 | uint32(d.hdr[7]))
			d.hdrRead = 0
			if d.remain == 0 {
				continue
			}
			break
		}
		if err != nil {
			if err == io.EOF && d.hdrRead == 0 {
				return 0, io.EOF
			}
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
	}
	if len(p) > d.remain {
		p = p[:d.remain]
	}
	n, err := d.r.Read(p)
	d.remain -= n
	if err == io.EOF && d.remain > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// ExecCreate POST /containers/{id}/exec，回傳 exec id。
func (c *Client) ExecCreate(ctx context.Context, containerID string, cmd []string, tty bool) (string, error) {
	body := map[string]any{
		"AttachStdin":  true,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          tty,
		"Cmd":          cmd,
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/exec", nil, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode exec create: %w", err)
	}
	return out.ID, nil
}

// ExecStart POST /exec/{id}/start 並 hijack 連線，回傳雙向 raw stream。
// tty=true 時串流為 raw；tty=false 時呼叫端需自行以 NewDemuxReader 解多工。
func (c *Client) ExecStart(ctx context.Context, execID string, tty bool) (net.Conn, error) {
	hp, err := c.hostPort()
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: c.timeout}
	conn, err := d.DialContext(ctx, "tcp", hp)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": tty})
	req, err := http.NewRequest(http.MethodPost, c.base+"/exec/"+url.PathEscape(execID)+"/start", bytes.NewReader(body))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode >= 400 {
		defer conn.Close()
		return nil, readAPIError(resp)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("exec start: unexpected status %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufConn{Conn: conn, r: br}, nil
}

// bufConn 讓 hijack 後 bufio 內已緩衝的 bytes 不遺失。
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// ExecResize POST /exec/{id}/resize?h=&w=。
func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	q := url.Values{}
	q.Set("h", strconv.Itoa(rows))
	q.Set("w", strconv.Itoa(cols))
	resp, err := c.do(ctx, http.MethodPost, "/exec/"+url.PathEscape(execID)+"/resize", q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
