package handler

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// A Wednesday 12:00 UTC, so a few hours either side stay on weekdays.
var agOverviewWednesday = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func TestAGPatchOverviewBusinessTime(t *testing.T) {
	fri := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
	if got := agOverviewBusinessDuration(fri, mon); got != 4*time.Hour {
		t.Errorf("Fri 22:00 to Mon 02:00: got %v, want 4h", got)
	}
	if got := agOverviewBusinessDuration(mon, fri); got != 0 {
		t.Errorf("backwards: got %v", got)
	}
	if got := agOverviewAddBusiness(fri, 3*time.Hour); !got.Equal(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("Fri 22:00 + 3h: got %v, want Mon 01:00", got)
	}
	sat := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	if got := agOverviewAddBusiness(sat, time.Hour); !got.Equal(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("Sat + 1h: got %v, want Mon 01:00", got)
	}
	if got := agOverviewAddBusiness(agOverviewWednesday, 0); !got.Equal(agOverviewWednesday) {
		t.Errorf("+0: got %v", got)
	}
}

// agOverviewDoneUnits builds n units that went todo -> in_progress (2h) ->
// in_review (1h) -> done, starting a week before now.
func agOverviewDoneUnits(n int, now time.Time) []agOverviewTransition {
	var out []agOverviewTransition
	start := now.Add(-7 * 24 * time.Hour)
	for i := range n {
		id := string(rune('a' + i))
		created := start.Add(-time.Hour)
		out = append(out,
			agOverviewTransition{IssueID: id, IssueCreatedAt: created, At: start, From: "todo", To: "in_progress"},
			agOverviewTransition{IssueID: id, IssueCreatedAt: created, At: start.Add(2 * time.Hour), From: "in_progress", To: "in_review"},
			agOverviewTransition{IssueID: id, IssueCreatedAt: created, At: start.Add(3 * time.Hour), From: "in_review", To: "done"},
		)
	}
	return out
}

func TestAGPatchOverviewStateHistory(t *testing.T) {
	now := agOverviewWednesday
	h := agOverviewBuildHistory(agOverviewDoneUnits(agOverviewMinSamples, now), now)
	for state, want := range map[string]time.Duration{"todo": time.Hour, "in_progress": 2 * time.Hour, "in_review": time.Hour} {
		if got := h.StateP90[state]; got != want {
			t.Errorf("p90 %s: got %v, want %v", state, got, want)
		}
	}
	if _, ok := h.StateP90["done"]; ok {
		t.Error("done has no stay")
	}
	for state, want := range map[string]time.Duration{"todo": 4 * time.Hour, "in_progress": 3 * time.Hour, "in_review": time.Hour} {
		if got := h.ToDone[state]; len(got) != agOverviewMinSamples || got[0] != want {
			t.Errorf("to done from %s: got %v, want %d x %v", state, got, agOverviewMinSamples, want)
		}
	}

	few := agOverviewBuildHistory(agOverviewDoneUnits(agOverviewMinSamples-1, now), now)
	if len(few.StateP90) != 0 {
		t.Errorf("under %d samples a state has no p90: %v", agOverviewMinSamples, few.StateP90)
	}

	old := agOverviewBuildHistory(agOverviewDoneUnits(agOverviewMinSamples, now), now.Add(agOverviewHistoryWindow))
	if len(old.StateP90) != 0 || len(old.ToDone) != 0 {
		t.Errorf("history outside the window still counts: %+v", old)
	}

	reopened := agOverviewDoneUnits(1, now)
	reopened = append(reopened, agOverviewTransition{IssueID: "a", At: now.Add(-time.Hour), From: "done", To: "in_progress"})
	if got := agOverviewBuildHistory(reopened, now); len(got.ToDone) != 0 {
		t.Errorf("a reopened unit is not done: %v", got.ToDone)
	}
}

