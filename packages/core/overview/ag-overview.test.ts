// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  agOverviewBoardColumns,
  agOverviewGrowth,
  agOverviewOpenUnits,
  agOverviewOutline,
  agOverviewStages,
  EMPTY_AG_OVERVIEW,
  parseAGOverview,
  parseAGOverviewPin,
  type AGOverviewNode,
  type AGOverviewUnit,
  type AGOverviewWave,
} from "./ag-overview";
import { AG_OVERVIEW_REFRESH_MS, agOverviewKeys, subscribeAGOverview } from "./ag-overview-queries";
import type { WSClient } from "../api/ws-client";

const ref = (n: number) => ({ id: `i${n}`, identifier: `MAG-${n}`, number: n, title: `Issue ${n}` });

const rawUnit = (over: Record<string, unknown> = {}) => ({
  ...ref(977),
  status: "in_review",
  prev_status: null,
  flow: ["bench", "tests", "review"],
  review_round: 2,
  in_state_since: "2026-11-05T18:00:00Z",
  ageing: true,
  waiting_on: "colleague",
  pr: { number: 8984, state: "open", checks: "SUCCESS", review: null },
  ...over,
});

const rawOverview = () => ({
  generated_at: "2026-11-06T15:25:00Z",
  desk_synced_at: "2026-11-06T15:24:00Z",
  now: {
    running: [
      {
        id: "t1",
        status: "running",
        agent_id: "a1",
        agent_name: "Builder",
        issue: ref(978),
        review_round: 6,
        since: "2026-11-06T15:07:00Z",
        tree: "AG-9003",
      },
    ],
    queued: [{ id: "t2", status: "queued", agent_id: "a2", agent_name: "Reviewer", issue: null, review_round: null, since: "2026-11-06T15:11:00Z", tree: null }],
    waiting_on_you: [{ id: "c1", card_type: "blocked", issue: ref(797) }],
    elsewhere: { colleague: 7, agent: 12, nobody: 12 },
    ageing: [{ issue: ref(973), status: "handoff", in_state_since: "2026-11-05T12:00:00Z", p90_hours: 9, waiting_on: "nobody", tree: "AG-9003" }],
  },
  trees: [
    {
      pin: { id: "p1", kind: "jira", ref: "AG-9003", label: "AG-9003" },
      title: null,
      issue_count: 106,
      started_at: "2026-10-01T09:00:00Z",
      run_hours: 162.4,
      known: { units: 7, done: 1, eta: { p50: "2026-11-09T12:00:00Z", p85: "2026-11-11T12:00:00Z", samples: 42 }, eta_hidden: null },
      unknown: {
        scope_open: true,
        unbroken: [{ ...ref(990), status: "backlog" }],
        unbroken_count: 1,
        recent_units: 2,
        recent_days: 3,
        past_growth: [{ root: ref(22), day_one: 33, final: 69 }],
      },
      waves: [
        {
          root: ref(950),
          done: false,
          issue_count: 9,
          started_at: "2026-11-04T09:00:00Z",
          finished_at: null,
          units: [rawUnit()],
          issues: [
            { ...ref(960), status: "in_progress", parent_id: "i950", waiting_on: "agent" },
            { ...ref(977), status: "in_review", parent_id: "i960", waiting_on: "colleague" },
          ],
        },
        { root: ref(22), done: true, issue_count: 69, started_at: "2026-10-01T09:00:00Z", finished_at: "2026-10-12T09:00:00Z", units: [], issues: [] },
      ],
    },
  ],
});

