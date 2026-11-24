package expose

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/wsl"
)

type fakeRunner struct {
	calls []string
	resp  map[string]string
	errs  map[string]error
}

func (f *fakeRunner) Run(_ context.Context, _ io.Reader, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, nil, err
	}
	out, ok := f.resp[key]
	if !ok {
		return nil, nil, errors.New("fake: unexpected " + key)
	}
	return []byte(out), nil, nil
}

func (f *fakeRunner) called(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

type fakeEngine struct {
	containers []dockerapi.Container
	mounts     map[string][]dockerapi.ContainerMount // id → mounts
	stopped    []string
	listErr    error
}

func (e *fakeEngine) ListContainers(context.Context, bool) ([]dockerapi.Container, error) {
	return e.containers, e.listErr
}

func (e *fakeEngine) Inspect(_ context.Context, id string) (dockerapi.ContainerDetail, error) {
	var d dockerapi.ContainerDetail
	d.ID = id
	d.Mounts = e.mounts[id]
	return d, nil
}

func (e *fakeEngine) StopContainer(_ context.Context, id string, _ int) error {
	e.stopped = append(e.stopped, id)
	return nil
}

func ct(id, name, state string) dockerapi.Container {
	return dockerapi.Container{ID: id, Names: []string{"/" + name}, State: state}
}

func bind(src string) dockerapi.ContainerMount {
	return dockerapi.ContainerMount{Type: "bind", Source: src, Destination: "/x"}
}

const (
	root     = "/mnt/wsl/distro"
	findmnt  = "-d docker-engine -- findmnt -rn -o TARGET"
	umountDb = "-d docker-engine -u root -- umount /mnt/wsl/distro/Debian1"
)

func newMgr(r *fakeRunner, e *fakeEngine) *Manager {
	return &Manager{WSL: r, Engine: e, EngineDistro: "docker-engine"}
}

func TestMountPath(t *testing.T) {
	if got := MountPath("Ubuntu-26.04"); got != root+"/Ubuntu-26.04" {
		t.Fatalf("got %q", got)
	}
}

func TestMountedListsOnlyDirectExposureMounts(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{
		findmnt: "/\n/mnt/c\n/mnt/wsl\n/mnt/wsl/distro/Ubuntu-26.04\n/mnt/wsl/distro/Debian1\n/mnt/wsl/distro/Debian1/proc\n/mnt/wsl/distro/Debian1/dev/pts\n",
	}}
	got, err := newMgr(r, &fakeEngine{}).Mounted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Debian1", "Ubuntu-26.04"}) {
		t.Fatalf("mounted = %v", got)
	}
}

func TestUsersGroupsContainersByExposedDistroIncludingStoppedOnes(t *testing.T) {
	e := &fakeEngine{
		containers: []dockerapi.Container{ct("a", "web", "running"), ct("b", "old", "exited"), ct("c", "db", "running"), ct("d", "x", "running")},
		mounts: map[string][]dockerapi.ContainerMount{
			"a": {bind(root + "/Debian1/home/u/proj/app"), {Type: "volume", Source: "/var/lib/docker/volumes/v/_data"}},
			"b": {bind(root + "/Debian1/home/u/proj/dev")},
			"c": {{Type: "volume", Source: "/var/lib/docker/volumes/pg/_data"}},
			"d": {bind("/mnt/c/Users/me/x"), bind(root + "/Other")},
		},
	}
	got, err := newMgr(&fakeRunner{}, e).Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got["Debian1"]) != 2 || got["Debian1"][0].ID != "a" || got["Debian1"][1].State != "exited" {
		t.Fatalf("Debian1 users = %+v", got["Debian1"])
	}
	if len(got["Other"]) != 1 || len(got) != 2 {
		t.Fatalf("users = %+v", got)
	}
}

