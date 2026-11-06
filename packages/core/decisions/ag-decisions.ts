import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// AG patch: Decision Desk. ag-jira-sync writes each card's text as data, so a
// new card type ships without a web build; this parser keeps whatever it can
// read and drops the rest. No API import here: api/ag-client.ts imports this.

/** The page warns once the engine has been silent this long. */
export const ENGINE_STALE_AFTER_MS = 10 * 60 * 1000;

/** The worker checks in every few seconds, so this long without one means it is not running. */
export const WORKER_OFFLINE_AFTER_MS = 60 * 1000;

export const WAITING_ON_ALAN = "alan";

export type AGDecisionChipTone = "neutral" | "good" | "warn" | "bad";

export interface AGDecisionChip {
  label: string;
  tone: AGDecisionChipTone;
}

export interface AGDecisionLink {
  label: string;
  url: string;
}

export interface AGDecisionAction {
  id: string;
  label: string;
  primary: boolean;
  batch: boolean;
  /** Why this needs a second tap, or empty when it does not. */
  confirm: string;
  /** Asks for text first. */
  note: { required: boolean; placeholder: string } | null;
}

export type AGDecisionIntentStatus =
  | "pending"
  | "running"
  | "succeeded"
  | "failed"
  | "refused"
  | "expired"
  | "cancelled";

export interface AGDecisionIntent {
  id: string;
  cardRevision: number;
  actionId: string;
  status: AGDecisionIntentStatus;
  message: string;
}

export interface AGDecisionCard {
  id: string;
  issueId: string | null;
  waitingOn: string;
  cardType: string;
  revision: number;
  headSha: string | null;
  /** When the issue entered its current status, or the card's first sighting if the engine could not read that. */
  waitingSince: string | null;
  title: string;
  group: string;
  rank: number;
  issueRef: string;
  recommended: string;
  chips: AGDecisionChip[];
  lines: string[];
  bullets: string[];
  blockers: string[];
  links: AGDecisionLink[];
  actions: AGDecisionAction[];
  /** The id of the action the engine recommends, when it recommends one it can run. */
  recommendedAction: string;
  overrideReasons: string[];
  latestIntent: AGDecisionIntent | null;
}

/** What the page sends for a click. The revision pins what was on screen. */
export interface AGDecisionIntentInput {
  revision: number;
  action_id: string;
  note: string;
  override_reason: string;
}

export interface AGDecisionList {
  cards: AGDecisionCard[];
  lastSyncAt: string | null;
  workerSeenAt: string | null;
}

export interface AGDecisionGroup {
  name: string;
  cards: AGDecisionCard[];
}

/** An array that keeps its readable items: one bad chip must not cost the card its others. */
function itemsOf<T extends z.ZodType>(item: T) {
  return z
    .array(z.unknown())
    .catch([])
    .transform((values) =>
      values.flatMap((value) => {
        const parsed = item.safeParse(value);
        return parsed.success ? [parsed.data as z.output<T>] : [];
      }),
    );
}

const isWebUrl = (url: string) => /^https?:\/\//i.test(url);

const contentSchema = z.object({
  title: z.string().catch(""),
  group: z.string().catch(""),
  rank: z.number().finite().catch(Number.MAX_SAFE_INTEGER),
  issue_ref: z.string().catch(""),
  recommended: z.string().catch(""),
  chips: itemsOf(
    z.object({
      label: z.string().min(1),
      tone: z.enum(["neutral", "good", "warn", "bad"]).catch("neutral"),
    }),
  ),
  lines: itemsOf(z.string().min(1)),
  bullets: itemsOf(z.string().min(1)),
  waiting_since: z.string().catch(""),
  blockers: itemsOf(z.string().min(1)),
  links: itemsOf(z.object({ label: z.string().min(1), url: z.string().refine(isWebUrl) })),
  actions: itemsOf(
    z.object({
      id: z.string().min(1),
      label: z.string().min(1),
      primary: z.boolean().catch(false),
      batch: z.boolean().catch(false),
      confirm: z.string().catch(""),
      note: z
        .object({ required: z.boolean().catch(false), placeholder: z.string().catch("") })
        .nullable()
        .catch(null)
        .default(null),
    }),
  ),
  recommended_action: z.string().catch(""),
  override_reasons: itemsOf(z.string().min(1)),
});

