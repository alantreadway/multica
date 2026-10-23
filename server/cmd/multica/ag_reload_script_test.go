package main

// AG patch: tests for ag-patches/multica-daemon-reload, run against stub
// launchctl, plutil and multica binaries so nothing real is touched.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const agReloadBase = "uk.test.multica-daemon"

func agReloadScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "ag-patches", "multica-daemon-reload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("reload script: %v", err)
	}
	return p
}

func TestAGPatchReloadScriptStatic(t *testing.T) {
	script := agReloadScriptPath(t)
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	t.Run("AG patch: the script is executable and parses", func(t *testing.T) {
		info, err := os.Stat(script)
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("script mode = %v, err = %v; want executable", info.Mode(), err)
		}
		if bash, err := exec.LookPath("bash"); err == nil {
			if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
				t.Fatalf("bash -n: %v\n%s", err, out)
			}
		}
	})

	t.Run("AG patch: it alternates between the .a and .b labels", func(t *testing.T) {
		if !strings.Contains(text, `LABELS=("$BASE_LABEL.a" "$BASE_LABEL.b")`) {
			t.Error("script does not define the two .a/.b labels")
		}
	})

	t.Run("AG patch: it only boots out a finished job, never a draining one", func(t *testing.T) {
		var bootouts []string
		for i, line := range strings.Split(text, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "#") || !strings.Contains(line, "launchctl bootout") {
				continue
			}
			// Prose in the usage text mentions bootout without running it.
			if strings.Contains(line, `launchctl bootout "$DOMAIN/`) {
				bootouts = append(bootouts, strconv.Itoa(i+1)+": "+trim)
			}
		}
		// Exactly two real calls: clearing the finished next job, and removing the
		// old job after --wait saw its process exit.
		if len(bootouts) != 2 {
			t.Fatalf("bootout call sites = %v, want exactly 2 (finished nxt, and cur after --wait)", bootouts)
		}
		waitAt := strings.Index(text, "while kill -0")
		curBootout := strings.Index(text, `launchctl bootout "$DOMAIN/$cur"`)
		if waitAt < 0 || curBootout < waitAt {
			t.Error(`bootout of "$cur" must come after the kill -0 wait loop`)
		}
	})

	t.Run("AG patch: the README documents the plists the script expects", func(t *testing.T) {
		readme, err := os.ReadFile(filepath.Join(filepath.Dir(script), "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"multica-daemon-reload", "--overlap", ".a", ".b", "overlap-next"} {
			if !strings.Contains(string(readme), want) {
				t.Errorf("ag-patches/README.md does not mention %q", want)
			}
		}
	})
}

type agReloadEnv struct {
	t      *testing.T
	script string
	dir    string // stub state
	agents string // plist directory
	home   string
	marker string
	uid    string
}

const agReloadStubLaunchctl = `#!/bin/sh
echo "launchctl $*" >> "$STUB_DIR/calls.log"
case "$1" in
  print)
    label=${2##*/}
    [ -f "$STUB_DIR/state/$label" ] || exit 113
    pid=$(cat "$STUB_DIR/state/$label")
    case "$pid" in
      ''|loaded) echo "gui/501/$label = {"; echo "	state = not running" ;;
      *) echo "gui/501/$label = {"; echo "	pid = $pid" ;;
    esac
    ;;
  bootstrap)
    label=$(basename "$3" .plist)
    if [ -e "$MULTICA_DAEMON_OVERLAP_MARKER" ]; then echo "marker-present-at-bootstrap $label" >> "$STUB_DIR/calls.log"; else echo "marker-absent-at-bootstrap $label" >> "$STUB_DIR/calls.log"; fi
    echo 4242 > "$STUB_DIR/state/$label"
    [ -f "$STUB_DIR/status-after.json" ] && cp "$STUB_DIR/status-after.json" "$STUB_DIR/status.json"
    ;;
  bootout)
    label=${2##*/}
    rm -f "$STUB_DIR/state/$label"
    ;;
esac
exit 0
`

const agReloadStubMultica = `#!/bin/sh
echo "multica $*" >> "$STUB_DIR/calls.log"
if [ "$1" = daemon ] && [ "$2" = status ]; then
  cat "$STUB_DIR/status.json" 2>/dev/null
  exit 0
fi
if [ "$1" = daemon ] && [ "$2" = drain ]; then
  exit "$(cat "$STUB_DIR/drain-exit" 2>/dev/null || echo 0)"
fi
exit 0
`