func TestReleaseRefusesWhileContainersReferenceTheDistro(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{umountDb: ""}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "exited")}, mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Debian1/home/u/app")}}}
	err := newMgr(r, e).Release(context.Background(), "Debian1", false)
	var inUse *InUseError
	if !errors.As(err, &inUse) || inUse.Distro != "Debian1" || len(inUse.Containers) != 1 {
		t.Fatalf("err = %v", err)
	}
	if r.called("umount") {
		t.Fatal("must not unmount while in use")
	}
	if err := newMgr(r, e).Release(context.Background(), "Debian1", true); err != nil {
		t.Fatalf("force release: %v", err)
	}
	if !r.called(umountDb) {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestReleaseUsesAPlainUmountInTheEngineNeverLazyOrRecursive(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{umountDb: ""}}
	if err := newMgr(r, &fakeEngine{}).Release(context.Background(), "Debian1", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "-l") || strings.Contains(c, "-R") || strings.Contains(c, "--make-private") {
			t.Fatalf("unsafe unmount flags: %s", c)
		}
	}
}

func TestReleaseFallsBackToTheDistroItselfWhenTheEngineIsDown(t *testing.T) {
	r := &fakeRunner{
		errs: map[string]error{umountDb: errors.New("engine down")},
		resp: map[string]string{"-d Debian1 -u root -- /usr/local/sbin/wsld-expose release": ""},
	}
	if err := newMgr(r, &fakeEngine{}).Release(context.Background(), "Debian1", false); err != nil {
		t.Fatal(err)
	}
	if !r.called("wsld-expose release") {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestReleaseReportsTheFailureWhenNothingWorks(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{umountDb: errors.New("target is busy"), "-d Debian1 -u root -- /usr/local/sbin/wsld-expose release": errors.New("no distro")}}
	err := newMgr(r, &fakeEngine{}).Release(context.Background(), "Debian1", true)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v", err)
	}
}

func TestReleaseRejectsUnsafeNames(t *testing.T) {
	r := &fakeRunner{}
	for _, n := range []string{"", "../x", "a b", "a;b", "-rf"} {
		if err := newMgr(r, &fakeEngine{}).Release(context.Background(), n, true); err == nil {
			t.Fatalf("%q accepted", n)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("no command may run: %v", r.calls)
	}
}

func TestExposeRunsTheFixedHelperAsRoot(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{"-d Debian1 -u root -- /usr/local/sbin/wsld-expose expose": ""}}
	if err := newMgr(r, &fakeEngine{}).Expose(context.Background(), "Debian1"); err != nil {
		t.Fatal(err)
	}
}

func TestGCReleasesStaleAndUnusedButKeepsRunningUsedOnes(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{
		findmnt: "/mnt/wsl/distro/Debian1\n/mnt/wsl/distro/Used\n/mnt/wsl/distro/Gone\n/mnt/wsl/distro/Idle\n",
		"-d docker-engine -u root -- umount /mnt/wsl/distro/Gone": "",
		"-d docker-engine -u root -- umount /mnt/wsl/distro/Idle": "",
	}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "running"), ct("g", "ghost", "exited")},
		mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Used/home/app")}, "g": {bind(root + "/Gone/home/app")}}}
	list := []wsl.Distro{{Name: "Debian1", State: "Running"}, {Name: "Used", State: "Running"}, {Name: "Gone", State: "Stopped"}, {Name: "Idle", State: "Running"}}
	// Debian1 掛著、沒有容器引用、Running → 也要放掉（用不到就不占著）
	r.resp["-d docker-engine -u root -- umount /mnt/wsl/distro/Debian1"] = ""
	got, err := newMgr(r, e).GC(context.Background(), list)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Debian1", "Gone", "Idle"}) {
		t.Fatalf("released = %v", got)
	}
	if r.called("distro/Used") && strings.Contains(strings.Join(r.calls, "\n"), "umount /mnt/wsl/distro/Used") {
		t.Fatal("Used is referenced by a running container and must stay")
	}
}

func TestGCDoesNothingWhenNothingIsMounted(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{findmnt: "/\n/mnt/c\n"}}
	got, err := newMgr(r, &fakeEngine{}).GC(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
	if r.called("umount") {
		t.Fatal("nothing to release")
	}
}

func TestGCSkipsListingContainersWhenNothingIsMounted(t *testing.T) {
	e := &fakeEngine{listErr: errors.New("engine down")}
	r := &fakeRunner{resp: map[string]string{findmnt: "/\n"}}
	if _, err := newMgr(r, e).GC(context.Background(), nil); err != nil {
		t.Fatalf("an idle GC must not need the docker API: %v", err)
	}
}

