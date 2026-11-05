import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  AGDecisionAction,
  AGDecisionCard,
  AGDecisionIntent,
  AGDecisionList,
} from "@multica/core/decisions/ag-decisions";
import { I18nProvider } from "@multica/core/i18n/react";
import enAg from "../locales/en/ag.json";
import enLayout from "../locales/en/layout.json";

const mockList = vi.hoisted(() => vi.fn());
const mockOpen = vi.hoisted(() => vi.fn());
const mockCreate = vi.hoisted(() => vi.fn());
const mockCancel = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: { listAGDecisions: mockList, createAGDecisionIntent: mockCreate, cancelAGDecisionIntent: mockCancel },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../issues/components/issue-peek", () => ({
  IssuePeekHost: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../issues/surface/peek-context", () => ({
  useIssuePeekActions: () => ({ open: mockOpen }),
}));

import { DecisionsPage } from "./decisions-page";

const card = (over: Partial<AGDecisionCard>): AGDecisionCard => ({
  id: "c1",
  issueId: "i1",
  waitingOn: "alan",
  cardType: "pr_review",
  revision: 1,
  headSha: null,
  waitingSince: "2026-11-01T00:00:00Z",
  title: "Fix axis",
  group: "PR review",
  rank: 1,
  issueRef: "MAG-1",
  recommended: "Approve",
  chips: [],
  lines: [],
  bullets: [],
  blockers: [],
  links: [],
  actions: [],
  recommendedAction: "",
  overrideReasons: [],
  latestIntent: null,
  ...over,
});

const action = (id: string, over: Partial<AGDecisionAction> = {}): AGDecisionAction => ({
  id,
  label: id,
  primary: false,
  batch: false,
  confirm: "",
  note: null,
  ...over,
});
const SEEN = new Date().toISOString();
const list = (cards: AGDecisionCard[], over: Partial<AGDecisionList> = {}): AGDecisionList => ({
  cards,
  lastSyncAt: SEEN,
  workerSeenAt: SEEN,
  ...over,
});
const intent = (status: AGDecisionIntent["status"], message = ""): AGDecisionIntent => ({
  id: "in1",
  cardRevision: 1,
  actionId: "park",
  status,
  message,
});

