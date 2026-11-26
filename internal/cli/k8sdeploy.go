package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/k8s"
)

// cmdK8sApply：wsld k8s apply -f <檔案|->（kubectl apply；- = stdin）。
func (a *App) cmdK8sApply(ctx context.Context, args []string) error {
	fs := a.newFlags("k8s apply")
	file := fs.String("f", "", "manifest file (YAML/JSON), - for stdin")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *file == "" || fs.NArg() != 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "apply -f <file|->")))
	}
	var data []byte
	var err error
	if *file == "-" {
		if a.Stdin == nil {
			return fail(ExitUsage, errors.New("no stdin"))
		}
		data, err = io.ReadAll(io.LimitReader(a.Stdin, k8s.MaxApplyBytes+1))
	} else {
		data, err = os.ReadFile(*file)
	}
	if err != nil {
		return fail(ExitUsage, err)
	}
	if k8s.IsComposeFile(data) {
		return fail(ExitUsage, errors.New(i18n.T("cli.k8s.apply_is_compose")))
	}
	cfg, err := a.k8sOn()
	if err != nil {
		return err
	}
	out, err := a.k8sManager(cfg).Apply(ctx, data)
	if err != nil {
		return fail(ExitError, err)
	}
	fmt.Fprintln(a.Stdout, strings.TrimSpace(out))
	return nil
}

// cmdK8sCompose：wsld k8s compose -f docker-compose.yml [--name N] [--namespace NS] [--dry-run]。
// 與 GUI 的 Import 相同的轉換；同資料夾的 .env 與 env_file 會自動讀取（不得跳出該資料夾）。
// 檔案在 \\wsl.localhost\<distro>\… 時視為該 distro 內的路徑（bind mount 經曝露對應到 engine）。
func (a *App) cmdK8sCompose(ctx context.Context, args []string) error {
	flags := a.newFlags("k8s compose")
	file := flags.String("f", "", "docker-compose.yml path")
	name := flags.String("name", "", "stack name (default: the folder name)")
	ns := flags.String("namespace", "", "namespace (default: the stack name)")
	dry := flags.Bool("dry-run", false, "print the converted manifest instead of applying it")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *file == "" || flags.NArg() != 0 {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "compose -f <docker-compose.yml> [--name N] [--namespace NS] [--dry-run]")))
	}
	abs, err := filepath.Abs(*file)
	if err != nil {
		return fail(ExitUsage, err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fail(ExitUsage, err)
	}
	if !k8s.IsComposeFile(data) {
		return fail(ExitUsage, errors.New(i18n.T("cli.k8s.compose_not_compose")))
	}
	dir := filepath.Dir(abs)
	stackName := *name
	if stackName == "" {
		stackName = filepath.Base(dir)
	}
	cfg, _ := a.loadConfig()
	engine := ""
	if e, ok := cfg.EngineMachine(); ok {
		engine = e.Distro
	}
	kind, source, bindDir := composeLocation(dir)
	res, err := k8s.ParseCompose(data, k8s.ComposeOptions{
		Name: stackName,
		ResolveBind: func(src string) (string, error) {
			return k8s.ResolveBind(kind, source, engine, bindDir, src)
		},
		ReadFile: func(rel string) ([]byte, error) {
			clean := filepath.Clean(filepath.FromSlash(rel))
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("%s: path must stay inside the compose file's folder", rel)
			}
			b, err := os.ReadFile(filepath.Join(dir, clean))
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
			}
			return b, err
		},
	})
	if err != nil {
		return fail(ExitUsage, err)
	}
	res.Spec.Namespace = *ns
	spec, err := res.Spec.Normalize()
	if err != nil {
		return fail(ExitUsage, err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(a.Stderr, "warning:", w)
	}
	if *dry {
		return a.printStackManifest(spec)
	}
	return a.deployStack(ctx, spec)
}

