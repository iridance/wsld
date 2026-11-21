package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// StatefulSet 是 GUI 用的精簡 StatefulSet（欄位與 Deployment 對應，供架構圖與卡片共用）。
type StatefulSet struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Ready     string            `json:"ready"`
	Replicas  int               `json:"replicas"`
	Selector  map[string]string `json:"selector,omitempty"`
	Image     string            `json:"image,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// IngressRule 是 Ingress 的單一路由：host + path → Service:port。
type IngressRule struct {
	Host    string `json:"host,omitempty"`
	Path    string `json:"path"`
	Service string `json:"service"`
	Port    string `json:"port"`
}

// Ingress 是 GUI 用的精簡 Ingress。
type Ingress struct {
	Name      string        `json:"name"`
	Namespace string        `json:"namespace"`
	Rules     []IngressRule `json:"rules"`
	CreatedAt time.Time     `json:"created_at"`
}

// ConfigMap 是 GUI 用的精簡 ConfigMap。
type ConfigMap struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Keys      int       `json:"keys"`
	CreatedAt time.Time `json:"created_at"`
}

// PVC 是 GUI 用的精簡 PersistentVolumeClaim。
type PVC struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Status       string    `json:"status"`
	Size         string    `json:"size,omitempty"`
	StorageClass string    `json:"storage_class,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Extras 是架構圖與 Config／Storage 分頁額外需要的資源種類（一次 kubectl 取得）。
type Extras struct {
	StatefulSets []StatefulSet `json:"statefulsets"`
	Ingresses    []Ingress     `json:"ingresses"`
	ConfigMaps   []ConfigMap   `json:"configmaps"`
	PVCs         []PVC         `json:"pvcs"`
}