func newAGReloadEnv(t *testing.T) *agReloadEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is needed to run the reload script", tool)
		}
	}
	root := t.TempDir()
	e := &agReloadEnv{
		t:      t,
		script: agReloadScriptPath(t),
		dir:    filepath.Join(root, "stub"),
		agents: filepath.Join(root, "agents"),
		home:   filepath.Join(root, "home"),
		marker: filepath.Join(root, "home", ".multica", "overlap-next"),
	}
	for _, d := range []string{filepath.Join(e.dir, "state"), filepath.Join(e.dir, "bin"), e.agents, e.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.write(filepath.Join(e.dir, "bin", "launchctl"), agReloadStubLaunchctl, 0o755)
	e.write(filepath.Join(e.dir, "bin", "plutil"), "#!/bin/sh\nexit 0\n", 0o755)
	e.write(filepath.Join(e.dir, "bin", "multica"), agReloadStubMultica, 0o755)
	for _, l := range []string{"a", "b"} {
		e.write(filepath.Join(e.agents, agReloadBase+"."+l+".plist"),
			"<!-- Max 2 concurrent runs machine-wide -->\n<string>MULTICA_DAEMON_MAX_CONCURRENT_TASKS=2</string>\n", 0o644)
	}
	return e
}

func (e *agReloadEnv) write(path, content string, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
}

// running marks label as a running launchd job with the given PID ("loaded" for
// a job with no process).
func (e *agReloadEnv) running(label, pid string) {
	e.write(filepath.Join(e.dir, "state", agReloadBase+"."+label), pid, 0o644)
}

func (e *agReloadEnv) status(before, after string) {
	e.write(filepath.Join(e.dir, "status.json"), before, 0o644)
	if after != "" {
		e.write(filepath.Join(e.dir, "status-after.json"), after, 0o644)
	}
}

func (e *agReloadEnv) calls() []string {
	b, _ := os.ReadFile(filepath.Join(e.dir, "calls.log"))
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" && !strings.HasPrefix(l, "launchctl print ") && !strings.HasPrefix(l, "multica daemon status") {
			out = append(out, l)
		}
	}
	return out
}