function renderPage(list: AGDecisionList) {
  mockList.mockResolvedValue(list);
  return render(
    <I18nProvider locale="en" resources={{ en: { ag: enAg, layout: enLayout } }}>
      <QueryClientProvider client={new QueryClient()}>
        <DecisionsPage />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

const recent = () => new Date().toISOString();

describe("DecisionsPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows Alan's cards first and switches holder on click", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(),
      cards: [card({}), card({ id: "c2", waitingOn: "author", title: "Their turn", issueRef: "MAG-2" })],
    });

    expect(await screen.findByText("Fix axis")).toBeTruthy();
    expect(screen.queryByText("Their turn")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Author 1" }));

    expect(screen.getByText("Their turn")).toBeTruthy();
    expect(screen.queryByText("Fix axis")).toBeNull();
  });

  it("lists a card's summary as separate bullets", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(),
      cards: [card({ bullets: ["Ships per-module files", "Raises the target"] })],
    });

    const items = await screen.findAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual(
      expect.arrayContaining(["Ships per-module files", "Raises the target"]),
    );
  });

  it("opens the issue peek from the card's reference", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(), cards: [card({})] });

    await userEvent.click(await screen.findByRole("button", { name: "MAG-1" }));

    expect(mockOpen).toHaveBeenCalledWith("i1");
  });

  it("warns when the engine has gone quiet", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: "2020-01-01T00:00:00Z", cards: [card({})] });

    expect((await screen.findByRole("status")).textContent).toContain("out of date");
  });

  it("says so before the first sync", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: null, cards: [] });

    expect(await screen.findByText("No sync yet.")).toBeTruthy();
  });

  it("says so when nothing is waiting on Alan", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(), cards: [] });

    expect(await screen.findByText("Nothing is waiting on you.")).toBeTruthy();
  });

  it("reports a failed load", async () => {
    mockList.mockRejectedValue(new Error("nope"));
    render(
      <I18nProvider locale="en" resources={{ en: { ag: enAg, layout: enLayout } }}>
        <QueryClientProvider client={new QueryClient()}>
          <DecisionsPage />
        </QueryClientProvider>
      </I18nProvider>,
    );

    expect(await screen.findByRole("alert")).toBeTruthy();
  });

  describe("actions", () => {
    beforeEach(() => {
      mockCreate.mockReset().mockResolvedValue(undefined);
      mockCancel.mockReset().mockResolvedValue(undefined);
    });
    const sent = (over: object) => ["c1", { revision: 1, action_id: "park", note: "", override_reason: "", ...over }];

    it("sends a plain action straight away, pinned to the revision on screen", async () => {
      renderPage(list([card({ actions: [action("park")] })]));
      await userEvent.click(await screen.findByRole("button", { name: "park" }));
      expect(mockCreate).toHaveBeenCalledWith(...sent({}));
    });

    it("asks for a second tap when the action has a reason to confirm", async () => {
      renderPage(list([card({ actions: [action("approve", { confirm: "Not clean: label Breaking." })] })]));
      await userEvent.click(await screen.findByRole("button", { name: "approve" }));
      expect(mockCreate).not.toHaveBeenCalled();
      expect(screen.getByText("Not clean: label Breaking.")).toBeTruthy();
      await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
      expect(mockCreate).toHaveBeenCalledWith(...sent({ action_id: "approve" }));
    });

    it("holds a required note until there is text, and sends it", async () => {
      renderPage(list([card({ actions: [action("reply", { note: { required: true, placeholder: "Your reply" } })] })]));
      await userEvent.click(await screen.findByRole("button", { name: "reply" }));
      const confirm = screen.getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
      expect(confirm.disabled).toBe(true);
      await userEvent.type(screen.getByRole("textbox", { name: "Your reply" }), "ship it");
      await userEvent.click(confirm);
      expect(mockCreate).toHaveBeenCalledWith(...sent({ action_id: "reply", note: "ship it" }));
    });

    it("offers override reasons only when leaving the recommendation", async () => {
      renderPage(
        list([
          card({
            actions: [action("merge", { primary: true }), action("park")],
            recommendedAction: "merge",
            overrideReasons: ["Too big"],
          }),
        ]),
      );
      await userEvent.click(await screen.findByRole("button", { name: "merge" }));
      expect(mockCreate).toHaveBeenCalledWith(...sent({ action_id: "merge" }));
      await userEvent.click(screen.getByRole("button", { name: "park" }));
      await userEvent.click(screen.getByRole("button", { name: "Too big" }));
      await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
      expect(mockCreate).toHaveBeenLastCalledWith(...sent({ override_reason: "Too big" }));
    });

    it("keeps the dialog open with the reason when queueing fails", async () => {
      mockCreate.mockRejectedValue(new Error("this card has changed; check it again"));
      renderPage(list([card({ actions: [action("approve", { confirm: "Sure?" })] })]));
      await userEvent.click(await screen.findByRole("button", { name: "approve" }));
      await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
      expect(await screen.findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("this card has changed"));
      expect(screen.getByRole("button", { name: "Confirm" })).toBeTruthy();
    });

    it("hides the buttons while queued, and can cancel", async () => {
      renderPage(list([card({ actions: [action("park")], latestIntent: intent("pending") })]));
      await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
      expect(mockCancel.mock.calls[0]?.[0]).toBe("in1");
      expect(screen.queryByRole("button", { name: "park" })).toBeNull();
    });

    it("shows a refusal and keeps the buttons to try again", async () => {
      renderPage(list([card({ actions: [action("park")], latestIntent: intent("refused", "The PR has a new head.") })]));
      expect(await screen.findByText("Not done: The PR has a new head.")).toBeTruthy();
      expect(screen.getByRole("button", { name: "park" })).toBeTruthy();
    });

    it("batches same-shape cards in a group behind a confirmation", async () => {
      const batch = action("park", { label: "Snooze", batch: true });
      renderPage(
        list([card({ id: "c1", actions: [batch] }), card({ id: "c2", title: "Other", rank: 2, actions: [batch] })]),
      );
      await userEvent.click(await screen.findByRole("button", { name: "Snooze: 2 cards" }));
      expect(mockCreate).not.toHaveBeenCalled();
      await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
      expect(mockCreate.mock.calls.map((call) => call[0])).toEqual(["c1", "c2"]);
    });

    it("warns when the worker is not running", async () => {
      renderPage(list([card({})], { workerSeenAt: null }));
      expect(await screen.findByText(/The worker is not running/)).toBeTruthy();
    });
  });
});
