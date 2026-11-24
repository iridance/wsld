package gui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/iridance/wsld/internal/wsl"
)

// MaxImportBytes 是匯入（Compose、.env、env_file）單一檔案的大小上限。
const MaxImportBytes = 1 << 20

// FileEntry 是檔案瀏覽器的一列。
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// FileSystem 是 Import 對話框瀏覽與讀取檔案的唯讀介面；Windows 本機與 WSL distro 各有一個實作。
type FileSystem interface {
	Home(ctx context.Context) (string, error)
	// List 列出 dir 的直接子項（未過濾）。
	List(ctx context.Context, dir string) ([]FileEntry, error)
	// Read 讀取檔案，超過 max 位元組回傳錯誤；找不到時錯誤需包裝 fs.ErrNotExist。
	Read(ctx context.Context, file string, max int64) ([]byte, error)
	Join(dir, name string) string
	// Parent 回傳上一層；已在根目錄時回傳空字串。
	Parent(dir string) string
	// Valid 檢查路徑是否可接受（絕對路徑、無控制字元）。
	Valid(p string) bool
}

// FileHost 提供檔案來源：Windows 磁碟機與 WSL distro。
type FileHost interface {
	// Drives 回傳可用的 Windows 磁碟機（例如 "C:\"）；非 Windows 環境為空。
	Drives() []string
	// Open 取得來源：kind = "windows"（name 為磁碟機）或 "wsl"（name 為 distro）。
	Open(kind, name string) (FileSystem, error)
}

// ---- 預設實作 ----

type hostFiles struct{ R wsl.Runner }

func (h hostFiles) Drives() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func (h hostFiles) Open(kind, name string) (FileSystem, error) {
	switch kind {
	case "windows":
		return osFS{drive: name}, nil
	case "wsl":
		if h.R == nil || name == "" {
			return nil, errors.New("wsl is not available")
		}
		return wslFS{r: h.R, distro: name}, nil
	}
	return nil, fmt.Errorf("unknown source %q", kind)
}

// osFS 讀 Windows 本機（測試環境下為本機檔案系統）。
type osFS struct{ drive string }

func (f osFS) Home(context.Context) (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if f.drive != "" && !strings.EqualFold(filepath.VolumeName(h), strings.TrimSuffix(f.drive, `\`)) {
		return f.drive, nil
	}
	return h, nil
}

func (osFS) List(_ context.Context, dir string) ([]FileEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(ents))
	for _, e := range ents {
		fe := FileEntry{Name: e.Name(), Dir: e.IsDir()}
		if !fe.Dir {
			if info, err := e.Info(); err == nil {
				fe.Size = info.Size()
			}
		}
		out = append(out, fe)
	}
	return out, nil
}

func (osFS) Read(_ context.Context, file string, max int64) ([]byte, error) {
	fh, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(file), max)
	}
	return b, nil
}

func (osFS) Join(dir, name string) string { return filepath.Join(dir, name) }

func (osFS) Parent(dir string) string {
	p := filepath.Dir(dir)
	if p == dir || p == "." {
		return ""
	}
	return p
}

func (osFS) Valid(p string) bool { return filepath.IsAbs(p) && !strings.ContainsAny(p, "\x00\n") }

// wslFS 以該 distro 的預設使用者（不是 root）唯讀存取，權限邊界與使用者在 shell 內相同。
// 指令一律以 stdin 餵給 `sh -s`：wsl.exe 會把參數交給 shell 再解析，反斜線與引號會被吃掉
// （例如 find 的 -printf '%y\t%s\n' 會變成亂碼），腳本放 stdin 就不經過那一層。
type wslFS struct {
	r      wsl.Runner
	distro string
}

// shq 以單引號包住字串供 sh 使用。
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (f wslFS) script(ctx context.Context, script string) ([]byte, error) {
	out, stderr, err := wsl.RunIn(ctx, f.r, f.distro, "", strings.NewReader(script+"\n"), "sh", "-s")
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if strings.Contains(msg, "No such file") {
			return nil, fmt.Errorf("%s: %w", msg, fs.ErrNotExist)
		}
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

func (f wslFS) Home(ctx context.Context) (string, error) {
	out, err := f.script(ctx, `printf %s "$HOME"`)
	if err != nil {
		return "", err
	}
	if h := strings.TrimSpace(string(out)); strings.HasPrefix(h, "/") {
		return h, nil
	}
	return "/", nil
}

func (f wslFS) List(ctx context.Context, dir string) ([]FileEntry, error) {
	out, err := f.script(ctx, "cd "+shq(dir)+` || exit 2
find -L . -maxdepth 1 -mindepth 1 -printf '%y\t%s\t%f\n'`)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var ents []FileEntry
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		switch parts[0] {
		case "d":
			ents = append(ents, FileEntry{Name: parts[2], Dir: true})
		case "f":
			n, _ := strconv.ParseInt(parts[1], 10, 64)
			ents = append(ents, FileEntry{Name: parts[2], Size: n})
		}
	}
	return ents, nil
}

func (f wslFS) Read(ctx context.Context, file string, max int64) ([]byte, error) {
	out, err := f.script(ctx, "head -c "+strconv.FormatInt(max+1, 10)+" -- "+shq(file))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", path.Base(file), max)
	}
	return bytes.Clone(out), nil
}

func (wslFS) Join(dir, name string) string { return path.Join(dir, name) }

func (wslFS) Parent(dir string) string {
	if dir == "/" || dir == "" {
		return ""
	}
	return path.Dir(dir)
}

func (wslFS) Valid(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\x00\n")
}

// importable 過濾給瀏覽器的項目：資料夾（不含隱藏）與 .yml／.yaml。
func importable(ents []FileEntry) []FileEntry {
	out := make([]FileEntry, 0, len(ents))
	for _, e := range ents {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		low := strings.ToLower(e.Name)
		if e.Dir || strings.HasSuffix(low, ".yml") || strings.HasSuffix(low, ".yaml") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// confinedReader 回傳只能讀 dir 之內檔案的函式（給 .env、env_file）：相對路徑不得跳出 dir。
func confinedReader(ctx context.Context, fsys FileSystem, dir string) func(rel string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		clean := path.Clean(strings.ReplaceAll(rel, `\`, "/"))
		if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(rel, ":") {
			return nil, fmt.Errorf("%s: path must stay inside the compose file's folder", rel)
		}
		return fsys.Read(ctx, fsys.Join(dir, clean), MaxImportBytes)
	}
}