func TestAGPatchOverviewEstimate(t *testing.T) {
	now := agOverviewWednesday
	toDone := map[string][]time.Duration{}
	for i := range 40 {
		toDone["in_progress"] = append(toDone["in_progress"], time.Duration(i+1)*time.Hour)
		toDone["in_review"] = append(toDone["in_review"], time.Hour)
	}
	units := []agOverviewOpenUnit{{State: "in_progress", InState: time.Hour}, {State: "in_review", InState: 5 * time.Hour}}

	run := func(seed uint64) *agOverviewETA {
		eta, hidden := agOverviewEstimate(units, toDone, now, rand.New(rand.NewPCG(seed, 1)))
		if eta == nil {
			t.Fatalf("hidden: %s", hidden)
		}
		return eta
	}
	a, b := run(7), run(7)
	if !a.P50.Equal(b.P50) || !a.P85.Equal(b.P85) {
		t.Fatalf("same seed, different ETA: %+v vs %+v", a, b)
	}
	if a.P85.Before(a.P50) || !a.P50.After(now) {
		t.Fatalf("p50 %v p85 %v now %v", a.P50, a.P85, now)
	}
	if a.MinSamples != 40 {
		t.Errorf("samples: got %d", a.MinSamples)
	}
	// in_progress draws come from units that took over an hour (1..39h left),
	// so the median lands near 20 weekday hours; review is past every sample
	// and adds at most an hour.
	if d := agOverviewBusinessDuration(now, a.P50); d < 15*time.Hour || d > 25*time.Hour {
		t.Errorf("p50 is %v of weekday time away", d)
	}

	fixed := map[string][]time.Duration{"in_progress": make([]time.Duration, agOverviewMinSamples)}
	for i := range fixed["in_progress"] {
		fixed["in_progress"][i] = 3 * time.Hour
	}
	eta, _ := agOverviewEstimate([]agOverviewOpenUnit{{State: "in_progress", InState: 10 * time.Minute}}, fixed, now, rand.New(rand.NewPCG(1, 1)))
	if want := now.Add(170 * time.Minute); eta == nil || !eta.P50.Equal(want) || !eta.P85.Equal(want) {
		t.Errorf("one fixed sample: got %+v, want %v", eta, want)
	}

	if eta, hidden := agOverviewEstimate(nil, toDone, now, rand.New(rand.NewPCG(1, 1))); eta != nil || hidden != agOverviewETANoOpenUnits {
		t.Errorf("no open units: %+v %q", eta, hidden)
	}
	if eta, hidden := agOverviewEstimate([]agOverviewOpenUnit{{State: "handoff"}}, toDone, now, rand.New(rand.NewPCG(1, 1))); eta != nil || hidden != agOverviewETAFewSamples {
		t.Errorf("state with no samples: %+v %q", eta, hidden)
	}
}

// --- DB-backed ------------------------------------------------------------------

