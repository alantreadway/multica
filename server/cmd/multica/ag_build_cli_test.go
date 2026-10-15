package main

// AG patch: ag-patches/build-cli.sh tests, run against a throwaway git repo
// with stub go and docker binaries so nothing is compiled.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type agBuildCLIRepo struct {
	dir       string
	binDir    string
	goLog     string
	dockerLog string
	testLog   string
}

// newAGBuildCLIRepo makes a repo on branch ag/v0.6.2 with the upstream tag
// v0.6.2 on an ancestor commit, a stub ag-patches/test.sh, and stub go and
// docker binaries on PATH.
func newAGBuildCLIRepo(t *testing.T, testShExit int) *agBuildCLIRepo {
	t.Helper()
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	r := &agBuildCLIRepo{dir: t.TempDir(), binDir: t.TempDir()}
	r.goLog = filepath.Join(r.binDir, "go.log")
	r.dockerLog = filepath.Join(r.binDir, "docker.log")
	r.testLog = filepath.Join(r.binDir, "test.log")

	// The stub records its environment and arguments, then creates the file
	// named by -o so the script finds a binary there.
	stub := `#!/bin/sh
{
  echo "GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO_ENABLED"
  echo "PWD=$(pwd)"
  echo "ARGS=$*"
} >> "` + r.goLog + `"
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then : > "$2"; fi
  shift
done
`
	if err := os.WriteFile(filepath.Join(r.binDir, "go"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// The docker stub runs from the repo root, so -o ../dist/x is server/../dist/x.
	dockerStub := `#!/bin/sh
{
  echo "CGO_ENABLED=$CGO_ENABLED GOOS=$GOOS GOARCH=$GOARCH"
  echo "ARGS=$*"
} >> "` + r.dockerLog + `"
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then : > "server/$2"; fi
  shift
done
`
	if err := os.WriteFile(filepath.Join(r.binDir, "docker"), []byte(dockerStub), 0o755); err != nil {
		t.Fatal(err)
	}

	r.git(t, "init", "-q", "-b", "main")
	r.write(t, "README", "x")
	r.write(t, "server/go.mod", "module example.com/x\n\ngo 1.26.9\n")
	r.write(t, ".gitignore", "dist/\n")
	r.git(t, "add", "README", "server/go.mod", ".gitignore")
	r.git(t, "commit", "-q", "-m", "upstream")
	r.git(t, "tag", "v0.6.2")
	r.git(t, "checkout", "-q", "-b", "ag/v0.6.2")
	// The real scripts are the code under test.
	for _, name := range []string{"lib.sh", "build-cli.sh"} {
		body, err := os.ReadFile(filepath.Join("../../../ag-patches", name))
		if err != nil {
			t.Fatal(err)
		}
		r.write(t, "ag-patches/"+name, string(body))
	}
	r.write(t, "ag-patches/test.sh", "#!/bin/sh\necho ran >> \""+r.testLog+"\"\nexit "+strconv.Itoa(testShExit)+"\n")
	if err := os.Chmod(filepath.Join(r.dir, "ag-patches/test.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(t, "add", "ag-patches")
	r.git(t, "commit", "-q", "-m", "patch")
	return r
}

func (r *agBuildCLIRepo) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *agBuildCLIRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// run executes the repo's own copy of build-cli.sh (committed from the real
// script) and returns combined output and the exit code.
func (r *agBuildCLIRepo) run(t *testing.T, args ...string) (string, int) {
	return r.runEnv(t, nil, args...)
}

func (r *agBuildCLIRepo) runEnv(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(r.dir, "ag-patches/build-cli.sh")}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(append(os.Environ(), "PATH="+r.binDir+string(os.PathListSeparator)+os.Getenv("PATH")), env...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run build-cli.sh: %v", err)
	}
	return buf.String(), code
}

func TestAGPatchBuildCLI(t *testing.T) {
	t.Run("AG patch: build-cli cross-compiles ./cmd/multica for darwin/arm64 with the version ldflags", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		out, code := r.run(t, "--rev", "3")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		bin := filepath.Join(r.dir, "dist", "multica-v0.6.2-ag.3-darwin-arm64")
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("expected %s: %v\n%s", bin, err, out)
		}
		log, _ := os.ReadFile(r.goLog)
		short := r.git(t, "rev-parse", "--short", "HEAD")
		for _, want := range []string{
			"GOOS=darwin GOARCH=arm64 CGO_ENABLED=0",
			"-X main.version=v0.6.2-ag.3",
			"-X main.commit=" + short,
			"./cmd/multica",
		} {
			if !strings.Contains(string(log), want) {
				t.Errorf("go invocation missing %q:\n%s", want, log)
			}
		}
		if !strings.Contains(string(log), "/server\n") {
			t.Errorf("go must run from server/:\n%s", log)
		}
	})

	t.Run("AG patch: build-cli runs test.sh first and does not build when it fails", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 1)
		out, code := r.run(t, "--rev", "1")
		if code == 0 {
			t.Fatalf("build-cli succeeded although test.sh failed:\n%s", out)
		}
		if _, err := os.Stat(r.testLog); err != nil {
			t.Error("test.sh was not run")
		}
		if _, err := os.Stat(r.goLog); err == nil {
			t.Error("go build ran although the guard tests failed")
		}
		if matches, _ := filepath.Glob(filepath.Join(r.dir, "dist", "*")); len(matches) != 0 {
			t.Errorf("no binary should be produced, got %v", matches)
		}
	})

	t.Run("AG patch: build-cli --skip-tests skips test.sh", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 1)
		out, code := r.run(t, "--rev", "2", "--skip-tests")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if _, err := os.Stat(r.testLog); err == nil {
			t.Error("test.sh ran under --skip-tests")
		}
	})

	t.Run("AG patch: build-cli rejects a bad --rev and unknown flags", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		for _, args := range [][]string{{"--rev", "abc"}, {"--rev"}, {"--bogus"}} {
			if out, code := r.run(t, args...); code == 0 {
				t.Errorf("%v: want a non-zero exit, got 0:\n%s", args, out)
			}
		}
		if _, err := os.Stat(r.goLog); err == nil {
			t.Error("go build ran for a rejected invocation")
		}
	})

	t.Run("AG patch: build-cli refuses a branch that is not ag/<tag>", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		r.git(t, "checkout", "-q", "-b", "feature/x")
		out, code := r.run(t, "--rev", "1")
		if code == 0 || !strings.Contains(out, "ag/") {
			t.Fatalf("want a refusal naming ag/<tag>, exit %d:\n%s", code, out)
		}
	})

	t.Run("AG patch: build-cli refuses a dirty tree", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		r.write(t, "untracked.txt", "x")
		out, code := r.run(t, "--rev", "1")
		if code == 0 || !strings.Contains(out, "dirty") {
			t.Fatalf("want a dirty-tree refusal, exit %d:\n%s", code, out)
		}
	})

	t.Run("AG patch: build-cli falls back to a golang container when go is not used", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		out, code := r.runEnv(t, []string{"AG_FORCE_DOCKER=1"}, "--rev", "4")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if _, err := os.Stat(r.goLog); err == nil {
			t.Error("local go ran although the container was forced")
		}
		if _, err := os.Stat(filepath.Join(r.dir, "dist", "multica-v0.6.2-ag.4-darwin-arm64")); err != nil {
			t.Errorf("expected the container build's binary in dist/: %v\n%s", err, out)
		}
		log, _ := os.ReadFile(r.dockerLog)
		for _, want := range []string{
			"CGO_ENABLED=0 GOOS=darwin GOARCH=arm64",
			"run --rm",
			":/src -w /src/server",
			"-v multica-ag-gomod:/go/pkg/mod",
			"-e CGO_ENABLED -e GOOS -e GOARCH",
			"golang:1.26.9 go build",
			"-buildvcs=false",
			"-X main.version=v0.6.2-ag.4",
			"-o ../dist/multica-v0.6.2-ag.4-darwin-arm64 ./cmd/multica",
		} {
			if !strings.Contains(string(log), want) {
				t.Errorf("docker invocation missing %q:\n%s", want, log)
			}
		}
	})

	t.Run("AG patch: build-cli refuses a --rev whose ag/ tag marks another commit", func(t *testing.T) {
		r := newAGBuildCLIRepo(t, 0)
		r.git(t, "tag", "ag/v0.6.2-ag.1", "HEAD~1")
		out, code := r.run(t, "--rev", "1")
		if code == 0 || !strings.Contains(out, "already marks") {
			t.Fatalf("want a tag-collision refusal, exit %d:\n%s", code, out)
		}
		if _, code := r.run(t, "--rev", "2"); code != 0 {
			t.Error("a free --rev was refused")
		}
		r.git(t, "tag", "ag/v0.6.2-ag.3", "HEAD")
		if out, code := r.run(t, "--rev", "3"); code != 0 {
			t.Errorf("rebuilding the tagged commit was refused:\n%s", out)
		}
	})
}
