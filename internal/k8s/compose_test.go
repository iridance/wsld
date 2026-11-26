package k8s

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

const shopCompose = `
name: Shop_App
services:
  web:
    image: shop/web:${WEB_TAG:-1.8}
    ports: ["3000:80"]
    depends_on: [api]
  api:
    image: shop/api:2.4
    build: ./api
    ports:
      - "8080"
      - "9090:9090"
    environment:
      DB_PASSWORD: ${DB_PASSWORD}
      LOG: info
      EMPTY:
    env_file: [app.env]
    depends_on:
      db:
        condition: service_healthy
    deploy:
      replicas: 2
    command: ["serve", "--port", "8080"]
    healthcheck:
      test: ["CMD", "true"]
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=${DB_PASSWORD}
      - POSTGRES_DB=shop
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./init:/docker-entrypoint-initdb.d
    entrypoint: docker-entrypoint.sh "postgres -c" max_connections=50
  worker:
    build: ./worker
volumes:
  pgdata:
`

func memRead(files map[string]string) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if v, ok := files[rel]; ok {
			return []byte(v), nil
		}
		return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
	}
}

func TestParseComposeShop(t *testing.T) {
	res, err := ParseCompose([]byte(shopCompose), ComposeOptions{ReadFile: memRead(map[string]string{
		".env":    "DB_PASSWORD=s3cret # comment\nWEB_TAG='2.0'\n",
		"app.env": "FROM_FILE=yes\nLOG=overridden\n",
	})})
	if err != nil {
		t.Fatal(err)
	}
	sp := res.Spec
	if sp.Name != "shop-app" || len(sp.Services) != 3 {
		t.Fatalf("spec = %+v", sp)
	}
	by := map[string]StackService{}
	for _, s := range sp.Services {
		by[s.Name] = s
	}
	if w := by["web"]; w.Image != "shop/web:2.0" || w.Port != 80 || w.Expose != ExposeLoadBalancer || w.HostPort != 3000 || w.DependsOn[0] != "api" {
		t.Fatalf("web = %+v", w)
	}
	a := by["api"]
	if a.Port != 8080 || a.Expose != "" || a.Replicas != 2 || a.DependsOn[0] != "db" || strings.Join(a.Args, "|") != "serve|--port|8080" {
		t.Fatalf("api = %+v", a)
	}
	envs := map[string]string{}
	for _, e := range a.Env {
		envs[e.Name] = e.Value
	}
	if envs["DB_PASSWORD"] != "s3cret" || envs["FROM_FILE"] != "yes" || envs["LOG"] != "info" {
		t.Fatalf("api env = %v (environment must win over env_file)", envs)
	}
	if _, has := envs["EMPTY"]; has {
		t.Fatal("valueless env without a .env value must be skipped")
	}
	d := by["db"]
	if len(d.Volumes) != 1 || d.Volumes[0].Name != "pgdata" || d.Volumes[0].MountPath != "/var/lib/postgresql/data" || len(d.Env) != 2 {
		t.Fatalf("db = %+v", d)
	}
	if strings.Join(d.Command, "|") != "docker-entrypoint.sh|postgres -c|max_connections=50" {
		t.Fatalf("db command = %q", d.Command)
	}
	if _, err := sp.Normalize(); err != nil {
		t.Fatalf("converted spec must normalize: %v", err)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"api: build: ignored", "api: only the first port is used", "api: healthcheck is not supported", "api: env EMPTY has no value", "bind mount ./init", "worker: has build: but no image"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning %q in:\n%s", want, joined)
		}
	}
	if strings.Join(res.ReadFiles, ",") != ".env,app.env" {
		t.Fatalf("read files = %v", res.ReadFiles)
	}
}

