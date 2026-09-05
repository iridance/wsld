// Package i18n 提供最小訊息表：跟隨 Windows 語系，中文→繁中，其餘英文；缺譯用英文（SPEC §2）。
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Lang 為支援語系。
type Lang string

const (
	EN   Lang = "en"
	ZhTW Lang = "zh-TW"
)

// EnvOverride 可強制語系（en／zh-TW）。
const EnvOverride = "WSLD_LANG"

var current atomic.Value

func init() { current.Store(EN) }

// Set 設定目前語系。
func Set(l Lang) { current.Store(l) }

// Current 回傳目前語系。
func Current() Lang { return current.Load().(Lang) }

// Parse 解析使用者輸入的語系代碼（不分大小寫；zh* 視為 zh-TW）。未知或空字串回傳 ok=false。
func Parse(s string) (Lang, bool) {
	switch v := strings.ToLower(strings.TrimSpace(s)); {
	case v == "en":
		return EN, true
	case strings.HasPrefix(v, "zh"):
		return ZhTW, true
	default:
		return "", false
	}
}

// DetectFrom 由 Windows LANGID（0 表示不可用）與環境變數決定語系。
func DetectFrom(langID uint16, getenv func(string) string) Lang {
	if v := strings.ToLower(getenv(EnvOverride)); v != "" {
		if strings.HasPrefix(v, "zh") {
			return ZhTW
		}
		return EN
	}
	if langID != 0 {
		if langID&0x3FF == 0x04 { // primary language: Chinese
			return ZhTW
		}
		return EN
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.ToLower(getenv(k)); v != "" {
			if strings.HasPrefix(v, "zh") {
				return ZhTW
			}
			return EN
		}
	}
	return EN
}

// T 取得目前語系訊息並以 fmt.Sprintf 格式化；缺譯回英文，再缺回 key。
func T(key string, args ...any) string {
	m, ok := messages[key]
	if !ok {
		return key
	}
	s := m[Current()]
	if s == "" {
		s = m[EN]
	}
	if s == "" {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// All 回傳某語系的完整訊息表（缺譯以英文補），供 GUI 前端使用。
func All(l Lang) map[string]string {
	out := make(map[string]string, len(messages))
	for k, m := range messages {
		s := m[l]
		if s == "" {
			s = m[EN]
		}
		out[k] = s
	}
	return out
}
