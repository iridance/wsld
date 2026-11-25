package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/iridance/wsld/internal/i18n"
)

// Pod 是 Pods 表格的一列。
type Pod struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // 對齊 kubectl 的 STATUS 欄（Running／Pending／CrashLoopBackOff／Completed…）
	Ready     string    `json:"ready"`  // "1/1"
	Restarts  int       `json:"restarts"`
	IP        string    `json:"ip,omitempty"`
	Created   time.Time `json:"created"`
}

// ListPods 列出所有 namespace 的 pod（k8s-engine 內的 k3s kubectl）。
func (m *Manager) ListPods(ctx context.Context) ([]Pod, error) {
	out, err := m.in(ctx, m.Distro, nil, "k3s", "kubectl", "get", "pods", "-A", "-o", "json", "--request-timeout=10s")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.list_failed"), err)
	}
	return ParsePods(out)
}

// ParsePods 解析 `kubectl get pods -o json`，依 namespace、名稱排序。
func ParsePods(js string) ([]Pod, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				Namespace         string     `json:"namespace"`
				Name              string     `json:"name"`
				CreationTimestamp time.Time  `json:"creationTimestamp"`
				DeletionTimestamp *time.Time `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct{} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string            `json:"phase"`
				Reason            string            `json:"reason"`
				PodIP             string            `json:"podIP"`
				ContainerStatuses []containerStatus `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(v.Items))
	for _, it := range v.Items {
		p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, IP: it.Status.PodIP, Created: it.Metadata.CreationTimestamp}
		ready := 0
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			p.Restarts += cs.RestartCount
		}
		p.Ready = strconv.Itoa(ready) + "/" + strconv.Itoa(len(it.Spec.Containers))
		p.Status = podStatus(it.Status.Phase, it.Status.Reason, it.Status.ContainerStatuses, it.Metadata.DeletionTimestamp != nil)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type containerStatus struct {
	Ready        bool `json:"ready"`
	RestartCount int  `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Terminated *struct {
			Reason string `json:"reason"`
		} `json:"terminated"`
	} `json:"state"`
}

// podStatus 近似 kubectl 的 STATUS：刪除中 → Terminating；容器在等待／已結束時用它的原因；否則 phase。
func podStatus(phase, reason string, cs []containerStatus, deleting bool) string {
	if deleting {
		return "Terminating"
	}
	if reason != "" {
		return reason
	}
	for _, c := range cs {
		if w := c.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason
		}
	}
	if phase == "Succeeded" {
		return "Completed"
	}
	for _, c := range cs {
		if t := c.State.Terminated; t != nil && t.Reason != "" && phase != "Running" {
			return t.Reason
		}
	}
	if phase == "" {
		return "Unknown"
	}
	return phase
}
