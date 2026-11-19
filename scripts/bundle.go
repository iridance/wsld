// Package scripts 以 go:embed 內嵌 bootstrap 腳本，供 wsld init 從 Windows 端
// 以 `wsl -d <distro> -u root -- bash -s -- <args>` 餵入執行。
package scripts

import (
	_ "embed"
	"strings"
)

//go:embed 02-bootstrap-docker.sh
var bootstrapMain string

//go:embed lib/bootstrap-lib.sh
var bootstrapLib string

//go:embed lib/distros.tsv
var distrosTable string

// BootstrapBundle 把分類表、lib 與主腳本合成單一自足腳本（移除 source 行）。
func BootstrapBundle() []byte {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# --- generated bundle: lib/distros.tsv + lib/bootstrap-lib.sh + 02-bootstrap-docker.sh ---\n")
	b.WriteString("WSLD_DISTROS_TABLE='")
	b.WriteString(distrosTable)
	b.WriteString("'\n")
	b.WriteString(stripShebang(bootstrapLib))
	b.WriteString("\n# --- main ---\n")
	for _, line := range strings.Split(stripShebang(bootstrapMain), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "SCRIPT_DIR="),
			strings.HasPrefix(t, "# shellcheck source="),
			strings.HasPrefix(t, ". \"$SCRIPT_DIR/lib/bootstrap-lib.sh\""),
			strings.HasPrefix(t, "source \"$SCRIPT_DIR/lib/bootstrap-lib.sh\""):
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func stripShebang(s string) string {
	if strings.HasPrefix(s, "#!") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[i+1:]
		}
		return ""
	}
	return s
}