// ParseExtras 解析 `kubectl get statefulsets,ingresses,configmaps,persistentvolumeclaims -o json`。
// 每個 namespace 自動產生的 kube-root-ca.crt 不是使用者的設定，略過。
func ParseExtras(js string) (Extras, error) {
	var v struct {
		Items []struct {
			Kind       string                     `json:"kind"`
			Metadata   objMeta                    `json:"metadata"`
			Data       map[string]json.RawMessage `json:"data"`
			BinaryData map[string]json.RawMessage `json:"binaryData"`
			Spec       struct {
				Replicas         int    `json:"replicas"`
				StorageClassName string `json:"storageClassName"`
				Selector         struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
				Rules []struct {
					Host string `json:"host"`
					HTTP struct {
						Paths []struct {
							Path    string `json:"path"`
							Backend struct {
								Service struct {
									Name string `json:"name"`
									Port struct {
										Number int    `json:"number"`
										Name   string `json:"name"`
									} `json:"port"`
								} `json:"service"`
							} `json:"backend"`
						} `json:"paths"`
					} `json:"http"`
				} `json:"rules"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas int    `json:"readyReplicas"`
				Phase         string `json:"phase"`
				Capacity      struct {
					Storage string `json:"storage"`
				} `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return Extras{}, err
	}
	x := Extras{StatefulSets: []StatefulSet{}, Ingresses: []Ingress{}, ConfigMaps: []ConfigMap{}, PVCs: []PVC{}}
	for _, it := range v.Items {
		m := it.Metadata
		switch it.Kind {
		case "StatefulSet":
			image := ""
			if cs := it.Spec.Template.Spec.Containers; len(cs) > 0 {
				image = cs[0].Image
			}
			x.StatefulSets = append(x.StatefulSets, StatefulSet{Name: m.Name, Namespace: m.Namespace, Ready: fmt.Sprintf("%d/%d", it.Status.ReadyReplicas, it.Spec.Replicas), Replicas: it.Spec.Replicas, Selector: it.Spec.Selector.MatchLabels, Image: image, CreatedAt: m.CreationTimestamp})
		case "Ingress":
			ing := Ingress{Name: m.Name, Namespace: m.Namespace, Rules: []IngressRule{}, CreatedAt: m.CreationTimestamp}
			for _, r := range it.Spec.Rules {
				for _, p := range r.HTTP.Paths {
					port := p.Backend.Service.Port.Name
					if port == "" && p.Backend.Service.Port.Number != 0 {
						port = strconv.Itoa(p.Backend.Service.Port.Number)
					}
					path := p.Path
					if path == "" {
						path = "/"
					}
					ing.Rules = append(ing.Rules, IngressRule{Host: r.Host, Path: path, Service: p.Backend.Service.Name, Port: port})
				}
			}
			x.Ingresses = append(x.Ingresses, ing)
		case "ConfigMap":
			if m.Name == "kube-root-ca.crt" {
				continue
			}
			x.ConfigMaps = append(x.ConfigMaps, ConfigMap{Name: m.Name, Namespace: m.Namespace, Keys: len(it.Data) + len(it.BinaryData), CreatedAt: m.CreationTimestamp})
		case "PersistentVolumeClaim":
			x.PVCs = append(x.PVCs, PVC{Name: m.Name, Namespace: m.Namespace, Status: it.Status.Phase, Size: it.Status.Capacity.Storage, StorageClass: it.Spec.StorageClassName, CreatedAt: m.CreationTimestamp})
		}
	}
	return x, nil
}

// ListExtras 列出 StatefulSet／Ingress／ConfigMap／PVC；ns 為空 = 所有 namespace。
func (m *Manager) ListExtras(ctx context.Context, ns string) (Extras, error) {
	scope, err := nsArgs(ns)
	if err != nil {
		return Extras{}, err
	}
	args := append([]string{"k3s", "kubectl", "get", "statefulsets,ingresses,configmaps,persistentvolumeclaims"}, scope...)
	out, err := m.root(ctx, nil, append(args, "-o", "json")...)
	if err != nil {
		return Extras{}, err
	}
	return ParseExtras(out)
}

// Revision 是 Deployment 的一個 rollout 版本（一個 ReplicaSet）。
type Revision struct {
	Revision  int       `json:"revision"`
	Image     string    `json:"image"`
	CreatedAt time.Time `json:"created_at"`
	Current   bool      `json:"current"`
}

// Event 是 Deployment 的事件。
type Event struct {
	Type     string    `json:"type"`
	Reason   string    `json:"reason"`
	Message  string    `json:"message"`
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

// DeploymentDetail 是 Deployment 詳情的 rollout 歷史與事件。
type DeploymentDetail struct {
	Revisions []Revision `json:"revisions"`
	Events    []Event    `json:"events"`
}

const maxEvents = 10

// ParseRevisions 從 `get replicasets -o json` 取出屬於指定 Deployment 的版本（新→舊）；目前版本 = 仍有副本的最新一版。
func ParseRevisions(js, deployment string) ([]Revision, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Annotations       map[string]string `json:"annotations"`
				OwnerReferences   []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Spec struct {
				Replicas int `json:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := []Revision{}
	for _, it := range v.Items {
		owned := false
		for _, o := range it.Metadata.OwnerReferences {
			if o.Kind == "Deployment" && o.Name == deployment {
				owned = true
			}
		}
		rev, err := strconv.Atoi(it.Metadata.Annotations["deployment.kubernetes.io/revision"])
		if !owned || err != nil {
			continue
		}
		image := ""
		if cs := it.Spec.Template.Spec.Containers; len(cs) > 0 {
			image = cs[0].Image
		}
		out = append(out, Revision{Revision: rev, Image: image, CreatedAt: it.Metadata.CreationTimestamp, Current: it.Spec.Replicas > 0})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	// 只留最新的一個「目前」版本（滾動更新途中舊版可能仍有副本）
	seen := false
	for i := range out {
		if out[i].Current && seen {
			out[i].Current = false
		}
		seen = seen || out[i].Current
	}
	return out, nil
}

// ParseEvents 解析 `get events -o json`（新→舊，最多 maxEvents 筆）。
func ParseEvents(js string) ([]Event, error) {
	var v struct {
		Items []struct {
			Type          string    `json:"type"`
			Reason        string    `json:"reason"`
			Message       string    `json:"message"`
			Count         int       `json:"count"`
			LastTimestamp time.Time `json:"lastTimestamp"`
			EventTime     time.Time `json:"eventTime"`
			Metadata      objMeta   `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(v.Items))
	for _, it := range v.Items {
		at := it.LastTimestamp
		if at.IsZero() {
			at = it.EventTime
		}
		if at.IsZero() {
			at = it.Metadata.CreationTimestamp
		}
		n := it.Count
		if n == 0 {
			n = 1
		}
		out = append(out, Event{Type: it.Type, Reason: it.Reason, Message: it.Message, Count: n, LastSeen: at})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out, nil
}

// DeploymentDetail 取得 rollout 歷史（ReplicaSet）與事件。
func (m *Manager) DeploymentDetail(ctx context.Context, ns, name string) (DeploymentDetail, error) {
	if err := checkRef(ns, name); err != nil {
		return DeploymentDetail{}, err
	}
	rsOut, err := m.root(ctx, nil, "k3s", "kubectl", "get", "replicasets", "-n", ns, "-o", "json")
	if err != nil {
		return DeploymentDetail{}, err
	}
	revs, err := ParseRevisions(rsOut, name)
	if err != nil {
		return DeploymentDetail{}, err
	}
	evOut, err := m.root(ctx, nil, "k3s", "kubectl", "get", "events", "-n", ns, "--field-selector", "involvedObject.kind=Deployment,involvedObject.name="+name, "-o", "json")
	if err != nil {
		return DeploymentDetail{}, err
	}
	evs, err := ParseEvents(evOut)
	if err != nil {
		return DeploymentDetail{}, err
	}
	return DeploymentDetail{Revisions: revs, Events: evs}, nil
}
