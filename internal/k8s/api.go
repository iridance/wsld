package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// 唯讀查詢直連 k3s 的 API server（https://127.0.0.1:<port>，經 WSL localhost 轉發），
// 省下每次 wsl.exe 的啟動成本。任何失敗（連不上、憑證、非 2xx）都退回 kubectl 路徑，
// 所以這條路徑只是加速，不是新的失敗來源。憑證只留在記憶體，不寫入磁碟。

const apiTimeout = 8 * time.Second

// apiCooldown 是 API 失敗後暫停嘗試的時間。
const apiCooldown = 15 * time.Second

// maxAPIBody 限制單次回應大小，避免異常回應吃光記憶體。
const maxAPIBody = 64 << 20

type apiConn struct {
	client *http.Client
	base   string // https://127.0.0.1:26443
}

// kubeconfigCreds 是 k3s.yaml 中連線所需的部分。
type kubeconfigCreds struct {
	Clusters []struct {
		Cluster struct {
			Server string `yaml:"server"`
			CAData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		User struct {
			CertData string `yaml:"client-certificate-data"`
			KeyData  string `yaml:"client-key-data"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// newAPIConn 由 k3s 的 kubeconfig 建立 client：以其 CA 驗證伺服器、以 client cert 認證。
// server 的 host 一律視為 127.0.0.1（與匯出的 kubeconfig 相同），埠取自檔案。
func newAPIConn(kubeconfig []byte) (*apiConn, error) {
	var kc kubeconfigCreds
	if err := yaml.Unmarshal(kubeconfig, &kc); err != nil {
		return nil, err
	}
	if len(kc.Clusters) == 0 || len(kc.Users) == 0 {
		return nil, errors.New("kubeconfig has no cluster or user")
	}
	ca, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	if err != nil || len(ca) == 0 {
		return nil, errors.New("kubeconfig has no usable CA")
	}
	cert, err := base64.StdEncoding.DecodeString(kc.Users[0].User.CertData)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(kc.Users[0].User.KeyData)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("kubeconfig CA is not valid PEM")
	}
	u, err := url.Parse(kc.Clusters[0].Cluster.Server)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		port = legacyAPIPort
	}
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: apiTimeout,
	}
	return &apiConn{client: &http.Client{Transport: tr, Timeout: apiTimeout}, base: APIServer(port)}, nil
}

// conn 取得（必要時建立並快取）API 連線；失敗回 nil。
func (m *Manager) conn(ctx context.Context) *apiConn {
	m.apiMu.Lock()
	defer m.apiMu.Unlock()
	if m.apiC != nil {
		return m.apiC
	}
	if time.Now().Before(m.apiDownUntil) {
		return nil
	}
	kc, err := m.Kubeconfig(ctx)
	if err != nil {
		m.apiDownUntil = time.Now().Add(apiCooldown)
		return nil
	}
	c, err := newAPIConn(kc)
	if err != nil {
		m.apiDownUntil = time.Now().Add(apiCooldown)
		return nil
	}
	m.apiC = c
	return c
}

// ResetAPI 丟棄快取的憑證與連線（k3s 重裝／reset 後憑證會變）。
func (m *Manager) ResetAPI() {
	m.apiMu.Lock()
	defer m.apiMu.Unlock()
	m.apiDownUntil = time.Time{}
	m.resetLocked()
}

// failAPI 在傳輸或驗證失敗時丟棄連線並進入冷卻期。
func (m *Manager) failAPI() {
	m.apiMu.Lock()
	defer m.apiMu.Unlock()
	m.resetLocked()
	m.apiDownUntil = time.Now().Add(apiCooldown)
}

func (m *Manager) resetLocked() {
	if m.apiC != nil {
		m.apiC.client.CloseIdleConnections()
		m.apiC = nil
	}
}

// apiGet 以 GET 取得 path 的回應本文；任何失敗回 ok=false（並丟棄快取，下次重建）。
func (m *Manager) apiGet(ctx context.Context, path string) (body string, ok bool) {
	if m.DisableAPI {
		return "", false
	}
	c := m.conn(ctx)
	if c == nil {
		return "", false
	}
	rctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		m.failAPI()
		return "", false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody+1))
	if err != nil || len(b) > maxAPIBody {
		return "", false
	}
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			m.failAPI() // 憑證可能已換
		}
		return "", false
	}
	return string(b), true
}

// get 先走 API（path 為 REST 路徑），失敗退回 kubectl。
func (m *Manager) get(ctx context.Context, path string, kubectl ...string) (string, error) {
	if out, ok := m.apiGet(ctx, path); ok {
		return out, nil
	}
	return m.root(ctx, nil, append([]string{"k3s", "kubectl"}, kubectl...)...)
}

// listPath 回傳核心資源的 list 路徑；ns 為空 = 所有 namespace。
// 只涵蓋單一種類的資源（多種類的 extras 查詢仍走 kubectl）。
func listPath(kind, ns string) (string, bool) {
	var prefix string
	switch kind {
	case "pods", "services", "namespaces", "nodes", "events":
		prefix = "/api/v1"
	case "deployments", "replicasets":
		prefix = "/apis/apps/v1"
	default:
		return "", false
	}
	if ns != "" && kind != "namespaces" && kind != "nodes" {
		prefix += "/namespaces/" + url.PathEscape(ns)
	}
	return fmt.Sprintf("%s/%s", prefix, kind), true
}
