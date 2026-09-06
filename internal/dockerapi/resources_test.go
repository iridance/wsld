package dockerapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func asAPIError(err error, target **APIError) bool { return errors.As(err, target) }

func TestListImages(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/json" || r.URL.Query().Get("all") != "" {
			t.Errorf("unexpected %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[
		  {"Id":"sha256:aaa111","RepoTags":["nginx:alpine"],"RepoDigests":["nginx@sha256:1"],"Created":1700000000,"Size":45000000,"Containers":2},
		  {"Id":"sha256:bbb222","RepoTags":null,"Created":1600000000,"Size":9000000,"Containers":-1}
		]`)
	}))
	got, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "sha256:aaa111" || got[0].RepoTags[0] != "nginx:alpine" || got[0].Size != 45000000 || got[0].Containers != 2 {
		t.Fatalf("got = %+v", got)
	}
	if got[0].ShortID() != "aaa111" || got[0].Name() != "nginx:alpine" {
		t.Fatalf("ShortID/Name = %q/%q", got[0].ShortID(), got[0].Name())
	}
	if got[1].Name() != "<none>" || got[1].Containers != -1 {
		t.Fatalf("dangling image name = %q containers=%d", got[1].Name(), got[1].Containers)
	}
}

func TestRemoveImageAndPrune(t *testing.T) {
	var calls []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/images/nginx:alpine":
			_, _ = io.WriteString(w, `[{"Untagged":"nginx:alpine"},{"Deleted":"sha256:aaa"}]`)
		case r.Method == http.MethodDelete && r.URL.Path == "/images/inuse":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"message":"conflict: unable to remove repository reference \"inuse\" (must force) - container 123 is using its referenced image"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/images/prune":
			_, _ = io.WriteString(w, `{"ImagesDeleted":[{"Deleted":"sha256:1"},{"Deleted":"sha256:2"}],"SpaceReclaimed":123456}`)
		default:
			w.WriteHeader(404)
		}
	}))
	ctx := context.Background()
	if err := c.RemoveImage(ctx, "nginx:alpine", false); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "DELETE /images/nginx:alpine?" {
		t.Fatalf("calls = %v", calls)
	}
	if err := c.RemoveImage(ctx, "nginx:alpine", true); err != nil || calls[1] != "DELETE /images/nginx:alpine?force=1" {
		t.Fatalf("force: %v %v", err, calls)
	}
	err := c.RemoveImage(ctx, "inuse", false)
	var ae *APIError
	if !asAPIError(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("in-use image should surface 409 APIError, got %v", err)
	}
	rep, err := c.PruneImages(ctx)
	if err != nil || rep.Count != 2 || rep.SpaceReclaimed != 123456 {
		t.Fatalf("prune = %+v %v", rep, err)
	}
	if calls[len(calls)-1] != "POST /images/prune?filters=%7B%22dangling%22%3A%5B%22false%22%5D%7D" {
		t.Fatalf("prune should remove all unused images (dangling=false): %v", calls[len(calls)-1])
	}
}

func TestListVolumesRemovePrune(t *testing.T) {
	var calls []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/volumes":
			_, _ = io.WriteString(w, `{"Volumes":[
			  {"Name":"pgdata","Driver":"local","Mountpoint":"/var/lib/docker/volumes/pgdata/_data","CreatedAt":"2026-09-01T00:00:00Z","Labels":{"com.docker.compose.project":"shop"},"Scope":"local"},
			  {"Name":"abc","Driver":"local","Mountpoint":"/var/lib/docker/volumes/abc/_data","CreatedAt":"2026-09-02T00:00:00Z","Labels":null,"Scope":"local"}
			],"Warnings":null}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/volumes/pgdata":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/volumes/prune":
			_, _ = io.WriteString(w, `{"VolumesDeleted":["abc"],"SpaceReclaimed":42}`)
		default:
			w.WriteHeader(404)
		}
	}))
	ctx := context.Background()
	vols, err := c.ListVolumes(ctx)
	if err != nil || len(vols) != 2 || vols[0].Name != "pgdata" || vols[0].Labels["com.docker.compose.project"] != "shop" {
		t.Fatalf("vols = %+v %v", vols, err)
	}
	if err := c.RemoveVolume(ctx, "pgdata", false); err != nil {
		t.Fatal(err)
	}
	if calls[1] != "DELETE /volumes/pgdata?" {
		t.Fatalf("calls = %v", calls)
	}
	rep, err := c.PruneVolumes(ctx)
	if err != nil || rep.Count != 1 || rep.SpaceReclaimed != 42 {
		t.Fatalf("prune = %+v %v", rep, err)
	}
	if calls[2] != "POST /volumes/prune?filters=%7B%22all%22%3A%5B%22true%22%5D%7D" {
		t.Fatalf("volume prune should include named volumes (all=true): %v", calls[2])
	}
}

func TestListNetworks(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/networks" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[
		  {"Name":"bridge","Id":"n1","Created":"2026-09-01T00:00:00Z","Scope":"local","Driver":"bridge","Internal":false,"Containers":{"c1":{"Name":"web"}}},
		  {"Name":"shop_default","Id":"n2","Created":"2026-09-02T00:00:00Z","Scope":"local","Driver":"bridge","Internal":false,"Labels":{"com.docker.compose.project":"shop"}}
		]`)
	}))
	nets, err := c.ListNetworks(context.Background())
	if err != nil || len(nets) != 2 {
		t.Fatalf("nets = %+v %v", nets, err)
	}
	if nets[0].Name != "bridge" || nets[0].Driver != "bridge" || nets[0].ContainerCount() != 1 || nets[1].ContainerCount() != 0 {
		t.Fatalf("nets = %+v", nets)
	}
	if !nets[0].Builtin() || nets[1].Builtin() {
		t.Fatalf("bridge is builtin, shop_default is not")
	}
}

func TestRemoveNetworkAndPrune(t *testing.T) {
	var calls []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/networks/shop_default":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/networks/inuse":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"network inuse has active endpoints"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/networks/prune":
			_, _ = io.WriteString(w, `{"NetworksDeleted":["a","b"],"SpaceReclaimed":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	ctx := context.Background()
	if err := c.RemoveNetwork(ctx, "shop_default"); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "DELETE /networks/shop_default?" {
		t.Fatalf("calls = %v", calls)
	}
	err := c.RemoveNetwork(ctx, "inuse")
	var ae *APIError
	if !asAPIError(err, &ae) || ae.Status != http.StatusForbidden {
		t.Fatalf("in-use network should surface APIError, got %v", err)
	}
	rep, err := c.PruneNetworks(ctx)
	if err != nil || rep.Count != 2 || rep.SpaceReclaimed != 0 {
		t.Fatalf("prune = %+v %v", rep, err)
	}
	if calls[len(calls)-1] != "POST /networks/prune?" {
		t.Fatalf("prune call = %v", calls[len(calls)-1])
	}
}
