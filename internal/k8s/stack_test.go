package k8s

import (
	"encoding/json"
	"strings"
	"testing"
)

func shopStack() StackSpec {
	return StackSpec{Name: "shop", Services: []StackService{
		{Name: "web", Image: "shop/web:1", Port: 80, Expose: ExposeLoadBalancer, DependsOn: []string{"api"}},
		{Name: "api", Image: "shop/api:1", Port: 8080, Replicas: 2, DependsOn: []string{"db"}, Env: []EnvVar{{Name: "LOG", Value: "info"}}},
		{Name: "db", Image: "postgres:16", Port: 5432, Stateful: true, Volumes: []StackVolume{{Name: "data", MountPath: "/var/lib/postgresql/data"}}},
	}}
}

func TestStackNormalizeAndOrder(t *testing.T) {
	s, err := shopStack().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if s.Namespace != "shop" || s.Services[2].Volumes[0].Size != "1Gi" || s.Services[0].HostPort != 80 {
		t.Fatalf("defaults not applied: %+v", s)
	}
	order, _ := s.Order()
	var names []string
	for _, sv := range order {
		names = append(names, sv.Name)
	}
	if got := strings.Join(names, ","); got != "db,api,web" {
		t.Fatalf("order = %s", got)
	}
}

func TestStackNormalizeErrors(t *testing.T) {
	mut := func(f func(*StackSpec)) StackSpec { s := shopStack(); f(&s); return s }
	cases := map[string]StackSpec{
		"bad name":  mut(func(s *StackSpec) { s.Name = "Bad_Name" }),
		"empty":     mut(func(s *StackSpec) { s.Services = nil }),
		"duplicate": mut(func(s *StackSpec) { s.Services[1].Name = "web" }),
		"unknown":   mut(func(s *StackSpec) { s.Services[0].DependsOn = []string{"nope"} }),
		"self":      mut(func(s *StackSpec) { s.Services[2].DependsOn = []string{"db"} }),
		"cycle":     mut(func(s *StackSpec) { s.Services[2].DependsOn = []string{"web"} }),
		"env":       mut(func(s *StackSpec) { s.Services[1].Env[0].Name = "1BAD" }),
		"mount":     mut(func(s *StackSpec) { s.Services[2].Volumes[0].MountPath = "data" }),
		"size":      mut(func(s *StackSpec) { s.Services[2].Volumes[0].Size = "lots" }),
		"image":     mut(func(s *StackSpec) { s.Services[0].Image = "-rm" }),
	}
	for name, c := range cases {
		if _, err := c.Normalize(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestStackManifest(t *testing.T) {
	s, _ := shopStack().Normalize()
	raw, err := StackManifest(s, map[string]int{"web": 8081})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, it := range list.Items {
		kinds = append(kinds, it["kind"].(string)+"/"+it["metadata"].(map[string]any)["name"].(string))
	}
	want := "PersistentVolumeClaim/db-data,StatefulSet/db,Service/db,Deployment/api,Service/api,Deployment/web,Service/web"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("items = %s", got)
	}
	js := string(raw)
	for _, frag := range []string{`"name":"DB_HOST","value":"db"`, `"name":"DB_PORT","value":"5432"`, `"name":"API_HOST"`, `"wsld.stack":"shop"`, `"type":"LoadBalancer"`, `"port":8081`, `"serviceName":"db"`} {
		if !strings.Contains(js, frag) {
			t.Errorf("manifest missing %s", frag)
		}
	}
}

func TestStackEnvUserWins(t *testing.T) {
	s := shopStack()
	s.Services[1].Env = []EnvVar{{Name: "DB_HOST", Value: "custom"}}
	s, _ = s.Normalize()
	raw, _ := StackManifest(s, nil)
	if strings.Contains(string(raw), `"value":"db"`) && strings.Count(string(raw), `"name":"DB_HOST"`) != 1 {
		t.Fatal("DB_HOST duplicated")
	}
	if !strings.Contains(string(raw), `"value":"custom"`) {
		t.Fatal("user env lost")
	}
}
