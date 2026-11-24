package k8s

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeOptions 是 ParseCompose 的選項。
type ComposeOptions struct {
	// Name 是 stack 名稱；空則取 Compose 檔的 name:，再沒有就是 "stack"。
	Name string
	// ReadFile 讀取與 Compose 檔同資料夾的檔案（.env、env_file）；參數是相對於該資料夾的路徑。
	// 找不到時應回傳包裝 fs.ErrNotExist 的錯誤。nil 表示不讀任何檔案。
	ReadFile func(rel string) ([]byte, error)
	// ResolveBind 把 bind mount 的來源（相對於 Compose 檔或絕對路徑）轉成 k3s 所在機器上的絕對路徑；
	// 不可達（例如在另一台 WSL 機器）時回傳錯誤，該 volume 會被略過並警告。nil 表示不支援 bind mount。
	ResolveBind func(src string) (string, error)
}

// ComposeResult 是轉換結果：stack、無法轉換的警告、實際讀取到的檔案。
type ComposeResult struct {
	Spec      StackSpec `json:"spec"`
	Warnings  []string  `json:"warnings"`
	ReadFiles []string  `json:"read_files"`
}

// composeWarnKeys 是會影響執行結果、但這版無法轉換的服務欄位；出現時警告。
var composeWarnKeys = []string{
	"healthcheck", "user", "secrets", "configs", "extends", "profiles", "privileged",
	"cap_add", "cap_drop", "devices", "volumes_from", "links", "external_links", "network_mode",
	"mem_limit", "cpus", "ulimits", "sysctls", "security_opt", "tmpfs",
}

// IsComposeFile 粗略判斷內容是否為 Compose 檔（頂層有 services）而不是 k8s manifest。
func IsComposeFile(data []byte) bool {
	var top map[string]any
	if yaml.Unmarshal(data, &top) != nil {
		return false
	}
	_, hasServices := top["services"]
	_, hasKind := top["kind"]
	return hasServices && !hasKind
}

// ParseCompose 把 docker-compose.yml 轉成 StackSpec。無法轉換的欄位以警告回報，不中斷。
func ParseCompose(data []byte, opt ComposeOptions) (ComposeResult, error) {
	var res ComposeResult
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return res, fmt.Errorf("invalid compose file: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return res, errors.New("invalid compose file: expected a mapping at the top level")
	}
	warn := func(f string, a ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(f, a...)) }

	// .env：Compose 變數插值的來源。
	env := map[string]string{}
	if opt.ReadFile != nil {
		if b, err := opt.ReadFile(".env"); err == nil {
			for k, v := range parseDotEnv(string(b)) {
				env[k] = v
			}
			res.ReadFiles = append(res.ReadFiles, ".env")
		} else if !errors.Is(err, fs.ErrNotExist) {
			warn(".env: %v", err)
		}
	}
	interp := &interpolator{env: env, warn: warn}
	interp.walk(doc.Content[0])

	top := doc.Content[0]
	var name string
	var servicesNode *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		switch top.Content[i].Value {
		case "name":
			name = top.Content[i+1].Value
		case "services":
			servicesNode = top.Content[i+1]
		}
	}
	if servicesNode == nil || servicesNode.Kind != yaml.MappingNode || len(servicesNode.Content) == 0 {
		return res, errors.New("compose file has no services")
	}
	if opt.Name != "" {
		name = opt.Name
	}
	res.Spec.Name = sanitizeName(name, "stack")

	skipped := map[string]bool{}
	byRaw := map[string]string{} // 原始服務名稱 → 轉換後名稱
	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		byRaw[servicesNode.Content[i].Value] = sanitizeName(servicesNode.Content[i].Value, "")
	}
	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		raw := servicesNode.Content[i].Value
		var cs map[string]any
		if err := servicesNode.Content[i+1].Decode(&cs); err != nil {
			return res, fmt.Errorf("service %q: %w", raw, err)
		}
		sv, ok := convertComposeService(raw, byRaw[raw], cs, env, opt, &res, warn)
		if !ok {
			skipped[byRaw[raw]] = true
			continue
		}
		res.Spec.Services = append(res.Spec.Services, sv)
	}
	if len(res.Spec.Services) == 0 {
		return res, errors.New("no service could be converted (every service is missing an image)")
	}
	// 相依到被略過的服務：移除並警告。
	for i := range res.Spec.Services {
		sv := &res.Spec.Services[i]
		keep := sv.DependsOn[:0]
		for _, d := range sv.DependsOn {
			if skipped[d] {
				warn("%s: depends on %s, which was skipped", sv.Name, d)
				continue
			}
			keep = append(keep, d)
		}
		sv.DependsOn = keep
	}
	return res, nil
}