describe("AG patch: parseAGOverview", () => {
  it("reads a full response", () => {
    const o = parseAGOverview(rawOverview());
    expect(o.deskSyncedAt).toBe("2026-11-06T15:24:00Z");
    expect(o.now.running[0]).toMatchObject({ agentName: "Builder", reviewRound: 6, tree: "AG-9003" });
    expect(o.now.queued[0]?.issue).toBeNull();
    expect(o.now.waitingOnYou[0]?.issue?.identifier).toBe("MAG-797");
    expect(o.now.elsewhere).toEqual({ colleague: 7, agent: 12, nobody: 12 });
    expect(o.now.ageing[0]).toMatchObject({ status: "handoff", p90Hours: 9, waitingOn: "nobody" });
    const tree = o.trees[0]!;
    expect(tree.known).toEqual({
      units: 7,
      done: 1,
      eta: { p50: "2026-11-09T12:00:00Z", p85: "2026-11-11T12:00:00Z", samples: 42 },
      etaHidden: null,
    });
    expect(tree.unknown).toMatchObject({ scopeOpen: true, unbrokenCount: 1, recentUnits: 2, recentDays: 3 });
    expect(tree.unknown.pastGrowth[0]).toEqual({ root: ref(22), dayOne: 33, final: 69 });
    expect(tree.waves[0]?.units[0]).toMatchObject({ identifier: "MAG-977", reviewRound: 2, pr: { number: 8984 } });
    expect(tree.waves[0]?.issues).toEqual([
      { ...ref(960), status: "in_progress", parentId: "i950", waitingOn: "agent" },
      { ...ref(977), status: "in_review", parentId: "i960", waitingOn: "colleague" },
    ]);
  });

  it("reads a wave from a server that sends no outline", () => {
    const raw = rawOverview();
    delete (raw.trees[0]!.waves[0] as Record<string, unknown>).issues;
    raw.trees[0]!.waves[1]!.issues = [{ title: "no id" }, { ...ref(23), parent_id: 7 }] as never;
    const waves = parseAGOverview(raw).trees[0]!.waves;
    expect(waves[0]!.issues).toEqual([]);
    expect(waves[0]!.units).toHaveLength(1);
    expect(waves[1]!.issues).toEqual([{ ...ref(23), status: "", parentId: null, waitingOn: null }]);
  });

  it("falls back to empty for a response that is not an object", () => {
    expect(parseAGOverview(null)).toEqual(EMPTY_AG_OVERVIEW);
    expect(parseAGOverview("<html>")).toEqual(EMPTY_AG_OVERVIEW);
    expect(parseAGOverview({})).toEqual(EMPTY_AG_OVERVIEW);
  });

  it("drops unreadable rows and keeps the rest", () => {
    const raw = rawOverview();
    raw.now.running.push({ id: "", agent_name: 3 } as never);
    raw.now.ageing.push({ status: "in_review" } as never);
    raw.trees.push({ pin: null } as never);
    raw.trees[0]!.waves[0]!.units.push({ title: "no id" } as never);
    const o = parseAGOverview(raw);
    expect(o.now.running).toHaveLength(1);
    expect(o.now.ageing).toHaveLength(1);
    expect(o.trees).toHaveLength(1);
    expect(o.trees[0]!.waves[0]!.units).toHaveLength(1);
  });

  it("defaults missing or malformed fields", () => {
    const raw = rawOverview() as Record<string, unknown>;
    raw.now = { running: "nope", elsewhere: { agent: -1, colleague: "7", nobody: 2 } };
    const tree = (raw.trees as Record<string, unknown>[])[0]!;
    tree.waves = [{ root: ref(950), units: [rawUnit({ flow: "bench", review_round: "2", pr: { state: "open" } })] }];
    tree.known = "broken";
    const o = parseAGOverview(raw);
    expect(o.now.running).toEqual([]);
    expect(o.now.elsewhere).toEqual({ nobody: 2 });
    const unit = o.trees[0]!.waves[0]!.units[0]!;
    expect(unit).toMatchObject({ flow: [], reviewRound: null, pr: null });
    expect(o.trees[0]!.known).toEqual({ units: 0, done: 0, eta: null, etaHidden: "unknown" });
  });

  it("never reads unreadable unknown-scope facts as complete scope", () => {
    const raw = rawOverview();
    (raw.trees[0] as Record<string, unknown>).unknown = 42;
    expect(parseAGOverview(raw).trees[0]!.unknown.scopeOpen).toBe(true);
  });

  it("hides the ETA for a reason this build does not know", () => {
    const raw = rawOverview();
    raw.trees[0]!.known = { units: 3, done: 0, eta: null, eta_hidden: "new_reason" } as never;
    expect(parseAGOverview(raw).trees[0]!.known.etaHidden).toBe("unknown");
    raw.trees[0]!.known = { units: 3, done: 0, eta: null, eta_hidden: "few_samples" } as never;
    expect(parseAGOverview(raw).trees[0]!.known.etaHidden).toBe("few_samples");
  });

  it("parses a pin and rejects one without an id", () => {
    expect(parseAGOverviewPin({ id: "p1", kind: "issue", ref: "uuid", label: "MAG-950" })).toEqual({
      id: "p1",
      kind: "issue",
      ref: "uuid",
      label: "MAG-950",
    });
    expect(parseAGOverviewPin({ kind: "jira" })).toBeNull();
  });
});

