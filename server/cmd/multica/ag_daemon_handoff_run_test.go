package main

// AG patch: handoff and the plain-start guard driven through
// runDaemonForeground, the only caller of agInstallHandoffHook and
// agRefuseWhileHandoffDraining.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	logger_pkg "github.com/multica-ai/multica/server/internal/logger"
)

// agRunEnv is a profile with a fake multica server, a fake claude on PATH and
// automatic update checks off, enough for runDaemonForeground to start.
type agRunEnv struct {
	profile string
	mu      sync.Mutex
	paths   []string
}

func (e *agRunEnv) count(suffix string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, p := range e.paths {
		if strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

func newAGRunEnv(t *testing.T, profile string) *agRunEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell and signals")
	}
	if logger_pkg.StderrIsTerminal() {
		t.Skip("the daemon logs to the terminal, not to daemon.log, when stderr is one")
	}
	mkProfiles(t, profile)
	e := &agRunEnv{profile: profile}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho '2.5.0 (Claude Code)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "false")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.paths = append(e.paths, r.URL.Path)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/daemon/workspaces":
			_, _ = w.Write([]byte(`[{"id":"ws-1","name":"one"}]`))
		case "/api/daemon/register":
			_, _ = w.Write([]byte(`{"runtimes":[{"id":"rt-1","name":"claude","provider":"claude","status":"online"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{ServerURL: srv.URL, Token: "mul_test_token"}, profile); err != nil {
		t.Fatalf("save cli config: %v", err)
	}
	return e
}

func (e *agRunEnv) cmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("profile", e.profile, "")
	return cmd
}

func agWaitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAGPatchHandoffHookThroughForegroundStart(t *testing.T) {
	const profile = "aghandoff-run"
	e := newAGRunEnv(t, profile)

	done := make(chan error, 1)
	go func() { done <- runDaemonForeground(e.cmd(t)) }()

	var port int
	agWaitCond(t, "the daemon to report running", func() bool {
		rec, ok := readDaemonPortRecord(profile)
		if !ok {
			return false
		}
		port = rec.Port
		h := agHealth(port)
		return daemonAlive(h) && h["status"] == "running"
	})

	if lock, err := acquireDaemonProfileLock(profile, false); err == nil {
		lock.Close()
		t.Fatal("control: the profile lock was free while the daemon ran, so the release below proves nothing")
	}
	if _, ok := agReadHandoffRecord(profile); ok {
		t.Fatal("control: a handoff record existed before handoff began")
	}

	if err := agHandoffBegin(port, io.Discard); err != nil {
		t.Fatalf("handoff: %v", err)
	}

	t.Run("AG patch: handoff releases the profile lock for the successor", func(t *testing.T) {
		lock, err := acquireDaemonProfileLock(profile, false)
		if err != nil {
			t.Fatalf("the successor could not take daemon.lock after handoff began: %v", err)
		}
		lock.Close()
	})

	t.Run("AG patch: handoff leaves a record naming this daemon's pid and port", func(t *testing.T) {
		rec, ok := agReadHandoffRecord(profile)
		if !ok || rec.PID != os.Getpid() || rec.Port != port {
			t.Fatalf("handoff record = %+v (ok=%v), want pid %d port %d", rec, ok, os.Getpid(), port)
		}
	})

	// Stop the daemon the way an operator would; its shutdown logs after the
	// switch.
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := self.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDaemonForeground: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not stop")
	}

	t.Run("AG patch: handoff moves later log output off daemon.log", func(t *testing.T) {
		mainLog, _ := os.ReadFile(daemonLogPathForProfile(profile))
		handoffLog, err := os.ReadFile(agHandoffLogPath(profile, os.Getpid()))
		if err != nil {
			t.Fatalf("no handoff log: %v", err)
		}
		const after = "leaving runtimes registered for the successor"
		if !strings.Contains(string(handoffLog), after) {
			t.Errorf("handoff log lacks the post-handoff line %q:\n%s", after, handoffLog)
		}
		if strings.Contains(string(mainLog), after) {
			t.Errorf("daemon.log still receives post-handoff output, so two writers would share it:\n%s", mainLog)
		}
		if !strings.Contains(string(mainLog), "starting daemon") {
			t.Errorf("daemon.log lost the pre-handoff output:\n%s", mainLog)
		}
	})

	t.Run("AG patch: a handoff daemon leaves its runtimes registered on exit", func(t *testing.T) {
		if got := e.count("/api/daemon/deregister"); got != 0 {
			t.Fatalf("deregister called %d times by a handoff daemon", got)
		}
	})

	t.Run("AG patch: the handoff record is removed when the daemon exits", func(t *testing.T) {
		if _, err := os.Stat(agHandoffRecordPath(profile)); !os.IsNotExist(err) {
			t.Fatalf("handoff record outlived its daemon (stat err %v)", err)
		}
	})
}

func TestAGPatchPlainStartRefusedBesideDrainingHandoffDaemon(t *testing.T) {
	const profile = "aghandoff-plain"

	// A handoff daemon (the parent process stands in: alive, and not this one)
	// on a port that is not the profile's default one, as a second-generation
	// daemon would be.
	drainingPID := os.Getppid()
	startDraining := func(t *testing.T, e *agRunEnv, healthPID int) int {
		t.Helper()
		f := &agHandoffFake{pid: healthPID, handoff: true, active: 2}
		port := startAGHandoffFake(t, profile, f)
		if port == healthPortForProfile(profile) {
			t.Skip("the fake landed on the profile's default port")
		}
		agWriteHandoffRecord(profile, agHandoffRecord{PID: drainingPID, Port: port})
		return port
	}

	t.Run("AG patch: a plain start beside a live handoff daemon neither starts nor recovers orphans", func(t *testing.T) {
		e := newAGRunEnv(t, profile)
		port := startDraining(t, e, drainingPID)

		// Without the guard this would be a real daemon that runs until
		// signalled, so bound it and stop it rather than hang the suite.
		started := make(chan error, 1)
		go func() { started <- runDaemonForeground(e.cmd(t)) }()
		var err error
		select {
		case err = <-started:
		case <-time.After(10 * time.Second):
			self, _ := os.FindProcess(os.Getpid())
			_ = self.Signal(os.Interrupt)
			<-started
			t.Fatal("a plain start beside a live handoff daemon started a daemon")
		}
		if err == nil || !strings.Contains(err.Error(), "has handed off and is still draining") {
			t.Fatalf("plain start error = %v, want it to refuse while the handoff daemon drains", err)
		}
		if got := e.count("/recover-orphans"); got != 0 {
			t.Fatalf("recover-orphans called %d times, which fails the draining daemon's tasks", got)
		}
		if got := e.count("/api/daemon/register"); got != 0 {
			t.Fatalf("a refused start still registered runtimes (%d calls)", got)
		}
		if rec, _ := readDaemonPortRecord(profile); rec.Port != port {
			t.Fatalf("a refused start overwrote the port file: %+v, want port %d", rec, port)
		}
		if _, ok := agReadHandoffRecord(profile); !ok {
			t.Fatal("a refused start removed the live handoff daemon's record")
		}
	})

	t.Run("AG patch: a handoff record whose pid is gone is stale and the start proceeds", func(t *testing.T) {
		mkProfiles(t, profile)
		agWriteHandoffRecord(profile, agHandoffRecord{PID: agDeadPID(t), Port: 1})
		if err := agRefuseWhileHandoffDraining(profile); err != nil {
			t.Fatalf("refused for a dead pid: %v", err)
		}
		if _, err := os.Stat(agHandoffRecordPath(profile)); !os.IsNotExist(err) {
			t.Fatalf("stale record not removed (stat err %v)", err)
		}
	})

	t.Run("AG patch: a handoff record whose port is answered by another process is stale", func(t *testing.T) {
		e := newAGRunEnv(t, profile)
		startDraining(t, e, os.Getpid()) // answers with a pid other than the record's
		if err := agRefuseWhileHandoffDraining(profile); err != nil {
			t.Fatalf("refused although another process answers on the record's port: %v", err)
		}
		if _, err := os.Stat(agHandoffRecordPath(profile)); !os.IsNotExist(err) {
			t.Fatalf("stale record not removed (stat err %v)", err)
		}
	})

	t.Run("AG patch: a live pid with nothing answering on its port still refuses", func(t *testing.T) {
		mkProfiles(t, profile)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		agWriteHandoffRecord(profile, agHandoffRecord{PID: drainingPID, Port: port})
		if err := agRefuseWhileHandoffDraining(profile); err == nil {
			t.Fatal("a start was allowed beside a live pid whose health could not be read")
		}
	})

	t.Run("AG patch: no record, an unreadable record or this process's own record never refuse", func(t *testing.T) {
		mkProfiles(t, profile)
		if err := agRefuseWhileHandoffDraining(profile); err != nil {
			t.Fatalf("refused with no record: %v", err)
		}
		if err := os.WriteFile(agHandoffRecordPath(profile), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := agRefuseWhileHandoffDraining(profile); err != nil {
			t.Fatalf("refused on an unreadable record: %v", err)
		}
		agWriteHandoffRecord(profile, agHandoffRecord{PID: os.Getpid(), Port: 1})
		if err := agRefuseWhileHandoffDraining(profile); err != nil {
			t.Fatalf("refused on its own record: %v", err)
		}
	})

	t.Run("AG patch: only the owner removes the handoff record", func(t *testing.T) {
		mkProfiles(t, profile)
		agWriteHandoffRecord(profile, agHandoffRecord{PID: drainingPID, Port: 1})
		agRemoveHandoffRecordIfOwned(profile, os.Getpid())
		if _, ok := agReadHandoffRecord(profile); !ok {
			t.Fatal("a daemon removed another daemon's record")
		}
		agRemoveHandoffRecordIfOwned(profile, drainingPID)
		if _, ok := agReadHandoffRecord(profile); ok {
			t.Fatal("the owner did not remove its record")
		}
	})
}
