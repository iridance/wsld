package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/iridance/wsld/internal/k8s"
	"github.com/iridance/wsld/internal/wsl"
)

func (s *Server) fileHost() FileHost {
	if s.Files != nil {
		return s.Files
	}
	return hostFiles{R: s.WSL}
}

// FileSourceDistro 是來源下拉的 WSL distro 一列。
type FileSourceDistro struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

// FileSources 是 GET /api/fs/sources 的回應。
type FileSources struct {
	Drives []string           `json:"drives"`
	WSL    []FileSourceDistro `json:"wsl"`
	// Owner 是目前跑 Kubernetes 的 machine；有值時 WSL 只列這一台（bind mount 與 build 都要求專案與 k3s 在同一台）。
	Owner string `json:"owner,omitempty"`
}

// k8sOwnerDistro 回傳目前跑 Kubernetes 的 machine；沒有時為空字串。
func (s *Server) k8sOwnerDistro() string {
	if m, ok := s.cfg().K8sMachine(); ok {
		return m.Distro
	}
	return ""
}

func isInternalDistro(name string) bool { return strings.HasPrefix(name, "docker-desktop") }

func (s *Server) distroRunning(ctx context.Context, name string) (known, running bool) {
	if s.WSL == nil {
		return false, false
	}
	list, err := wsl.ListDistros(ctx, s.WSL)
	if err != nil {
		return false, false
	}
	for _, d := range list {
		if d.Name == name {
			return true, d.State == "Running"
		}
	}
	return false, false
}

func (s *Server) handleFSSources(w http.ResponseWriter, r *http.Request) {
	owner := s.k8sOwnerDistro()
	out := FileSources{Drives: s.fileHost().Drives(), WSL: []FileSourceDistro{}, Owner: owner}
	if out.Drives == nil {
		out.Drives = []string{}
	}
	if s.WSL != nil {
		if list, err := wsl.ListDistros(r.Context(), s.WSL); err == nil {
			for _, d := range list {
				if !isInternalDistro(d.Name) && (owner == "" || strings.EqualFold(d.Name, owner)) {
					out.WSL = append(out.WSL, FileSourceDistro{Name: d.Name, Running: d.State == "Running"})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// openSource 取得 kind／name 指定的檔案系統；WSL distro 未執行且沒有 start=1 時回 409（不偷偷喚醒機器）。
func (s *Server) openSource(w http.ResponseWriter, r *http.Request, kind, name string, start bool) (FileSystem, bool) {
	if kind == "wsl" {
		if owner := s.k8sOwnerDistro(); owner != "" && !strings.EqualFold(owner, name) {
			writeErr(w, http.StatusBadRequest, "usage", fmt.Errorf("Import reads from the machine where Kubernetes runs (%s)", owner))
			return nil, false
		}
		known, running := s.distroRunning(r.Context(), name)
		if !known || isInternalDistro(name) {
			writeErr(w, http.StatusNotFound, "not_found", errors.New("unknown WSL distro"))
			return nil, false
		}
		if !running && !start {
			writeErr(w, http.StatusConflict, "stopped", errors.New("this machine is stopped; starting it is required to read files"))
			return nil, false
		}
	}
	fsys, err := s.fileHost().Open(kind, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return nil, false
	}
	return fsys, true
}

// FileListing 是 GET /api/fs/list 的回應。
type FileListing struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fsys, ok := s.openSource(w, r, q.Get("kind"), q.Get("name"), q.Get("start") == "1")
	if !ok {
		return
	}
	dir := q.Get("path")
	if dir == "" {
		h, err := fsys.Home(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, "error", err)
			return
		}
		dir = h
	}
	if !fsys.Valid(dir) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("path must be absolute"))
		return
	}
	ents, err := fsys.List(r.Context(), dir)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, fs.ErrNotExist) {
			code = http.StatusNotFound
		}
		writeErr(w, code, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, FileListing{Path: dir, Parent: fsys.Parent(dir), Entries: importable(ents)})
}

// composePreviewRequest 是 POST /api/k8s/compose/preview 的請求本文。
type composePreviewRequest struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Start     bool   `json:"start,omitempty"`
	StackName string `json:"stack_name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// ComposePreview 是預覽回應。Kind = compose 時帶 Spec／YAML／Order；= manifest 時帶 Manifest（交給 /api/k8s/apply）。
type ComposePreview struct {
	Kind        string         `json:"kind"`
	DefaultName string         `json:"default_name"`
	Spec        *k8s.StackSpec `json:"spec,omitempty"`
	YAML        string         `json:"yaml,omitempty"`
	Order       []string       `json:"order,omitempty"`
	Warnings    []string       `json:"warnings"`
	ReadFiles   []string       `json:"read_files"`
	Manifest    string         `json:"manifest,omitempty"`
}

func lastElem(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (s *Server) handleComposePreview(w http.ResponseWriter, r *http.Request) {
	var req composePreviewRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid JSON body"))
		return
	}
	fsys, ok := s.openSource(w, r, req.Kind, req.Name, req.Start)
	if !ok {
		return
	}
	low := strings.ToLower(req.Path)
	if !fsys.Valid(req.Path) || !(strings.HasSuffix(low, ".yml") || strings.HasSuffix(low, ".yaml")) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("choose a .yml or .yaml file"))
		return
	}
	data, err := fsys.Read(r.Context(), req.Path, MaxImportBytes)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, fs.ErrNotExist) {
			code = http.StatusNotFound
		}
		writeErr(w, code, "error", err)
		return
	}
	dir := fsys.Parent(req.Path)
	out := ComposePreview{DefaultName: lastElem(dir), Warnings: []string{}, ReadFiles: []string{}}
	if !k8s.IsComposeFile(data) {
		out.Kind, out.Manifest = "manifest", string(data)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Kind = "compose"
	name := req.StackName
	if name == "" {
		name = out.DefaultName
	}
	owner := s.k8sOwnerDistro()
	res, err := k8s.ParseCompose(data, k8s.ComposeOptions{
		Name:     name,
		ReadFile: confinedReader(r.Context(), fsys, dir),
		ResolveBind: func(src string) (string, error) {
			return k8s.ResolveBind(req.Kind, req.Name, owner, dir, src)
		},
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	res.Spec.Namespace = req.Namespace
	spec, err := res.Spec.Normalize()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	raw, err := k8s.StackManifest(spec, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, raw, "", "  ")
	order, _ := spec.Order()
	for _, sv := range order {
		out.Order = append(out.Order, sv.Name)
	}
	out.Spec, out.YAML = &spec, pretty.String()
	out.Warnings = append(out.Warnings, res.Warnings...)
	out.ReadFiles = append(out.ReadFiles, res.ReadFiles...)
	writeJSON(w, http.StatusOK, out)
}
