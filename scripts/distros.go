package scripts

import "strings"

// DistroRow 為 lib/distros.tsv 的一列：WSL 線上發行版的分類。
type DistroRow struct {
	OSID           string   // /etc/os-release 的 ID
	Family         string   // debian | arch | suse | rhel | azure
	DockerRepo     string   // download.docker.com/linux/<repo>；非 apt 系為 "-"
	DockerCodename string   // apt codename；"-" 表示沿用 os-release
	WSLNames       []string // `wsl --install <name>` 的名稱
}

// Supported 回報 bootstrap 是否支援此發行版系列。
func (r DistroRow) Supported() bool {
	switch r.Family {
	case "debian", "arch", "suse":
		return true
	}
	return false
}

// DistroRows 解析內嵌的 lib/distros.tsv。
func DistroRows() []DistroRow {
	var rows []DistroRow
	for _, line := range strings.Split(distrosTable, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		rows = append(rows, DistroRow{
			OSID:           f[0],
			Family:         f[1],
			DockerRepo:     f[2],
			DockerCodename: f[3],
			WSLNames:       strings.Split(f[4], ","),
		})
	}
	return rows
}

// DistroForWSLName 以 `wsl --list --online` 的名稱查分類（不分大小寫）。
func DistroForWSLName(name string) (DistroRow, bool) {
	for _, r := range DistroRows() {
		for _, n := range r.WSLNames {
			if strings.EqualFold(n, name) {
				return r, true
			}
		}
	}
	return DistroRow{}, false
}
