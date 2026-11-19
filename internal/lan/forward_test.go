package lan

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/dockerapi"
)

// echoServer 在 127.0.0.1 隨機埠回覆 "pong:<line>"。
func echoServer(t *testing.T) (port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "pong:%s", line)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// freePort 找一個 127.0.0.2 上可用的埠（當作「區網位址」）。
func lanAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("127.0.0.2 not available")
	}
	_ = ln.Close()
	return "127.0.0.2"
}

func roundTrip(t *testing.T, addr string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, "ping\n")
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestForwarderForwardsAndStops(t *testing.T) {
	port := echoServer(t)
	lan := lanAddr(t)
	f := &Forwarder{}
	t.Cleanup(f.Close)
	f.Sync([]string{lan}, []Target{{Port: port, Container: "web"}})

	fw := f.Forwards()
	if len(fw) != 1 || fw[0].Error != "" || fw[0].Container != "web" || fw[0].Address != lan {
		t.Fatalf("forwards = %+v", fw)
	}
	if got := roundTrip(t, net.JoinHostPort(lan, fmt.Sprint(port))); got != "pong:ping\n" {
		t.Fatalf("got %q", got)
	}

	// 容器名稱更新不重開監聽
	f.Sync([]string{lan}, []Target{{Port: port, Container: "web-2"}})
	if fw := f.Forwards(); fw[0].Container != "web-2" {
		t.Fatalf("forwards = %+v", fw)
	}

	// 埠不再 published → 關閉監聽
	f.Sync([]string{lan}, nil)
	if len(f.Forwards()) != 0 {
		t.Fatal("expected no forwards")
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(lan, fmt.Sprint(port)), 500*time.Millisecond); err == nil {
		t.Fatal("listener must be closed")
	}
}

func TestForwarderRecordsListenErrorAndRetries(t *testing.T) {
	fails := 1
	f := &Forwarder{Listen: func(network, addr string) (net.Listener, error) {
		if fails > 0 {
			fails--
			return nil, errors.New("bind: address already in use")
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}}
	t.Cleanup(f.Close)
	f.Sync([]string{"192.168.1.20"}, []Target{{Port: 8080, Container: "web"}})
	fw := f.Forwards()
	if len(fw) != 1 || !strings.Contains(fw[0].Error, "already in use") {
		t.Fatalf("forwards = %+v", fw)
	}
	f.Sync([]string{"192.168.1.20"}, []Target{{Port: 8080, Container: "web"}})
	if fw := f.Forwards(); fw[0].Error != "" {
		t.Fatalf("retry should succeed: %+v", fw)
	}
}

func TestForwarderMultipleAddressesSorted(t *testing.T) {
	var opened []string
	f := &Forwarder{Listen: func(network, addr string) (net.Listener, error) {
		opened = append(opened, addr)
		return net.Listen("tcp", "127.0.0.1:0")
	}}
	t.Cleanup(f.Close)
	f.Sync([]string{"192.168.1.20", "10.0.0.5"}, []Target{{Port: 8080}, {Port: 3000}})
	if len(opened) != 4 {
		t.Fatalf("opened = %v", opened)
	}
	fw := f.Forwards()
	got := []string{}
	for _, x := range fw {
		got = append(got, fmt.Sprintf("%s:%d", x.Address, x.Port))
	}
	if strings.Join(got, ",") != "10.0.0.5:3000,10.0.0.5:8080,192.168.1.20:3000,192.168.1.20:8080" {
		t.Fatalf("got %v", got)
	}
	// 位址消失（換網路）→ 關掉該位址的轉發
	f.Sync([]string{"10.0.0.5"}, []Target{{Port: 8080}, {Port: 3000}})
	if len(f.Forwards()) != 2 || len(opened) != 4 {
		t.Fatalf("forwards=%+v opened=%v", f.Forwards(), opened)
	}
}

func TestPublishedTargets(t *testing.T) {
	cs := []dockerapi.Container{
		{ID: "a", Names: []string{"/web"}, State: "running", Ports: []dockerapi.Port{
			{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 5353, Type: "udp"},
			{PrivatePort: 9000, Type: "tcp"},
		}},
		{ID: "b", Names: []string{"/db"}, State: "running", Ports: []dockerapi.Port{
			{IP: "127.0.0.1", PrivatePort: 5432, PublicPort: 5432, Type: "tcp"},
			{IP: "0.0.0.0", PrivatePort: 6379, PublicPort: 6379, Type: "tcp"},
		}},
		{ID: "c", Names: []string{"/stopped"}, State: "exited", Ports: []dockerapi.Port{{IP: "0.0.0.0", PublicPort: 3000, Type: "tcp"}}},
		{ID: "d", Names: []string{"/api"}, State: "running", Ports: []dockerapi.Port{{IP: "0.0.0.0", PublicPort: 23750, Type: "tcp"}}},
	}
	got := PublishedTargets(cs, 23750)
	want := []Target{{Port: 6379, Container: "db"}, {Port: 8080, Container: "web"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