func convertComposeService(raw, name string, cs map[string]any, env map[string]string, opt ComposeOptions, res *ComposeResult, warn func(string, ...any)) (StackService, bool) {
	sv := StackService{Name: name}
	if name == "" {
		warn("%s: name can't be used as a Kubernetes name, skipped", raw)
		return sv, false
	}
	image, _ := cs["image"].(string)
	if strings.TrimSpace(image) == "" {
		if _, hasBuild := cs["build"]; hasBuild {
			warn("%s: has build: but no image:, skipped (build the image first and set image:)", raw)
		} else {
			warn("%s: no image, skipped", raw)
		}
		return sv, false
	}
	if _, hasBuild := cs["build"]; hasBuild {
		warn("%s: build: ignored, using image %s as is", raw, image)
	}
	sv.Image = strings.TrimSpace(image)

	for _, k := range composeWarnKeys {
		if _, ok := cs[k]; ok {
			warn("%s: %s is not supported, ignored", raw, k)
		}
	}

	// ports／expose
	var ports []any
	if l, ok := cs["ports"].([]any); ok {
		ports = l
	}
	for _, p := range ports {
		container, host, ok := parseComposePort(p)
		if !ok {
			warn("%s: port %v is not supported, ignored", raw, p)
			continue
		}
		if sv.Port != 0 {
			warn("%s: only the first port is used, ignored %v", raw, p)
			continue
		}
		sv.Port = container
		if host > 0 {
			sv.Expose, sv.HostPort = ExposeLoadBalancer, host
		}
	}
	if sv.Port == 0 {
		if l, ok := cs["expose"].([]any); ok && len(l) > 0 {
			if n, err := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(l[0]), "/tcp")); err == nil && n > 0 {
				sv.Port = n
			}
		}
	}

	// environment（env_file 在下，environment 優先）
	vars := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}
	for _, f := range stringList(cs["env_file"], "path") {
		if opt.ReadFile == nil {
			warn("%s: env_file %s was not read", raw, f)
			continue
		}
		b, err := opt.ReadFile(f)
		if err != nil {
			warn("%s: env_file %s: %v", raw, f, err)
			continue
		}
		res.ReadFiles = append(res.ReadFiles, f)
		m := parseDotEnv(string(b))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			set(k, m[k])
		}
	}
	switch e := cs["environment"].(type) {
	case map[string]any:
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e[k] == nil {
				if v, ok := env[k]; ok {
					set(k, v)
				} else {
					warn("%s: env %s has no value, skipped", raw, k)
				}
				continue
			}
			set(k, fmt.Sprint(e[k]))
		}
	case []any:
		for _, it := range e {
			kv := fmt.Sprint(it)
			if k, v, ok := strings.Cut(kv, "="); ok {
				set(k, v)
			} else if v, ok := env[kv]; ok {
				set(kv, v)
			} else {
				warn("%s: env %s has no value, skipped", raw, kv)
			}
		}
	}
	for _, k := range order {
		sv.Env = append(sv.Env, EnvVar{Name: k, Value: vars[k]})
	}

	// volumes
	used := map[string]bool{}
	if l, ok := cs["volumes"].([]any); ok {
		for _, v := range l {
			src, dst, kind, ro := parseComposeVolume(v)
			switch {
			case kind == "named" && dst != "":
				vn := sanitizeName(src, "")
				if vn == "" || used[vn] {
					warn("%s: volume %q can't be used, ignored", raw, src)
					continue
				}
				used[vn] = true
				sv.Volumes = append(sv.Volumes, StackVolume{Name: vn, MountPath: dst, ReadOnly: ro})
			case kind == "bind" && dst != "":
				if opt.ResolveBind == nil {
					warn("%s: bind mount %s is not supported here, ignored", raw, src)
					continue
				}
				hp, err := opt.ResolveBind(src)
				if err != nil {
					warn("%s: bind mount %s: %v, ignored", raw, src, err)
					continue
				}
				vn := sanitizeName("bind-"+strings.Trim(strings.ReplaceAll(dst, "/", "-"), "-"), "")
				for i := 2; vn == "" || used[vn]; i++ {
					vn = sanitizeName(fmt.Sprintf("bind-%d", i), "")
				}
				used[vn] = true
				sv.Volumes = append(sv.Volumes, StackVolume{Name: vn, MountPath: dst, HostPath: hp, ReadOnly: ro})
			default:
				warn("%s: volume %v is not supported, ignored", raw, v)
			}
		}
	}
	if wd, ok := cs["working_dir"].(string); ok && wd != "" {
		sv.WorkingDir = wd
	}

	// depends_on
	switch d := cs["depends_on"].(type) {
	case []any:
		for _, x := range d {
			sv.DependsOn = append(sv.DependsOn, sanitizeName(fmt.Sprint(x), ""))
		}
	case map[string]any:
		keys := make([]string, 0, len(d))
		for k := range d {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv.DependsOn = append(sv.DependsOn, sanitizeName(k, ""))
		}
	}

	// deploy.replicas
	if dep, ok := cs["deploy"].(map[string]any); ok {
		if r, ok := dep["replicas"]; ok {
			if n, err := strconv.Atoi(fmt.Sprint(r)); err == nil && n > 0 {
				sv.Replicas = n
			} else {
				warn("%s: deploy.replicas %v is not a positive number, ignored", raw, r)
			}
		}
	}
	sv.Command = shellList(cs["entrypoint"])
	sv.Args = shellList(cs["command"])
	return sv, true
}

