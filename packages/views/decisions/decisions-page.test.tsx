import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AGDecisionCard, AGDecisionList } from "@multica/core/decisions/ag-decisions";
import { I18nProvider } from "@multica/core/i18n/react";
import enAg from "../locales/en/ag.json";
import enLayout from "../locales/en/layout.json";

const mockList = vi.hoisted(() => vi.fn());
const mockOpen = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({ api: { listAGDecisions: mockList } }));
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
  ...over,
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
    renderPage({
      lastSyncAt: recent(),
      cards: [card({}), card({ id: "c2", waitingOn: "author", title: "Their turn", issueRef: "MAG-2" })],
    });

    expect(await screen.findByText("Fix axis")).toBeTruthy();
    expect(screen.queryByText("Their turn")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Author 1" }));

    expect(screen.getByText("Their turn")).toBeTruthy();
    expect(screen.queryByText("Fix axis")).toBeNull();
  });

  it("lists a card's summary as separate bullets", async () => {
    renderPage({
      lastSyncAt: recent(),
      cards: [card({ bullets: ["Ships per-module files", "Raises the target"] })],
    });

    const items = await screen.findAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual(
      expect.arrayContaining(["Ships per-module files", "Raises the target"]),
    );
  });

  it("opens the issue peek from the card's reference", async () => {
    renderPage({ lastSyncAt: recent(), cards: [card({})] });

    await userEvent.click(await screen.findByRole("button", { name: "MAG-1" }));

    expect(mockOpen).toHaveBeenCalledWith("i1");
  });

  it("warns when the engine has gone quiet", async () => {
    renderPage({ lastSyncAt: "2020-01-01T00:00:00Z", cards: [card({})] });

    expect((await screen.findByRole("status")).textContent).toContain("out of date");
  });

  it("says so before the first sync", async () => {
    renderPage({ lastSyncAt: null, cards: [] });

    expect(await screen.findByText("No sync yet.")).toBeTruthy();
  });

  it("says so when nothing is waiting on Alan", async () => {
    renderPage({ lastSyncAt: recent(), cards: [] });

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
});