// cmdK8sStack：wsld k8s stack rm <namespace>/<名稱> [--pvc] [--yes]（刪除 Import 部署的 stack）。
func (a *App) cmdK8sStack(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "rm" && args[0] != "delete") {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "stack rm <namespace>/<name> [--pvc] [--yes]")))
	}
	fs := a.newFlags("k8s stack rm")
	pvc := fs.Bool("pvc", false, "also delete PersistentVolumeClaims (data is lost)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	ns, name, ok := strings.Cut(fs.Arg(0), "/")
	if fs.NArg() != 1 || !ok {
		return fail(ExitUsage, errors.New(i18n.T("cli.missing_arg", "stack rm <namespace>/<name> [--pvc] [--yes]")))
	}
	cfg, err := a.k8sOn()
	if err != nil {
		return err
	}
	if !*yes && !a.confirm(i18n.T("cli.k8s.stack_rm_confirm", ns+"/"+name)) {
		return fail(ExitError, errors.New(i18n.T("cli.k8s.cancelled")))
	}
	if err := a.k8sManager(cfg).DeleteStack(ctx, ns, name, *pvc); err != nil {
		return fail(ExitError, err)
	}
	fmt.Fprintln(a.Stdout, i18n.T("cli.k8s.stack_removed", ns+"/"+name))
	return nil
}

func (a *App) printStackManifest(spec k8s.StackSpec) error {
	manifest, err := k8s.StackManifest(spec, nil)
	if err != nil {
		return fail(ExitError, err)
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, manifest, "", "  ")
	fmt.Fprintln(a.Stdout, pretty.String())
	return nil
}

// deployStack 確保 hostPath 用到的 client 已曝露，再部署並逐一印出結果。
func (a *App) deployStack(ctx context.Context, spec k8s.StackSpec) error {
	cfg, err := a.k8sOn()
	if err != nil {
		return err
	}
	if err := a.exposeFor(ctx, cfg, k8s.ExposedDistros(spec)); err != nil {
		return err
	}
	res, derr := a.k8sManager(cfg).DeployStack(ctx, spec)
	for _, r := range res.Services {
		fmt.Fprintln(a.Stdout, deployLine(r))
	}
	if derr != nil {
		return fail(ExitError, derr)
	}
	return nil
}

func (a *App) exposeFor(ctx context.Context, cfg config.Config, distros []string) error {
	if len(distros) == 0 {
		return nil
	}
	m, ok := a.exposeManager(cfg)
	if !ok {
		return nil
	}
	for _, d := range distros {
		if err := m.Expose(ctx, d); err != nil {
			return fail(ExitError, fmt.Errorf("%s: %w", i18n.T("k8s.expose_failed", d), err))
		}
	}
	return nil
}

func deployLine(r k8s.DeployResult) string {
	line := fmt.Sprintf("%s/%s", r.Namespace, r.Name)
	switch {
	case r.HostPort > 0:
		line += " → " + k8s.LocalURL(r.HostPort)
		if r.PortChanged {
			line += " (" + i18n.T("cli.k8s.port_changed") + ")"
		}
	case r.NodePort > 0:
		line += " → " + k8s.LocalURL(r.NodePort) + " (NodePort)"
	}
	return line
}

// composeLocation 判斷 Compose 檔所在：\\wsl.localhost\<distro>\… 或 \\wsl$\<distro>\… → 該 distro 內的 Linux 路徑；
// Windows 路徑 → windows；其他（在 WSL 內執行 wsld 時的 Linux 路徑）→ wsl、來源不明（路徑原樣）。
func composeLocation(dir string) (kind, source, bindDir string) {
	d := strings.ReplaceAll(dir, `\`, "/")
	for _, pre := range []string{"//wsl.localhost/", "//wsl$/"} {
		if len(d) > len(pre) && strings.EqualFold(d[:len(pre)], pre) {
			rest := d[len(pre):]
			distro, p, _ := strings.Cut(rest, "/")
			return "wsl", distro, "/" + p
		}
	}
	if len(d) >= 2 && d[1] == ':' {
		return "windows", "", dir
	}
	return "wsl", "", dir
}
