package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/iridance/wsld/internal/i18n"
)

// PodStats 是一個 pod 的資源用量（其容器在 engine 上 `docker stats` 的加總；kubectl top 在這裡無法使用）。
type PodStats struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	CPU       float64 `json:"cpu"`       // 百分比（100 = 一個核心）
	MemBytes  int64   `json:"mem_bytes"` // 記憶體用量
}

// ListPodStats 在 engine 跑一次 `docker stats --no-stream`，依 pod 加總（不含 sandbox／pause 容器）。
func (m *Manager) ListPodStats(ctx context.Context) ([]PodStats, error) {
	if m.EngineDistro == "" {
		return nil, errors.New(i18n.T("k8s.no_engine"))
	}
	out, err := m.in(ctx, m.EngineDistro, nil, "docker", "stats", "--no-stream", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.stats_failed"), err)
	}
	return ParseDockerStats(out), nil
}

// ParseDockerStats 解析 `docker stats --format json`（每行一個 JSON），只取 cri-dockerd 的 pod 容器。
// 容器名稱格式是 k8s_<容器>_<pod>_<namespace>_<uid>_<次數>；k8s_POD_ 是 sandbox。
func ParseDockerStats(out string) []PodStats {
	type key struct{ ns, name string }
	acc := map[key]*PodStats{}
	var order []key
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var row struct {
			Name     string `json:"Name"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(sc.Text())), &row) != nil {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(row.Name, "/"), "_")
		if len(parts) < 6 || parts[0] != "k8s" || parts[1] == "POD" {
			continue
		}
		k := key{parts[3], parts[2]}
		ps, ok := acc[k]
		if !ok {
			ps = &PodStats{Namespace: k.ns, Name: k.name}
			acc[k] = ps
			order = append(order, k)
		}
		ps.CPU += parsePercent(row.CPUPerc)
		used, _, _ := strings.Cut(row.MemUsage, "/")
		ps.MemBytes += ParseSize(used)
	}
	res := make([]PodStats, 0, len(order))
	for _, k := range order {
		res = append(res, *acc[k])
	}
	return res
}

func parsePercent(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return f
}

// ParseSize 解析 docker 的大小字串（"12.5MiB"、"1.2GB"、"512B"）。
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mul    float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil {
				return 0
			}
			return int64(f * u.mul)
		}
	}
	return 0
}