func (e *agReloadEnv) run(args ...string) (stdout string, code int) {
	e.t.Helper()
	cmd := exec.Command("bash", append([]string{e.script}, args...)...)
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MULTICA_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"PATH="+filepath.Join(e.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+e.home,
		"STUB_DIR="+e.dir,
		"MULTICA_BIN="+filepath.Join(e.dir, "bin", "multica"),
		"MULTICA_DAEMON_LABEL_BASE="+agReloadBase,
		"MULTICA_DAEMON_AGENTS_DIR="+e.agents,
		"MULTICA_DAEMON_OVERLAP_MARKER="+e.marker,
		"MULTICA_DAEMON_READY_WAIT=4",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code = 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("run script: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), code
}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

const (
	agReloadOldStatus = `{"status":"running","pid":111,"max_concurrent_tasks":2}`
	agReloadNewStatus = `{"status":"running","pid":4242,"max_concurrent_tasks":2,"predecessor_pid":111}`
)

func TestAGPatchReloadScriptHandoff(t *testing.T) {
	t.Run("AG patch: a running .a is handed off, then .b is bootstrapped with the overlap marker set", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadNewStatus)

		out, code := e.run()
		if code != 0 || !strings.Contains(out, "OK") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		calls := e.calls()
		drain, boot := indexOf(calls, "multica daemon drain --handoff"), indexOf(calls, "launchctl bootstrap")
		if drain < 0 || boot < 0 || drain > boot {
			t.Fatalf("calls = %v, want drain --handoff before bootstrap", calls)
		}
		if !strings.HasSuffix(calls[boot], agReloadBase+".b.plist") {
			t.Errorf("bootstrapped %q, want the .b plist", calls[boot])
		}
		if !slices.Contains(calls, "marker-present-at-bootstrap "+agReloadBase+".b") {
			t.Errorf("the overlap marker was not set when .b started: %v", calls)
		}
		if indexOf(calls, "launchctl bootout") >= 0 {
			t.Errorf("a label was booted out, which kills the draining daemon's agents: %v", calls)
		}
		if _, err := os.Stat(e.marker); !os.IsNotExist(err) {
			t.Error("the overlap marker was left behind")
		}
		if !strings.Contains(out, "Do NOT 'launchctl bootout") {
			t.Errorf("output should warn against booting out the draining label:\n%s", out)
		}
	})

	t.Run("AG patch: a running .b is handed off to .a", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("b", "222")
		e.status(`{"status":"running","pid":222,"max_concurrent_tasks":2}`, agReloadNewStatus)

		out, code := e.run()
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		calls := e.calls()
		if boot := indexOf(calls, "launchctl bootstrap"); boot < 0 || !strings.HasSuffix(calls[boot], agReloadBase+".a.plist") {
			t.Fatalf("calls = %v, want .a bootstrapped", calls)
		}
		if indexOf(calls, "launchctl bootout") >= 0 {
			t.Errorf("unexpected bootout: %v", calls)
		}
	})

	t.Run("AG patch: with nothing running .a is started normally, with no handoff and no marker", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.status(`{"status":"stopped"}`, agReloadNewStatus)

		out, code := e.run()
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		calls := e.calls()
		if indexOf(calls, "multica daemon drain") >= 0 {
			t.Errorf("cold start drained something: %v", calls)
		}
		if !slices.Contains(calls, "marker-absent-at-bootstrap "+agReloadBase+".a") {
			t.Errorf("a cold start must not be an overlap start: %v", calls)
		}
	})

	t.Run("AG patch: a finished job on the next label is cleared first, never the running one", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.running("b", "loaded") // the previous generation, exited
		e.status(agReloadOldStatus, agReloadNewStatus)

		out, code := e.run()
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		calls := e.calls()
		bootout := indexOf(calls, "launchctl bootout")
		if bootout < 0 || !strings.HasSuffix(calls[bootout], agReloadBase+".b") {
			t.Fatalf("calls = %v, want only the finished .b booted out", calls)
		}
		if bootout > indexOf(calls, "launchctl bootstrap") {
			t.Errorf("the finished job must be cleared before bootstrap: %v", calls)
		}
		for _, c := range calls {
			if strings.HasPrefix(c, "launchctl bootout") && strings.HasSuffix(c, ".a") {
				t.Errorf("the running label was booted out: %v", calls)
			}
		}
	})

	t.Run("AG patch: both labels running means a handoff is still draining; nothing is touched", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.running("b", "222")
		e.status(agReloadOldStatus, "")

		out, code := e.run()
		if code != 1 || !strings.Contains(out, "previous handoff is still draining") {
			t.Fatalf("exit %d, want 1 with the draining message:\n%s", code, out)
		}
		if calls := e.calls(); len(calls) != 0 {
			t.Fatalf("calls = %v, want none", calls)
		}
	})

	t.Run("AG patch: --dry-run changes nothing", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadNewStatus)

		out, code := e.run("--dry-run")
		if code != 0 || !strings.Contains(out, "Dry run") || !strings.Contains(out, "--overlap") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if calls := e.calls(); len(calls) != 0 {
			t.Fatalf("dry run made calls: %v", calls)
		}
	})

	t.Run("AG patch: a failed handoff stops before the next daemon starts", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadNewStatus)
		e.write(filepath.Join(e.dir, "drain-exit"), "1", 0o644)

		out, code := e.run()
		if code != 1 || !strings.Contains(out, "handoff failed") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if indexOf(e.calls(), "launchctl bootstrap") >= 0 {
			t.Fatalf("bootstrapped after a failed handoff: %v", e.calls())
		}
	})

	t.Run("AG patch: a new daemon that never reports running fails and leaves the old one handing off", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadOldStatus) // still reports the old pid

		out, code := e.run()
		if code != 1 || !strings.Contains(out, "did not report running") || !strings.Contains(out, "exits once its runs finish") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if indexOf(e.calls(), "launchctl bootout") >= 0 {
			t.Errorf("a failure booted out a label: %v", e.calls())
		}
	})
}

