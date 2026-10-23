package repocache

// AG patch: further cross-process repo cache lock tests; TestAGPatch* as
// counted by ag-patches/test.sh. The first one re-executes the test binary as a
// real second process.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	agRepoHelperRootEnv = "AG_REPO_HELPER_ROOT"
	agRepoHelperPrefix  = "AGREPO "
)

// TestAGPatchRepoLockHelperProcess holds a repo lock until its stdin closes. In
// a normal run it does nothing.
func TestAGPatchRepoLockHelperProcess(t *testing.T) {
	root := os.Getenv(agRepoHelperRootEnv)
	if root == "" {
		t.Skip("helper process body")
	}
	cache := New(root, testLogger())
	err := cache.WithRepoLockContext(context.Background(), filepath.Join(root, "repo.git"), func() error {
		fmt.Fprintf(os.Stdout, "%sheld\n", agRepoHelperPrefix)
		_, _ = io.Copy(io.Discard, os.Stdin)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stdout, "%serror %v\n", agRepoHelperPrefix, err)
	}
}

func startAGRepoHelper(t *testing.T, root string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAGPatchRepoLockHelperProcess$")
	cmd.Env = append(os.Environ(), agRepoHelperRootEnv+"="+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, agRepoHelperPrefix) {
			if msg := strings.TrimPrefix(line, agRepoHelperPrefix); msg != "held" {
				t.Fatalf("helper failed: %s", msg)
			}
			go io.Copy(io.Discard, stdout)
			return cmd
		}
	}
	t.Fatal("helper exited without taking the repo lock")
	return nil
}

func TestAGPatchRepoLockExcludesAnotherProcess(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	helper := startAGRepoHelper(t, root)

	cache := New(root, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ran := false
	err := cache.WithRepoLockContext(ctx, barePath, func() error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("while another process holds the repo: err=%v ran=%v", err, ran)
	}
	if ran, err := cache.WithRepoMaintenance(context.Background(), barePath, func(context.Context) error {
		t.Error("maintenance ran while another process holds the repo")
		return nil
	}); ran || err != nil {
		t.Fatalf("maintenance: ran=%v err=%v; want skipped", ran, err)
	}

	// A process that dies without cleaning up releases the lock (kernel flock).
	_ = helper.Process.Kill()
	_ = helper.Wait()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := cache.WithRepoLockContext(ctx2, barePath, func() error { return nil }); err != nil {
		t.Fatalf("after the other process died: %v", err)
	}
}

// A foreground waiter still preempts in-process maintenance, and the flock moves
// from the cancelled maintenance to the foreground operation without a gap in
// which another daemon could slip in.
func TestAGPatchRepoLockForegroundPreemptsMaintenanceKeepsFlock(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	daemonA, daemonB := New(root, testLogger()), New(root, testLogger())

	started := make(chan struct{})
	maintDone := make(chan error, 1)
	var cause error
	go func() {
		_, err := daemonA.WithRepoMaintenance(context.Background(), barePath, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			cause = context.Cause(ctx)
			return nil
		})
		maintDone <- err
	}()
	<-started

	// Another daemon can't have the repo while A's maintenance runs.
	ctxB, cancelB := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelB()
	if err := daemonB.WithRepoLockContext(ctxB, barePath, func() error { return nil }); err == nil {
		t.Fatal("B got the repo while A's maintenance held it")
	}

	fgRan := false
	if err := daemonA.WithRepoLockContext(context.Background(), barePath, func() error {
		fgRan = true
		// Still exclusive against B while A's foreground work runs.
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := daemonB.WithRepoLockContext(ctx, barePath, func() error { return nil }); err == nil {
			t.Error("B got the repo during A's foreground work")
		}
		return nil
	}); err != nil || !fgRan {
		t.Fatalf("foreground: ran=%v err=%v", fgRan, err)
	}
	if err := <-maintDone; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(cause, ErrMaintenancePreempted) {
		t.Fatalf("maintenance cancel cause = %v, want ErrMaintenancePreempted", cause)
	}
	if err := daemonB.WithRepoLockContext(context.Background(), barePath, func() error { return nil }); err != nil {
		t.Fatalf("B after both finished: %v", err)
	}
}

// The flock is dropped when the guarded function fails, and the lock file sits
// beside the repo (surviving a wipe of the repo directory) rather than in it.
func TestAGPatchRepoLockReleasedOnErrorAndLivesBesideRepo(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	daemonA, daemonB := New(root, testLogger()), New(root, testLogger())

	boom := errors.New("fetch failed")
	if err := daemonA.WithRepoLockContext(context.Background(), barePath, func() error {
		if err := os.MkdirAll(barePath, 0o755); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the function's error", err)
	}
	lockPath := agRepoFlockPath(barePath)
	if filepath.Dir(lockPath) != root || strings.HasPrefix(lockPath, barePath+string(filepath.Separator)) {
		t.Fatalf("lock file %s must sit beside %s, not inside it", lockPath, barePath)
	}
	if err := os.RemoveAll(barePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should survive removing the repo: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := daemonB.WithRepoLockContext(ctx, barePath, func() error { return nil }); err != nil {
		t.Fatalf("B after A's failed operation: %v", err)
	}
}
