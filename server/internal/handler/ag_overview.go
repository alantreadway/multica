package handler

// AG patch: Overview dashboard. One read-only endpoint for what is happening
// now and how far each pinned work tree has got, computed from tables the
// server already holds, plus the pins themselves. It never acts on anything:
// the Decisions page does. See ag-patches/OVERVIEW-DASHBOARD-DESIGN.md.

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	agOverviewPinJira  = "jira"
	agOverviewPinIssue = "issue"
	agOverviewMaxPins  = 50
	// Open non-unit issues listed per tree; the count covers the rest.
	agOverviewMaxUnbroken = 10
)

var agOverviewJiraKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`)

// States a unit works through; backlog and todo have not started.
var agOverviewWorkingStates = map[string]bool{"in_progress": true, "in_review": true, "handoff": true, "blocked": true}

type agOverviewIssueRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Number     int32  `json:"number"`
	Title      string `json:"title"`
}

type agOverviewPinResponse struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type agOverviewTask struct {
	ID          string              `json:"id"`
	Status      string              `json:"status"`
	AgentID     string              `json:"agent_id"`
	AgentName   string              `json:"agent_name"`
	Issue       *agOverviewIssueRef `json:"issue"`
	ReviewRound *int32              `json:"review_round"`
	// When it started running, or was queued.
	Since string  `json:"since"`
	Tree  *string `json:"tree"`
}

type agOverviewCard struct {
	ID       string              `json:"id"`
	CardType string              `json:"card_type"`
	Issue    *agOverviewIssueRef `json:"issue"`
}

type agOverviewAgeing struct {
	Issue        agOverviewIssueRef `json:"issue"`
	Status       string             `json:"status"`
	InStateSince string             `json:"in_state_since"`
	P90Hours     float64            `json:"p90_hours"`
	WaitingOn    *string            `json:"waiting_on"`
	Tree         *string            `json:"tree"`
}

type agOverviewNow struct {
	Running      []agOverviewTask   `json:"running"`
	Queued       []agOverviewTask   `json:"queued"`
	WaitingOnYou []agOverviewCard   `json:"waiting_on_you"`
	Elsewhere    map[string]int     `json:"elsewhere"`
	Ageing       []agOverviewAgeing `json:"ageing"`
}

type agOverviewPR struct {
	Number int32   `json:"number"`
	State  string  `json:"state"`
	Checks *string `json:"checks"`
	Review *string `json:"review"`
}

type agOverviewUnit struct {
	agOverviewIssueRef
	Status string `json:"status"`
	// The status before handoff or blocked, so the stage strip can place it.
	PrevStatus   *string       `json:"prev_status"`
	Flow         []string      `json:"flow"`
	ReviewRound  *int32        `json:"review_round"`
	InStateSince string        `json:"in_state_since"`
	Ageing       bool          `json:"ageing"`
	WaitingOn    *string       `json:"waiting_on"`
	PR           *agOverviewPR `json:"pr"`
}

type agOverviewWave struct {
	Root       agOverviewIssueRef `json:"root"`
	Done       bool               `json:"done"`
	IssueCount int                `json:"issue_count"`
	StartedAt  string             `json:"started_at"`
	// Last update of any issue in a done wave.
	FinishedAt *string          `json:"finished_at"`
	Units      []agOverviewUnit `json:"units"`
}

type agOverviewETAResponse struct {
	P50     string `json:"p50"`
	P85     string `json:"p85"`
	Samples int    `json:"samples"`
}

type agOverviewKnown struct {
	Units     int                    `json:"units"`
	Done      int                    `json:"done"`
	ETA       *agOverviewETAResponse `json:"eta"`
	ETAHidden *string                `json:"eta_hidden"`
}

type agOverviewUnbroken struct {
	agOverviewIssueRef
	Status string `json:"status"`
}

type agOverviewGrowth struct {
	Root agOverviewIssueRef `json:"root"`
	// Issues in the wave at the end of its first day, and at the end.
	DayOne int `json:"day_one"`
	Final  int `json:"final"`
}

type agOverviewUnknown struct {
	ScopeOpen     bool                 `json:"scope_open"`
	Unbroken      []agOverviewUnbroken `json:"unbroken"`
	UnbrokenCount int                  `json:"unbroken_count"`
	RecentUnits   int                  `json:"recent_units"`
	RecentDays    int                  `json:"recent_days"`
	PastGrowth    []agOverviewGrowth   `json:"past_growth"`
}

type agOverviewTree struct {
	Pin        agOverviewPinResponse `json:"pin"`
	Title      *string               `json:"title"`
	IssueCount int                   `json:"issue_count"`
	StartedAt  *string               `json:"started_at"`
	RunHours   float64               `json:"run_hours"`
	Known      agOverviewKnown       `json:"known"`
	Unknown    agOverviewUnknown     `json:"unknown"`
	Waves      []agOverviewWave      `json:"waves"`
}

type agOverviewResponse struct {
	GeneratedAt  string           `json:"generated_at"`
	DeskSyncedAt *string          `json:"desk_synced_at"`
	Now          agOverviewNow    `json:"now"`
	Trees        []agOverviewTree `json:"trees"`
}

// AGOverviewPinRequest pins a JIRA key or a root issue (MAG-950).
type AGOverviewPinRequest struct {
	Ref string `json:"ref"`
}

func agOverviewTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func agOverviewRef(prefix string, id pgtype.UUID, number int32, title string) agOverviewIssueRef {
	return agOverviewIssueRef{ID: uuidToString(id), Identifier: agOverviewIdentifier(prefix, number), Number: number, Title: title}
}

func agOverviewIdentifier(prefix string, number int32) string {
	if prefix == "" {
		return "#" + strconv.Itoa(int(number))
	}
	return prefix + "-" + strconv.Itoa(int(number))
}

func agOverviewRound(r int32) *int32 {
	if r < 0 {
		return nil
	}
	return &r
}

func agOverviewFlow(flow string) []string {
	out := []string{}
	for _, s := range strings.Split(flow, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// agOverviewTreeKey is the key a root issue names, used to cut descendants that
// belong to other work.
func agOverviewTreeKey(metadata []byte) string {
	var m map[string]any
	if json.Unmarshal(metadata, &m) != nil {
		return ""
	}
	for _, field := range []string{"jira_key", "review_jira", "source_key"} {
		if s, ok := m[field].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func agOverviewSeed(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// --- Work tree summary (pure) -----------------------------------------------

type agOverviewStateInfo struct {
	Since time.Time
	Prev  string
}

// agOverviewTreeInput is everything the summary needs besides the rows.
type agOverviewTreeInput struct {
	Prefix    string
	Now       time.Time
	States    map[string]agOverviewStateInfo
	PRs       map[string]*agOverviewPR
	WaitingOn map[string]string
	History   agOverviewHistory
}

func agOverviewIsUnit(row db.AGOverviewTreeIssuesRow) bool {
	return row.UnitFlow != "" || (row.HasPr && row.IsLeaf)
}

func (in agOverviewTreeInput) stateSince(row db.AGOverviewTreeIssuesRow) agOverviewStateInfo {
	if s, ok := in.States[uuidToString(row.ID)]; ok {
		return s
	}
	return agOverviewStateInfo{Since: row.CreatedAt.Time}
}

// agOverviewBuildTree splits one tree's issues into waves (one per root: an
// issue whose parent is outside the tree), units, and the known and unknown
// scope. ETA and run time are filled in by the caller.
func agOverviewBuildTree(rows []db.AGOverviewTreeIssuesRow, in agOverviewTreeInput) agOverviewTree {
	tree := agOverviewTree{IssueCount: len(rows), Waves: []agOverviewWave{}}
	tree.Unknown = agOverviewUnknown{Unbroken: []agOverviewUnbroken{}, RecentDays: agOverviewRecentUnitDays, PastGrowth: []agOverviewGrowth{}}
	if len(rows) == 0 {
		return tree
	}
	byID := make(map[string]db.AGOverviewTreeIssuesRow, len(rows))
	for _, row := range rows {
		byID[uuidToString(row.ID)] = row
	}
	rootOf := func(row db.AGOverviewTreeIssuesRow) string {
		id := uuidToString(row.ID)
		for seen := 0; seen <= len(rows); seen++ {
			parent := uuidToString(byID[id].ParentIssueID)
			if _, ok := byID[parent]; !ok || !byID[id].ParentIssueID.Valid {
				return id
			}
			id = parent
		}
		return id // a parent cycle; stop anywhere
	}

	waveIndex := map[string]int{}
	waveRows := [][]db.AGOverviewTreeIssuesRow{}
	for _, row := range rows {
		root := rootOf(row)
		i, ok := waveIndex[root]
		if !ok {
			i = len(waveRows)
			waveIndex[root] = i
			waveRows = append(waveRows, nil)
		}
		waveRows[i] = append(waveRows[i], row)
	}

	started := rows[0].CreatedAt.Time
	recentSince := in.Now.Add(-agOverviewRecentUnitDays * 24 * time.Hour)
	for _, members := range waveRows {
		rootRow := byID[rootOf(members[0])]
		wave := agOverviewWave{
			Root:       agOverviewRef(in.Prefix, rootRow.ID, rootRow.Number, rootRow.Title),
			Done:       true,
			IssueCount: len(members),
			StartedAt:  agOverviewTime(rootRow.CreatedAt.Time),
			Units:      []agOverviewUnit{},
		}
		var finished time.Time
		dayOne := 0
		for _, row := range members {
			started = minTime(started, row.CreatedAt.Time)
			if !agOverviewClosed(row.Status) {
				wave.Done = false
			}
			if row.UpdatedAt.Time.After(finished) {
				finished = row.UpdatedAt.Time
			}
			if !row.CreatedAt.Time.After(rootRow.CreatedAt.Time.Add(24 * time.Hour)) {
				dayOne++
			}
			if !agOverviewIsUnit(row) {
				if !agOverviewClosed(row.Status) && row.IsLeaf {
					tree.Unknown.UnbrokenCount++
					if len(tree.Unknown.Unbroken) < agOverviewMaxUnbroken {
						tree.Unknown.Unbroken = append(tree.Unknown.Unbroken, agOverviewUnbroken{
							agOverviewIssueRef: agOverviewRef(in.Prefix, row.ID, row.Number, row.Title),
							Status:             row.Status,
						})
					}
				}
				continue
			}
			if row.Status == "cancelled" {
				continue
			}
			tree.Known.Units++
			if row.Status == "done" {
				tree.Known.Done++
			}
			if row.CreatedAt.Time.After(recentSince) {
				tree.Unknown.RecentUnits++
			}
			wave.Units = append(wave.Units, agOverviewBuildUnit(row, in))
		}
		if wave.Done {
			f := agOverviewTime(finished)
			wave.FinishedAt = &f
			if len(members) >= agOverviewMinGrowthWaveIssues {
				tree.Unknown.PastGrowth = append(tree.Unknown.PastGrowth, agOverviewGrowth{Root: wave.Root, DayOne: dayOne, Final: len(members)})
			}
		}
		tree.Waves = append(tree.Waves, wave)
	}
	s := agOverviewTime(started)
	tree.StartedAt = &s
	tree.Unknown.ScopeOpen = tree.Unknown.UnbrokenCount > 0 || tree.Unknown.RecentUnits > 0
	return tree
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func agOverviewBuildUnit(row db.AGOverviewTreeIssuesRow, in agOverviewTreeInput) agOverviewUnit {
	id := uuidToString(row.ID)
	state := in.stateSince(row)
	unit := agOverviewUnit{
		agOverviewIssueRef: agOverviewRef(in.Prefix, row.ID, row.Number, row.Title),
		Status:             row.Status,
		Flow:               agOverviewFlow(row.UnitFlow),
		ReviewRound:        agOverviewRound(row.ReviewRound),
		InStateSince:       agOverviewTime(state.Since),
		Ageing:             agOverviewIsAgeing(row.Status, state.Since, in.Now, in.History),
		PR:                 in.PRs[id],
	}
	if state.Prev != "" && (row.Status == "handoff" || row.Status == "blocked") {
		prev := state.Prev
		unit.PrevStatus = &prev
	}
	if w, ok := in.WaitingOn[id]; ok {
		unit.WaitingOn = &w
	}
	return unit
}

func agOverviewIsAgeing(status string, since, now time.Time, history agOverviewHistory) bool {
	p90, ok := history.StateP90[status]
	return ok && agOverviewWorkingStates[status] && now.Sub(since) > p90
}

// agOverviewOpenUnits lists a tree's open known units for the ETA.
func agOverviewOpenUnits(rows []db.AGOverviewTreeIssuesRow, in agOverviewTreeInput) []agOverviewOpenUnit {
	var out []agOverviewOpenUnit
	for _, row := range rows {
		if !agOverviewIsUnit(row) || agOverviewClosed(row.Status) {
			continue
		}
		since := in.stateSince(row).Since
		out = append(out, agOverviewOpenUnit{State: row.Status, InState: agOverviewBusinessDuration(since, in.Now)})
	}
	return out
}

// --- Handlers ----------------------------------------------------------------

func (h *Handler) agOverviewPinLabel(ctx context.Context, pin db.AgOverviewPin, prefix string) string {
	if pin.Kind != agOverviewPinIssue {
		return pin.Ref
	}
	id, err := util.ParseUUID(pin.Ref)
	if err != nil {
		return pin.Ref
	}
	issue, err := h.Queries.GetIssue(ctx, id)
	if err != nil || uuidToString(issue.WorkspaceID) != uuidToString(pin.WorkspaceID) {
		return pin.Ref
	}
	return agOverviewIdentifier(prefix, issue.Number)
}

func agOverviewPinToResponse(pin db.AgOverviewPin, label string) agOverviewPinResponse {
	return agOverviewPinResponse{ID: uuidToString(pin.ID), Kind: pin.Kind, Ref: pin.Ref, Label: label}
}

// agOverviewTreeRows loads one pin's tree, and the root's title for an issue pin.
func (h *Handler) agOverviewTreeRows(ctx context.Context, wsUUID pgtype.UUID, pin db.AgOverviewPin) ([]db.AGOverviewTreeIssuesRow, *string, error) {
	params := db.AGOverviewTreeIssuesParams{WorkspaceID: wsUUID}
	var title *string
	switch pin.Kind {
	case agOverviewPinJira:
		params.SeedKey = pgtype.Text{String: pin.Ref, Valid: true}
		params.TreeKey = params.SeedKey
	case agOverviewPinIssue:
		id, err := util.ParseUUID(pin.Ref)
		if err != nil {
			return nil, nil, nil
		}
		root, err := h.Queries.GetIssue(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && uuidToString(root.WorkspaceID) != uuidToString(wsUUID)) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		params.RootID = id
		if key := agOverviewTreeKey(root.Metadata); key != "" {
			params.TreeKey = pgtype.Text{String: key, Valid: true}
		}
		title = &root.Title
	default:
		return nil, nil, nil
	}
	rows, err := h.Queries.AGOverviewTreeIssues(ctx, params)
	return rows, title, err
}

// GetAGOverview serves the whole Overview page in one response.
func (h *Handler) GetAGOverview(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	out, err := h.agBuildOverview(r.Context(), wsUUID, time.Now())
	if err != nil {
		slog.Warn("GetAGOverview failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load overview")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) agBuildOverview(ctx context.Context, wsUUID pgtype.UUID, now time.Time) (*agOverviewResponse, error) {
	prefix := h.getIssuePrefix(ctx, wsUUID)
	pins, err := h.Queries.AGListOverviewPins(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	type pinnedTree struct {
		pin   db.AgOverviewPin
		rows  []db.AGOverviewTreeIssuesRow
		title *string
	}
	trees := make([]pinnedTree, 0, len(pins))
	treeOf := map[string]string{} // issue id -> pin label, first pin wins
	labels := map[string]string{}
	for _, pin := range pins {
		rows, title, err := h.agOverviewTreeRows(ctx, wsUUID, pin)
		if err != nil {
			return nil, err
		}
		label := h.agOverviewPinLabel(ctx, pin, prefix)
		labels[uuidToString(pin.ID)] = label
		for _, row := range rows {
			if _, ok := treeOf[uuidToString(row.ID)]; !ok {
				treeOf[uuidToString(row.ID)] = label
			}
		}
		trees = append(trees, pinnedTree{pin: pin, rows: rows, title: title})
	}

	openUnits, err := h.Queries.AGOverviewOpenUnits(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	var ids []pgtype.UUID
	seen := map[string]bool{}
	addID := func(id pgtype.UUID) {
		if s := uuidToString(id); !seen[s] {
			seen[s] = true
			ids = append(ids, id)
		}
	}
	for _, t := range trees {
		for _, row := range t.rows {
			if agOverviewIsUnit(row) {
				addID(row.ID)
			}
		}
	}
	for _, u := range openUnits {
		addID(u.ID)
	}

	states := map[string]agOverviewStateInfo{}
	prs := map[string]*agOverviewPR{}
	if len(ids) > 0 {
		changes, err := h.Queries.AGOverviewLatestStatusChanges(ctx, ids)
		if err != nil {
			return nil, err
		}
		current := map[string]string{}
		for _, t := range trees {
			for _, row := range t.rows {
				current[uuidToString(row.ID)] = row.Status
			}
		}
		for _, u := range openUnits {
			current[uuidToString(u.ID)] = u.Status
		}
		for _, c := range changes {
			id := uuidToString(c.IssueID)
			// A latest change that does not lead to the current status means the
			// log missed one; fall back to the issue's own timestamps.
			if c.ToStatus == current[id] {
				states[id] = agOverviewStateInfo{Since: c.CreatedAt.Time, Prev: c.FromStatus}
			}
		}
		prRows, err := h.Queries.AGOverviewIssuePullRequests(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, p := range prRows {
			id := uuidToString(p.IssueID)
			existing := prs[id]
			open := p.State == "open" || p.State == "draft"
			if existing == nil || (open && existing.State != "open" && existing.State != "draft") {
				prs[id] = &agOverviewPR{Number: p.PrNumber, State: p.State, Checks: textToPtr(p.ChecksRollupState), Review: textToPtr(p.ReviewDecision)}
			}
		}
	}

	transitionRows, err := h.Queries.AGOverviewUnitTransitions(ctx, db.AGOverviewUnitTransitionsParams{
		WorkspaceID: wsUUID,
		// Twice the window, so units done inside it keep their earlier states.
		Since: pgtype.Timestamptz{Time: now.Add(-2 * agOverviewHistoryWindow), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	transitions := make([]agOverviewTransition, len(transitionRows))
	for i, t := range transitionRows {
		transitions[i] = agOverviewTransition{
			IssueID: uuidToString(t.IssueID), IssueCreatedAt: t.IssueCreatedAt.Time,
			At: t.CreatedAt.Time, From: t.FromStatus, To: t.ToStatus,
		}
	}
	history := agOverviewBuildHistory(transitions, now)

	cards, err := h.Queries.AGOverviewOpenDeskCards(ctx, wsUUID)
	if err != nil {
		return nil, err
	}
	engine, err := h.Queries.GetAGDecisionEngine(ctx, wsUUID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	tasks, err := h.Queries.AGOverviewActiveTasks(ctx, wsUUID)
	if err != nil {
		return nil, err
	}

	waitingOn := map[string]string{}
	result := &agOverviewResponse{
		GeneratedAt:  agOverviewTime(now),
		DeskSyncedAt: timestampToPtr(engine.LastSyncAt),
		Now: agOverviewNow{
			Running: []agOverviewTask{}, Queued: []agOverviewTask{}, WaitingOnYou: []agOverviewCard{},
			Elsewhere: map[string]int{}, Ageing: []agOverviewAgeing{},
		},
		Trees: []agOverviewTree{},
	}
	for _, c := range cards {
		var issue *agOverviewIssueRef
		if c.IssueID.Valid && c.IssueNumber.Valid {
			ref := agOverviewRef(prefix, c.IssueID, c.IssueNumber.Int32, c.IssueTitle.String)
			issue = &ref
			if _, ok := waitingOn[ref.ID]; !ok {
				waitingOn[ref.ID] = c.WaitingOn
			}
		}
		if c.WaitingOn == agDecisionWaitingOnAlan {
			result.Now.WaitingOnYou = append(result.Now.WaitingOnYou, agOverviewCard{ID: uuidToString(c.ID), CardType: c.CardType, Issue: issue})
		} else {
			result.Now.Elsewhere[c.WaitingOn]++
		}
	}

	treeLabel := func(id string) *string {
		if label, ok := treeOf[id]; ok {
			return &label
		}
		return nil
	}
	for _, t := range tasks {
		task := agOverviewTask{
			ID: uuidToString(t.ID), Status: t.Status, AgentID: uuidToString(t.AgentID), AgentName: t.AgentName,
			Since: agOverviewTime(t.CreatedAt.Time),
		}
		if t.IssueID.Valid && t.IssueNumber.Valid {
			ref := agOverviewRef(prefix, t.IssueID, t.IssueNumber.Int32, t.IssueTitle.String)
			task.Issue = &ref
			task.ReviewRound = agOverviewRound(t.ReviewRound)
			task.Tree = treeLabel(ref.ID)
		}
		if t.Status == "running" || t.Status == "dispatched" {
			if t.StartedAt.Valid {
				task.Since = agOverviewTime(t.StartedAt.Time)
			}
			result.Now.Running = append(result.Now.Running, task)
		} else {
			result.Now.Queued = append(result.Now.Queued, task)
		}
	}

	for _, u := range openUnits {
		id := uuidToString(u.ID)
		state, ok := states[id]
		if !ok {
			state = agOverviewStateInfo{Since: u.UpdatedAt.Time}
		}
		if !agOverviewIsAgeing(u.Status, state.Since, now, history) {
			continue
		}
		entry := agOverviewAgeing{
			Issue:        agOverviewRef(prefix, u.ID, u.Number, u.Title),
			Status:       u.Status,
			InStateSince: agOverviewTime(state.Since),
			P90Hours:     math.Round(history.StateP90[u.Status].Hours()*10) / 10,
			Tree:         treeLabel(id),
		}
		if w, ok := waitingOn[id]; ok {
			entry.WaitingOn = &w
		}
		result.Now.Ageing = append(result.Now.Ageing, entry)
	}

	in := agOverviewTreeInput{Prefix: prefix, Now: now, States: states, PRs: prs, WaitingOn: waitingOn, History: history}
	day := now.UTC().Truncate(24 * time.Hour)
	for _, t := range trees {
		tree := agOverviewBuildTree(t.rows, in)
		tree.Pin = agOverviewPinToResponse(t.pin, labels[uuidToString(t.pin.ID)])
		tree.Title = t.title
		if len(t.rows) > 0 {
			treeIDs := make([]pgtype.UUID, len(t.rows))
			for i, row := range t.rows {
				treeIDs[i] = row.ID
			}
			seconds, err := h.Queries.AGOverviewRunSeconds(ctx, treeIDs)
			if err != nil {
				return nil, err
			}
			tree.RunHours = math.Round(seconds/3600*10) / 10
		}
		seed := agOverviewSeed(uuidToString(t.pin.ID), day.Format(time.DateOnly))
		eta, hidden := agOverviewEstimate(agOverviewOpenUnits(t.rows, in), history.ToDone, now, rand.New(rand.NewPCG(seed, seed>>1)))
		forecast := db.AGInsertOverviewForecastParams{
			WorkspaceID: wsUUID, Kind: t.pin.Kind, Ref: t.pin.Ref,
			Day:        pgtype.Date{Time: day, Valid: true},
			KnownUnits: int32(tree.Known.Units), DoneUnits: int32(tree.Known.Done), ScopeOpen: tree.Unknown.ScopeOpen,
		}
		if eta != nil {
			tree.Known.ETA = &agOverviewETAResponse{P50: agOverviewTime(eta.P50), P85: agOverviewTime(eta.P85), Samples: eta.MinSamples}
			forecast.P50At = pgtype.Timestamptz{Time: eta.P50, Valid: true}
			forecast.P85At = pgtype.Timestamptz{Time: eta.P85, Valid: true}
		} else {
			tree.Known.ETAHidden = &hidden
		}
		// The log is a side record: a failed write must not blank the page.
		if _, err := h.Queries.AGInsertOverviewForecast(ctx, forecast); err != nil {
			slog.Warn("AG overview forecast write failed", "error", err, "kind", t.pin.Kind, "ref", t.pin.Ref)
		}
		result.Trees = append(result.Trees, tree)
	}
	return result, nil
}

// CreateAGOverviewPin pins a JIRA key or a root issue. Pinning one twice
// returns the existing pin.
func (h *Handler) CreateAGOverviewPin(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var req AGOverviewPinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" || len(ref) > 64 {
		writeError(w, http.StatusBadRequest, "ref is required")
		return
	}
	kind, value := "", ""
	if issue, ok := h.resolveIssueByIdentifier(r.Context(), ref, workspaceID); ok {
		kind, value = agOverviewPinIssue, uuidToString(issue.ID)
	} else if key := strings.ToUpper(ref); agOverviewJiraKey.MatchString(key) {
		kind, value = agOverviewPinJira, key
	} else {
		writeError(w, http.StatusBadRequest, "ref must be a JIRA key or an issue in this workspace")
		return
	}
	existing, err := h.Queries.AGListOverviewPins(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to pin")
		return
	}
	if len(existing) >= agOverviewMaxPins {
		writeError(w, http.StatusBadRequest, "too many pins")
		return
	}
	var createdBy pgtype.UUID
	if u, err := util.ParseUUID(requestUserID(r)); err == nil {
		createdBy = u
	}
	pin, err := h.Queries.AGInsertOverviewPin(r.Context(), db.AGInsertOverviewPinParams{
		WorkspaceID: wsUUID, Kind: kind, Ref: value, CreatedBy: createdBy,
	})
	if err != nil {
		slog.Warn("CreateAGOverviewPin failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to pin")
		return
	}
	writeJSON(w, http.StatusOK, agOverviewPinToResponse(pin, h.agOverviewPinLabel(r.Context(), pin, h.getIssuePrefix(r.Context(), wsUUID))))
}

// DeleteAGOverviewPin unpins a tree. Its forecast log stays.
func (h *Handler) DeleteAGOverviewPin(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	n, err := h.Queries.AGDeleteOverviewPin(r.Context(), db.AGDeleteOverviewPinParams{ID: id, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unpin")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "pin not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
