package k8s

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// MergeKubeconfig 把 ours（wsld 匯出的 kubeconfig）中名為 ContextName 的 cluster／user／context 併進 existing
// （使用者的 ~/.kube/config），其他項目原樣保留。existing 為空時產生新的。
// setCurrent 為 true，或 existing 沒有 current-context 時，把 current-context 設為 ContextName。
func MergeKubeconfig(existing, ours []byte, setCurrent bool) ([]byte, error) {
	src, err := mappingOf(ours)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, errors.New("empty kubeconfig")
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := yaml.Unmarshal(existing, &doc); err != nil {
			return nil, err
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		dst := doc.Content[0]
		setScalar(dst, "apiVersion", "v1")
		setScalar(dst, "kind", "Config")
	}
	dst := doc.Content[0]
	for _, key := range []string{"clusters", "users", "contexts"} {
		item := namedItem(valueOf(src, key), ContextName)
		if item == nil {
			return nil, errors.New("kubeconfig has no " + key + " entry named " + ContextName)
		}
		seq := valueOf(dst, key)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			seq = &yaml.Node{Kind: yaml.SequenceNode}
			setValue(dst, key, seq)
		}
		replaced := false
		for i, it := range seq.Content {
			if nameOf(it) == ContextName {
				seq.Content[i] = item
				replaced = true
			}
		}
		if !replaced {
			seq.Content = append(seq.Content, item)
		}
	}
	if cur := valueOf(dst, "current-context"); setCurrent || cur == nil || cur.Value == "" {
		setScalar(dst, "current-context", ContextName)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CurrentContext 回傳 kubeconfig 的 current-context（讀不到為空）。
func CurrentContext(kc []byte) string {
	m, err := mappingOf(kc)
	if err != nil || m == nil {
		return ""
	}
	if v := valueOf(m, "current-context"); v != nil {
		return v.Value
	}
	return ""
}

// MergeKubeconfigFile 合併到 path（不存在就建立，權限 0600）。
func MergeKubeconfigFile(path string, ours []byte, setCurrent bool) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	merged, err := MergeKubeconfig(existing, ours, setCurrent)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".wsld.tmp"
	if err := os.WriteFile(tmp, merged, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UserKubeconfigPath 是使用者的 kubeconfig（$KUBECONFIG 的第一個，否則 ~/.kube/config）。
func UserKubeconfigPath() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		if first := filepath.SplitList(v); len(first) > 0 && first[0] != "" {
			return first[0]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

func mappingOf(b []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	return doc.Content[0], nil
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setValue(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func setScalar(m *yaml.Node, key, val string) {
	setValue(m, key, &yaml.Node{Kind: yaml.ScalarNode, Value: val})
}

func nameOf(item *yaml.Node) string {
	if v := valueOf(item, "name"); v != nil {
		return v.Value
	}
	return ""
}

func namedItem(seq *yaml.Node, name string) *yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, it := range seq.Content {
		if nameOf(it) == name {
			return it
		}
	}
	return nil
}
