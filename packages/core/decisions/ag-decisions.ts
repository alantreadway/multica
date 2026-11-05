import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// AG patch: Decision Desk. ag-jira-sync writes each card's text as data, so a
// new card type ships without a web build; this parser keeps whatever it can
// read and drops the rest. No API import here: api/ag-client.ts imports this.

/** The page warns once the engine has been silent this long. */
export const ENGINE_STALE_AFTER_MS = 10 * 60 * 1000;

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
}

export interface AGDecisionList {
  cards: AGDecisionCard[];
  lastSyncAt: string | null;
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
});

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
    };
  });

const listSchema = z.object({
  cards: itemsOf(cardSchema),
  last_sync_at: z.string().nullable().catch(null),
});

export const EMPTY_AG_DECISION_LIST: AGDecisionList = { cards: [], lastSyncAt: null };

export function parseAGDecisionList(raw: unknown): AGDecisionList {
  const parsed = parseWithFallback<{ cards: AGDecisionCard[]; last_sync_at: string | null } | null>(
    raw,
    listSchema,
    null,
    { endpoint: "/api/ag/decisions" },
  );
  return parsed ? { cards: parsed.cards, lastSyncAt: parsed.last_sync_at } : EMPTY_AG_DECISION_LIST;
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
