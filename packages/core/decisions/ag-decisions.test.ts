// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  countAGDecisionsByWaitingOn,
  ENGINE_STALE_AFTER_MS,
  groupAGDecisionCards,
  isAGDecisionEngineStale,
  parseAGDecisionCount,
  parseAGDecisionList,
  type AGDecisionCard,
} from "./ag-decisions";

const rawCard = (over: Record<string, unknown> = {}) => ({
  id: "c1",
  issue_id: "i1",
  waiting_on: "alan",
  card_type: "pr_review",
  revision: 2,
  head_sha: "abc",
  first_seen_at: "2026-11-01T00:00:00Z",
  content: {
    title: "Fix axis",
    group: "PR review",
    rank: 1,
    issue_ref: "MAG-1",
    recommended: "Approve",
    chips: [{ label: "CI green", tone: "good" }],
    lines: ["One file"],
    bullets: ["First", "Second"],
    waiting_since: "2026-10-20T00:00:00Z",
    blockers: ["Label: Breaking"],
    links: [{ label: "PR", url: "https://github.com/ag-grid/ag-charts/pull/1" }],
  },
  ...over,
});

const card = (over: Partial<AGDecisionCard>): AGDecisionCard => ({
  id: "c",
  issueId: null,
  waitingOn: "alan",
  cardType: "t",
  revision: 0,
  headSha: null,
  waitingSince: null,
  title: "",
  group: "",
  rank: 0,
  issueRef: "",
  recommended: "",
  chips: [],
  lines: [],
  bullets: [],
  blockers: [],
  links: [],
  ...over,
});

describe("parseAGDecisionList", () => {
  it("reads a well-formed list", () => {
    const list = parseAGDecisionList({ cards: [rawCard()], last_sync_at: "2026-11-05T10:00:00Z" });
    expect(list.lastSyncAt).toBe("2026-11-05T10:00:00Z");
    expect(list.cards[0]).toMatchObject({
      id: "c1",
      issueId: "i1",
      title: "Fix axis",
      issueRef: "MAG-1",
      chips: [{ label: "CI green", tone: "good" }],
    });
  });

  it.each([null, "nope", 42, { cards: "nope" }])("returns an empty list for %j", (raw) => {
    expect(parseAGDecisionList(raw).cards).toEqual([]);
  });

  it("drops an unreadable card and keeps the others", () => {
    const list = parseAGDecisionList({ cards: [{ nope: true }, rawCard()], last_sync_at: null });
    expect(list.cards.map((c) => c.id)).toEqual(["c1"]);
  });

  it("keeps a card whose content is not an object", () => {
    const list = parseAGDecisionList({ cards: [rawCard({ content: "nope" })], last_sync_at: null });
    expect(list.cards[0]).toMatchObject({ id: "c1", title: "", chips: [] });
  });

  it("keeps a card's readable parts when one chip or line is bad", () => {
    const content = {
      chips: [{ label: "ok", tone: "shiny" }, { tone: "good" }],
      lines: ["kept", 7],
    };
    const [parsed] = parseAGDecisionList({ cards: [rawCard({ content })], last_sync_at: null }).cards;
    expect(parsed?.chips).toEqual([{ label: "ok", tone: "neutral" }]);
    expect(parsed?.lines).toEqual(["kept"]);
  });

  it("drops a link that is not http or https", () => {
    const content = {
      links: [
        { label: "bad", url: "javascript:alert(1)" },
        { label: "good", url: "https://example.com" },
      ],
    };
    const [parsed] = parseAGDecisionList({ cards: [rawCard({ content })], last_sync_at: null }).cards;
    expect(parsed?.links.map((l) => l.label)).toEqual(["good"]);
  });
});

describe("parseAGDecisionCount", () => {
  it("reads a count, and zero from anything else", () => {
    expect(parseAGDecisionCount({ count: 3 })).toBe(3);
    expect(parseAGDecisionCount({ count: -1 })).toBe(0);
    expect(parseAGDecisionCount(null)).toBe(0);
  });
});

describe("parseAGDecisionList waiting time and bullets", () => {
  const parsedCard = (content: Record<string, unknown>) =>
    parseAGDecisionList({ cards: [rawCard({ content })], last_sync_at: null }).cards[0];

  it("prefers when the issue entered its status over the card's first sighting", () => {
    expect(parsedCard({ waiting_since: "2026-10-20T00:00:00Z" })?.waitingSince).toBe("2026-10-20T00:00:00Z");
  });

  it.each([
    ["missing", {}],
    ["empty", { waiting_since: "" }],
    ["malformed", { waiting_since: 7 }],
  ])("falls back to the first sighting when waiting_since is %s", (_, content) => {
    expect(parsedCard(content)?.waitingSince).toBe("2026-11-01T00:00:00Z");
  });

  it("keeps the readable bullets", () => {
    expect(parsedCard({ bullets: ["First", 3, ""] })?.bullets).toEqual(["First"]);
    expect(parsedCard({ bullets: "First" })?.bullets).toEqual([]);
  });
});

describe("groupAGDecisionCards", () => {
  it("orders groups by their lowest rank and cards by rank, then longest waiting", () => {
    const groups = groupAGDecisionCards([
      card({ id: "late", group: "B", rank: 5, waitingSince: "2026-11-02" }),
      card({ id: "early", group: "B", rank: 5, waitingSince: "2026-11-01" }),
      card({ id: "top", group: "A", rank: 1 }),
    ]);
    expect(groups.map((g) => g.name)).toEqual(["A", "B"]);
    expect(groups[1]?.cards.map((c) => c.id)).toEqual(["early", "late"]);
  });
});

describe("countAGDecisionsByWaitingOn", () => {
  it("leads with Alan even at zero, then the largest", () => {
    expect(
      countAGDecisionsByWaitingOn([
        card({ waitingOn: "author" }),
        card({ waitingOn: "agent" }),
        card({ waitingOn: "agent" }),
      ]),
    ).toEqual([
      { waitingOn: "alan", count: 0 },
      { waitingOn: "agent", count: 2 },
      { waitingOn: "author", count: 1 },
    ]);
  });
});

describe("isAGDecisionEngineStale", () => {
  const at = "2026-11-05T10:00:00Z";
  const base = Date.parse(at);

  it("turns stale only past the limit", () => {
    expect(isAGDecisionEngineStale(at, base + ENGINE_STALE_AFTER_MS)).toBe(false);
    expect(isAGDecisionEngineStale(at, base + ENGINE_STALE_AFTER_MS + 1)).toBe(true);
  });

  it("is not stale without a readable report", () => {
    expect(isAGDecisionEngineStale(null, base)).toBe(false);
    expect(isAGDecisionEngineStale("nope", base)).toBe(false);
  });
});
