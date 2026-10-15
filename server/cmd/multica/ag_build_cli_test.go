package main

// AG patch: ag-patches/build-cli.sh tests. The script runs against a throwaway
// git repo with a stub `go` first on PATH, so nothing is compiled. Names are
// TestAGPatch* so ag-patches/test.sh picks them up.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type agBuildCLIRepo struct {
	dir     string
	binDir  string
	goLog   string
	testLog string
}

// newAGBuildCLIRepo makes a repo on branch ag/v0.6.2 with the upstream tag
// v0.6.2 on an ancestor commit, a stub ag-patches/test.sh and a stub go.
func newAGBuildCLIRepo(t *testing.T, testShExit int) *agBuildCLIRepo {
	t.Helper()
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	r := &agBuildCLIRepo{dir: t.TempDir(), binDir: t.TempDir()}
	r.goLog = filepath.Join(r.binDir, "go.log")
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

	r.git(t, "init", "-q", "-b", "main")
	r.write(t, "README", "x")
	r.write(t, "server/.keep", "")
	r.git(t, "add", "README", "server/.keep")
	r.git(t, "commit", "-q", "-m", "upstream")
	r.git(t, "tag", "v0.6.2")
	r.git(t, "checkout", "-q", "-b", "ag/v0.6.2")
	r.write(t, "ag-patches/test.sh", "#!/bin/sh\necho ran >> \""+r.testLog+"\"\nexit "+string(rune('0'+testShExit))+"\n")
	if err := os.Chmod(filepath.Join(r.dir, "ag-patches/test.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(t, "add", "ag-patches/test.sh")
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

// run executes build-cli.sh and returns combined output and the exit code.
func (r *agBuildCLIRepo) run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	script, err := filepath.Abs("../../../ag-patches/build-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("build-cli.sh not found: %v", err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
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
}
