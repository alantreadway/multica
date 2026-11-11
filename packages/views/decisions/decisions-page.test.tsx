import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
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
const mockCreate = vi.hoisted(() => vi.fn());
const mockCancel = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: { listAGDecisions: mockList, createAGDecisionIntent: mockCreate, cancelAGDecisionIntent: mockCancel },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../issues/components/issue-peek", () => ({
  IssuePeekHost: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../issues/components/issue-detail", () => ({
  IssueDetail: ({ issueId, highlightCommentId }: { issueId: string; highlightCommentId?: string }) => (
    <div data-testid="issue" data-issue={issueId} data-comment={highlightCommentId ?? ""} />
  ),
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
  focusCommentId: null,
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
  effect: "",
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

    expect(await screen.findByRole("heading", { name: "Fix axis" })).toBeTruthy();
    expect(screen.queryByText("Their turn")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Author 1" }));

    expect(screen.getByRole("heading", { name: "Their turn" })).toBeTruthy();
    expect(screen.queryByText("Fix axis")).toBeNull();
  });

  it("lists the summary of a card with no issue as separate bullets", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(),
      cards: [card({ issueId: null, bullets: ["Ships per-module files", "Raises the target"] })],
    });

    const items = await screen.findAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual(
      expect.arrayContaining(["Ships per-module files", "Raises the target"]),
    );
  });

  it("shows labelled lines as fields and the rest as prose", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(),
      cards: [card({ lines: ["Status: PR ready", "#8953 by someone: 12 lines"] })],
    });

    const term = await screen.findByText("Status");
    expect(term.tagName).toBe("DT");
    expect(screen.getByText("PR ready").tagName).toBe("DD");
    expect(screen.getByText("#8953 by someone: 12 lines").tagName).toBe("P");
  });

  it("shows the card's issue under its summary, opened at the comment the card is about", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(),
      cards: [card({ lines: ["Status: PR ready"], bullets: ["Restates the description"], focusCommentId: "cm1" })],
    });

    const issue = await screen.findByTestId("issue");
    expect(issue.dataset).toMatchObject({ issue: "i1", comment: "cm1" });
    expect(screen.getByText("PR ready")).toBeTruthy();
    expect(screen.queryByText("Restates the description")).toBeNull();
  });

  it("opens the issue at its own position when the card names no comment", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(), cards: [card({})] });

    expect((await screen.findByTestId("issue")).dataset.comment).toBe("");
  });

  it("shows a card with no issue by its summary alone", async () => {
    renderPage({ workerSeenAt: recent(), lastSyncAt: recent(), cards: [card({ issueId: null })] });

    expect(await screen.findByRole("heading", { name: "Fix axis" })).toBeTruthy();
    expect(screen.queryByTestId("issue")).toBeNull();
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

    it("says what each button does, by its key, and leaves out a button that needs no telling", async () => {
      renderPage(list([card({
        actions: [action("close", { label: "Done reviewing", effect: "Marks this item done." }), action("park", { label: "Snooze" })],
      })]));

      const button = await screen.findByRole("button", { name: /Done reviewing/ });
      const effect = screen.getByText("Marks this item done.");
      expect(button.getAttribute("aria-describedby")).toBe(effect.closest("li")?.id);
      expect(within(effect.closest("ul") as HTMLElement).getAllByRole("listitem")).toHaveLength(1);
      expect(screen.getByRole("button", { name: /Snooze/ }).getAttribute("aria-describedby")).toBeNull();
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
      const recommending = list([
        card({
          actions: [action("merge", { primary: true }), action("park")],
          recommendedAction: "merge",
          overrideReasons: ["Too big"],
        }),
      ]);
      const first = renderPage(recommending);
      await userEvent.click(await screen.findByRole("button", { name: "merge" }));
      expect(mockCreate).toHaveBeenCalledWith(...sent({ action_id: "merge" }));
      first.unmount();

      renderPage(recommending);
      await userEvent.click(await screen.findByRole("button", { name: "park" }));
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

    it("lists a queued card under Activity without its buttons, and can undo it", async () => {
      renderPage(list([card({ actions: [action("park")], latestIntent: intent("pending") })]));
      const activity = await screen.findByRole("region", { name: "Activity" });
      expect(within(activity).getByText("Queued: park")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "park" })).toBeNull();
      await userEvent.click(within(activity).getByRole("button", { name: "Undo" }));
      expect(mockCancel.mock.calls[0]?.[0]).toBe("in1");
    });

    it("moves a sent card out of the queue and opens the next one", async () => {
      renderPage(
        list([
          card({ id: "c1", actions: [action("park")] }),
          card({ id: "c2", title: "Next up", issueRef: "MAG-2", rank: 2, actions: [action("park")] }),
        ]),
      );
      await userEvent.click(await screen.findByRole("button", { name: "park" }));
      expect(mockCreate).toHaveBeenCalledWith(...sent({}));
      expect(await screen.findByRole("heading", { name: "Next up" })).toBeTruthy();
      const activity = screen.getByRole("region", { name: "Activity" });
      expect(within(activity).getByText("Queued: park")).toBeTruthy();
      expect(screen.getByRole("button", { name: "You 1" })).toBeTruthy();
    });

    it("moves with J and K, and acts with a number key", async () => {
      renderPage(
        list([
          card({ id: "c1", actions: [action("park")] }),
          card({ id: "c2", title: "Next up", rank: 2, actions: [action("close"), action("park")], recommendedAction: "park" }),
        ]),
      );
      await screen.findByRole("heading", { name: "Fix axis" });
      await userEvent.keyboard("j");
      expect(screen.getByRole("heading", { name: "Next up" })).toBeTruthy();
      await userEvent.keyboard("k");
      expect(screen.getByRole("heading", { name: "Fix axis" })).toBeTruthy();
      await userEvent.keyboard("j1");
      expect(mockCreate).toHaveBeenCalledWith("c2", { revision: 1, action_id: "park", note: "", override_reason: "" });
    });

    it("offers an agent's options as numbered choices", async () => {
      renderPage(
        list([
          card({
            cardType: "agent_question",
            actions: [action("reply:a", { label: "(a) Keep it" }), action("reply", { note: { required: true, placeholder: "Your reply" } })],
            recommendedAction: "reply:a",
          }),
        ]),
      );
      const option = await screen.findByRole("button", { name: "(a) Keep it" });
      expect(option.getAttribute("aria-keyshortcuts")).toBe("1");
      await userEvent.click(option);
      expect(mockCreate).toHaveBeenCalledWith(...sent({ action_id: "reply:a" }));
    });

    it("brings a refused card back under Needs another look", async () => {
      renderPage(list([card({ actions: [action("park")], latestIntent: intent("refused", "The PR has a new head.") })]));
      const retry = await screen.findByRole("region", { name: "Needs another look" });
      expect(within(retry).getByText("Not done")).toBeTruthy();
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
