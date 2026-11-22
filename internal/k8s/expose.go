package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ExposeSpec 描述替既有 Deployment 建立（或更新）同名 Service 的要求。
type ExposeSpec struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Expose：nodeport | loadbalancer
	Expose string `json:"expose"`
	// Port 是容器埠；0 = 取 Deployment 第一個宣告的 containerPort。
	Port int `json:"port,omitempty"`
	// HostPort：loadbalancer 時 localhost 上想用的埠（預設同容器埠）。
	HostPort int `json:"host_port,omitempty"`
}

type deploymentForExpose struct {
	Spec struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template struct {
			Spec struct {
				Containers []struct {
					Ports []struct {
						ContainerPort int `json:"containerPort"`
					} `json:"ports"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// Expose 替既有 Deployment 建立同名 Service（selector 取自 Deployment 的 matchLabels，所以一定指向它的 Pod）。
// 已有同名 Service 時就地更新（改埠或改類型）。
func (m *Manager) Expose(ctx context.Context, spec ExposeSpec) (DeployResult, error) {
	if err := checkRef(spec.Namespace, spec.Name); err != nil {
		return DeployResult{}, err
	}
	switch spec.Expose {
	case ExposeNodePort, ExposeLoadBalancer:
	default:
		return DeployResult{}, fmt.Errorf("invalid expose %q", spec.Expose)
	}
	if spec.Port < 0 || spec.Port > 65535 || spec.HostPort < 0 || spec.HostPort > 65535 {
		return DeployResult{}, errors.New("port must be between 1 and 65535")
	}
	out, err := m.get(ctx, "/apis/apps/v1/namespaces/"+spec.Namespace+"/deployments/"+spec.Name,
		"get", "deployment", spec.Name, "-n", spec.Namespace, "-o", "json")
	if err != nil {
		return DeployResult{}, err
	}
	var d deploymentForExpose
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return DeployResult{}, err
	}
	sel := d.Spec.Selector.MatchLabels
	if len(sel) == 0 {
		return DeployResult{}, errors.New("deployment has no selector labels")
	}
	port := spec.Port
	if port == 0 {
		for _, c := range d.Spec.Template.Spec.Containers {
			for _, p := range c.Ports {
				if port == 0 {
					port = p.ContainerPort
				}
			}
		}
	}
	if port == 0 {
		return DeployResult{}, errors.New("the deployment declares no container port; enter one")
	}
	res := DeployResult{Namespace: spec.Namespace, Name: spec.Name, Expose: spec.Expose}
	svcPort, typ := port, "NodePort"
	if spec.Expose == ExposeLoadBalancer {
		typ = "LoadBalancer"
		want := spec.HostPort
		if want == 0 {
			want = port
		}
		svcPort = m.pickHostPort(ctx, DeploySpec{Namespace: spec.Namespace, Name: spec.Name, HostPort: want})
		res.HostPort, res.PortChanged = svcPort, svcPort != want
	}
	manifest, err := json.Marshal(serviceObject(spec.Namespace, spec.Name, typ, svcPort, port, sel))
	if err != nil {
		return DeployResult{}, err
	}
	if _, err := m.root(ctx, bytes.NewReader(manifest), "k3s", "kubectl", "apply", "-f", "-"); err != nil {
		return DeployResult{}, err
	}
	if spec.Expose == ExposeNodePort {
		if svcOut, err := m.root(ctx, nil, "k3s", "kubectl", "get", "service", spec.Name, "-n", spec.Namespace, "-o", "json"); err == nil {
			if svcs, err := ParseServices(`{"items":[` + svcOut + `]}`); err == nil && len(svcs) == 1 && len(svcs[0].Ports) > 0 {
				res.NodePort = svcs[0].Ports[0].NodePort
			}
		}
	}
	return res, nil
}
