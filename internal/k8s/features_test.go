package k8s

import (
	"context"
	"strings"
	"testing"
)

func TestParsePodsContainersInitAndLabels(t *testing.T) {
	js := `{"items":[
 {"metadata":{"namespace":"default","name":"web","labels":{"app":"web"},"creationTimestamp":"2026-11-25T07:52:00Z"},
  "spec":{"initContainers":[{"name":"migrate"}],"containers":[{"name":"nginx"},{"name":"sidecar"}]},
  "status":{"phase":"Running","podIP":"172.17.0.5",
   "initContainerStatuses":[{"name":"migrate","containerID":"docker://aaa","state":{"terminated":{"reason":"Completed","containerID":"docker://aaa"}}}],
   "containerStatuses":[
    {"name":"nginx","containerID":"docker://bbb","ready":true,"state":{"running":{}}},
    {"name":"sidecar","ready":false,"restartCount":3,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"Error","containerID":"docker://ccc"}}}]}},
 {"metadata":{"namespace":"default","name":"init-wait","creationTimestamp":"2026-11-25T07:52:00Z"},
  "spec":{"initContainers":[{"name":"a"},{"name":"b"}],"containers":[{"name":"main"}]},
  "status":{"phase":"Pending","initContainerStatuses":[
   {"name":"a","state":{"terminated":{"reason":"Completed"}}},
   {"name":"b","state":{"running":{}}}],
   "containerStatuses":[{"name":"main","state":{"waiting":{"reason":"PodInitializing"}}}]}},
 {"metadata":{"namespace":"default","name":"init-crash","creationTimestamp":"2026-11-25T07:52:00Z"},
  "spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"main"}]},
  "status":{"phase":"Pending","initContainerStatuses":[{"name":"a","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}
]}`
	pods, err := ParsePods(js)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Pod{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	web := byName["web"]
	if web.Labels["app"] != "web" || len(web.Containers) != 3 {
		t.Fatalf("web: %+v", web)
	}
	c := web.Containers
	if !c[0].Init || c[0].Name != "migrate" || c[0].ID != "aaa" || c[0].State != "terminated" || c[0].Reason != "Completed" {
		t.Fatalf("init container: %+v", c[0])
	}
	if c[1].ID != "bbb" || c[1].State != "running" || !c[1].Ready {
		t.Fatalf("nginx: %+v", c[1])
	}
	// 重啟中：沿用上一個結束的容器 ID（logs 看得到原因）
	if c[2].ID != "ccc" || c[2].State != "waiting" || c[2].Reason != "CrashLoopBackOff" || c[2].Restarts != 3 {
		t.Fatalf("sidecar: %+v", c[2])
	}
	if web.Status != "CrashLoopBackOff" {
		t.Fatalf("web status %q", web.Status)
	}
	if s := byName["init-wait"].Status; s != "Init:1/2" {
		t.Fatalf("init-wait status %q", s)
	}
	if s := byName["init-crash"].Status; s != "Init:CrashLoopBackOff" {
		t.Fatalf("init-crash status %q", s)
	}
}

func TestParseServicesURLsAndNodePorts(t *testing.T) {
	js := `{"items":[
 {"metadata":{"namespace":"default","name":"web","creationTimestamp":"2026-11-25T07:52:00Z"},
  "spec":{"type":"LoadBalancer","selector":{"app":"web"},"clusterIP":"10.43.12.7","ports":[{"port":8080,"targetPort":80,"nodePort":31080,"protocol":"TCP"},{"port":53,"targetPort":"dns","nodePort":31053,"protocol":"UDP"}]},
  "status":{"loadBalancer":{"ingress":[{"ip":"172.20.3.68"}]}}},
 {"metadata":{"namespace":"default","name":"pending-lb"},
  "spec":{"type":"LoadBalancer","clusterIP":"10.43.1.1","ports":[{"port":80,"nodePort":30080,"protocol":"TCP"}]},"status":{}},
 {"metadata":{"namespace":"default","name":"np"},"spec":{"type":"NodePort","clusterIP":"10.43.1.2","ports":[{"port":80,"nodePort":30081}]}},
 {"metadata":{"namespace":"default","name":"headless"},"spec":{"clusterIP":"None","ports":[{"port":80}]}},
 {"metadata":{"namespace":"default","name":"ext"},"spec":{"type":"ExternalName","externalName":"example.com"}}
]}`
	svcs, err := ParseServices(js)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Service{}
	for _, s := range svcs {
		by[s.Name] = s
	}
	if u := by["web"].URLs; len(u) != 1 || u[0] != "http://localhost:8080" {
		t.Fatalf("web urls: %v", u)
	}
	if sel := by["web"].Selector; len(sel) != 1 || sel["app"] != "web" || by["np"].Selector != nil {
		t.Fatalf("selector: %v / %v", sel, by["np"].Selector)
	}
	if p := by["web"].Ports; p[0].TargetPort != "80" || p[1].TargetPort != "dns" {
		t.Fatalf("target ports: %+v", p)
	}
	if e := by["web"].External; len(e) != 1 || e[0] != "172.20.3.68" {
		t.Fatalf("external: %v", e)
	}
	// LoadBalancer 尚未拿到位址：用 nodePort
	if u := by["pending-lb"].URLs; len(u) != 1 || u[0] != "http://localhost:30080" {
		t.Fatalf("pending-lb urls: %v", u)
	}
	if u := by["np"].URLs; len(u) != 1 || u[0] != "http://localhost:30081" || by["np"].Ports[0].Protocol != "TCP" {
		t.Fatalf("np: %+v", by["np"])
	}
	if by["headless"].Type != "ClusterIP" || by["headless"].ClusterIP != "None" || len(by["headless"].URLs) != 0 {
		t.Fatalf("headless: %+v", by["headless"])
	}
	if e := by["ext"].External; len(e) != 1 || e[0] != "example.com" {
		t.Fatalf("ext: %+v", by["ext"])
	}
	np := NodePorts(svcs)
	if len(np) != 3 || np[31080] != "default/web" || np[30081] != "default/np" || np[31053] != "" {
		t.Fatalf("nodeports: %v", np)
	}
}

func TestParseDockerStatsSumsPodContainers(t *testing.T) {
	out := `{"Name":"k8s_nginx_web-1_default_uid1_0","CPUPerc":"1.25%","MemUsage":"10MiB / 7.6GiB"}
{"Name":"k8s_sidecar_web-1_default_uid1_2","CPUPerc":"0.75%","MemUsage":"6MiB / 7.6GiB"}
{"Name":"k8s_POD_web-1_default_uid1_0","CPUPerc":"0.00%","MemUsage":"500KiB / 7.6GiB"}
{"Name":"k8s_coredns_coredns-x_kube-system_uid2_0","CPUPerc":"0.30%","MemUsage":"18.5MB / 7.6GB"}
{"Name":"my-app","CPUPerc":"50%","MemUsage":"1GiB / 7.6GiB"}
not json`
	st := ParseDockerStats(out)
	if len(st) != 2 {
		t.Fatalf("stats: %+v", st)
	}
	if st[0].Namespace != "default" || st[0].Name != "web-1" || st[0].CPU != 2 || st[0].MemBytes != 16<<20 {
		t.Fatalf("web: %+v", st[0])
	}
	if st[1].Namespace != "kube-system" || st[1].MemBytes != 18_500_000 {
		t.Fatalf("coredns: %+v", st[1])
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"512B": 512, "1.5KiB": 1536, "2MiB": 2 << 20, "1GB": 1e9, "3kB": 3000, "": 0, "abc": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseForwardTargetAndPorts(t *testing.T) {
	cases := map[string]string{
		"svc/web":             "default svc web",
		"default/service/web": "default service→svc web",
		"kube-system/pod/x":   "kube-system pod x",
		"po/x":                "default pod x",
	}
	for in := range cases {
		ns, kind, name, err := ParseForwardTarget(in)
		if err != nil || ns == "" || (kind != "svc" && kind != "pod") || name == "" {
			t.Errorf("%s → %s %s %s %v", in, ns, kind, name, err)
		}
	}
	for _, bad := range []string{"web", "a/b/c/d", "default/deploy/web"} {
		if _, _, _, err := ParseForwardTarget(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if l, r, err := ParsePortPair("8080:80"); err != nil || l != 8080 || r != 80 {
		t.Fatalf("pair: %d %d %v", l, r, err)
	}
	if l, r, err := ParsePortPair("9000"); err != nil || l != 9000 || r != 9000 {
		t.Fatalf("single: %d %d %v", l, r, err)
	}
	for _, bad := range []string{"0", "70000", "a:b", "80:"} {
		if _, _, err := ParsePortPair(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestForwardRunsSocatOnTheEngineAgainstTheClusterIP(t *testing.T) {
	f := newFake().when("get service -n default web", `{"spec":{"clusterIP":"10.43.12.7"}}`, 0)
	f.distros["k8s-engine"] = "Running"
	m := mgr(f)
	fw, err := m.Forward(context.Background(), ForwardRequest{Namespace: "default", Kind: "svc", Name: "web", LocalPort: 18080, RemotePort: 80})
	if err != nil {
		t.Fatal(err)
	}
	if fw.URL != "http://localhost:18080" || !fw.Running {
		t.Fatalf("forward: %+v", fw)
	}
	run := f.index("docker run -d --name wsld-k8s-fwd-18080")
	if run < 0 || f.index("docker rm -f wsld-k8s-fwd-18080") > run {
		t.Fatalf("calls: %v", f.calls)
	}
	line := f.calls[run]
	for _, want := range []string{"-d docker-engine", "--network host", "--label wsld.k8s.forward=default/svc/web:80", ForwardImage, "TCP-LISTEN:18080,bind=127.0.0.1,fork,reuseaddr", "TCP:10.43.12.7:80"} {
		if !strings.Contains(line, want) {
			t.Fatalf("docker run missing %q: %s", want, line)
		}
	}
	// headless service：沒有可轉的 IP
	f.when("get service -n default headless", `{"spec":{"clusterIP":"None"}}`, 0)
	if _, err := m.Forward(context.Background(), ForwardRequest{Namespace: "default", Kind: "svc", Name: "headless", LocalPort: 18081}); err == nil {
		t.Fatal("headless service forwarded")
	}
	if _, err := m.Forward(context.Background(), ForwardRequest{Namespace: "default", Kind: "svc", Name: "bad name;rm", LocalPort: 1}); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestParseForwards(t *testing.T) {
	out := `{"Labels":"wsld.k8s.forward=default/svc/web:80,wsld.k8s.forward.local=18080","State":"running"}
{"Labels":"wsld.k8s.forward.local=9000,wsld.k8s.forward=kube-system/pod/coredns-x:53","State":"exited"}
{"Labels":"other=1","State":"running"}`
	fws := ParseForwards(out)
	if len(fws) != 2 {
		t.Fatalf("forwards: %+v", fws)
	}
	if fws[0].LocalPort != 9000 || fws[0].Kind != "pod" || fws[0].Running || fws[0].RemotePort != 53 {
		t.Fatalf("first: %+v", fws[0])
	}
	if fws[1].LocalPort != 18080 || fws[1].Name != "web" || !fws[1].Running || fws[1].URL != "http://localhost:18080" {
		t.Fatalf("second: %+v", fws[1])
	}
}

func TestRestartAndResetSequence(t *testing.T) {
	f := newFake().when("get nodes", readyNodes, 0).when("cat /etc/rancher/k3s/k3s.yaml", k3sYAML, 0).when("is-active docker", "active", 0)
	f.distros["k8s-engine"] = "Running"
	m := mgr(f)
	var steps []string
	m.Progress = func(s string) { steps = append(steps, s) }
	if _, err := m.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop, wipe, start := f.index("systemctl stop k3s"), f.index("wsld-k8s.sh reset"), f.index("systemctl start k3s")
	if stop < 0 || wipe < stop || start < wipe {
		t.Fatalf("order stop=%d wipe=%d start=%d: %v", stop, wipe, start, f.calls)
	}
	if strings.Join(steps, ",") != strings.Join(ResetSteps, ",") {
		t.Fatalf("steps %v", steps)
	}
	// Stop 也要清掉轉發容器
	if f.called("label=wsld.k8s.forward") == 0 {
		t.Fatalf("forward containers not removed on stop: %v", f.calls)
	}
}

func TestStartRewritesK3sConfigWithExtras(t *testing.T) {
	f := newFake().when("get nodes", readyNodes, 0).when("cat /etc/rancher/k3s/k3s.yaml", k3sYAML, 0).when("is-active docker", "active", 0)
	f.distros["k8s-engine"] = "Running"
	m := mgr(f)
	m.TLSSAN = []string{"my-pc.lan", "192.168.1.20"}
	m.KubeletArgs = []string{"max-pods=50"}
	if _, err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfgIdx := f.index("wsld-k8s.sh configure k8s-engine 26443 --tls-san=my-pc.lan --tls-san=192.168.1.20 --kubelet-arg=max-pods=50")
	if cfgIdx < 0 || cfgIdx > f.index("systemctl start k3s") {
		t.Fatalf("configure before start: %v", f.calls)
	}
}

func TestMergeKubeconfigKeepsOtherEntries(t *testing.T) {
	ours := RenameKubeconfig([]byte(k3sYAML), ContextName)
	existing := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://prod.example.com
  name: prod
- cluster:
    server: https://old
  name: wsld
contexts:
- context:
    cluster: prod
    user: prod
  name: prod
current-context: prod
users:
- name: prod
  user:
    token: secret
preferences: {}
`)
	out, err := MergeKubeconfig(existing, ours, false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "https://prod.example.com") || !strings.Contains(s, "token: secret") || strings.Contains(s, "https://old") {
		t.Fatalf("merge:\n%s", s)
	}
	if strings.Count(s, "name: wsld") != 3 || CurrentContext(out) != "prod" {
		t.Fatalf("wsld entries / current-context:\n%s", s)
	}
	out, err = MergeKubeconfig(out, ours, true)
	if err != nil || CurrentContext(out) != ContextName || strings.Count(string(out), "name: wsld") != 3 {
		t.Fatalf("set current: %v\n%s", err, out)
	}
	// 沒有既有檔案：產生新的，並設為 current-context
	out, err = MergeKubeconfig(nil, ours, false)
	if err != nil || CurrentContext(out) != ContextName || !strings.Contains(string(out), "kind: Config") {
		t.Fatalf("fresh: %v\n%s", err, out)
	}
	if _, err := MergeKubeconfig(existing, []byte("apiVersion: v1\n"), true); err == nil {
		t.Fatal("kubeconfig without wsld entries accepted")
	}
}