const INTENT_STATUSES = ["pending", "running", "succeeded", "failed", "refused", "expired", "cancelled"] as const;

/** A status this build does not know reads as failed: the card keeps its buttons, and says what the server said. */
const intentSchema = z
  .object({
    id: z.string().min(1),
    card_revision: z.number().catch(0),
    action_id: z.string().catch(""),
    status: z.enum(INTENT_STATUSES).catch("failed"),
    message: z.string().catch(""),
  })
  .transform(
    (intent): AGDecisionIntent => ({
      id: intent.id,
      cardRevision: intent.card_revision,
      actionId: intent.action_id,
      status: intent.status,
      message: intent.message,
    }),
  );

const cardSchema = z
  .object({
    id: z.string().min(1),
    issue_id: z.string().nullable().catch(null),
    waiting_on: z.string().min(1),
    card_type: z.string().catch(""),
    revision: z.number().catch(0),
    head_sha: z.string().nullable().catch(null),
    first_seen_at: z.string().nullable().catch(null),
    content: z.unknown(),
    latest_intent: intentSchema.nullable().catch(null).default(null),
  })
  .transform((card): AGDecisionCard => {
    const parsed = contentSchema.safeParse(card.content ?? {});
    const content = parsed.success ? parsed.data : contentSchema.parse({});
    return {
      id: card.id,
      issueId: card.issue_id,
      waitingOn: card.waiting_on,
      cardType: card.card_type,
      revision: card.revision,
      headSha: card.head_sha,
      waitingSince: content.waiting_since || card.first_seen_at,
      title: content.title,
      group: content.group,
      rank: content.rank,
      issueRef: content.issue_ref,
      recommended: content.recommended,
      chips: content.chips,
      lines: content.lines,
      bullets: content.bullets,
      blockers: content.blockers,
      links: content.links,
      actions: content.actions,
      recommendedAction: content.recommended_action,
      overrideReasons: content.override_reasons,
      latestIntent: card.latest_intent,
    };
  });

const listSchema = z.object({
  cards: itemsOf(cardSchema),
  last_sync_at: z.string().nullable().catch(null),
  worker_seen_at: z.string().nullable().catch(null),
});

export const EMPTY_AG_DECISION_LIST: AGDecisionList = { cards: [], lastSyncAt: null, workerSeenAt: null };

export function parseAGDecisionList(raw: unknown): AGDecisionList {
  const parsed = parseWithFallback<{
    cards: AGDecisionCard[];
    last_sync_at: string | null;
    worker_seen_at: string | null;
  } | null>(
    raw,
    listSchema,
    null,
    { endpoint: "/api/ag/decisions" },
  );
  return parsed
    ? { cards: parsed.cards, lastSyncAt: parsed.last_sync_at, workerSeenAt: parsed.worker_seen_at }
    : EMPTY_AG_DECISION_LIST;
}

export function parseAGDecisionCount(raw: unknown): number {
  return parseWithFallback<{ count: number }>(
    raw,
    z.object({ count: z.number().int().nonnegative().catch(0) }),
    { count: 0 },
    { endpoint: "/api/ag/decisions/count" },
  ).count;
}

/** Groups in engine order (the lowest rank inside each), cards by rank, then longest waiting. */
export function groupAGDecisionCards(cards: readonly AGDecisionCard[]): AGDecisionGroup[] {
  const sorted = [...cards].sort(
    (a, b) => a.rank - b.rank || (a.waitingSince ?? "").localeCompare(b.waitingSince ?? ""),
  );
  const groups = new Map<string, AGDecisionCard[]>();
  for (const card of sorted) {
    const members = groups.get(card.group);
    if (members) members.push(card);
    else groups.set(card.group, [card]);
  }
  return [...groups].map(([name, members]) => ({ name, cards: members }));
}