func TestAGPatchReloadScriptGuards(t *testing.T) {
	t.Run("AG patch: both plists are required", func(t *testing.T) {
		e := newAGReloadEnv(t)
		if err := os.Remove(filepath.Join(e.agents, agReloadBase+".b.plist")); err != nil {
			t.Fatal(err)
		}
		out, code := e.run()
		if code != 2 || !strings.Contains(out, "plist not found") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	t.Run("AG patch: it refuses to run inside an agent run", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadNewStatus)
		// run() strips MULTICA_* from the environment; put the guard variable back.
		cmd := exec.Command("bash", e.script)
		cmd.Env = append(os.Environ(), "MULTICA_TASK_ID=task-1", "STUB_DIR="+e.dir,
			"PATH="+filepath.Join(e.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
			"MULTICA_DAEMON_AGENTS_DIR="+e.agents, "MULTICA_DAEMON_LABEL_BASE="+agReloadBase, "HOME="+e.home)
		out, err := cmd.CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 || !strings.Contains(string(out), "inside a Multica agent run") {
			t.Fatalf("err = %v:\n%s", err, out)
		}
		if calls := e.calls(); len(calls) != 0 {
			t.Fatalf("calls = %v, want none", calls)
		}
	})

	t.Run("AG patch: bad arguments are usage errors", func(t *testing.T) {
		e := newAGReloadEnv(t)
		for _, args := range [][]string{{"--bogus"}, {"--cap"}, {"--cap", "0"}, {"--cap", "x"}} {
			if out, code := e.run(args...); code != 2 {
				t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
			}
		}
	})
}

func TestAGPatchReloadScriptCapChange(t *testing.T) {
	t.Run("AG patch: a new cap waits for the old daemon to exit instead of overlapping", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, `{"status":"running","pid":4242,"max_concurrent_tasks":4}`)

		out, code := e.run("--cap", "4")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		calls := e.calls()
		if indexOf(calls, "multica daemon drain --handoff --wait") < 0 {
			t.Fatalf("calls = %v, want drain --handoff --wait", calls)
		}
		if drain, boot := indexOf(calls, "multica daemon drain"), indexOf(calls, "launchctl bootstrap"); drain > boot {
			t.Errorf("bootstrap came before the old daemon exited: %v", calls)
		}
		if !slices.Contains(calls, "marker-absent-at-bootstrap "+agReloadBase+".b") {
			t.Errorf("a changed cap must not start in overlap mode: %v", calls)
		}
		for _, l := range []string{"a", "b"} {
			plist, _ := os.ReadFile(filepath.Join(e.agents, agReloadBase+"."+l+".plist"))
			if !strings.Contains(string(plist), "MULTICA_DAEMON_MAX_CONCURRENT_TASKS=4") || !strings.Contains(string(plist), "raised from 2") {
				t.Errorf("%s plist not updated:\n%s", l, plist)
			}
		}
		backups, _ := filepath.Glob(filepath.Join(e.home, ".multica", "*.plist.bak-*"))
		if len(backups) != 2 {
			t.Errorf("backups = %v, want one per plist", backups)
		}
	})

	t.Run("AG patch: the same cap still overlaps", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, agReloadNewStatus)

		if out, code := e.run("--cap", "2"); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if !slices.Contains(e.calls(), "marker-present-at-bootstrap "+agReloadBase+".b") {
			t.Errorf("an unchanged cap should overlap: %v", e.calls())
		}
	})

	t.Run("AG patch: --dry-run --cap shows the plist diff and writes nothing", func(t *testing.T) {
		e := newAGReloadEnv(t)
		e.running("a", "111")
		e.status(agReloadOldStatus, "")

		out, code := e.run("--cap", "4", "--dry-run")
		if code != 0 || !strings.Contains(out, "cap 2 -> 4") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		plist, _ := os.ReadFile(filepath.Join(e.agents, agReloadBase+".a.plist"))
		if strings.Contains(string(plist), "=4") {
			t.Error("dry run edited a plist")
		}
	})
}

func TestAGPatchReloadScriptWaitBootsOutOnlyAfterExit(t *testing.T) {
	e := newAGReloadEnv(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("needs sleep")
	}
	old := exec.Command(sleep, "3")
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	exitedAt := make(chan time.Time, 1)
	go func() { _ = old.Wait(); exitedAt <- time.Now() }() // reaped, so kill -0 sees it gone
	t.Cleanup(func() { _ = old.Process.Kill() })

	pid := strconv.Itoa(old.Process.Pid)
	e.running("a", pid)
	e.status(`{"status":"running","pid":`+pid+`,"max_concurrent_tasks":2}`, agReloadNewStatus)

	out, code := e.run("--wait")
	done := time.Now()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if exit := <-exitedAt; done.Before(exit) {
		t.Fatalf("--wait returned at %s, before the old daemon exited at %s", done, exit)
	}
	calls := e.calls()
	boot, bootout := indexOf(calls, "launchctl bootstrap"), indexOf(calls, "launchctl bootout")
	if boot < 0 || bootout < 0 || bootout < boot || !strings.HasSuffix(calls[bootout], agReloadBase+".a") {
		t.Fatalf("calls = %v, want the old .a booted out after .b was bootstrapped", calls)
	}
}