const unit = (over: Partial<AGOverviewUnit>): AGOverviewUnit => ({
  ...ref(1),
  status: "in_progress",
  prevStatus: null,
  flow: ["bench", "tests", "review"],
  reviewRound: null,
  inStateSince: "",
  ageing: false,
  waitingOn: null,
  pr: null,
  ...over,
});

const strip = (u: AGOverviewUnit) =>
  agOverviewStages(u)
    .map((s) => `${s.name}:${s.state}${s.status ? `(${s.status})` : ""}${s.round ? `r${s.round}` : ""}`)
    .join(" ");

describe("AG patch: agOverviewStages", () => {
  it("places each status on the strip", () => {
    expect(strip(unit({ status: "in_progress", reviewRound: 6 }))).toBe(
      "build:currentr6 bench:todo tests:todo review:todo merge:todo",
    );
    expect(strip(unit({ status: "in_review", reviewRound: 2 }))).toBe(
      "build:done bench:done tests:done review:currentr2 merge:todo",
    );
    expect(strip(unit({ status: "done" }))).toBe("build:done bench:done tests:done review:done merge:done");
    expect(strip(unit({ status: "backlog" }))).toBe("build:todo bench:todo tests:todo review:todo merge:todo");
  });

  it("puts handoff and blocked where the previous status was", () => {
    expect(strip(unit({ status: "handoff", prevStatus: "in_review", flow: [] }))).toBe(
      "build:done review:current(handoff) merge:todo",
    );
    expect(strip(unit({ status: "blocked", prevStatus: "in_progress" }))).toBe(
      "build:current(blocked) bench:todo tests:todo review:todo merge:todo",
    );
    expect(strip(unit({ status: "something_new" }))).toBe(
      "build:current(something_new) bench:todo tests:todo review:todo merge:todo",
    );
  });

  it("does not repeat build or merge from the flow", () => {
    expect(strip(unit({ flow: ["build", "tests", "merge"], status: "in_review" }))).toBe(
      "build:done tests:current merge:todo",
    );
  });
});

