// @vitest-environment node
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { WSClient } from "../api/ws-client";
import { agDecisionKeys, subscribeAGDecisions } from "./ag-decisions-queries";
import {
  agDecisionBatchActions,
  agDecisionCardIsActionable,
  agDecisionKeyedActions,
  agDecisionPlace,
  countAGDecisionsByWaitingOn,
  ENGINE_STALE_AFTER_MS,
  groupAGDecisionCards,
  isAGDecisionEngineStale,
  isAGDecisionWorkerOffline,
  parseAGDecisionCount,
  parseAGDecisionList,
  splitAGDecisionLine,
  visibleAGDecisionIntent,
  WORKER_OFFLINE_AFTER_MS,
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
  actions: [],
  recommendedAction: "",
  overrideReasons: [],
  focusCommentId: null,
  latestIntent: null,
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

  it("reads the comment a card is about, and has none when it is missing or malformed", () => {
    const focus = (content: unknown) =>
      parseAGDecisionList({ cards: [rawCard({ content })], last_sync_at: null }).cards[0]?.focusCommentId;
    expect(focus({ focus_comment_id: "cm1" })).toBe("cm1");
    expect(focus({})).toBeNull();
    expect(focus({ focus_comment_id: 7 })).toBeNull();
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

describe("actions", () => {
  const parsed = (content: Record<string, unknown>, over: Record<string, unknown> = {}) =>
    parseAGDecisionList({ cards: [{ ...rawCard(over), content }], last_sync_at: null, worker_seen_at: "2026-11-01T00:00:00Z" });
  const firstCard = (content: Record<string, unknown>, over: Record<string, unknown> = {}) =>
    parsed(content, over).cards[0] as AGDecisionCard;

  it("keeps the readable actions, and defaults what an older engine leaves out", () => {
    const card = firstCard({ actions: [{ id: "park", label: "Snooze" }, { id: "" }, "nope"], recommended_action: 7 });
    expect(card.actions).toEqual([
      { id: "park", label: "Snooze", primary: false, batch: false, confirm: "", note: null, effect: "" },
    ]);
    expect(card.recommendedAction).toBe("");
    expect(card.latestIntent).toBeNull();
  });

  it("reads what an action does", () => {
    const card = firstCard({ actions: [{ id: "park", label: "Snooze", effect: "Moves it to backlog." }] });
    expect(card.actions[0]?.effect).toBe("Moves it to backlog.");
  });

  it("reads the worker's last check-in, and a payload without one", () => {
    expect(parsed({}).workerSeenAt).toBe("2026-11-01T00:00:00Z");
    expect(parseAGDecisionList({ cards: [], last_sync_at: null }).workerSeenAt).toBeNull();
  });

  it("reads an unknown intent status as failed, and a malformed intent as none", () => {
    const intent = { id: "i", card_revision: 2, action_id: "park", status: "paused", message: "m" };
    expect(firstCard({}, { latest_intent: intent }).latestIntent?.status).toBe("failed");
    expect(firstCard({}, { latest_intent: { id: 7 } }).latestIntent).toBeNull();
  });

  const withIntent = (status: string, revision = 2) =>
    firstCard(
      { actions: [{ id: "park", label: "Snooze", batch: true }] },
      { latest_intent: { id: "i", card_revision: revision, action_id: "park", status, message: "" } },
    );

  it("shows a live intent always, and a finished one only on its own revision", () => {
    expect(visibleAGDecisionIntent(withIntent("pending", 1))?.status).toBe("pending");
    expect(visibleAGDecisionIntent(withIntent("failed", 2))?.status).toBe("failed");
    expect(visibleAGDecisionIntent(withIntent("failed", 1))).toBeNull();
    expect(visibleAGDecisionIntent(withIntent("cancelled", 2))).toBeNull();
  });

  it("offers actions unless one is under way or has just worked", () => {
    expect(agDecisionCardIsActionable(withIntent("pending"))).toBe(false);
    expect(agDecisionCardIsActionable(withIntent("running"))).toBe(false);
    expect(agDecisionCardIsActionable(withIntent("succeeded"))).toBe(false);
    expect(agDecisionCardIsActionable(withIntent("succeeded", 1))).toBe(true);
    expect(agDecisionCardIsActionable(withIntent("failed"))).toBe(true);
  });

  it("batches only what every ready card offers, and needs two cards", () => {
    const park = { id: "park", label: "Snooze", batch: true };
    const close = { id: "close", label: "Close", batch: true };
    const a = firstCard({ actions: [park, close] });
    const b = firstCard({ actions: [park] });
    expect(agDecisionBatchActions([a, b]).map((batch) => batch.action.id)).toEqual(["park"]);
    expect(agDecisionBatchActions([a])).toEqual([]);
    expect(agDecisionBatchActions([a, withIntent("pending")])).toEqual([]);
  });

  it("places a card in the queue, back for a retry, or in Activity by its visible intent", () => {
    expect(agDecisionPlace(firstCard({}))).toBe("queue");
    expect(agDecisionPlace(withIntent("cancelled"))).toBe("queue");
    expect(agDecisionPlace(withIntent("succeeded", 1))).toBe("queue");
    for (const status of ["pending", "running", "succeeded"]) expect(agDecisionPlace(withIntent(status))).toBe("activity");
    for (const status of ["refused", "failed", "expired"]) expect(agDecisionPlace(withIntent(status))).toBe("retry");
  });

  it("orders a card's actions for shortcuts: options, the recommendation, then the rest", () => {
    const card = firstCard({
      actions: [{ id: "park", label: "Snooze" }, { id: "approve", label: "Approve" }, { id: "reply", label: "Reply" }, { id: "reply:a", label: "(a) Keep" }],
      recommended_action: "approve",
    });
    expect(agDecisionKeyedActions(card).map((action) => action.id)).toEqual(["reply:a", "approve", "park", "reply"]);
  });

  it("calls a worker offline when never seen or quiet too long", () => {
    const now = Date.parse("2026-11-01T01:00:00Z");
    expect(isAGDecisionWorkerOffline(null, now)).toBe(true);
    expect(isAGDecisionWorkerOffline(new Date(now - WORKER_OFFLINE_AFTER_MS - 1).toISOString(), now)).toBe(true);
    expect(isAGDecisionWorkerOffline(new Date(now - 1000).toISOString(), now)).toBe(false);
  });
});

describe("splitAGDecisionLine", () => {
  it("splits a short leading label from its value", () => {
    expect(splitAGDecisionLine("Status: PR ready — merged latest")).toEqual({ label: "Status", value: "PR ready — merged latest" });
    expect(splitAGDecisionLine("Do next: Review PR #8971")).toEqual({ label: "Do next", value: "Review PR #8971" });
  });

  it("keeps prose, a long label or a lowercase start as prose", () => {
    expect(splitAGDecisionLine("#8953 by lsjroberts: 1588 lines")).toBeNull();
    expect(splitAGDecisionLine("note: lower case")).toBeNull();
    expect(splitAGDecisionLine("A label that runs far too long: value")).toBeNull();
    expect(splitAGDecisionLine("Status:")).toBeNull();
  });
});

describe("subscribeAGDecisions", () => {
  it("refetches on a card change and on an issue's status change, and on nothing else", () => {
    const handlers = new Map<string, (payload: unknown) => void>();
    const offs: string[] = [];
    const ws = {
      on: (event: string, handler: (payload: unknown) => void) => {
        handlers.set(event, handler);
        return () => offs.push(event);
      },
    } as unknown as WSClient;
    const qc = new QueryClient();
    const invalidate = vi.spyOn(qc, "invalidateQueries").mockResolvedValue();

    const unsubscribe = subscribeAGDecisions(ws, qc);
    handlers.get("issue:updated")!({ issue: {}, status_changed: false });
    handlers.get("issue:updated")!(null);
    expect(invalidate).not.toHaveBeenCalled();

    handlers.get("issue:updated")!({ issue: {}, status_changed: true });
    handlers.get("ag:decision_changed")!({});
    expect(invalidate).toHaveBeenCalledTimes(2);
    expect(invalidate).toHaveBeenLastCalledWith({ queryKey: agDecisionKeys.all() });

    unsubscribe();
    expect(offs.sort()).toEqual(["ag:decision_changed", "issue:updated"]);
  });
});
