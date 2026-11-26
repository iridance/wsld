package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/i18n"
)

// Pod 是 Pods 表格的一列。
type Pod struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Status    string            `json:"status"` // 對齊 kubectl 的 STATUS 欄（Running／Pending／CrashLoopBackOff／Completed…）
	Ready     string            `json:"ready"`  // "1/1"
	Restarts  int               `json:"restarts"`
	IP        string            `json:"ip,omitempty"`
	Created   time.Time         `json:"created"`
	Labels    map[string]string `json:"labels,omitempty"`
	// Containers：init containers 在前（Init=true），再來是一般容器。ID 是 engine 上的 docker 容器 ID
	// （cri-dockerd 建立，所以 logs／terminal 直接沿用容器頁的端點）。
	Containers []PodContainer `json:"containers"`
}

// PodContainer 是 pod 內的一個容器。
type PodContainer struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"` // docker 容器 ID（尚未建立時為空）
	Init     bool   `json:"init,omitempty"`
	Ready    bool   `json:"ready"`
	State    string `json:"state"` // running／waiting／terminated
	Reason   string `json:"reason,omitempty"`
	Restarts int    `json:"restarts"`
}

// SystemNamespaces 是 GUI 預設收起的 namespace。
var SystemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// nameRe 是 namespace／pod／service 名稱（DNS-1123；會當成 wsl.exe 參數，不可含 shell 字元）。
var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// ValidName 檢查 k8s 物件名稱。
func ValidName(s string) bool { return nameRe.MatchString(s) }

func (m *Manager) kubectl(ctx context.Context, args ...string) (string, error) {
	return m.in(ctx, m.Distro, nil, append([]string{"k3s", "kubectl"}, args...)...)
}

// ListPods 列出所有 namespace 的 pod（k8s-engine 內的 k3s kubectl）。
func (m *Manager) ListPods(ctx context.Context) ([]Pod, error) {
	out, err := m.kubectl(ctx, "get", "pods", "-A", "-o", "json", "--request-timeout=10s")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.list_failed"), err)
	}
	return ParsePods(out)
}

// DeletePod 刪除 pod（不等待；由 controller 重建的會再出現）。
func (m *Manager) DeletePod(ctx context.Context, ns, name string) error {
	if !ValidName(ns) || !ValidName(name) {
		return errors.New(i18n.T("k8s.bad_name"))
	}
	if _, err := m.kubectl(ctx, "delete", "pod", "-n", ns, name, "--wait=false", "--request-timeout=10s"); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("k8s.delete_failed", ns+"/"+name), err)
	}
	return nil
}

// ParsePods 解析 `kubectl get pods -o json`，依 namespace、名稱排序。
func ParsePods(js string) ([]Pod, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				Namespace         string            `json:"namespace"`
				Name              string            `json:"name"`
				Labels            map[string]string `json:"labels"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				InitContainers []struct {
					Name string `json:"name"`
				} `json:"initContainers"`
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase                 string            `json:"phase"`
				Reason                string            `json:"reason"`
				PodIP                 string            `json:"podIP"`
				InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
				ContainerStatuses     []containerStatus `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(v.Items))
	for _, it := range v.Items {
		p := Pod{
			Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, IP: it.Status.PodIP,
			Created: it.Metadata.CreationTimestamp, Labels: it.Metadata.Labels, Containers: []PodContainer{},
		}
		ready := 0
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			p.Restarts += cs.RestartCount
		}
		p.Ready = strconv.Itoa(ready) + "/" + strconv.Itoa(len(it.Spec.Containers))
		p.Status = podStatus(it.Status.Phase, it.Status.Reason, it.Status.InitContainerStatuses, it.Status.ContainerStatuses, it.Metadata.DeletionTimestamp != nil)
		for _, c := range it.Spec.InitContainers {
			p.Containers = append(p.Containers, podContainer(c.Name, true, it.Status.InitContainerStatuses))
		}
		for _, c := range it.Spec.Containers {
			p.Containers = append(p.Containers, podContainer(c.Name, false, it.Status.ContainerStatuses))
		}
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

func podContainer(name string, init bool, statuses []containerStatus) PodContainer {
	pc := PodContainer{Name: name, Init: init, State: "waiting"}
	for _, cs := range statuses {
		if cs.Name != name {
			continue
		}
		pc.Ready, pc.Restarts = cs.Ready, cs.RestartCount
		pc.ID = strings.TrimPrefix(cs.ContainerID, "docker://")
		switch {
		case cs.State.Running != nil:
			pc.State = "running"
		case cs.State.Terminated != nil:
			pc.State, pc.Reason = "terminated", cs.State.Terminated.Reason
		case cs.State.Waiting != nil:
			pc.Reason = cs.State.Waiting.Reason
		}
		// 重啟中（waiting）時 containerID 是空的：沿用上一個結束的容器，logs 還看得到原因
		if pc.ID == "" && cs.LastState.Terminated != nil {
			pc.ID = strings.TrimPrefix(cs.LastState.Terminated.ContainerID, "docker://")
		}
	}
	return pc
}

type containerStatus struct {
	Name         string         `json:"name"`
	ContainerID  string         `json:"containerID"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	State        containerState `json:"state"`
	LastState    containerState `json:"lastState"`
}

type containerState struct {
	Running *struct{} `json:"running"`
	Waiting *struct {
		Reason string `json:"reason"`
	} `json:"waiting"`
	Terminated *struct {
		Reason      string `json:"reason"`
		ContainerID string `json:"containerID"`
	} `json:"terminated"`
}

// podStatus 近似 kubectl 的 STATUS：刪除中 → Terminating；init 容器未完成 → Init:x/y 或其原因；
// 容器在等待／已結束時用它的原因；否則 phase。
func podStatus(phase, reason string, initCS, cs []containerStatus, deleting bool) string {
	if deleting {
		return "Terminating"
	}
	if reason != "" {
		return reason
	}
	for i, c := range initCS {
		if t := c.State.Terminated; t != nil && t.Reason == "Completed" {
			continue
		}
		if w := c.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return "Init:" + w.Reason
		}
		if t := c.State.Terminated; t != nil && t.Reason != "" {
			return "Init:" + t.Reason
		}
		return "Init:" + strconv.Itoa(i) + "/" + strconv.Itoa(len(initCS))
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
