package gui

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// MemFS 是記憶體中的唯讀檔案樹（測試主機與單元測試用）。Win = true 時路徑用反斜線（C:\…）。
type MemFS struct {
	Win     bool
	HomeDir string
	Files   map[string]string
	Dirs    []string // 空資料夾
}

func (m *MemFS) sep() string {
	if m.Win {
		return `\`
	}
	return "/"
}

// Home 實作 FileSystem。
func (m *MemFS) Home(context.Context) (string, error) { return m.HomeDir, nil }

// Join 實作 FileSystem。
func (m *MemFS) Join(dir, name string) string {
	return strings.TrimRight(dir, m.sep()) + m.sep() + name
}

// Parent 實作 FileSystem。
func (m *MemFS) Parent(dir string) string {
	d := strings.TrimRight(dir, m.sep())
	i := strings.LastIndex(d, m.sep())
	switch {
	case i < 0:
		return ""
	case i == 0:
		return m.sep()
	case m.Win && i == 2: // C:\foo → C:\
		return d[:3]
	}
	if d == "" || (m.Win && len(d) <= 2) {
		return ""
	}
	return d[:i]
}

// Valid 實作 FileSystem。
func (m *MemFS) Valid(p string) bool {
	if m.Win {
		return len(p) >= 3 && p[1] == ':' && p[2] == '\\'
	}
	return strings.HasPrefix(p, "/")
}

// List 實作 FileSystem。
func (m *MemFS) List(_ context.Context, dir string) ([]FileEntry, error) {
	prefix := strings.TrimRight(dir, m.sep()) + m.sep()
	seen := map[string]FileEntry{}
	for p, c := range m.Files {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			if name, _, more := strings.Cut(rest, m.sep()); more {
				seen[name] = FileEntry{Name: name, Dir: true}
			} else {
				seen[rest] = FileEntry{Name: rest, Size: int64(len(c))}
			}
		}
	}
	for _, d := range m.Dirs {
		if rest, ok := strings.CutPrefix(d, prefix); ok && rest != "" {
			name, _, _ := strings.Cut(rest, m.sep())
			seen[name] = FileEntry{Name: name, Dir: true}
		}
	}
	if len(seen) == 0 && !m.dirExists(dir) {
		return nil, fmt.Errorf("%s: %w", dir, fs.ErrNotExist)
	}
	out := make([]FileEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemFS) dirExists(dir string) bool {
	d := strings.TrimRight(dir, m.sep())
	if d == "" || (m.Win && len(d) == 2) {
		return true
	}
	for _, x := range m.Dirs {
		if strings.TrimRight(x, m.sep()) == d {
			return true
		}
	}
	return false
}

// Read 實作 FileSystem。
func (m *MemFS) Read(_ context.Context, file string, max int64) ([]byte, error) {
	c, ok := m.Files[file]
	if !ok {
		return nil, fmt.Errorf("%s: %w", file, fs.ErrNotExist)
	}
	if int64(len(c)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", file, max)
	}
	return []byte(c), nil
}

// FakeFileHost 以記憶體檔案樹提供 Windows 與各 distro 的來源。
type FakeFileHost struct {
	mu      sync.Mutex
	Windows *MemFS
	WSL     map[string]*MemFS
}

// NewFakeFileHost 建立含範例專案的來源：WSL Ubuntu-26.04 的 ~/projects/shop（Compose＋.env＋manifest）與 Windows 的 blog。
func NewFakeFileHost() *FakeFileHost {
	shop := `name: shop
services:
  web:
    image: shop/web:${WEB_TAG:-1.8}
    ports: ["3000:80"]
    depends_on: [api]
    working_dir: /app
    volumes:
      - ~/cache:/cache
  api:
    image: shop/api:2.4
    ports: ["8080"]
    environment:
      DB_PASSWORD: ${DB_PASSWORD}
    depends_on: [db]
    healthcheck:
      test: ["CMD", "true"]
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./init:/docker-entrypoint-initdb.d
volumes:
  pgdata:
`
	return &FakeFileHost{
		Windows: &MemFS{Win: true, HomeDir: `C:\Users\irice`, Files: map[string]string{
			`C:\Users\irice\projects\blog\compose.yaml`: "services:\n  blog:\n    image: blog:1\n    ports: [\"4000:80\"]\n",
		}},
		WSL: map[string]*MemFS{"Ubuntu-26.04": {HomeDir: "/home/irice", Dirs: []string{"/home/irice/projects/shop/api", "/home/irice/projects/shop/web"}, Files: map[string]string{
			"/home/irice/projects/shop/docker-compose.yml": shop,
			"/home/irice/projects/shop/.env":               "DB_PASSWORD=s3cret\n",
			"/home/irice/projects/shop/k8s-extra.yaml":     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\n",
		}}},
	}
}

// Drives 實作 FileHost。
func (f *FakeFileHost) Drives() []string { return []string{`C:\`, `D:\`} }

// Open 實作 FileHost。
func (f *FakeFileHost) Open(kind, name string) (FileSystem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case "windows":
		return f.Windows, nil
	case "wsl":
		if m, ok := f.WSL[name]; ok {
			return m, nil
		}
		return &MemFS{HomeDir: "/home/user", Dirs: []string{"/home/user"}}, nil
	}
	return nil, fmt.Errorf("unknown source %q", kind)
}
