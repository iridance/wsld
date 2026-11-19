package lan

import (
	"errors"
	"strings"
)

// ErrUTF16Config 表示 .wslconfig 以 UTF-16 儲存（記事本「Unicode」），wsld 不改寫以免毀損。
var ErrUTF16Config = errors.New(".wslconfig is UTF-16 encoded; save it as UTF-8 first")

// iniLine 是 .wslconfig 的一列解析結果。
type iniLine struct {
	section string // 此列所屬段落（小寫）；段落標題列本身為新段落名
	header  bool
	key     string // 小寫；非 key=value 列為空
	value   string
}

func parseLine(raw, section string) iniLine {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
		return iniLine{section: section}
	}
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		return iniLine{section: strings.ToLower(strings.TrimSpace(strings.Trim(t, "[]"))), header: true}
	}
	k, v, ok := strings.Cut(t, "=")
	if !ok {
		return iniLine{section: section}
	}
	return iniLine{section: section, key: strings.ToLower(strings.TrimSpace(k)), value: strings.TrimSpace(v)}
}

func splitLines(content string) (lines []string, eol string) {
	eol = "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	content = strings.TrimPrefix(content, "\ufeff")
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	norm = strings.TrimSuffix(norm, "\n")
	if norm == "" {
		return nil, eol
	}
	return strings.Split(norm, "\n"), eol
}

func joinLines(lines []string, eol, orig string) string {
	bom := ""
	if strings.HasPrefix(orig, "\ufeff") {
		bom = "\ufeff"
	}
	if len(lines) == 0 {
		return bom
	}
	return bom + strings.Join(lines, eol) + eol
}

// GetKey 回傳 [section] 下 key 的值（大小寫不敏感；最後一筆為準）；找不到回 ""。
func GetKey(content, section, key string) string {
	section, key = strings.ToLower(section), strings.ToLower(key)
	lines, _ := splitLines(content)
	cur, val := "", ""
	for _, raw := range lines {
		l := parseLine(raw, cur)
		cur = l.section
		if !l.header && l.section == section && l.key == key {
			val = l.value
		}
	}
	return val
}

// SetKey 在 [section] 設定 key=value：已存在則就地改寫（重複者只留第一筆），
// 不存在則接在該段最後一個非空列之後；沒有該段則附加新段。保留換行風格、BOM、註解。
func SetKey(content, section, key, value string) string {
	lsec, lkey := strings.ToLower(section), strings.ToLower(key)
	lines, eol := splitLines(content)
	out := make([]string, 0, len(lines)+3)
	cur := ""
	replaced := false
	inserted := false
	lastInSection := -1 // out 中該段最後一個非空列的索引
	for _, raw := range lines {
		l := parseLine(raw, cur)
		if l.header && cur == lsec && l.section != lsec && !replaced && !inserted && lastInSection >= 0 {
			out = insertAt(out, lastInSection+1, key+"="+value)
			inserted = true
		}
		cur = l.section
		if !l.header && l.section == lsec && l.key == lkey {
			if replaced {
				continue
			}
			out = append(out, key+"="+value)
			replaced = true
			lastInSection = len(out) - 1
			continue
		}
		out = append(out, raw)
		if l.section == lsec && strings.TrimSpace(raw) != "" {
			lastInSection = len(out) - 1
		}
	}
	if replaced || inserted {
		return joinLines(out, eol, content)
	}
	if lastInSection >= 0 && cur == lsec {
		out = insertAt(out, lastInSection+1, key+"="+value)
		return joinLines(out, eol, content)
	}
	if lastInSection < 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "["+section+"]", key+"="+value)
	}
	return joinLines(out, eol, content)
}

// RemoveKey 移除 [section] 下所有 key=value 列；其他內容原樣保留。
func RemoveKey(content, section, key string) string {
	section, key = strings.ToLower(section), strings.ToLower(key)
	lines, eol := splitLines(content)
	out := make([]string, 0, len(lines))
	cur := ""
	for _, raw := range lines {
		l := parseLine(raw, cur)
		cur = l.section
		if !l.header && l.section == section && l.key == key {
			continue
		}
		out = append(out, raw)
	}
	return joinLines(out, eol, content)
}

func insertAt(s []string, i int, v string) []string {
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// isUTF16 偵測 UTF-16 BOM。
func isUTF16(b []byte) bool {
	return len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF)
}