// parseComposePort 解析一筆 ports：回傳容器埠與 localhost 埠（0 = 不對外）。
func parseComposePort(p any) (container, host int, ok bool) {
	switch v := p.(type) {
	case int:
		return v, 0, v > 0 && v <= 65535
	case map[string]any:
		if proto := fmt.Sprint(v["protocol"]); proto == "udp" {
			return 0, 0, false
		}
		c, err := strconv.Atoi(fmt.Sprint(v["target"]))
		if err != nil || c < 1 || c > 65535 {
			return 0, 0, false
		}
		h, _ := strconv.Atoi(fmt.Sprint(v["published"]))
		return c, h, true
	case string:
		s := v
		if strings.HasSuffix(s, "/udp") {
			return 0, 0, false
		}
		s = strings.TrimSuffix(s, "/tcp")
		parts := strings.Split(s, ":")
		if len(parts) > 3 || strings.Contains(s, "-") { // 範圍不支援
			return 0, 0, false
		}
		c, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || c < 1 || c > 65535 {
			return 0, 0, false
		}
		if len(parts) == 1 {
			return c, 0, true
		}
		h, err := strconv.Atoi(parts[len(parts)-2])
		if err != nil || h < 1 || h > 65535 {
			return 0, 0, false
		}
		return c, h, true
	}
	return 0, 0, false
}

