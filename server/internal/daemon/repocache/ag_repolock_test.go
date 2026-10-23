package repocache

// AG patch: cross-process repo cache lock tests; TestAGPatch* as counted by
// ag-patches/test.sh. Two Cache values over one directory stand in for two
// daemons: each has its own in-process repoLock, and flock conflicts between
// separate open file descriptions even inside one process.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func skipWithoutRepoFlock(t *testing.T) {
	t.Helper()
	if !agRepoFlockSupported {
		t.Skip("flock not available on this platform")
	}
}

func TestAGPatchRepoLockExcludesAnotherDaemon(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	daemonA, daemonB := New(root, testLogger()), New(root, testLogger())

	held := make(chan struct{})
	release := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- daemonA.WithRepoLockContext(context.Background(), barePath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// B can't take the repo while A holds it, and never runs its function.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ran := false
	err := daemonB.WithRepoLockContext(ctx, barePath, func() error { ran = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("B while A holds the repo: err=%v ran=%v; want a deadline and no run", err, ran)
	}

	// A different repo is independent.
	if err := daemonB.WithRepoLockContext(context.Background(), filepath.Join(root, "other.git"), func() error { return nil }); err != nil {
		t.Fatalf("B on another repo: %v", err)
	}

	// Once A lets go, B gets in. B's failed attempt left nothing locked.
	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if err := daemonB.WithRepoLockContext(context.Background(), barePath, func() error { return nil }); err != nil {
		t.Fatalf("B after A released: %v", err)
	}
}

func TestAGPatchRepoLockMaintenanceYieldsToAnotherDaemon(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	daemonA, daemonB := New(root, testLogger()), New(root, testLogger())

	held := make(chan struct{})
	release := make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- daemonA.WithRepoLockContext(context.Background(), barePath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ran, err := daemonB.WithRepoMaintenance(context.Background(), barePath, func(context.Context) error {
		t.Error("maintenance ran while another daemon holds the repo")
		return nil
	})
	if ran || err != nil {
		t.Fatalf("maintenance on a busy repo: ran=%v err=%v; want skipped", ran, err)
	}

	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	ran, err = daemonB.WithRepoMaintenance(context.Background(), barePath, func(context.Context) error { return nil })
	if !ran || err != nil {
		t.Fatalf("maintenance on an idle repo: ran=%v err=%v", ran, err)
	}
}

func TestAGPatchRepoLockFailsClosedWhenLockFileUnusable(t *testing.T) {
	skipWithoutRepoFlock(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "repo.git")
	// A directory where the lock file should be: it can't be opened read-write.
	if err := os.Mkdir(agRepoFlockPath(barePath), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := New(root, testLogger())

	for i := 0; i < 2; i++ { // twice: the failed attempt must not leave the in-process lock held
		ran := false
		err := cache.WithRepoLockContext(context.Background(), barePath, func() error { ran = true; return nil })
		if err == nil || ran {
			t.Fatalf("attempt %d: err=%v ran=%v; want an error and no run", i, err, ran)
		}
	}
	ran, err := cache.WithRepoMaintenance(context.Background(), barePath, func(context.Context) error {
		t.Error("maintenance ran without the repo flock")
		return nil
	})
	if ran || err != nil {
		t.Fatalf("maintenance: ran=%v err=%v; want skipped", ran, err)
	}
	if err := cache.SyncContext(context.Background(), "ws", []RepoInfo{{URL: "https://example.invalid/org/repo.git"}}); err == nil {
		t.Fatal("SyncContext must fail rather than run git unlocked")
	}
}
