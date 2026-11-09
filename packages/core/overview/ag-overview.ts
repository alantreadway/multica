import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// AG patch: Overview dashboard. GET /api/ag/overview computes everything; this
// module parses it and holds the pure rules the page needs (the stage strip,
// the scope verdict). No API import here: api/ag-client.ts imports this.

export interface AGOverviewIssueRef {
  id: string;
  identifier: string;
  number: number;
  title: string;
}

export interface AGOverviewPin {
  id: string;
  /** "jira" or "issue"; an unknown kind still lists and unpins. */
  kind: string;
  ref: string;
  label: string;
}

export interface AGOverviewTask {
  id: string;
  status: string;
  agentId: string;
  agentName: string;
  issue: AGOverviewIssueRef | null;
  reviewRound: number | null;
  /** When it started running, or was queued. */
  since: string;
  tree: string | null;
}

export interface AGOverviewCard {
  id: string;
  cardType: string;
  issue: AGOverviewIssueRef | null;
}

export interface AGOverviewAgeing {
  issue: AGOverviewIssueRef;
  status: string;
  inStateSince: string;
  p90Hours: number;
  waitingOn: string | null;
  tree: string | null;
}

export interface AGOverviewPR {
  number: number;
  state: string;
  checks: string | null;
  review: string | null;
}

export interface AGOverviewUnit extends AGOverviewIssueRef {
  status: string;
  prevStatus: string | null;
  flow: string[];
  reviewRound: number | null;
  inStateSince: string;
  ageing: boolean;
  waitingOn: string | null;
  pr: AGOverviewPR | null;
}

export interface AGOverviewWave {
  root: AGOverviewIssueRef;
  done: boolean;
  issueCount: number;
  startedAt: string;
  finishedAt: string | null;
  units: AGOverviewUnit[];
}

export type AGOverviewETAHidden = "few_samples" | "no_open_units" | "unknown";

export interface AGOverviewTree {
  pin: AGOverviewPin;
  title: string | null;
  issueCount: number;
  startedAt: string | null;
  runHours: number;
  known: {
    units: number;
    done: number;
    eta: { p50: string; p85: string; samples: number } | null;
    etaHidden: AGOverviewETAHidden | null;
  };
  unknown: {
    scopeOpen: boolean;
    unbroken: (AGOverviewIssueRef & { status: string })[];
    unbrokenCount: number;
    recentUnits: number;
    recentDays: number;
    pastGrowth: { root: AGOverviewIssueRef; dayOne: number; final: number }[];
  };
  waves: AGOverviewWave[];
}

export interface AGOverview {
  generatedAt: string | null;
  deskSyncedAt: string | null;
  now: {
    running: AGOverviewTask[];
    queued: AGOverviewTask[];
    waitingOnYou: AGOverviewCard[];
    /** Open Desk cards by who else they wait on. */
    elsewhere: Record<string, number>;
    ageing: AGOverviewAgeing[];
  };
  trees: AGOverviewTree[];
}

export const EMPTY_AG_OVERVIEW: AGOverview = {
  generatedAt: null,
  deskSyncedAt: null,
  now: { running: [], queued: [], waitingOnYou: [], elsewhere: {}, ageing: [] },
  trees: [],
};

/** An array that keeps its readable items: one bad row must not blank the section. */
function itemsOf<T extends z.ZodType>(item: T) {
  return z
    .array(z.unknown())
    .catch([])
    .default([])
    .transform((values) =>
      values.flatMap((value) => {
        const parsed = item.safeParse(value);
        return parsed.success ? [parsed.data as z.output<T>] : [];
      }),
    );
}

const optionalString = z.string().nullable().catch(null).default(null);
const optionalCount = z.number().int().nonnegative().catch(0).default(0);
const optionalRound = z.number().int().nonnegative().nullable().catch(null).default(null);

const issueRefSchema = z
  .object({
    id: z.string().min(1),
    identifier: z.string().catch(""),
    number: z.number().catch(0),
    title: z.string().catch(""),
  })
  .transform((ref): AGOverviewIssueRef => ({
    id: ref.id,
    identifier: ref.identifier || `#${ref.number}`,
    number: ref.number,
    title: ref.title,
  }));

const optionalIssueRef = issueRefSchema.nullable().catch(null).default(null);

export const agOverviewPinSchema = z.object({
  id: z.string().min(1),
  kind: z.string().catch(""),
  ref: z.string().catch(""),
  label: z.string().catch(""),
});

const taskSchema = z
  .object({
    id: z.string().min(1),
    status: z.string().catch(""),
    agent_id: z.string().catch(""),
    agent_name: z.string().catch(""),
    issue: optionalIssueRef,
    review_round: optionalRound,
    since: z.string().catch(""),
    tree: optionalString,
  })
  .transform(
    (t): AGOverviewTask => ({
      id: t.id,
      status: t.status,
      agentId: t.agent_id,
      agentName: t.agent_name,
      issue: t.issue,
      reviewRound: t.review_round,
      since: t.since,
      tree: t.tree,
    }),
  );

