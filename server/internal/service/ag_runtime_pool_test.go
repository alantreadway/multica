package service

// These need the test database and are not run by ag-patches/test.sh; see its
// header. ag-patches/test-go-full.sh runs them.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// agPoolFixture is one workspace with a home runtime (the agent's runtime,
// "Bastion") and a second runtime ("laptop"), each optionally in a pool.
type agPoolFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	rows   *testutil.Fixture
	svc    *TaskService
	home   string
	laptop string
	agent  string
	name   string // the pool name both runtimes use unless a test says otherwise
}

type agPoolRuntime struct {
	pool string // "" = no pool
	over testutil.Cols
}

func newAGPoolFixture(t *testing.T, home, laptop agPoolRuntime) *agPoolFixture {
	t.Helper()
	pool := sharedTestPool(t)
	rows := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	user := rows.User(t, "AG Pool", fmt.Sprintf("ag-pool-%d@multica.ai", suffix))
	ws := rows.Workspace(t, "AG Pool", fmt.Sprintf("ag-pool-%d", suffix), testutil.Cols{"issue_prefix": "AGP"})
	rows.WorkspaceID, rows.UserID = ws, user
	rows.Member(t, ws, user, "owner")

	f := &agPoolFixture{ctx: context.Background(), pool: pool, rows: rows}
	f.home = f.runtime(t, "AG pool home", home)
	f.laptop = f.runtime(t, "AG pool laptop", laptop)
	f.agent = rows.Agent(t, "AG pool agent", f.home, testutil.Cols{"max_concurrent_tasks": 5})
	// Retries the code under test creates are not fixture rows.
	rows.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, ws)
	f.svc = NewTaskService(db.New(pool), pool, nil, events.New())
	return f
}

// agPoolName is unique per test so runtimes of different tests never pool.
func agPoolName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("ag-pool-%d", time.Now().UnixNano())
}

func (f *agPoolFixture) runtime(t *testing.T, name string, r agPoolRuntime) string {
	t.Helper()
	cols := testutil.Cols{}
	if r.pool != "" {
		cols["metadata"] = testutil.Raw(fmt.Sprintf(`'{"ag_pool": %q}'::jsonb`, r.pool))
	}
	for k, v := range r.over {
		cols[k] = v
	}
	return f.rows.Runtime(t, name, cols)
}

func (f *agPoolFixture) issue(t *testing.T) string {
	t.Helper()
	id := f.rows.Issue(t, "AG pool issue", testutil.Cols{"status": "in_progress"})
	f.rows.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
	return id
}

// queue queues a task for the agent on runtimeID.
func (f *agPoolFixture) queue(t *testing.T, runtimeID, issueID string, over ...testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID, "context": testutil.Raw(`'{}'::jsonb`)}
	for _, o := range over {
		for k, v := range o {
			cols[k] = v
		}
	}
	return f.rows.Task(t, f.agent, cols)
}

// finished records a run of the agent on issueID that ran on runtimeID and
// finished `ago` before now.
func (f *agPoolFixture) finished(t *testing.T, runtimeID, issueID string, ago time.Duration) string {
	t.Helper()
	return f.queue(t, runtimeID, issueID, testutil.Cols{
		"status":        "completed",
		"dispatched_at": testutil.Raw(fmt.Sprintf("now() - interval '%d seconds'", int(ago.Seconds())+60)),
		"started_at":    testutil.Raw(fmt.Sprintf("now() - interval '%d seconds'", int(ago.Seconds())+60)),
		"completed_at":  testutil.Raw(fmt.Sprintf("now() - interval '%d seconds'", int(ago.Seconds()))),
	})
}

