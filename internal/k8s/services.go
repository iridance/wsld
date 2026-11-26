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

// Service 是 Services 表格的一列。
type Service struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Type      string            `json:"type"` // ClusterIP／NodePort／LoadBalancer／ExternalName
	ClusterIP string            `json:"cluster_ip,omitempty"`
	Ports     []ServicePort     `json:"ports"`
	External  []string          `json:"external,omitempty"` // LoadBalancer ingress IP／hostname、ExternalName
	Labels    map[string]string `json:"labels,omitempty"`
	// Selector：選到的 pod（GUI 以 pod labels 比對出後端 pod；空 = 不選 pod，例如 ExternalName 或手動 Endpoints）。
	Selector map[string]string `json:"selector,omitempty"`
	Created  time.Time         `json:"created"`
	// URLs：從 Windows 可開啟的位址（LoadBalancer 的 port、NodePort 的 nodePort，TCP 才算）。
	URLs []string `json:"urls"`
}

// ServicePort 是 service 的一個埠。
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int    `json:"port"`
	TargetPort string `json:"target_port,omitempty"`
	NodePort   int    `json:"node_port,omitempty"`
	Protocol   string `json:"protocol"`
}

// ListServices 列出所有 namespace 的 service。
func (m *Manager) ListServices(ctx context.Context) ([]Service, error) {
	out, err := m.kubectl(ctx, "get", "services", "-A", "-o", "json", "--request-timeout=10s")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("k8s.list_services_failed"), err)
	}
	return ParseServices(out)
}

// ParseServices 解析 `kubectl get services -o json`，依 namespace、名稱排序。
func ParseServices(js string) ([]Service, error) {
	var v struct {
		Items []struct {
			Metadata struct {
				Namespace         string            `json:"namespace"`
				Name              string            `json:"name"`
				Labels            map[string]string `json:"labels"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Type         string            `json:"type"`
				Selector     map[string]string `json:"selector"`
				ClusterIP    string            `json:"clusterIP"`
				ExternalName string            `json:"externalName"`
				Ports        []struct {
					Name       string          `json:"name"`
					Port       int             `json:"port"`
					TargetPort json.RawMessage `json:"targetPort"`
					NodePort   int             `json:"nodePort"`
					Protocol   string          `json:"protocol"`
				} `json:"ports"`
			} `json:"spec"`
			Status struct {
				LoadBalancer struct {
					Ingress []struct {
						IP       string `json:"ip"`
						Hostname string `json:"hostname"`
					} `json:"ingress"`
				} `json:"loadBalancer"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &v); err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(v.Items))
	for _, it := range v.Items {
		s := Service{
			Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Type: it.Spec.Type, Labels: it.Metadata.Labels,
			Selector: it.Spec.Selector, Created: it.Metadata.CreationTimestamp, Ports: []ServicePort{}, URLs: []string{},
		}
		if s.Type == "" {
			s.Type = "ClusterIP"
		}
		if it.Spec.ClusterIP != "None" {
			s.ClusterIP = it.Spec.ClusterIP
		} else {
			s.ClusterIP = "None"
		}
		if it.Spec.ExternalName != "" {
			s.External = append(s.External, it.Spec.ExternalName)
		}
		for _, in := range it.Status.LoadBalancer.Ingress {
			if in.IP != "" {
				s.External = append(s.External, in.IP)
			} else if in.Hostname != "" {
				s.External = append(s.External, in.Hostname)
			}
		}
		lbReady := len(it.Status.LoadBalancer.Ingress) > 0
		for _, p := range it.Spec.Ports {
			sp := ServicePort{Name: p.Name, Port: p.Port, NodePort: p.NodePort, Protocol: p.Protocol, TargetPort: rawPort(p.TargetPort)}
			if sp.Protocol == "" {
				sp.Protocol = "TCP"
			}
			s.Ports = append(s.Ports, sp)
			if sp.Protocol != "TCP" {
				continue
			}
			switch {
			case s.Type == "LoadBalancer" && lbReady:
				s.URLs = append(s.URLs, LocalURL(sp.Port))
			case (s.Type == "NodePort" || s.Type == "LoadBalancer") && sp.NodePort > 0:
				s.URLs = append(s.URLs, LocalURL(sp.NodePort))
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// LocalURL 是 Windows localhost 上的位址（與容器頁的「在瀏覽器開啟」同格式）。
func LocalURL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// NodePorts 回傳 NodePort／LoadBalancer service 的 TCP nodePort（區網轉發用；名稱為 ns/name）。
func NodePorts(svcs []Service) map[int]string {
	out := map[int]string{}
	for _, s := range svcs {
		if s.Type != "NodePort" && s.Type != "LoadBalancer" {
			continue
		}
		for _, p := range s.Ports {
			if p.NodePort > 0 && p.Protocol == "TCP" {
				if _, ok := out[p.NodePort]; !ok {
					out[p.NodePort] = s.Namespace + "/" + s.Name
				}
			}
		}
	}
	return out
}

// rawPort 把 targetPort（數字或具名埠）轉成字串。
func rawPort(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return strconv.Itoa(n)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