describe("AG patch: overview helpers", () => {
  it("lists open units with ageing first, then the most stuck status", () => {
    const wave = {
      root: ref(950),
      done: false,
      issueCount: 6,
      startedAt: "",
      finishedAt: null,
      issues: [],
      units: [
        unit({ id: "a", number: 5, status: "backlog" }),
        unit({ id: "b", number: 4, status: "in_progress" }),
        unit({ id: "c", number: 3, status: "done" }),
        unit({ id: "d", number: 2, status: "in_review" }),
        unit({ id: "e", number: 1, status: "in_progress", ageing: true }),
      ],
    };
    expect(agOverviewOpenUnits(wave).map((u) => u.id)).toEqual(["e", "d", "b", "a"]);
  });

  const node = (n: number, parent: number | null, status = "in_progress"): AGOverviewNode => ({
    ...ref(n),
    status,
    parentId: parent === null ? null : `i${parent}`,
    waitingOn: null,
  });
  const wave = (over: Partial<AGOverviewWave>): AGOverviewWave => ({
    root: ref(950),
    done: false,
    issueCount: 0,
    startedAt: "",
    finishedAt: null,
    units: [],
    issues: [],
    ...over,
  });
  const outlined = (w: AGOverviewWave, showClosed = false) => {
    const { rows, closed } = agOverviewOutline(w, showClosed);
    return { rows: rows.map((r) => `${"  ".repeat(r.depth)}${r.node.number}${r.unit ? "*" : ""}`), closed };
  };

  it("outlines a wave with every issue under its parent, units marked, the most stuck sibling first", () => {
    const w = wave({
      units: [unit({ id: "i4", number: 4, status: "todo" }), unit({ id: "i6", number: 6, status: "in_progress", ageing: true })],
      issues: [
        node(2, 950, "todo"),
        node(3, 950, "in_progress"),
        node(4, 3, "todo"),
        node(5, 3, "blocked"),
        node(6, 2),
        node(7, 950, "done"),
      ],
    });
    expect(outlined(w)).toEqual({ rows: ["3", "  5", "  4*", "2", "  6*"], closed: 1 });
  });

  it("keeps closed issues out until asked, except a closed parent of open work", () => {
    const w = wave({
      units: [unit({ id: "i5", number: 5, status: "done" })],
      issues: [node(2, 950, "done"), node(3, 2, "todo"), node(4, 2, "cancelled"), node(5, 950, "done"), node(6, 5, "done")],
    });
    expect(outlined(w)).toEqual({ rows: ["2", "  3"], closed: 3 });
    expect(outlined(w, true)).toEqual({ rows: ["2", "  3", "  4", "5*", "  6"], closed: 3 });
  });

  it("leads the outline with a root that is itself a unit", () => {
    const root = unit({ ...ref(950), status: "in_review" });
    expect(outlined(wave({ units: [root], issues: [node(2, 950, "todo")] })).rows).toEqual(["950*", "  2"]);
    expect(outlined(wave({ units: [root] })).rows).toEqual(["950*"]);
  });

  it("lists units flat without an outline, and tops an issue whose parent is missing", () => {
    const units = [unit({ id: "i5", number: 5, status: "backlog" }), unit({ id: "i4", number: 4, status: "in_review" }), unit({ id: "i3", number: 3, status: "done" })];
    expect(outlined(wave({ units }))).toEqual({ rows: ["4*", "5*"], closed: 1 });
    expect(outlined(wave({ issues: [node(2, 999), node(3, 2)] })).rows).toEqual(["2", "  3"]);
  });

  it("merges every unit's stages into one ordered set of board columns", () => {
    expect(
      agOverviewBoardColumns([
        unit({ flow: ["review"] }),
        unit({ flow: ["tests", "review"] }),
        unit({ flow: ["bench", "tests", "review"] }),
      ]),
    ).toEqual(["build", "bench", "tests", "review", "merge"]);
    expect(agOverviewBoardColumns([unit({ flow: ["docs"] }), unit({ flow: ["review"] })])).toEqual([
      "build",
      "review",
      "docs",
      "merge",
    ]);
    expect(agOverviewBoardColumns([])).toEqual([]);
  });

  it("rounds growth to one decimal", () => {
    expect(agOverviewGrowth({ dayOne: 33, final: 69 })).toBe(2.1);
    expect(agOverviewGrowth({ dayOne: 8, final: 25 })).toBe(3.1);
  });
});

describe("AG patch: subscribeAGOverview", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("refetches once per window, however many events arrive", () => {
    vi.useFakeTimers();
    const handlers = new Map<string, () => void>();
    const offs: string[] = [];
    const ws = {
      on: (event: string, handler: () => void) => {
        handlers.set(event, handler);
        return () => offs.push(event);
      },
    } as unknown as WSClient;
    const qc = new QueryClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries").mockResolvedValue();

    const unsubscribe = subscribeAGOverview(ws, qc);
    handlers.get("task:running")!();
    handlers.get("issue:updated")!();
    handlers.get("ag:decision_changed")!();
    vi.advanceTimersByTime(AG_OVERVIEW_REFRESH_MS - 1);
    expect(invalidate).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: agOverviewKeys.all() });

    handlers.get("task:completed")!();
    unsubscribe();
    vi.advanceTimersByTime(AG_OVERVIEW_REFRESH_MS);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(offs).toContain("task:running");
  });
});