const cardSchema = z
  .object({ id: z.string().min(1), card_type: z.string().catch(""), issue: optionalIssueRef })
  .transform((c): AGOverviewCard => ({ id: c.id, cardType: c.card_type, issue: c.issue }));

const ageingSchema = z
  .object({
    issue: issueRefSchema,
    status: z.string().catch(""),
    in_state_since: z.string().catch(""),
    p90_hours: z.number().catch(0),
    waiting_on: optionalString,
    tree: optionalString,
  })
  .transform(
    (a): AGOverviewAgeing => ({
      issue: a.issue,
      status: a.status,
      inStateSince: a.in_state_since,
      p90Hours: a.p90_hours,
      waitingOn: a.waiting_on,
      tree: a.tree,
    }),
  );

const prSchema = z
  .object({
    number: z.number(),
    state: z.string().catch(""),
    checks: optionalString,
    review: optionalString,
  })
  .nullable()
  .catch(null)
  .default(null);

const unitSchema = z
  .object({
    id: z.string().min(1),
    identifier: z.string().catch(""),
    number: z.number().catch(0),
    title: z.string().catch(""),
    status: z.string().catch(""),
    prev_status: optionalString,
    flow: itemsOf(z.string().min(1)),
    review_round: optionalRound,
    in_state_since: z.string().catch(""),
    ageing: z.boolean().catch(false),
    waiting_on: optionalString,
    pr: prSchema,
  })
  .transform(
    (u): AGOverviewUnit => ({
      id: u.id,
      identifier: u.identifier || `#${u.number}`,
      number: u.number,
      title: u.title,
      status: u.status,
      prevStatus: u.prev_status,
      flow: u.flow,
      reviewRound: u.review_round,
      inStateSince: u.in_state_since,
      ageing: u.ageing,
      waitingOn: u.waiting_on,
      pr: u.pr,
    }),
  );

const waveSchema = z
  .object({
    root: issueRefSchema,
    done: z.boolean().catch(false),
    issue_count: optionalCount,
    started_at: z.string().catch(""),
    finished_at: optionalString,
    units: itemsOf(unitSchema),
  })
  .transform(
    (w): AGOverviewWave => ({
      root: w.root,
      done: w.done,
      issueCount: w.issue_count,
      startedAt: w.started_at,
      finishedAt: w.finished_at,
      units: w.units,
    }),
  );

const ETA_HIDDEN = ["few_samples", "no_open_units"] as const;

const treeSchema = z
  .object({
    pin: agOverviewPinSchema,
    title: optionalString,
    issue_count: optionalCount,
    started_at: optionalString,
    run_hours: z.number().nonnegative().catch(0).default(0),
    known: z
      .object({
        units: optionalCount,
        done: optionalCount,
        eta: z
          .object({ p50: z.string().min(1), p85: z.string().min(1), samples: optionalCount })
          .nullable()
          .catch(null)
          .default(null),
        // A reason this build does not know still hides the ETA.
        eta_hidden: z
          .union([z.enum(ETA_HIDDEN), z.string().transform(() => "unknown" as const)])
          .nullable()
          .catch(null)
          .default(null),
      })
      .catch({ units: 0, done: 0, eta: null, eta_hidden: null }),
    unknown: z
      .object({
        scope_open: z.boolean().catch(true),
        unbroken: itemsOf(
          z.object({
            id: z.string().min(1),
            identifier: z.string().catch(""),
            number: z.number().catch(0),
            title: z.string().catch(""),
            status: z.string().catch(""),
          }),
        ),
        unbroken_count: optionalCount,
        recent_units: optionalCount,
        recent_days: optionalCount,
        past_growth: itemsOf(
          z.object({ root: issueRefSchema, day_one: z.number().int().positive(), final: z.number().int().positive() }),
        ),
      })
      // Unreadable unknown-scope facts must not read as "scope looks complete".
      .catch({ scope_open: true, unbroken: [], unbroken_count: 0, recent_units: 0, recent_days: 0, past_growth: [] }),
    waves: itemsOf(waveSchema),
  })
  .transform(
    (t): AGOverviewTree => ({
      pin: t.pin,
      title: t.title,
      issueCount: t.issue_count,
      startedAt: t.started_at,
      runHours: t.run_hours,
      known: {
        units: t.known.units,
        done: t.known.done,
        eta: t.known.eta,
        etaHidden: t.known.eta ? null : (t.known.eta_hidden ?? "unknown"),
      },
      unknown: {
        scopeOpen: t.unknown.scope_open,
        unbroken: t.unknown.unbroken.map((u) => ({ ...u, identifier: u.identifier || `#${u.number}` })),
        unbrokenCount: Math.max(t.unknown.unbroken_count, t.unknown.unbroken.length),
        recentUnits: t.unknown.recent_units,
        recentDays: t.unknown.recent_days,
        pastGrowth: t.unknown.past_growth.map((g) => ({ root: g.root, dayOne: g.day_one, final: g.final })),
      },
      waves: t.waves,
    }),
  );

