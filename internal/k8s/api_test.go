package k8s

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testPKI struct {
	caPEM, clientCertPEM, clientKeyPEM []byte
	serverCert                         tls.Certificate
	caPool                             *x509.CertPool
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	issue := func(serial int64, cn string, usage x509.ExtKeyUsage, ips []net.IP) ([]byte, []byte, *ecdsa.PrivateKey) {
		k := key()
		tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: ips}
		der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kd, _ := x509.MarshalECPrivateKey(k)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), k
	}
	sc, sk, _ := issue(2, "server", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	cc, ck, _ := issue(3, "admin", x509.ExtKeyUsageClientAuth, nil)
	pair, err := tls.X509KeyPair(sc, sk)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return testPKI{caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		clientCertPEM: cc, clientKeyPEM: ck, serverCert: pair, caPool: pool}
}

func (p testPKI) kubeconfig(server string) string {
	b := func(x []byte) string { return base64.StdEncoding.EncodeToString(x) }
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: default
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: default
  user:
    client-certificate-data: %s
    client-key-data: %s
`, server, b(p.caPEM), b(p.clientCertPEM), b(p.clientKeyPEM))
}

// startAPI 啟動要求 client cert 的假 API server，回傳其位址與請求計數。
func startAPI(t *testing.T, p testPKI, h http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		h(w, r)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{p.serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: p.caPool}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &hits
}

func apiMgr(f *fakeRunner, kubeconfig string) *Manager {
	m := newMgr(f)
	m.DisableAPI = false
	f.on(d+" cat "+k3sKubeconf, resp{out: kubeconfig})
	return m
}

const apiPodsJSON = `{"kind":"PodList","items":[{"metadata":{"name":"web","namespace":"default"},"status":{"phase":"Running"}}]}`

func TestListPodsUsesTCPAPIWithoutKubectl(t *testing.T) {
	p := newTestPKI(t)
	srv, hits := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/default/pods" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, apiPodsJSON)
	})
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	pods, err := m.ListPods(context.Background(), "default")
	if err != nil || len(pods) != 1 || pods[0].Name != "web" {
		t.Fatalf("pods = %+v, err %v", pods, err)
	}
	if _, err := m.ListPods(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d", *hits)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "kubectl") {
			t.Fatalf("kubectl should not run: %s", c)
		}
	}
	// 憑證快取：kubeconfig 只讀一次。
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, "cat "+k3sKubeconf) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("kubeconfig read %d times", n)
	}
}

func TestAPIFailureFallsBackToKubectl(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	f.on(d+" k3s kubectl get pods -n default -o json", resp{out: apiPodsJSON})
	pods, err := m.ListPods(context.Background(), "default")
	if err != nil || len(pods) != 1 {
		t.Fatalf("pods = %+v, err %v", pods, err)
	}
}

func TestAPIUnreachableFallsBackAndDropsCache(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, apiPodsJSON) })
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	if _, err := m.ListPods(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	srv.Close() // 連線失敗
	f.on(d+" k3s kubectl get pods -n default -o json", resp{out: apiPodsJSON})
	if _, err := m.ListPods(context.Background(), "default"); err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if m.apiC != nil {
		t.Fatal("cache should be dropped after a transport error")
	}
}

func TestAPIRejectsUntrustedServerCert(t *testing.T) {
	p := newTestPKI(t)
	other := newTestPKI(t) // 不同 CA：伺服器憑證不被信任
	srv, hits := startAPI(t, other, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, apiPodsJSON) })
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	f.on(d+" k3s kubectl get pods -n default -o json", resp{out: apiPodsJSON})
	if _, err := m.ListPods(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if *hits != 0 {
		t.Fatalf("request reached an untrusted server (%d)", *hits)
	}
}

func TestReadyzAndNodesViaAPI(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			fmt.Fprint(w, "ok")
		default:
			http.NotFound(w, r)
		}
	})
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	out, err := m.get(context.Background(), "/readyz", "get", "--raw", "/readyz")
	if err != nil || out != "ok" {
		t.Fatalf("readyz = %q, %v", out, err)
	}
}

func TestListPathCoversSingleKindResourcesOnly(t *testing.T) {
	cases := []struct{ kind, ns, want string }{
		{"pods", "", "/api/v1/pods"},
		{"pods", "a b", "/api/v1/namespaces/a%20b/pods"},
		{"deployments", "x", "/apis/apps/v1/namespaces/x/deployments"},
		{"namespaces", "x", "/api/v1/namespaces"},
		{"nodes", "", "/api/v1/nodes"},
	}
	for _, c := range cases {
		if got, ok := listPath(c.kind, c.ns); !ok || got != c.want {
			t.Errorf("listPath(%s,%s) = %q,%v", c.kind, c.ns, got, ok)
		}
	}
	if _, ok := listPath("statefulsets,ingresses", ""); ok {
		t.Error("multi-kind query must stay on kubectl")
	}
}

func TestResetAPIDropsCredentials(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, apiPodsJSON) })
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	if m.conn(context.Background()) == nil {
		t.Fatal("conn")
	}
	m.ResetAPI()
	if m.apiC != nil {
		t.Fatal("not reset")
	}
}

func TestAPIFailureEntersCooldownUntilReset(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, apiPodsJSON) })
	f := newFake()
	m := apiMgr(f, p.kubeconfig(srv.URL))
	f.on(d+" k3s kubectl get pods -n default -o json", resp{out: apiPodsJSON})
	if _, err := m.ListPods(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	for i := 0; i < 3; i++ { // 第一次失敗後進入冷卻，之後不再重讀 kubeconfig／重試
		if _, err := m.ListPods(context.Background(), "default"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, "cat "+k3sKubeconf) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("kubeconfig read %d times during cooldown", n)
	}
	m.ResetAPI()
	if !m.apiDownUntil.IsZero() {
		t.Fatal("reset should clear the cooldown")
	}
}

// 憑證已快取且 /readyz 通了：輪詢不再開任何 wsl.exe（不查 list／test／is-active／記憶體／IP）。
func TestStatusPollingUsesNoWSLOnceAPIIsUp(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			fmt.Fprint(w, "ok")
		case "/api/v1/nodes":
			fmt.Fprint(w, nodesReady)
		default:
			http.NotFound(w, r)
		}
	})
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "active\n"})
	f.on(d+" systemctl show k3s -p MemoryCurrent --value", resp{out: "439762944\n"})
	f.on(d+" hostname -I", resp{out: "172.20.3.68\n"})
	m := apiMgr(f, p.kubeconfig(srv.URL))
	if st := m.Status(context.Background()); st.State != StateReady || st.MemoryBytes != 439762944 {
		t.Fatalf("first poll: %+v", st)
	}
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	st := m.Status(context.Background())
	if st.State != StateReady || !st.Ready || st.MemoryBytes != 439762944 || st.NodeName != "framework" {
		t.Fatalf("second poll: %+v", st)
	}
	if len(f.calls) != 0 {
		t.Fatalf("steady-state polling must not run wsl.exe: %v", f.calls)
	}
}

// API 掛了就落回完整探測（distro 停了要看得出來，且不能喚醒它）。
func TestStatusFallsBackWhenAPIGoesDown(t *testing.T) {
	p := newTestPKI(t)
	srv, _ := startAPI(t, p, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	f := newFake()
	f.running()
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesReady})
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "active\n"})
	f.on(d+" systemctl show k3s -p MemoryCurrent --value", resp{out: "1\n"})
	f.on(d+" hostname -I", resp{out: "172.20.3.68\n"})
	m := apiMgr(f, p.kubeconfig(srv.URL))
	m.Status(context.Background())
	srv.Close()
	f.on("--list --verbose", resp{out: "  NAME            STATE           VERSION\n* Ubuntu-26.04    Stopped         2\n"})
	if st := m.Status(context.Background()); st.State != StateStopped {
		t.Fatalf("State = %q", st.State)
	}
}