func TestStopUsersStopsOnlyRunningContainersOfThatDistro(t *testing.T) {
	e := &fakeEngine{
		containers: []dockerapi.Container{ct("a", "web", "running"), ct("b", "old", "exited"), ct("c", "other", "running")},
		mounts: map[string][]dockerapi.ContainerMount{
			"a": {bind(root + "/Debian1/home/app")}, "b": {bind(root + "/Debian1/home/old")}, "c": {bind(root + "/Else/x")},
		},
	}
	n, err := newMgr(&fakeRunner{}, e).StopUsers(context.Background(), "Debian1")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !reflect.DeepEqual(e.stopped, []string{"a"}) {
		t.Fatalf("stopped = %v", e.stopped)
	}
}

func TestReportMarksStaleAndUsedEntries(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{findmnt: "/mnt/wsl/distro/Debian1\n/mnt/wsl/distro/Gone\n"}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "running")}, mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Debian1/x")}}}
	list := []wsl.Distro{{Name: "Debian1", State: "Running"}, {Name: "Gone", State: "Stopped"}}
	rep, err := newMgr(r, e).Report(context.Background(), list)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep) != 2 || rep[0].Distro != "Debian1" || !rep[0].Mounted || rep[0].Stale || len(rep[0].Containers) != 1 {
		t.Fatalf("report[0] = %+v", rep)
	}
	if rep[1].Distro != "Gone" || !rep[1].Stale {
		t.Fatalf("report[1] = %+v", rep[1])
	}
}

func TestDistrosOfListsExposedDistrosReferencedByAContainer(t *testing.T) {
	var d dockerapi.ContainerDetail
	d.Mounts = []dockerapi.ContainerMount{
		bind(root + "/Debian1/home/u/a"), bind(root + "/Debian1/home/u/b"), bind(root + "/Other/x"),
		bind("/mnt/c/x"), {Type: "volume", Source: root + "/Fake/x"},
	}
	if got := DistrosOf(d); !reflect.DeepEqual(got, []string{"Debian1", "Other"}) {
		t.Fatalf("got %v", got)
	}
}

func TestEnsureForExposesARunningDistroThatIsNotMountedYet(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{
		findmnt: "/\n",
		"-d Debian1 -u root -- /usr/local/sbin/wsld-expose expose": "",
	}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "exited")}, mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Debian1/home/app")}}}
	list := []wsl.Distro{{Name: "Debian1", State: "Running"}}
	if err := newMgr(r, e).EnsureFor(context.Background(), "a", list); err != nil {
		t.Fatal(err)
	}
	if !r.called("wsld-expose expose") {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestEnsureForIsANoopWhenAlreadyMountedOrNotUsingExposure(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{findmnt: "/mnt/wsl/distro/Debian1\n"}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "exited"), ct("b", "plain", "exited")},
		mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Debian1/home/app")}, "b": {bind("/mnt/c/x")}}}
	list := []wsl.Distro{{Name: "Debian1", State: "Running"}}
	m := newMgr(r, e)
	if err := m.EnsureFor(context.Background(), "a", list); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureFor(context.Background(), "b", list); err != nil {
		t.Fatal(err)
	}
	if r.called("expose") {
		t.Fatalf("nothing to expose: %v", r.calls)
	}
}

func TestEnsureForReportsAStoppedSourceDistro(t *testing.T) {
	r := &fakeRunner{resp: map[string]string{findmnt: "/\n"}}
	e := &fakeEngine{containers: []dockerapi.Container{ct("a", "web", "exited")}, mounts: map[string][]dockerapi.ContainerMount{"a": {bind(root + "/Debian1/home/app")}}}
	err := newMgr(r, e).EnsureFor(context.Background(), "a", []wsl.Distro{{Name: "Debian1", State: "Stopped"}})
	var stopped *DistroStoppedError
	if !errors.As(err, &stopped) || stopped.Distro != "Debian1" {
		t.Fatalf("err = %v", err)
	}
	if r.called("expose") {
		t.Fatal("must not wake a stopped distro")
	}
}