func TestParseComposeInterpolationAndMissingVars(t *testing.T) {
	res, err := ParseCompose([]byte("services:\n  a:\n    image: x:${TAG}\n    environment:\n      P: $$HOME\n      Q: ${NOPE:-dflt}\n"), ComposeOptions{Name: "My Stack"})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Spec.Services[0]
	if a.Image != "x:" || res.Spec.Name != "my-stack" {
		t.Fatalf("a = %+v name=%s", a, res.Spec.Name)
	}
	if a.Env[0].Value != "$HOME" || a.Env[1].Value != "dflt" {
		t.Fatalf("env = %+v", a.Env)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "TAG is not set") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestParseComposeErrorsAndPorts(t *testing.T) {
	if _, err := ParseCompose([]byte("services: {}"), ComposeOptions{}); err == nil {
		t.Fatal("no services must fail")
	}
	if _, err := ParseCompose([]byte("services:\n  a:\n    build: .\n"), ComposeOptions{}); err == nil {
		t.Fatal("only build-only services must fail")
	}
	if _, err := ParseCompose([]byte("- not a mapping"), ComposeOptions{}); err == nil {
		t.Fatal("non-mapping must fail")
	}
	for _, tc := range []struct {
		in   any
		want [2]int
	}{
		{"8080:80", [2]int{80, 8080}}, {"127.0.0.1:8081:80", [2]int{80, 8081}}, {"80", [2]int{80, 0}}, {3000, [2]int{3000, 0}},
		{map[string]any{"target": 80, "published": 8082}, [2]int{80, 8082}},
	} {
		c, h, ok := parseComposePort(tc.in)
		if !ok || c != tc.want[0] || h != tc.want[1] {
			t.Errorf("%v → %d %d %v", tc.in, c, h, ok)
		}
	}
	for _, bad := range []any{"53:53/udp", "8000-8010:80", "x"} {
		if _, _, ok := parseComposePort(bad); ok {
			t.Errorf("%v must be rejected", bad)
		}
	}
}

func TestIsComposeFile(t *testing.T) {
	if !IsComposeFile([]byte("services:\n  a:\n    image: x\n")) {
		t.Fatal("compose")
	}
	if IsComposeFile([]byte("apiVersion: v1\nkind: Service\n")) {
		t.Fatal("manifest")
	}
}

func TestResolveBind(t *testing.T) {
	cases := []struct {
		kind, source, engine, dir, src string
		want, errPart                  string
	}{
		{"wsl", "docker-engine", "docker-engine", "/home/a/shop", "./osrm/data", "/home/a/shop/osrm/data", ""},
		{"wsl", "U", "docker-engine", "/home/a/shop", "./osrm/data", "/mnt/wsl/distro/U/home/a/shop/osrm/data", ""},
		{"wsl", "U", "docker-engine", "/home/a/shop", "../shared", "/mnt/wsl/distro/U/home/a/shared", ""},
		{"wsl", "U", "docker-engine", "/home/a/shop", ".", "/mnt/wsl/distro/U/home/a/shop", ""},
		{"wsl", "U", "docker-engine", "/home/a/shop", "/data/x", "/mnt/wsl/distro/U/data/x", ""},
		{"wsl", "U", "docker-engine", "/mnt/c/proj", "./x", "/mnt/c/proj/x", ""},
		{"wsl", "U", "", "/home/a/shop", "./x", "/home/a/shop/x", ""},
		{"windows", "C:\\", "docker-engine", `C:\Users\a\shop`, "./data", "/mnt/c/Users/a/shop/data", ""},
		{"windows", "C:\\", "docker-engine", `C:\Users\a\shop`, `D:\media`, "/mnt/d/media", ""},
		{"wsl", "U", "docker-engine", "/home/a/shop", "~/x", "", "~ is not supported"},
		{"wsl", "U", "docker-engine", "/home/a/shop", `C:\x`, "", "only be used from a compose file on Windows"},
		{"windows", "C:\\", "docker-engine", `C:\p`, "/etc/x", "", "can't be mapped"},
	}
	for _, c := range cases {
		got, err := ResolveBind(c.kind, c.source, c.engine, c.dir, c.src)
		if c.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), c.errPart) {
				t.Errorf("%+v: err = %v", c, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q err %v", c, got, err)
		}
	}
}

func TestComposeBindMountAndWorkingDir(t *testing.T) {
	src := "services:\n  fe:\n    image: node:22\n    working_dir: /app\n    volumes:\n      - ./frontend:/app\n      - ./dev/oidc:/etc/oidc:ro\n      - C:\\x:/win\n      - cache:/cache\nvolumes:\n  cache:\n"
	res, err := ParseCompose([]byte(src), ComposeOptions{Name: "p", ResolveBind: func(s string) (string, error) {
		if strings.HasPrefix(s, "C:") {
			return "", fmt.Errorf("nope")
		}
		return "/h/p/" + strings.TrimPrefix(s, "./"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	fe := res.Spec.Services[0]
	if fe.WorkingDir != "/app" || len(fe.Volumes) != 3 {
		t.Fatalf("fe = %+v", fe)
	}
	if v := fe.Volumes[0]; v.HostPath != "/h/p/frontend" || v.MountPath != "/app" || v.ReadOnly {
		t.Fatalf("bind = %+v", v)
	}
	if v := fe.Volumes[1]; v.HostPath != "/h/p/dev/oidc" || !v.ReadOnly {
		t.Fatalf("ro bind = %+v", v)
	}
	if fe.Volumes[2].HostPath != "" || fe.Volumes[2].Name != "cache" {
		t.Fatalf("named = %+v", fe.Volumes[2])
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "C:\\x: nope") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	sp, err := res.Spec.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := StackManifest(sp, nil)
	js := string(raw)
	for _, frag := range []string{`"hostPath":{"path":"/h/p/frontend"}`, `"readOnly":true`, `"workingDir":"/app"`} {
		if !strings.Contains(js, frag) {
			t.Errorf("manifest missing %s", frag)
		}
	}
	if strings.Count(js, `"kind":"PersistentVolumeClaim"`) != 1 {
		t.Errorf("only the named volume gets a PVC: %s", js)
	}
}

func TestExposedDistros(t *testing.T) {
	s := StackSpec{Services: []StackService{
		{Volumes: []StackVolume{{HostPath: "/mnt/wsl/distro/U/home/a"}, {HostPath: "/mnt/c/x"}}},
		{Volumes: []StackVolume{{HostPath: "/mnt/wsl/distro/U/data"}, {HostPath: "/mnt/wsl/distro/Arch/srv"}, {Name: "db"}}},
	}}
	if got := ExposedDistros(s); strings.Join(got, ",") != "Arch,U" {
		t.Fatalf("got %v", got)
	}
}