const overviewSchema = z.object({
  generated_at: optionalString,
  desk_synced_at: optionalString,
  now: z
    .object({
      running: itemsOf(taskSchema),
      queued: itemsOf(taskSchema),
      waiting_on_you: itemsOf(cardSchema),
      elsewhere: z
        .record(z.string(), z.unknown())
        .catch({})
        .default({})
        .transform((counts) =>
          Object.fromEntries(
            Object.entries(counts).filter(
              (entry): entry is [string, number] => Number.isInteger(entry[1]) && (entry[1] as number) > 0,
            ),
          ),
        ),
      ageing: itemsOf(ageingSchema),
    })
    .catch({ running: [], queued: [], waiting_on_you: [], elsewhere: {}, ageing: [] }),
  trees: itemsOf(treeSchema),
});

export function parseAGOverview(raw: unknown): AGOverview {
  const parsed = parseWithFallback<z.output<typeof overviewSchema> | null>(raw, overviewSchema, null, {
    endpoint: "/api/ag/overview",
  });
  if (!parsed) return EMPTY_AG_OVERVIEW;
  return {
    generatedAt: parsed.generated_at,
    deskSyncedAt: parsed.desk_synced_at,
    now: {
      running: parsed.now.running,
      queued: parsed.now.queued,
      waitingOnYou: parsed.now.waiting_on_you,
      elsewhere: parsed.now.elsewhere,
      ageing: parsed.now.ageing,
    },
    trees: parsed.trees,
  };
}

export function parseAGOverviewPin(raw: unknown): AGOverviewPin | null {
  return parseWithFallback<AGOverviewPin | null>(raw, agOverviewPinSchema, null, {
    endpoint: "POST /api/ag/overview/pins",
  });
}

// --- Stage strip ----------------------------------------------------------------

export type AGOverviewStageState = "done" | "current" | "todo";

export interface AGOverviewStage {
  /** A unit_flow step, or "build" / "merge" around them. */
  name: string;
  state: AGOverviewStageState;
  /** What the current stage shows instead of its name, e.g. "handoff" or "blocked". */
  status: string | null;
  /** Review round on the current stage, when past round 0. */
  round: number | null;
}

/**
 * build ─ <unit_flow steps> ─ merge, with the unit's place marked. A unit in
 * handoff or blocked sits where its previous status put it.
 */
export function agOverviewStages(unit: Pick<AGOverviewUnit, "status" | "prevStatus" | "flow" | "reviewRound">): AGOverviewStage[] {
  const steps = unit.flow.filter((s) => s !== "build" && s !== "merge");
  const names = ["build", ...(steps.length > 0 ? steps : ["review"]), "merge"];
  const reviewIndex = names.lastIndexOf("review") > 0 ? names.lastIndexOf("review") : names.length - 2;
  const indexFor = (status: string | null): number => (status === "in_review" ? reviewIndex : 0);

  let current = -1;
  let status: string | null = null;
  switch (unit.status) {
    case "done":
      current = names.length;
      break;
    case "backlog":
    case "todo":
    case "cancelled":
      current = -1;
      break;
    case "in_progress":
    case "in_review":
      current = indexFor(unit.status);
      break;
    default:
      current = indexFor(unit.prevStatus);
      status = unit.status || null;
  }
  return names.map((name, i) => ({
    name,
    state: i < current ? "done" : i === current ? "current" : "todo",
    status: i === current ? status : null,
    round: i === current && unit.reviewRound && unit.reviewRound > 0 ? unit.reviewRound : null,
  }));
}

/**
 * The stage columns for a board of units: build first, merge last, and every
 * flow step in between in the order the units run them. A unit without a step
 * leaves that column empty.
 */
export function agOverviewBoardColumns(units: Pick<AGOverviewUnit, "status" | "prevStatus" | "flow" | "reviewRound">[]): string[] {
  const columns: string[] = [];
  for (const unit of units) {
    let at = -1;
    for (const { name } of agOverviewStages(unit)) {
      const found = columns.indexOf(name);
      if (found >= 0) {
        at = Math.max(at, found);
      } else {
        at += 1;
        columns.splice(at, 0, name);
      }
    }
  }
  return columns;
}

/** Units still to finish, in the order the page lists them: the most stuck first. */
const STATUS_ORDER = ["blocked", "handoff", "in_review", "in_progress", "todo", "backlog"];

export function agOverviewOpenUnits(wave: AGOverviewWave): AGOverviewUnit[] {
  const rank = (u: AGOverviewUnit) => {
    const i = STATUS_ORDER.indexOf(u.status);
    return i < 0 ? STATUS_ORDER.length : i;
  };
  return wave.units
    .filter((u) => u.status !== "done" && u.status !== "cancelled")
    .sort((a, b) => Number(b.ageing) - Number(a.ageing) || rank(a) - rank(b) || a.number - b.number);
}

/** Growth of a past wave after its first day, e.g. 2.1 for 33 issues that ended at 69. */
export function agOverviewGrowth(g: { dayOne: number; final: number }): number {
  return Math.round((g.final / g.dayOne) * 10) / 10;
}
