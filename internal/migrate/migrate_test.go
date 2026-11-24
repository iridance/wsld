package migrate

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
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

const pre = "-d Debian1 -u bob -- "

func base() *fakeRunner {
	return &fakeRunner{
		resp: map[string]string{
			pre + "docker --context default info":                                       "",
			pre + "docker --context default image ls --format {{.Repository}}:{{.Tag}}": "nginx:1.27\nmyapp:dev\n<none>:<none>\nbusybox:<none>\n",
			pre + "docker --context default volume ls -q":                               "pgdata\n" + strings.Repeat("a", 64) + "\nweb_cache\n",
		},
		errs: map[string]error{},
	}
}

const (
	saveKey = pre + "bash -c set -o pipefail; docker --context default save 'nginx:1.27' 'myapp:dev' | docker --context wsld load"
)

func volKey(v string) string {
	return pre + "bash -c set -o pipefail; docker --context default run --rm -v '" + v + "':/from alpine tar -C /from -cf - . | docker --context wsld run --rm -i -v '" + v + "':/to alpine tar -C /to -xf -"
}

func TestRunCopiesTaggedImagesAndNamedVolumes(t *testing.T) {
	f := base()
	f.resp[saveKey] = "Loaded image: nginx:1.27\n"
	f.resp[volKey("pgdata")] = ""
	f.resp[volKey("web_cache")] = ""
	res, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true, Volumes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Images, []string{"nginx:1.27", "myapp:dev"}) {
		t.Fatalf("images = %v", res.Images)
	}
	if !reflect.DeepEqual(res.Volumes, []string{"pgdata", "web_cache"}) {
		t.Fatalf("volumes = %v (anonymous 64-hex volumes must be skipped)", res.Volumes)
	}
	if len(res.Skipped) == 0 {
		t.Fatal("untagged images and anonymous volumes should be reported as skipped")
	}
}

func TestRunDryRunOnlyLists(t *testing.T) {
	f := base()
	res, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true, Volumes: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 2 || len(res.Volumes) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if f.called("save") || f.called("tar") {
		t.Fatalf("dry run must not copy: %v", f.calls)
	}
}

func TestRunRespectsOptions(t *testing.T) {
	f := base()
	f.resp[saveKey] = ""
	res, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Volumes) != 0 || f.called("volume ls") {
		t.Fatalf("volumes were not requested: %+v %v", res, f.calls)
	}
}

func TestRunStartsTheLocalDockerWhenItIsDown(t *testing.T) {
	f := base()
	info := pre + "docker --context default info"
	f.errs[info] = errors.New("down")
	f.resp["-d Debian1 -u root -- systemctl start docker"] = ""
	f.resp[saveKey] = ""
	// 第一次 info 失敗、啟動 docker 後第二次成功
	g := &sequenceRunner{fakeRunner: f, infoKey: info, failFirst: 1}
	if _, err := Run(context.Background(), g, "Debian1", "bob", Options{Images: true}); err != nil {
		t.Fatal(err)
	}
	if !f.called("systemctl start docker") {
		t.Fatalf("calls = %v", f.calls)
	}
}

type sequenceRunner struct {
	*fakeRunner
	infoKey   string
	failFirst int
	seen      int
}

func (s *sequenceRunner) Run(ctx context.Context, in io.Reader, args ...string) ([]byte, []byte, error) {
	if strings.Join(args, " ") == s.infoKey {
		s.seen++
		s.fakeRunner.calls = append(s.fakeRunner.calls, s.infoKey)
		if s.seen <= s.failFirst {
			return nil, nil, errors.New("down")
		}
		return nil, nil, nil
	}
	return s.fakeRunner.Run(ctx, in, args...)
}

func TestRunReportsNoLocalDockerWhenItCannotBeStarted(t *testing.T) {
	f := base()
	f.errs[pre+"docker --context default info"] = errors.New("down")
	f.errs["-d Debian1 -u root -- systemctl start docker"] = errors.New("no unit")
	_, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true})
	if !errors.Is(err, ErrNoLocalDocker) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunNothingToMigrate(t *testing.T) {
	f := &fakeRunner{resp: map[string]string{
		pre + "docker --context default info":                                       "",
		pre + "docker --context default image ls --format {{.Repository}}:{{.Tag}}": "",
		pre + "docker --context default volume ls -q":                               "",
	}, errs: map[string]error{}}
	res, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true, Volumes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images)+len(res.Volumes) != 0 || f.called("save") {
		t.Fatalf("res=%+v calls=%v", res, f.calls)
	}
}

func TestRunRefusesUnsafeNames(t *testing.T) {
	f := base()
	f.resp[pre+"docker --context default image ls --format {{.Repository}}:{{.Tag}}"] = "ok:1\nbad;rm -rf /:x\n"
	f.resp[pre+"docker --context default volume ls -q"] = "good\nbad name\n$(x)\n"
	f.resp[pre+"bash -c set -o pipefail; docker --context default save 'ok:1' | docker --context wsld load"] = ""
	f.resp[volKey("good")] = ""
	res, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true, Volumes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Images, []string{"ok:1"}) || !reflect.DeepEqual(res.Volumes, []string{"good"}) {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Skipped) != 3 {
		t.Fatalf("skipped = %v", res.Skipped)
	}
}

func TestRunVolumeCopyFailureIsReportedWithItsName(t *testing.T) {
	f := base()
	f.resp[saveKey] = ""
	f.resp[volKey("pgdata")] = ""
	f.errs[volKey("web_cache")] = errors.New("tar: boom")
	_, err := Run(context.Background(), f, "Debian1", "bob", Options{Images: true, Volumes: true})
	if err == nil || !strings.Contains(err.Error(), "web_cache") {
		t.Fatalf("err = %v", err)
	}
}
