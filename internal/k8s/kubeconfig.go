// Package k8s 管理每台 machine 上的單節點 k3s（安裝、啟停、狀態、kubeconfig 與資源查詢）。
// 全部經 wsl.exe 在 distro 內執行 systemctl／kubectl，不引入 client-go。
package k8s

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// DefaultAPIPort 是新安裝 k3s 的 API server 埠。刻意避開 6443（Docker Desktop／minikube／kind
// 等常用的預設埠），與 docker 的 23750 一樣走「非預設埠」。
const DefaultAPIPort = 26443

// legacyAPIPort 是 k3s 自己的預設埠：舊安裝（沒帶 --https-listen-port）仍在這個埠。
const legacyAPIPort = 6443

// APIServer 回傳匯出 kubeconfig 的 server 位址：Windows 端經 WSL localhost 轉發連到 k3s。
func APIServer(port int) string { return fmt.Sprintf("https://127.0.0.1:%d", port) }

// ContextName 回傳此 distro 在合併後 kubeconfig 中的 context／cluster／user 名稱。
func ContextName(distro string) string { return "wsld-" + distro }

// MergeKubeconfig 把 k3s 產生的 kubeconfig（k3sYAML）以 wsld-<distro> 名稱合併進 existing：
//   - 保留 existing 的其他 cluster／context／user、註解與 current-context（existing 沒有時才採用本 context）
//   - 同名項目（重複匯出）取代為新內容，不重複
//   - server 的 host 一律改為 127.0.0.1，埠沿用 k3s.yaml 裡的實際值（新安裝為自訂埠，舊安裝為 6443）
//
// existing 為空表示尚無檔案；existing 無法解析時回錯誤，呼叫端不應覆寫使用者的檔案。
func MergeKubeconfig(existing, k3sYAML []byte, distro string) ([]byte, error) {
	name := ContextName(distro)

	src, err := parseRoot(k3sYAML)
	if err != nil {
		return nil, fmt.Errorf("parse k3s kubeconfig: %w", err)
	}
	cluster := firstEntry(src, "clusters")
	user := firstEntry(src, "users")
	if cluster == nil || user == nil {
		return nil, errors.New("k3s kubeconfig has no cluster or user")
	}
	setEntryName(cluster, name)
	setEntryName(user, name)
	setClusterServer(cluster, APIServer(serverPort(cluster)))
	ctx := contextEntry(name)

	var root *yaml.Node
	if len(bytes.TrimSpace(existing)) == 0 {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapScalar(root, "apiVersion", "v1")
		setMapScalar(root, "kind", "Config")
	} else if root, err = parseRoot(existing); err != nil {
		return nil, fmt.Errorf("parse existing kubeconfig: %w", err)
	}

	upsertEntry(root, "clusters", name, cluster)
	upsertEntry(root, "contexts", name, ctx)
	upsertEntry(root, "users", name, user)
	if cur := mapValue(root, "current-context"); cur == nil || cur.Value == "" {
		setMapScalar(root, "current-context", name)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseRoot 解析成頂層 mapping node（保留註解與順序）。
func parseRoot(b []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	// 回傳 document 內的 mapping，但保留 document 層的 head comment。
	m := doc.Content[0]
	if m.HeadComment == "" {
		m.HeadComment = doc.HeadComment
	}
	return m, nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMapScalar(m *yaml.Node, key, val string) {
	if v := mapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Content = yaml.ScalarNode, "!!str", val, nil
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

func firstEntry(root *yaml.Node, section string) *yaml.Node {
	seq := mapValue(root, section)
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return nil
	}
	return seq.Content[0]
}

func setEntryName(entry *yaml.Node, name string) {
	if entry.Kind == yaml.MappingNode {
		setMapScalar(entry, "name", name)
	}
}

func setClusterServer(entry *yaml.Node, server string) {
	if c := mapValue(entry, "cluster"); c != nil && c.Kind == yaml.MappingNode {
		setMapScalar(c, "server", server)
	}
}

// serverPort 讀出 cluster 的 server 埠；讀不到或不合法時回 k3s 預設埠。
func serverPort(entry *yaml.Node) int {
	c := mapValue(entry, "cluster")
	if c == nil {
		return legacyAPIPort
	}
	v := mapValue(c, "server")
	if v == nil {
		return legacyAPIPort
	}
	u, err := url.Parse(v.Value)
	if err != nil {
		return legacyAPIPort
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return legacyAPIPort
	}
	return p
}

func contextEntry(name string) *yaml.Node {
	inner := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMapScalar(inner, "cluster", name)
	setMapScalar(inner, "user", name)
	e := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	e.Content = append(e.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "context"}, inner)
	setMapScalar(e, "name", name)
	return e
}

// upsertEntry 在 section（clusters|contexts|users）序列中以 name 取代或附加 entry。
func upsertEntry(root *yaml.Node, section, name string, entry *yaml.Node) {
	seq := mapValue(root, section)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		// 缺少或為 null（如 `clusters: null`／`[]` 以外的標量）：以新序列取代
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		replaced := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == section {
				root.Content[i+1] = seq
				replaced = true
			}
		}
		if !replaced {
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}, seq)
		}
	}
	for i, it := range seq.Content {
		if it.Kind == yaml.MappingNode {
			if n := mapValue(it, "name"); n != nil && n.Value == name {
				seq.Content[i] = entry
				return
			}
		}
	}
	seq.Content = append(seq.Content, entry)
}

// DefaultKubeconfigPath 回傳 Windows 側（或目前系統）的使用者 kubeconfig：%USERPROFILE%\.kube\config。
// 不採用 KUBECONFIG 環境變數（可能是多檔路徑清單），匯出一律寫預設位置。
func DefaultKubeconfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// HasExportedContext 回報 path 的 kubeconfig 是否已有此 distro 的 wsld context（使用者曾匯出過）。
// 檔案不存在或無法解析都回 false。
func HasExportedContext(path, distro string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var kc struct {
		Contexts []struct {
			Name string `yaml:"name"`
		} `yaml:"contexts"`
	}
	if yaml.Unmarshal(b, &kc) != nil {
		return false
	}
	for _, c := range kc.Contexts {
		if c.Name == ContextName(distro) {
			return true
		}
	}
	return false
}

// ExportKubeconfig 把 k3s kubeconfig 合併寫入 path（不存在則建立）。
// 既有檔案第一次被修改前備份為 path+".wsld-bak"（之後不覆蓋該備份）；無法解析時原檔不動並回錯誤。
func ExportKubeconfig(path string, k3sYAML []byte, distro string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	merged, err := MergeKubeconfig(existing, k3sYAML, distro)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(existing) > 0 {
		bak := path + ".wsld-bak"
		if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(bak, existing, 0o600); err != nil {
				return err
			}
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, merged, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