// parseComposeVolume 回傳來源、掛載路徑、種類（named | bind | other）與是否唯讀。
func parseComposeVolume(v any) (src, dst, kind string, ro bool) {
	switch x := v.(type) {
	case string:
		parts := strings.Split(x, ":")
		// Windows 磁碟機來源：C:\x:/app 或 C:/x:/app，前兩段併回去
		if len(parts) >= 3 && len(parts[0]) == 1 && (strings.HasPrefix(parts[1], `\`) || strings.HasPrefix(parts[1], "/")) {
			parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
		}
		if len(parts) < 2 {
			return x, "", "other", false // 匿名 volume
		}
		src, dst = parts[0], parts[1]
		for _, o := range parts[2:] {
			for _, f := range strings.Split(o, ",") {
				ro = ro || f == "ro"
			}
		}
		isPath := strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~") || strings.Contains(src, `\`) || (len(src) >= 2 && src[1] == ':')
		if isPath {
			return src, dst, "bind", ro
		}
		return src, dst, "named", ro
	case map[string]any:
		src, _ = x["source"].(string)
		dst, _ = x["target"].(string)
		ro, _ = x["read_only"].(bool)
		switch x["type"] {
		case "volume":
			return src, dst, "named", ro
		case "bind":
			return src, dst, "bind", ro
		}
	}
	return "", "", "other", false
}

// ResolveBind 把 bind mount 來源轉成 k3s 所在機器（owner）上的絕對路徑。
// kind／source 是 Compose 檔所在的位置（"windows" 或 "wsl"＋distro 名稱），dir 是 Compose 檔所在資料夾。
// Windows 路徑對應到 /mnt/<磁碟機>/…；在另一台 WSL 機器上的路徑無法掛載，回傳錯誤。owner 為空時不檢查。
func ResolveBind(kind, source, owner, dir, src string) (string, error) {
	if strings.HasPrefix(src, "~") {
		return "", errors.New("~ is not supported, use a path relative to the compose file")
	}
	if strings.HasPrefix(src, `\\`) || strings.HasPrefix(src, "//") {
		return "", errors.New("network paths are not supported")
	}
	slash := func(s string) string { return strings.ReplaceAll(s, `\`, "/") }
	var joined string
	switch {
	case len(src) >= 2 && src[1] == ':': // C:\x 或 C:/x
		joined = slash(src)
	case strings.HasPrefix(src, "/"):
		joined = src
	default:
		joined = pathJoin(slash(dir), slash(src))
	}
	if len(joined) >= 2 && joined[1] == ':' { // Windows 磁碟機 → /mnt/c/…
		if kind != "windows" && kind != "" {
			return "", errors.New("a Windows path can only be used from a compose file on Windows")
		}
		return "/mnt/" + strings.ToLower(joined[:1]) + pathJoin("/", joined[2:]), nil
	}
	if kind == "windows" {
		return "", errors.New("a Linux path in a compose file on Windows can't be mapped")
	}
	if owner != "" && source != "" && !strings.EqualFold(source, owner) {
		return "", fmt.Errorf("it lives in WSL machine %s, but Kubernetes runs in %s", source, owner)
	}
	return joined, nil
}

func pathJoin(a, b string) string {
	p := path.Join(a, b)
	if !strings.HasPrefix(p, "/") && !(len(p) >= 2 && p[1] == ':') {
		p = "/" + p
	}
	return p
}

var nameBad = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeName 轉成 DNS-1035 標籤（小寫、_ → -）；無法轉換時回傳 fallback。
func sanitizeName(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = nameBad.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	if s == "" {
		return fallback
	}
	return s
}

// stringList 把「字串、字串清單，或含 key 欄位的物件清單」轉成字串清單。
func stringList(v any, key string) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, it := range x {
			switch y := it.(type) {
			case string:
				out = append(out, y)
			case map[string]any:
				if p, ok := y[key].(string); ok {
					out = append(out, p)
				}
			}
		}
		return out
	}
	return nil
}

// shellList 把 command／entrypoint（字串或清單）轉成 argv。
func shellList(v any) []string {
	switch x := v.(type) {
	case string:
		return shellSplit(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			out = append(out, fmt.Sprint(it))
		}
		return out
	}
	return nil
}

// shellSplit 以空白切開字串，支援單雙引號；不展開變數或跳脫。
func shellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// parseDotEnv 解析 .env：KEY=VALUE、可有 export 前綴、單雙引號、整行與行尾 # 註解（引號外）。
func parseDotEnv(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '"' && strings.Contains(v[1:], `"`):
			v = v[1 : 1+strings.Index(v[1:], `"`)]
		case len(v) >= 2 && v[0] == '\'' && strings.Contains(v[1:], "'"):
			v = v[1 : 1+strings.Index(v[1:], "'")]
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// interpolator 套用 Compose 變數插值：$VAR、${VAR}、${VAR:-d}、${VAR-d}、${VAR:?msg}、$$。
type interpolator struct {
	env  map[string]string
	warn func(string, ...any)
	seen map[string]bool
}

var interpRe = regexp.MustCompile(`\$(\$|[A-Za-z_][A-Za-z0-9_]*|\{[^}]*\})`)

func (it *interpolator) walk(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!int" && n.Tag != "!!bool" && n.Tag != "!!float" {
		if strings.Contains(n.Value, "$") {
			n.Value = it.expand(n.Value)
			n.Tag = "!!str"
		}
		return
	}
	for _, c := range n.Content {
		it.walk(c)
	}
}

func (it *interpolator) expand(s string) string {
	return interpRe.ReplaceAllStringFunc(s, func(m string) string {
		body := m[1:]
		if body == "$" {
			return "$"
		}
		if strings.HasPrefix(body, "{") {
			body = body[1 : len(body)-1]
			name, def, op := body, "", ""
			for _, o := range []string{":-", ":?", ":+", "-", "?", "+"} {
				if i := strings.Index(body, o); i > 0 {
					name, def, op = body[:i], body[i+len(o):], o
					break
				}
			}
			v, ok := it.env[name]
			switch op {
			case ":-":
				if !ok || v == "" {
					return def
				}
			case "-":
				if !ok {
					return def
				}
			case ":?", "?":
				if !ok || (op == ":?" && v == "") {
					it.missing(name + " (" + def + ")")
					return ""
				}
			case ":+":
				if ok && v != "" {
					return def
				}
				return ""
			case "+":
				if ok {
					return def
				}
				return ""
			default:
				if !ok {
					it.missing(name)
				}
			}
			return v
		}
		v, ok := it.env[body]
		if !ok {
			it.missing(body)
		}
		return v
	})
}

func (it *interpolator) missing(name string) {
	if it.seen == nil {
		it.seen = map[string]bool{}
	}
	if it.seen[name] {
		return
	}
	it.seen[name] = true
	it.warn("variable %s is not set (add it to .env next to the compose file); using an empty value", name)
}