func (f *agPoolFixture) task(t *testing.T, taskID string) (status, runtimeID string) {
	t.Helper()
	f.rows.QueryRow(t, `SELECT status, runtime_id::text FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status, &runtimeID)
	return status, runtimeID
}

func (f *agPoolFixture) claim(t *testing.T, runtimeID string) []db.AgentTaskQueue {
	t.Helper()
	got, err := f.svc.ClaimTasksForRuntimes(f.ctx, []pgtype.UUID{util.MustParseUUID(runtimeID)}, 5)
	if err != nil {
		t.Fatalf("claim on %s: %v", runtimeID, err)
	}
	return got
}

func agPoolIDs(tasks []db.AgentTaskQueue) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, util.UUIDToString(task.ID)+"@"+util.UUIDToString(task.RuntimeID))
	}
	return out
}

func TestAGPatchRuntimePoolPeerClaimsHomeTaskAndBindsIt(t *testing.T) {
	name := agPoolName(t)
	f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
	task := f.queue(t, f.home, f.issue(t))

	got := f.claim(t, f.laptop)
	if len(got) != 1 || util.UUIDToString(got[0].ID) != task || util.UUIDToString(got[0].RuntimeID) != f.laptop {
		t.Fatalf("laptop claimed %v, want %s on the laptop runtime", agPoolIDs(got), task)
	}
	if status, rt := f.task(t, task); status != "dispatched" || rt != f.laptop {
		t.Fatalf("task is %s on %s, want dispatched on the laptop %s", status, rt, f.laptop)
	}
}

func TestAGPatchRuntimePoolSingularClaimPath(t *testing.T) {
	name := agPoolName(t)
	f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
	task := f.queue(t, f.home, f.issue(t))

	got, err := f.svc.ClaimTaskForRuntime(f.ctx, util.MustParseUUID(f.laptop))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || util.UUIDToString(got.ID) != task || util.UUIDToString(got.RuntimeID) != f.laptop {
		t.Fatalf("laptop claimed %+v, want %s on the laptop runtime", got, task)
	}
}

func TestAGPatchRuntimePoolNonPeersKeepUpstreamFence(t *testing.T) {
	name := agPoolName(t)
	cases := map[string]struct {
		home, laptop agPoolRuntime
		otherOwner   bool
	}{
		"no pool on either runtime":  {},
		"pool on the claimer only":   {laptop: agPoolRuntime{pool: name}},
		"different pool names":       {home: agPoolRuntime{pool: name}, laptop: agPoolRuntime{pool: name + "-b"}},
		"different provider":         {home: agPoolRuntime{pool: name}, laptop: agPoolRuntime{pool: name, over: testutil.Cols{"provider": "ag_pool_other"}}},
		"different owner":            {home: agPoolRuntime{pool: name}, laptop: agPoolRuntime{pool: name}, otherOwner: true},
		"same pool, other workspace": {home: agPoolRuntime{pool: name}},
	}
	for label, c := range cases {
		t.Run("AG patch: "+label, func(t *testing.T) {
			f := newAGPoolFixture(t, c.home, c.laptop)
			switch {
			case c.otherOwner:
				other := f.rows.User(t, "AG Pool other", fmt.Sprintf("ag-pool-other-%d@multica.ai", time.Now().UnixNano()))
				f.rows.Exec(t, `UPDATE agent_runtime SET owner_id = $2 WHERE id = $1`, f.laptop, other)
			case label == "same pool, other workspace":
				ws := f.rows.Workspace(t, "AG Pool other", fmt.Sprintf("ag-pool-other-%d", time.Now().UnixNano()), testutil.Cols{"issue_prefix": "AGQ"})
				f.rows.Member(t, ws, f.rows.UserID, "owner")
				f.laptop = f.runtime(t, "AG pool other workspace", agPoolRuntime{pool: name, over: testutil.Cols{"workspace_id": ws}})
			}
			task := f.queue(t, f.home, f.issue(t))

			if got := f.claim(t, f.laptop); len(got) != 0 {
				t.Fatalf("non-peer claimed %v, want nothing", agPoolIDs(got))
			}
			if status, rt := f.task(t, task); status != "queued" || rt != f.home {
				t.Fatalf("task is %s on %s, want queued on home %s", status, rt, f.home)
			}
			got := f.claim(t, f.home)
			if len(got) != 1 || util.UUIDToString(got[0].RuntimeID) != f.home {
				t.Fatalf("home claimed %v, want the task on home", agPoolIDs(got))
			}
		})
	}
}

func TestAGPatchRuntimePoolOfflineRetryReturnsHome(t *testing.T) {
	name := agPoolName(t)
	f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
	issue := f.issue(t)
	// The laptop claimed and started a run, then its lid closed.
	running := f.queue(t, f.laptop, issue, testutil.Cols{
		"status":        "running",
		"dispatched_at": testutil.Raw("now() - interval '2 hours'"),
		"started_at":    testutil.Raw("now() - interval '2 hours'"),
		"max_attempts":  2,
	})
	f.rows.Exec(t, `UPDATE agent_runtime SET status = 'offline', last_seen_at = now() - interval '2 hours' WHERE id = $1`, f.laptop)
	// The offline sweep's own write (FailTasksForOfflineRuntimes), scoped to
	// this task: the query itself sweeps the whole shared test database.
	f.rows.Exec(t, `UPDATE agent_task_queue SET status = 'failed', completed_at = now(), error = 'runtime went offline',
		failure_reason = 'runtime_offline', wait_reason = NULL WHERE id = $1`, running)
	failed, err := f.svc.Queries.GetAgentTask(f.ctx, util.MustParseUUID(running))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.HandleFailedTasks(f.ctx, []db.AgentTaskQueue{failed})

	var retry, retryStatus, retryRuntime string
	f.rows.QueryRow(t, `SELECT id::text, status, runtime_id::text FROM agent_task_queue WHERE parent_task_id = $1`, running).Scan(&retry, &retryStatus, &retryRuntime)
	if retryStatus != "deferred" || retryRuntime != f.laptop {
		t.Fatalf("retry is %s on %s, want the upstream deferred retry on the laptop", retryStatus, retryRuntime)
	}
	// Past its fire_at: upstream would promote it only once the laptop is back.
	f.rows.Exec(t, `UPDATE agent_task_queue SET fire_at = now() - interval '1 second' WHERE id = $1`, retry)

	got := f.claim(t, f.home)
	if len(got) != 1 || util.UUIDToString(got[0].ID) != retry || util.UUIDToString(got[0].RuntimeID) != f.home {
		t.Fatalf("home claimed %v, want the retry %s on home", agPoolIDs(got), retry)
	}
}

func TestAGPatchRuntimePoolRequeuedTaskReturnsHome(t *testing.T) {
	name := agPoolName(t)
	f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
	// A failed claim finalization requeues the row on the claimer.
	task := f.queue(t, f.laptop, f.issue(t))

	got := f.claim(t, f.home)
	if len(got) != 1 || util.UUIDToString(got[0].ID) != task || util.UUIDToString(got[0].RuntimeID) != f.home {
		t.Fatalf("home claimed %v, want the stray task %s", agPoolIDs(got), task)
	}
}

func TestAGPatchRuntimePoolStaleDispatchReclaimedByClaimer(t *testing.T) {
	name := agPoolName(t)
	f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
	task := f.queue(t, f.laptop, f.issue(t), testutil.Cols{
		"status":        "dispatched",
		"dispatched_at": testutil.Raw("now() - interval '10 minutes'"),
	})

	got := f.claim(t, f.laptop)
	if len(got) != 1 || util.UUIDToString(got[0].ID) != task {
		t.Fatalf("laptop reclaimed %v, want its lost dispatch %s", agPoolIDs(got), task)
	}
	if got := f.claim(t, f.home); len(got) != 0 {
		t.Fatalf("home took %v, want the laptop's dispatch left alone", agPoolIDs(got))
	}
}

func TestAGPatchRuntimePoolCapsStayPerAgentAndPerMachine(t *testing.T) {
	name := agPoolName(t)
	t.Run("AG patch: the agent's cap spans both machines", func(t *testing.T) {
		f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name})
		f.rows.Exec(t, `UPDATE agent SET max_concurrent_tasks = 1 WHERE id = $1`, f.agent)
		f.queue(t, f.home, f.issue(t), testutil.Cols{"status": "running", "dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()")})
		f.queue(t, f.home, f.issue(t))
		if got := f.claim(t, f.laptop); len(got) != 0 {
			t.Fatalf("laptop claimed %v past the agent's cap", agPoolIDs(got))
		}
	})
	t.Run("AG patch: a runtime cap counts that machine's runs only", func(t *testing.T) {
		f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name, over: testutil.Cols{"max_concurrent_tasks": 1}})
		f.queue(t, f.laptop, f.issue(t), testutil.Cols{"status": "running", "dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()")})
		task := f.queue(t, f.home, f.issue(t))
		if got := f.claim(t, f.laptop); len(got) != 0 {
			t.Fatalf("full laptop claimed %v", agPoolIDs(got))
		}
		if got := f.claim(t, f.home); len(got) != 1 || util.UUIDToString(got[0].ID) != task {
			t.Fatalf("home claimed %v, want %s", agPoolIDs(got), task)
		}
	})
}

func TestAGPatchRuntimePoolAffinity(t *testing.T) {
	name := agPoolName(t)
	type setup struct {
		laptop     testutil.Cols // overrides on the laptop runtime
		lastAgo    time.Duration // when the laptop's run on the issue finished
		taskAge    time.Duration
		laptopBusy bool // one run already on the laptop
		lastOnHome bool // the last run was on home, not the laptop
	}
	base := setup{laptop: testutil.Cols{"max_concurrent_tasks": 3}, lastAgo: 10 * time.Minute}
	with := func(mod func(*setup)) setup {
		s := base
		s.laptop = testutil.Cols{"max_concurrent_tasks": 3}
		mod(&s)
		return s
	}

	cases := []struct {
		label    string
		s        setup
		homeTake bool
	}{
		{"held for the warm laptop", base, false},
		{"held when the laptop has no runtime cap", with(func(s *setup) { s.laptop = testutil.Cols{} }), false},
		{"released once the task is over 3 minutes old", with(func(s *setup) { s.taskAge = 4 * time.Minute }), true},
		{"released when the laptop's run is over an hour old", with(func(s *setup) { s.lastAgo = 61 * time.Minute }), true},
		{"released while the laptop drains", with(func(s *setup) {
			s.laptop["metadata"] = testutil.Raw(fmt.Sprintf(`'{"ag_pool": %q, "ag_drain": {"draining": true, "idle": false, "active_tasks": 0}}'::jsonb`, name))
		}), true},
		{"released while the laptop is offline", with(func(s *setup) { s.laptop["status"] = "offline" }), true},
		{"released while the laptop is at its cap", with(func(s *setup) { s.laptop["max_concurrent_tasks"] = 1; s.laptopBusy = true }), true},
		{"not held when the last run was on home", with(func(s *setup) { s.lastOnHome = true }), true},
	}
	for _, c := range cases {
		t.Run("AG patch: "+c.label, func(t *testing.T) {
			f := newAGPoolFixture(t, agPoolRuntime{pool: name}, agPoolRuntime{pool: name, over: c.s.laptop})
			issue := f.issue(t)
			last := f.laptop
			if c.s.lastOnHome {
				last = f.home
			}
			f.finished(t, last, issue, c.s.lastAgo)
			if c.s.laptopBusy {
				f.queue(t, f.laptop, f.issue(t), testutil.Cols{"status": "running", "dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()")})
			}
			task := f.queue(t, f.home, issue, testutil.Cols{
				"created_at": testutil.Raw(fmt.Sprintf("now() - interval '%d seconds'", int(c.s.taskAge.Seconds()))),
			})

			got := f.claim(t, f.home)
			if c.homeTake {
				if len(got) != 1 || util.UUIDToString(got[0].ID) != task {
					t.Fatalf("home claimed %v, want %s", agPoolIDs(got), task)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("home claimed %v, want the task held for the laptop", agPoolIDs(got))
			}
			got = f.claim(t, f.laptop)
			if len(got) != 1 || util.UUIDToString(got[0].ID) != task || util.UUIDToString(got[0].RuntimeID) != f.laptop {
				t.Fatalf("laptop claimed %v, want %s", agPoolIDs(got), task)
			}
		})
	}
}

type agPoolWakeRecorder struct {
	mu   sync.Mutex
	keys []string
}

func (r *agPoolWakeRecorder) NotifyTaskAvailable(runtimeID, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, runtimeID)
}

func TestAGPatchRuntimePoolWakesPeers(t *testing.T) {
	name := agPoolName(t)
	for label, laptop := range map[string]agPoolRuntime{"in the pool": {pool: name}, "outside it": {}} {
		t.Run("AG patch: laptop "+label, func(t *testing.T) {
			f := newAGPoolFixture(t, agPoolRuntime{pool: name}, laptop)
			rec := &agPoolWakeRecorder{}
			f.svc.Wakeup = rec
			f.svc.NotifyRuntimeMayHaveWork(util.MustParseUUID(f.home))

			woken := map[string]bool{}
			for _, k := range rec.keys {
				woken[k] = true
			}
			wantLaptop := laptop.pool != ""
			if !woken[f.home] || woken[f.laptop] != wantLaptop || len(rec.keys) != len(woken) {
				t.Fatalf("woke %v, want home and laptop=%v once each", rec.keys, wantLaptop)
			}
		})
	}
}