func agOverviewCleanup(t *testing.T) {
	t.Helper()
	clean := func() {
		ctx := context.Background()
		for _, table := range []string{"ag_overview_pin", "ag_overview_forecast", "ag_decision_card"} {
			if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id = $1`, testWorkspaceID); err != nil {
				t.Fatalf("clean %s: %v", table, err)
			}
		}
	}
	clean()
	t.Cleanup(clean)
}

func agOverviewIssue(t *testing.T, title, status, metadata string, parent string, created time.Time) string {
	t.Helper()
	cols := testutil.Cols{
		"status":     status,
		"metadata":   testutil.Raw("'" + metadata + "'::jsonb"),
		"created_at": created,
		"updated_at": created,
	}
	if parent != "" {
		cols["parent_issue_id"] = parent
	}
	return dbfx.Issue(t, title, cols)
}

func agOverviewStatusChange(t *testing.T, issueID, from, to string, at time.Time) {
	t.Helper()
	dbfx.Insert(t, "activity_log", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"issue_id":     issueID,
		"actor_type":   "system",
		"action":       "status_changed",
		"details":      testutil.Raw(`'{"from":"` + from + `","to":"` + to + `"}'::jsonb`),
		"created_at":   at,
	})
}

func agOverviewPin(t *testing.T, ref string) agOverviewPinResponse {
	t.Helper()
	var out agOverviewPinResponse
	req := newRequestAs(testUserID, http.MethodPost, "/api/ag/overview/pins", map[string]string{"ref": ref})
	testutil.Call(t, testHandler.CreateAGOverviewPin, req).Want(http.StatusOK).JSON(&out)
	return out
}

func agOverviewFindTree(t *testing.T, out *agOverviewResponse, label string) agOverviewTree {
	t.Helper()
	for _, tree := range out.Trees {
		if tree.Pin.Label == label {
			return tree
		}
	}
	t.Fatalf("no tree %q in %+v", label, out.Trees)
	return agOverviewTree{}
}

func agOverviewTreeIDs(tree agOverviewTree) map[string]bool {
	ids := map[string]bool{}
	for _, w := range tree.Waves {
		ids[w.Root.ID] = true
		for _, u := range w.Units {
			ids[u.ID] = true
		}
	}
	for _, u := range tree.Unknown.Unbroken {
		ids[u.ID] = true
	}
	return ids
}

func TestAGPatchOverviewPins(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agOverviewCleanup(t)
	setWorkspaceIssuePrefixForTest(t, "OVW")
	root := agOverviewIssue(t, "Pinned root", "todo", `{}`, "", time.Now())
	var number int32
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, root).Scan(&number)

	jira := agOverviewPin(t, " agx-9003 ")
	if jira.Kind != agOverviewPinJira || jira.Ref != "AGX-9003" || jira.Label != "AGX-9003" {
		t.Fatalf("jira pin: %+v", jira)
	}
	if again := agOverviewPin(t, "AGX-9003"); again.ID != jira.ID {
		t.Fatalf("pinning twice made a second pin: %s vs %s", again.ID, jira.ID)
	}
	identifier := agOverviewIdentifier("OVW", number)
	issue := agOverviewPin(t, identifier)
	if issue.Kind != agOverviewPinIssue || issue.Ref != root || issue.Label != identifier {
		t.Fatalf("issue pin: %+v", issue)
	}

	for _, bad := range []string{"", "not a key", "9003"} {
		req := newRequestAs(testUserID, http.MethodPost, "/api/ag/overview/pins", map[string]string{"ref": bad})
		testutil.Call(t, testHandler.CreateAGOverviewPin, req).Want(http.StatusBadRequest)
	}

	del := func(id string, want int) {
		req := newRequestAs(testUserID, http.MethodDelete, "/api/ag/overview/pins/"+id, nil)
		req = testutil.WithURLParams(req, "id", id)
		testutil.Call(t, testHandler.DeleteAGOverviewPin, req).Want(want)
	}
	del(jira.ID, http.StatusNoContent)
	del(jira.ID, http.StatusNotFound)
	if n := dbfx.Count(t, `SELECT count(*) FROM ag_overview_pin WHERE workspace_id = $1`, testWorkspaceID); n != 1 {
		t.Fatalf("after unpin: %d pins, want 1", n)
	}
}

// agOverviewSeedHistory gives in_progress (2h) and in_review (1h) enough done
// units for p90s and ETA samples.
func agOverviewSeedHistory(t *testing.T, now time.Time) {
	t.Helper()
	start := now.Add(-7 * 24 * time.Hour)
	for range agOverviewMinSamples {
		id := agOverviewIssue(t, "Past unit", "done", `{"unit_flow":"tests,review"}`, "", start.Add(-time.Hour))
		agOverviewStatusChange(t, id, "todo", "in_progress", start)
		agOverviewStatusChange(t, id, "in_progress", "in_review", start.Add(2*time.Hour))
		agOverviewStatusChange(t, id, "in_review", "done", start.Add(3*time.Hour))
	}
}

func TestAGPatchOverviewTreesAndNow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agOverviewCleanup(t)
	setWorkspaceIssuePrefixForTest(t, "OVW")
	ctx := context.Background()
	now := agOverviewWednesday
	agOverviewSeedHistory(t, now)

	// Tree AGX-1: an open wave, a done wave that grew after day one, a done
	// review-queue item, and work under the open root that belongs to AGX-3.
	day := 24 * time.Hour
	root := agOverviewIssue(t, "Open wave", "in_progress", `{"jira_key":"AGX-1"}`, "", now.Add(-5*day))
	review := agOverviewIssue(t, "Ageing review", "in_review", `{"jira_key":"AGX-1","unit_flow":"bench,tests,review","review_round":2}`, root, now.Add(-4*day))
	agOverviewStatusChange(t, review, "in_progress", "in_review", now.Add(-5*time.Hour))
	build := agOverviewIssue(t, "Fresh build", "in_progress", `{"unit_flow":"tests,review"}`, root, now.Add(-day))
	agOverviewStatusChange(t, build, "todo", "in_progress", now.Add(-10*time.Minute))
	done := agOverviewIssue(t, "Done unit", "done", `{"unit_flow":"review"}`, root, now.Add(-4*day))
	agOverviewIssue(t, "Cancelled unit", "cancelled", `{"unit_flow":"review"}`, root, now.Add(-4*day))
	spike := agOverviewIssue(t, "Spike the options API", "backlog", `{}`, root, now.Add(-4*day))
	// A sub-task with a unit of its own below it: neither is lost from the outline.
	group := agOverviewIssue(t, "Test coverage", "in_progress", `{}`, root, now.Add(-2*day))
	nested := agOverviewIssue(t, "Nested unit", "in_progress", `{"unit_flow":"tests,review"}`, group, now.Add(-2*day))
	agOverviewStatusChange(t, nested, "todo", "in_progress", now.Add(-10*time.Minute))
	other := agOverviewIssue(t, "Other epic", "in_progress", `{"jira_key":"AGX-3","unit_flow":"review"}`, root, now.Add(-4*day))
	agOverviewStatusChange(t, other, "todo", "in_progress", now.Add(-30*time.Minute))
	otherChild := agOverviewIssue(t, "Other epic child", "todo", `{}`, other, now.Add(-4*day))
	queueItem := agOverviewIssue(t, "Review queue item", "done", `{"review_jira":"AGX-1"}`, "", now.Add(-20*day))
	pastRoot := agOverviewIssue(t, "Past wave", "done", `{"source_key":"AGX-1"}`, "", now.Add(-10*day))
	for _, offset := range []time.Duration{2 * time.Hour, 3 * time.Hour, 3 * day} {
		agOverviewIssue(t, "Past wave issue", "done", `{}`, pastRoot, now.Add(-10*day+offset))
	}

	// Tree pinned by issue: the root names AGX-2, so a child naming AGX-9 is cut.
	issueRoot := agOverviewIssue(t, "Issue-pinned root", "in_progress", `{"jira_key":"AGX-2"}`, "", now.Add(-3*day))
	handoff := agOverviewIssue(t, "Waiting handoff", "handoff", `{"unit_flow":"review"}`, issueRoot, now.Add(-3*day))
	agOverviewStatusChange(t, handoff, "in_review", "handoff", now.Add(-30*time.Hour))
	cut := agOverviewIssue(t, "Cut child", "todo", `{"jira_key":"AGX-9"}`, issueRoot, now.Add(-3*day))

	dbfx.Insert(t, "ag_decision_card", testutil.Cols{"workspace_id": testWorkspaceID, "dedupe_key": "ovw-review", "issue_id": review, "waiting_on": "colleague", "card_type": "pr"})
	dbfx.Insert(t, "ag_decision_card", testutil.Cols{"workspace_id": testWorkspaceID, "dedupe_key": "ovw-spike", "issue_id": spike, "waiting_on": "alan", "card_type": "blocked"})
	runtime := handlerTestRuntimeID(t)
	agent := dbfx.Agent(t, "Overview Builder", runtime)
	dbfx.Task(t, agent, testutil.Cols{"issue_id": build, "status": "running", "runtime_id": runtime, "started_at": now.Add(-5 * time.Minute)})
	dbfx.Task(t, agent, testutil.Cols{"issue_id": review, "status": "queued", "runtime_id": runtime})

	agOverviewPin(t, "AGX-1")
	var number int32
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, issueRoot).Scan(&number)
	pinB := agOverviewPin(t, agOverviewIdentifier("OVW", number))

	out, err := testHandler.agBuildOverview(ctx, parseUUID(testWorkspaceID), now)
	if err != nil {
		t.Fatalf("agBuildOverview: %v", err)
	}

	a := agOverviewFindTree(t, out, "AGX-1")
	ids := agOverviewTreeIDs(a)
	for name, id := range map[string]string{"review": review, "build": build, "done": done, "spike": spike, "queue item": queueItem, "past wave": pastRoot} {
		if !ids[id] {
			t.Errorf("AGX-1 is missing %s", name)
		}
	}
	for name, id := range map[string]string{"other key": other, "other key's child": otherChild} {
		if ids[id] {
			t.Errorf("AGX-1 holds %s", name)
		}
	}
	if a.IssueCount != 13 {
		t.Errorf("AGX-1 issues: got %d, want 13", a.IssueCount)
	}
	if len(a.Waves) != 3 {
		t.Fatalf("AGX-1 waves: got %d, want 3", len(a.Waves))
	}
	if a.Known.Units != 4 || a.Known.Done != 1 {
		t.Errorf("known: got %d/%d, want 1/4", a.Known.Done, a.Known.Units)
	}
	if !a.Unknown.ScopeOpen || a.Unknown.UnbrokenCount != 1 || a.Unknown.Unbroken[0].ID != spike || a.Unknown.RecentUnits != 2 {
		t.Errorf("unknown: %+v", a.Unknown)
	}
	if len(a.Unknown.PastGrowth) != 1 || a.Unknown.PastGrowth[0].DayOne != 3 || a.Unknown.PastGrowth[0].Final != 4 {
		t.Errorf("past growth: %+v", a.Unknown.PastGrowth)
	}
	// Review is past every review sample (1h), so it adds its shortest; the
	// build and nested units have 3h - 10m left on every sample.
	want := agOverviewTime(now.Add(170 * time.Minute))
	if a.Known.ETA == nil || a.Known.ETA.P50 != want || a.Known.ETA.P85 != want {
		t.Errorf("AGX-1 ETA: got %+v, want %s", a.Known.ETA, want)
	}

	units := map[string]agOverviewUnit{}
	outline := map[string]agOverviewNode{}
	for _, w := range a.Waves {
		for _, u := range w.Units {
			units[u.ID] = u
		}
		if w.Done && len(w.Issues) != 0 {
			t.Errorf("done wave %s sends an outline: %+v", w.Root.Identifier, w.Issues)
		}
		for _, n := range w.Issues {
			outline[n.ID] = n
		}
	}
	// The open wave lists every issue below its root with its parent, closed ones too.
	if len(outline) != 7 {
		t.Errorf("open wave outline: got %d issues, want 7: %+v", len(outline), outline)
	}
	if _, ok := outline[root]; ok {
		t.Errorf("the outline repeats the wave's root")
	}
	if g := outline[group]; g.ParentID != root || g.Status != "in_progress" {
		t.Errorf("group node: %+v", g)
	}
	if n := outline[nested]; n.ParentID != group || n.Status != "in_progress" {
		t.Errorf("nested node: %+v", n)
	}
	if s := outline[spike]; s.WaitingOn == nil || *s.WaitingOn != "alan" {
		t.Errorf("spike node waits on: %+v", s)
	}
	r := units[review]
	if !r.Ageing || r.ReviewRound == nil || *r.ReviewRound != 2 || r.WaitingOn == nil || *r.WaitingOn != "colleague" {
		t.Errorf("review unit: %+v", r)
	}
	if len(r.Flow) != 3 || r.Flow[0] != "bench" || r.InStateSince != agOverviewTime(now.Add(-5*time.Hour)) {
		t.Errorf("review unit flow and state: %+v", r)
	}
	if b := units[build]; b.Ageing || b.ReviewRound != nil {
		t.Errorf("build unit: %+v", b)
	}

	b := agOverviewFindTree(t, out, pinB.Label)
	idsB := agOverviewTreeIDs(b)
	if b.Title == nil || *b.Title != "Issue-pinned root" || !idsB[handoff] || idsB[cut] || b.IssueCount != 2 {
		t.Errorf("issue pin tree: %+v", b)
	}
	if b.Known.ETA != nil || b.Known.ETAHidden == nil || *b.Known.ETAHidden != agOverviewETAFewSamples {
		t.Errorf("handoff has no samples, so no ETA: %+v", b.Known)
	}
	h := b.Waves[0].Units[0]
	if h.PrevStatus == nil || *h.PrevStatus != "in_review" || h.Ageing {
		t.Errorf("handoff unit: %+v", h)
	}

	// Now: running and queued tasks carry their tree, the card waiting on Alan
	// is listed, and only the review unit is past its p90.
	if len(out.Now.Running) != 1 || out.Now.Running[0].Issue.ID != build || out.Now.Running[0].Tree == nil || *out.Now.Running[0].Tree != "AGX-1" {
		t.Errorf("running: %+v", out.Now.Running)
	}
	if len(out.Now.Queued) != 1 || out.Now.Queued[0].Issue.ID != review || *out.Now.Queued[0].ReviewRound != 2 {
		t.Errorf("queued: %+v", out.Now.Queued)
	}
	if len(out.Now.WaitingOnYou) != 1 || out.Now.WaitingOnYou[0].Issue.ID != spike || out.Now.Elsewhere["colleague"] != 1 {
		t.Errorf("desk: %+v %+v", out.Now.WaitingOnYou, out.Now.Elsewhere)
	}
	if len(out.Now.Ageing) != 1 || out.Now.Ageing[0].Issue.ID != review || out.Now.Ageing[0].P90Hours != 1 {
		t.Errorf("ageing: %+v", out.Now.Ageing)
	}

	// The forecast log keeps the first write of the day.
	var p50 time.Time
	var known, doneUnits int
	var open bool
	dbfx.QueryRow(t, `SELECT p50_at, known_units, done_units, scope_open FROM ag_overview_forecast
		WHERE workspace_id = $1 AND kind = 'jira' AND ref = 'AGX-1' AND day = $2`, testWorkspaceID, now.Format(time.DateOnly)).Scan(&p50, &known, &doneUnits, &open)
	if !p50.Equal(now.Add(170*time.Minute)) || known != 4 || doneUnits != 1 || !open {
		t.Errorf("forecast row: %v %d %d %v", p50, known, doneUnits, open)
	}
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, build)
	if _, err := testHandler.agBuildOverview(ctx, parseUUID(testWorkspaceID), now.Add(time.Hour)); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM ag_overview_forecast WHERE workspace_id = $1 AND ref = 'AGX-1'`, testWorkspaceID); n != 1 {
		t.Errorf("forecast rows for one day: %d", n)
	}
	dbfx.QueryRow(t, `SELECT done_units FROM ag_overview_forecast WHERE workspace_id = $1 AND ref = 'AGX-1'`, testWorkspaceID).Scan(&doneUnits)
	if doneUnits != 1 {
		t.Errorf("a later call rewrote the day's forecast: done %d", doneUnits)
	}
	if _, err := testHandler.agBuildOverview(ctx, parseUUID(testWorkspaceID), now.Add(day)); err != nil {
		t.Fatalf("next day: %v", err)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM ag_overview_forecast WHERE workspace_id = $1 AND kind = $2 AND ref = $3`, testWorkspaceID, pinB.Kind, pinB.Ref); n != 2 {
		t.Errorf("issue pin forecast rows over two days: %d", n)
	}
}

func TestAGPatchOverviewEndpoint(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agOverviewCleanup(t)
	agOverviewPin(t, "AGX-404")

	var out struct {
		GeneratedAt string `json:"generated_at"`
		Now         struct {
			Running   []json.RawMessage `json:"running"`
			Elsewhere map[string]int    `json:"elsewhere"`
		} `json:"now"`
		Trees []struct {
			Pin        agOverviewPinResponse `json:"pin"`
			IssueCount int                   `json:"issue_count"`
			Known      struct {
				ETAHidden *string `json:"eta_hidden"`
			} `json:"known"`
			Waves []json.RawMessage `json:"waves"`
		} `json:"trees"`
	}
	req := newRequestAs(testUserID, http.MethodGet, "/api/ag/overview/", nil)
	testutil.Call(t, testHandler.GetAGOverview, req).Want(http.StatusOK).JSON(&out)
	if out.GeneratedAt == "" || out.Now.Running == nil || out.Now.Elsewhere == nil {
		t.Fatalf("empty lists must be arrays and maps: %+v", out)
	}
	if len(out.Trees) != 1 || out.Trees[0].Pin.Ref != "AGX-404" || out.Trees[0].IssueCount != 0 || out.Trees[0].Waves == nil {
		t.Fatalf("a pin with no issues: %+v", out.Trees)
	}
	if out.Trees[0].Known.ETAHidden == nil || *out.Trees[0].Known.ETAHidden != agOverviewETANoOpenUnits {
		t.Errorf("eta_hidden: %v", out.Trees[0].Known.ETAHidden)
	}
}

func TestAGPatchDeleteWorkspaceClearsOverviewRows(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agOverviewCleanup(t)
	ctx := context.Background()
	wsID := dbfx.Workspace(t, "AG Overview Delete", "handler-tests-ag-overview-delete")
	t.Cleanup(func() {
		for _, table := range []string{"ag_overview_pin", "ag_overview_forecast"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id = $1`, wsID)
		}
	})
	for _, id := range []string{wsID, testWorkspaceID} {
		dbfx.Exec(t, `INSERT INTO ag_overview_pin (workspace_id, kind, ref) VALUES ($1, 'jira', 'AGX-1')`, id)
		dbfx.Exec(t, `INSERT INTO ag_overview_forecast (workspace_id, kind, ref, day, known_units, done_units, scope_open)
			VALUES ($1, 'jira', 'AGX-1', current_date, 0, 0, false)`, id)
	}
	if err := testHandler.Queries.DeleteWorkspace(ctx, parseUUID(wsID)); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	for _, table := range []string{"ag_overview_pin", "ag_overview_forecast"} {
		deleted := dbfx.Count(t, `SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, wsID)
		kept := dbfx.Count(t, `SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, testWorkspaceID)
		if deleted != 0 || kept != 1 {
			t.Errorf("%s: deleted workspace has %d rows, other workspace has %d; want 0 and 1", table, deleted, kept)
		}
	}
}