/** Who holds how many cards, the most first, with Alan always leading. */
export function countAGDecisionsByWaitingOn(
  cards: readonly AGDecisionCard[],
): { waitingOn: string; count: number }[] {
  const counts = new Map<string, number>([[WAITING_ON_ALAN, 0]]);
  for (const card of cards) counts.set(card.waitingOn, (counts.get(card.waitingOn) ?? 0) + 1);
  return [...counts]
    .map(([waitingOn, count]) => ({ waitingOn, count }))
    .sort((a, b) => Number(b.waitingOn === WAITING_ON_ALAN) - Number(a.waitingOn === WAITING_ON_ALAN) || b.count - a.count);
}

/** True when the engine has reported but not recently. Before any report the page says so itself. */
export function isAGDecisionEngineStale(lastSyncAt: string | null, now: number): boolean {
  if (!lastSyncAt) return false;
  const at = Date.parse(lastSyncAt);
  return !Number.isNaN(at) && now - at > ENGINE_STALE_AFTER_MS;
}

/** True when clicks would sit in the queue. A worker never seen counts: nothing is going to pick them up. */
export function isAGDecisionWorkerOffline(workerSeenAt: string | null, now: number): boolean {
  if (!workerSeenAt) return true;
  const at = Date.parse(workerSeenAt);
  return Number.isNaN(at) || now - at > WORKER_OFFLINE_AFTER_MS;
}

/** The intent worth showing: anything live, or how the last click on this revision ended. A newer revision starts clean. */
export function visibleAGDecisionIntent(card: AGDecisionCard): AGDecisionIntent | null {
  const intent = card.latestIntent;
  if (!intent || intent.status === "cancelled") return null;
  if (intent.status === "pending" || intent.status === "running") return intent;
  return intent.cardRevision === card.revision ? intent : null;
}

/** Actions stay offered unless one is under way or has just worked, so a failure's retry is the same button. */
export function agDecisionCardIsActionable(card: AGDecisionCard): boolean {
  const status = visibleAGDecisionIntent(card)?.status;
  return status !== "pending" && status !== "running" && status !== "succeeded";
}

/** Actions every actionable card in a group offers, for clearing same-shape cards together. Needs two cards to be a batch. */
export function agDecisionBatchActions(
  cards: readonly AGDecisionCard[],
): { action: AGDecisionAction; cards: AGDecisionCard[] }[] {
  const ready = cards.filter(agDecisionCardIsActionable);
  const [first] = ready;
  if (!first || ready.length < 2) return [];
  return first.actions
    .filter((action) => action.batch && ready.every((card) => card.actions.some((a) => a.id === action.id && a.batch)))
    .map((action) => ({ action, cards: ready }));
}

/**
 * Where a card sits on the page: in the queue waiting for a decision, back for a retry after a click that did not
 * work, or in Activity once a click is under way or has worked.
 */
export type AGDecisionPlace = "queue" | "retry" | "activity";

export function agDecisionPlace(card: AGDecisionCard): AGDecisionPlace {
  const status = visibleAGDecisionIntent(card)?.status;
  if (!status) return "queue";
  return status === "pending" || status === "running" || status === "succeeded" ? "activity" : "retry";
}

const LINE_LABEL = /^([A-Z][A-Za-z ]{1,15}):\s+(\S.*)$/;

/** "Status: PR ready" as a label and its value. The engine writes lines as prose, so one without a short leading label stays prose. */
export function splitAGDecisionLine(line: string): { label: string; value: string } | null {
  const match = LINE_LABEL.exec(line);
  return match ? { label: match[1] as string, value: match[2] as string } : null;
}

/** An answer to an agent's question: the engine offers each of the agent's options as a `reply:<letter>` action. */
export const isAGDecisionOption = (action: AGDecisionAction) => action.id.startsWith("reply:");

/** A card's actions in shortcut order: the agent's options, then the recommended action, then the rest as the engine sent them. */
export function agDecisionKeyedActions(card: AGDecisionCard): AGDecisionAction[] {
  const options = card.actions.filter(isAGDecisionOption);
  const rest = card.actions.filter((action) => !isAGDecisionOption(action));
  const recommended = rest.filter((action) => action.id === card.recommendedAction);
  return [...options, ...recommended, ...rest.filter((action) => action.id !== card.recommendedAction)];
}
