package inspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// call is one runtime command a fake runner received.
type call struct {
	args   []string
	env    []string
	ctxErr error
}

// fakeRunner answers runtime commands by their first argument.
type fakeRunner struct {
	out   map[string]string
	errs  map[string]error
	calls []call
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		out: map[string]string{
			"info":    "5.8.4|true|true",
			"run":     "0123456789abcdef",
			"port":    "127.0.0.1:40000",
			"inspect": "true",
		},
		errs: map[string]error{},
	}
}

func (f *fakeRunner) run(ctx context.Context, env []string, _ string, args ...string) (string, error) {
	f.calls = append(f.calls, call{args: args, env: env, ctxErr: ctx.Err()})
	if err := f.errs[args[0]]; err != nil {
		return "", err
	}
	return f.out[args[0]], nil
}

func (f *fakeRunner) called(cmd string) *call {
	for i := range f.calls {
		if f.calls[i].args[0] == cmd {
			return &f.calls[i]
		}
	}
	return nil
}

func installed(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(names, name) {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestDetectRuntime(t *testing.T) {
	tests := []struct {
		name      string
		installed []string
		env       string
		info      string
		infoErr   error
		want      Runtime
		wantErr   string
	}{
		{name: "podman preferred", installed: []string{"podman", "docker"}, info: "5.8.4|true|true",
			want: Runtime{Name: Podman, Path: "/usr/bin/podman", Version: "5.8.4", Rootless: true, SELinux: true}},
		{name: "docker fallback", installed: []string{"docker"}, info: `29.8.2|["name=seccomp,profile=builtin","name=cgroupns"]`,
			want: Runtime{Name: Docker, Path: "/usr/bin/docker", Version: "29.8.2"}},
		{name: "docker with SELinux, rootless", installed: []string{"docker"}, info: `28.0.1|["name=selinux","name=rootless"]`,
			want: Runtime{Name: Docker, Path: "/usr/bin/docker", Version: "28.0.1", Rootless: true, SELinux: true}},
		{name: "forced docker", installed: []string{"podman", "docker"}, env: "docker", info: `29.8.2|[]`,
			want: Runtime{Name: Docker, Path: "/usr/bin/docker", Version: "29.8.2"}},
		{name: "forced but missing", installed: []string{"docker"}, env: "podman", wantErr: "podman is not installed"},
		{name: "forced unknown", installed: []string{"podman"}, env: "lxc", wantErr: "must be podman or docker"},
		{name: "none", wantErr: "install podman"},
		{name: "docker daemon down", installed: []string{"docker"}, infoErr: errors.New("docker info: Cannot connect"),
			wantErr: "docker group"},
		{name: "odd answer", installed: []string{"podman"}, info: "", wantErr: "unexpected answer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			f.out["info"] = tc.info
			if tc.infoErr != nil {
				f.errs["info"] = tc.infoErr
			}
			in := &inspector{
				getenv:   func(k string) string { return map[string]string{EnvRuntime: tc.env}[k] },
				lookPath: installed(tc.installed...),
				run:      f.run,
			}
			got, err := in.detectRuntime(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestRunArgs(t *testing.T) {
	mounts := []mount{{host: "/home/u/a b.bak", container: "/bak/1.bak", readOnly: true}, {host: "/tmp/out", container: "/backup"}}
	podman := runArgs(Runtime{Name: Podman, SELinux: true}, "dialga-inspect-x", DefaultImage, mounts)
	want := []string{"run", "-d", "--name", "dialga-inspect-x", "--pull=never", "-u", "0",
		"--label", "io.dialga.inspect=true",
		"-e", "ACCEPT_EULA=Y", "-e", "MSSQL_PID=Developer", "-e", "MSSQL_SA_PASSWORD",
		"-p", "127.0.0.1::1433", "--security-opt", "label=disable",
		"-v", "/home/u/a b.bak:/bak/1.bak:ro", "-v", "/tmp/out:/backup", DefaultImage}
	if !slices.Equal(podman, want) {
		t.Fatalf("podman:\n got %q\nwant %q", podman, want)
	}
	docker := runArgs(Runtime{Name: Docker}, "dialga-inspect-x", DefaultImage, mounts[:1])
	if slices.Contains(docker, "--security-opt") || docker[len(docker)-1] != DefaultImage ||
		!slices.Contains(docker, "/home/u/a b.bak:/bak/1.bak:ro") {
		t.Fatalf("docker: %q", docker)
	}
}

func TestRemoveArgs(t *testing.T) {
	if got := removeArgs(Runtime{Name: Podman}, "c"); !slices.Equal(got, []string{"rm", "-f", "-i", "-t", "0", "c"}) {
		t.Fatalf("podman: %q", got)
	}
	if got := removeArgs(Runtime{Name: Docker}, "c"); !slices.Equal(got, []string{"rm", "-f", "c"}) {
		t.Fatalf("docker: %q", got)
	}
	f := newFakeRunner()
	f.errs["rm"] = errors.New("docker rm: Error response from daemon: No such container: c")
	c := &container{in: &inspector{run: f.run, cleanup: 1}, rt: Runtime{Name: Docker}, name: "c"}
	if err := c.remove(context.Background()); err != nil {
		t.Fatalf("docker, already gone: %v", err)
	}
	f.errs["rm"] = errors.New("docker rm: permission denied")
	if err := c.remove(context.Background()); err == nil {
		t.Fatal("docker rm failure: no error")
	}
}

func TestImageExists(t *testing.T) {
	for _, rt := range []Runtime{{Name: Podman}, {Name: Docker}} {
		f := newFakeRunner()
		in := &inspector{run: f.run}
		if err := in.imageExists(context.Background(), rt, DefaultImage); err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{Podman: {"image", "exists", DefaultImage},
			Docker: {"image", "inspect", "--format", "{{.Id}}", DefaultImage}}[rt.Name]
		if !slices.Equal(f.calls[0].args, want) {
			t.Fatalf("%s: %q", rt.Name, f.calls[0].args)
		}
		f.errs["image"] = errors.New("exit status 1")
		if err := in.imageExists(context.Background(), rt, DefaultImage); err == nil ||
			!strings.Contains(err.Error(), rt.Name+" pull "+DefaultImage) {
			t.Fatalf("%s missing: %v", rt.Name, err)
		}
	}
}

func TestPublishedAddr(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:41234":                   "127.0.0.1:41234",
		"127.0.0.1:41234\n[::1]:41234":      "127.0.0.1:41234",
		"0.0.0.0:32768\n":                   "127.0.0.1:32768",
		"":                                  "",
		"Error: no port 1433/tcp published": "",
	}
	for out, want := range tests {
		got, err := publishedAddr(out)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("publishedAddr(%q) = %q, %v", out, got, err)
		}
	}
}

func TestValidateImage(t *testing.T) {
	for _, ok := range []string{DefaultImage, "localhost:5000/mssql:2025", "mcr.microsoft.com/mssql/server@sha256:0123abcd"} {
		if err := ValidateImage(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "--privileged", "img with space", "img;rm"} {
		if err := ValidateImage(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestResolvePaths(t *testing.T) {
	skipOnWindows(t)
	dir := resolvedTempDir(t)
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := write("a.bak"), write("b c.bak")
	link := filepath.Join(dir, "link.bak")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	colon := write("x:y.bak")
	t.Chdir(dir)

	got, err := ResolvePaths([]string{"a.bak", b})
	if err != nil || !slices.Equal(got, []string{a, b}) {
		t.Fatalf("relative: %q %v", got, err)
	}
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"none", nil, "no .bak file"},
		{"too many", make([]string, MaxFiles+1), "too many files"},
		{"missing", []string{filepath.Join(dir, "nope.bak")}, "no such file"},
		{"directory", []string{dir}, "not a regular file"},
		{"same file through a link", []string{a, link}, "are the same file"},
		{"colon", []string{colon}, "cannot contain ':'"},
	}
	for _, tc := range tests {
		if _, err := ResolvePaths(tc.paths); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	if os.Geteuid() != 0 {
		locked := write("locked.bak")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolvePaths([]string{locked}); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("unreadable: %v", err)
		}
	}
}
