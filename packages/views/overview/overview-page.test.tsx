import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  EMPTY_AG_OVERVIEW,
  type AGOverview,
  type AGOverviewNode,
  type AGOverviewTree,
  type AGOverviewUnit,
} from "@multica/core/overview/ag-overview";
import { I18nProvider } from "@multica/core/i18n/react";
import enAg from "../locales/en/ag.json";
import enLayout from "../locales/en/layout.json";

const mockGet = vi.hoisted(() => vi.fn());
const mockPin = vi.hoisted(() => vi.fn());
const mockUnpin = vi.hoisted(() => vi.fn());
const mockOpen = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: { getAGOverview: mockGet, createAGOverviewPin: mockPin, deleteAGOverviewPin: mockUnpin },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ decisions: () => "/acme/decisions" }) }));
vi.mock("../navigation", () => ({
  AppLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
}));
vi.mock("../issues/components/issue-peek", () => ({
  IssuePeekHost: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../issues/surface/peek-context", () => ({
  useIssuePeekActions: () => ({ open: mockOpen }),
}));

import { OverviewPage } from "./overview-page";

const HOUR = 3_600_000;
const ago = (hours: number) => new Date(Date.now() - hours * HOUR).toISOString();
const ref = (n: number, title = `Issue ${n}`) => ({ id: `i${n}`, identifier: `MAG-${n}`, number: n, title });

const unit = (n: number, over: Partial<AGOverviewUnit> = {}): AGOverviewUnit => ({
  ...ref(n),
  status: "in_progress",
  prevStatus: null,
  flow: ["review"],
  reviewRound: null,
  inStateSince: ago(2),
  ageing: false,
  waitingOn: null,
  pr: null,
  ...over,
});

const tree = (over: Partial<AGOverviewTree> = {}): AGOverviewTree => ({
  pin: { id: "p1", kind: "jira", ref: "AG-9003", label: "AG-9003" },
  title: "Theming",
  issueCount: 12,
  startedAt: ago(24 * 20),
  runHours: 40,
  known: { units: 5, done: 3, eta: null, etaHidden: "few_samples" },
  unknown: {
    scopeOpen: false,
    unbroken: [],
    unbrokenCount: 0,
    recentUnits: 0,
    recentDays: 3,
    pastGrowth: [],
  },
  waves: [
    {
      root: ref(10, "Wave two"),
      done: false,
      issueCount: 6,
      startedAt: ago(24 * 5),
      finishedAt: null,
      issues: [],
      units: [
        unit(11, { title: "Fix the axis" }),
        unit(12, { status: "blocked", prevStatus: "in_review", ageing: true, waitingOn: "author", inStateSince: ago(30) }),
        unit(13, { status: "done" }),
      ],
    },
  ],
  ...over,
});

const overview = (over: Partial<AGOverview> = {}): AGOverview => ({
  ...EMPTY_AG_OVERVIEW,
  generatedAt: new Date().toISOString(),
  ...over,
});

function renderPage(data: AGOverview) {
  mockGet.mockResolvedValue(data);
  return render(
    <I18nProvider locale="en" resources={{ en: { ag: enAg, layout: enLayout } }}>
      <QueryClientProvider client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}>
        <OverviewPage />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("OverviewPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("shows what runs now, what waits on you, and links to Decisions", async () => {
    renderPage(
      overview({
        now: {
          running: [
            {
              id: "t1",
              status: "running",
              agentId: "a1",
              agentName: "Builder",
              issue: ref(5, "Port the legend"),
              reviewRound: 2,
              since: ago(1),
              tree: "AG-9003",
            },
          ],
          queued: [],
          waitingOnYou: [{ id: "c1", cardType: "pr_review", issue: ref(6, "Merge the theme") }],
          elsewhere: { author: 2 },
          ageing: [
            { issue: ref(7), status: "in_review", inStateSince: ago(50), p90Hours: 20, waitingOn: null, tree: null },
          ],
        },
      }),
    );

    const running = await screen.findByRole("region", { name: "Running" });
    expect(within(running).getByText("Builder")).toBeTruthy();
    expect(within(running).getByText("round 2")).toBeTruthy();

    const waiting = screen.getByRole("region", { name: "Waiting on you" });
    expect(within(waiting).getByText("Merge the theme")).toBeTruthy();
    expect(within(waiting).getByRole("link", { name: "Open Decisions" }).getAttribute("href")).toBe("/acme/decisions");
    expect(within(waiting).getByRole("img", { name: "3 open Desk cards, by who holds them" })).toBeTruthy();
    expect(within(waiting).getByText("You 1")).toBeTruthy();
    expect(within(waiting).getByText("Author 2")).toBeTruthy();

    const ageing = screen.getByRole("region", { name: "Ageing" });
    expect(within(ageing).getByText("In review 2d, usually under 20h")).toBeTruthy();
    await userEvent.click(within(ageing).getByRole("button", { name: "MAG-7 · In review 2d, usually under 20h" }));
    expect(mockOpen).toHaveBeenCalledWith("i7");
  });

  it("splits a tree into known scope and the facts that say more is coming", async () => {
    renderPage(
      overview({
        trees: [
          tree({
            known: { units: 5, done: 3, eta: { p50: "2026-11-09T12:00:00Z", p85: "2026-11-11T12:00:00Z", samples: 9 }, etaHidden: null },
            unknown: {
              scopeOpen: true,
              unbroken: [{ ...ref(20, "Plan wave three"), status: "todo" }],
              unbrokenCount: 2,
              recentUnits: 4,
              recentDays: 3,
              pastGrowth: [{ root: ref(1), dayOne: 33, final: 69 }],
            },
          }),
        ],
      }),
    );

    const known = await screen.findByRole("region", { name: "Known scope" });
    expect(within(known).getAllByText("3 of 5 units done").length).toBeGreaterThan(0);
    expect(within(known).getByText("Known units finish, p50–p85. Not a delivery date.")).toBeTruthy();

    const unknown = screen.getByRole("region", { name: "Unknown scope" });
    expect(within(unknown).getByText("Scope still open")).toBeTruthy();
    expect(within(unknown).getByText("2 issues not yet broken into units:")).toBeTruthy();
    expect(within(unknown).getByRole("button", { name: "MAG-20" })).toBeTruthy();
    expect(within(unknown).getByText("4 units added in the last 3 days")).toBeTruthy();
    expect(within(unknown).getByText("Past waves grew after day one: 2.1×")).toBeTruthy();
  });

  it("hides the ETA with too few samples and says when scope looks complete", async () => {
    renderPage(overview({ trees: [tree()] }));

    const known = await screen.findByRole("region", { name: "Known scope" });
    expect(within(known).getByText("Too few finished units for an estimate")).toBeTruthy();
    expect(within(known).queryByText(/Not a delivery date/)).toBeNull();
    expect(within(screen.getByRole("region", { name: "Unknown scope" })).getByText("Scope looks complete")).toBeTruthy();
  });

  it("lists open units most stuck first and opens the issue peek", async () => {
    renderPage(overview({ trees: [tree()] }));

    const [, ...rows] = within(await screen.findByRole("table", { name: "Stages" })).getAllByRole("row");
    expect(rows.map((row) => within(row).getAllByRole("button")[0]?.textContent)).toEqual(["MAG-12", "MAG-11"]);
    expect(within(rows[0]!).getByLabelText("Past its usual time")).toBeTruthy();

    await userEvent.click(within(rows[1]!).getByRole("button", { name: "MAG-11" }));
    expect(mockOpen).toHaveBeenCalledWith("i11");
  });

  it("places each open unit in its current stage column, with owners in a legend", async () => {
    renderPage(
      overview({
        now: { ...EMPTY_AG_OVERVIEW.now, running: [{ id: "t1", status: "running", agentId: "a1", agentName: "Builder", issue: ref(11), reviewRound: null, since: ago(1), tree: null }] },
        trees: [
          tree({
            waves: [
              {
                root: ref(10, "Wave two"),
                done: false,
                issueCount: 3,
                startedAt: ago(24 * 5),
                finishedAt: null,
                issues: [],
                units: [
                  unit(11, { flow: ["bench", "review"], reviewRound: 3, waitingOn: "agent", pr: { number: 901, state: "open", checks: "FAILURE", review: null } }),
                  unit(12, { status: "in_review", flow: ["review"], waitingOn: "alan" }),
                ],
              },
            ],
          }),
        ],
      }),
    );

    const board = await screen.findByRole("table", { name: "Stages" });
    expect(within(board).getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "",
      "build1",
      "bench",
      "review1",
      "merge",
      "PR",
    ]);
    const [, first, second] = within(board).getAllByRole("row");
    const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((cell) => cell.textContent);
    expect(cells(first!)).toEqual(["MAG-12", "Issue 12", "Done", "", "2hYou", "", ""]);
    expect(cells(second!)).toEqual(["MAG-11", "Issue 11", "r32hAgent", "", "", "", "#901"]);
    expect(within(second!).getByLabelText("CI failed")).toBeTruthy();

    const article = screen.getByRole("article", { name: "AG-9003" });
    expect(within(article).getByText("You 1")).toBeTruthy();
    expect(within(article).getByText("Agent 1")).toBeTruthy();
  });

  it("outlines nested sub-tasks under their parent, units or not, and folds closed ones", async () => {
    const node = (n: number, parent: number, status: string, waitingOn: string | null = null): AGOverviewNode => ({
      ...ref(n),
      status,
      parentId: `i${parent}`,
      waitingOn,
    });
    renderPage(
      overview({
        trees: [
          tree({
            waves: [
              {
                root: ref(10, "Wave two"),
                done: false,
                issueCount: 6,
                startedAt: ago(24 * 5),
                finishedAt: null,
                units: [unit(22, { status: "in_review", waitingOn: "alan" }), unit(24, { status: "done", pr: { number: 77, state: "merged", checks: null, review: null } })],
                issues: [
                  node(21, 10, "in_progress", "agent"),
                  node(22, 21, "in_review", "alan"),
                  node(23, 21, "todo"),
                  node(24, 10, "done"),
                  node(25, 21, "cancelled"),
                ],
              },
            ],
          }),
        ],
      }),
    );

    const board = await screen.findByRole("table", { name: "Stages" });
    const outline = () =>
      within(board)
        .getAllByRole("row")
        .slice(1)
        .map((row) => {
          const [id, title, ...rest] = within(row).getAllByRole("cell");
          return [title!.style.paddingLeft, id!.textContent, ...rest.map((cell) => cell.textContent)].join("|");
        });
    expect(outline()).toEqual([
      "0rem|MAG-21|In progressAgent|",
      "0.875rem|MAG-22|Done|2hYou||",
      "0.875rem|MAG-23|Todo|",
    ]);

    const article = screen.getByRole("article", { name: "AG-9003" });
    expect(within(article).getByText("You 1")).toBeTruthy();
    expect(within(article).getByText("Agent 1")).toBeTruthy();

    await userEvent.click(within(article).getByRole("button", { name: "Show 2 closed issues" }));
    expect(outline()).toEqual([
      "0rem|MAG-21|In progressAgent|",
      "0.875rem|MAG-22|Done|2hYou||",
      "0.875rem|MAG-23|Todo|",
      "0.875rem|MAG-25|Cancelled|",
      "0rem|MAG-24|Done|#77",
    ]);
    await userEvent.click(within(article).getByRole("button", { name: "Hide closed issues" }));
    expect(outline()).toHaveLength(3);
  });

  it("outlines a wave that has no units yet", async () => {
    renderPage(
      overview({
        trees: [
          tree({
            waves: [
              {
                root: ref(10, "Wave two"),
                done: false,
                issueCount: 3,
                startedAt: ago(24 * 5),
                finishedAt: null,
                units: [],
                issues: [
                  { ...ref(31), status: "todo", parentId: "i10", waitingOn: null },
                  { ...ref(32), status: "in_progress", parentId: "i31", waitingOn: null },
                ],
              },
            ],
          }),
        ],
      }),
    );

    const [, first, second] = within(await screen.findByRole("table", { name: "Stages" })).getAllByRole("row");
    const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((cell) => cell.textContent);
    expect(cells(first!)).toEqual(["MAG-31", "Issue 31", "Todo", ""]);
    expect(cells(second!)).toEqual(["MAG-32", "Issue 32", "In progress", ""]);
  });

  it("pins a trimmed reference and unpins by id", async () => {
    mockPin.mockResolvedValue({ id: "p2", kind: "issue", ref: "i9", label: "MAG-9" });
    mockUnpin.mockResolvedValue(undefined);
    renderPage(overview({ trees: [tree()] }));

    await userEvent.type(await screen.findByRole("textbox", { name: "JIRA key or MAG-n" }), "  MAG-9 ");
    await userEvent.click(screen.getByRole("button", { name: "Pin" }));
    expect(mockPin).toHaveBeenCalledWith("MAG-9");

    await userEvent.click(screen.getByRole("button", { name: "Unpin AG-9003" }));
    expect(mockUnpin).toHaveBeenCalledWith("p1");
  });

  it("says when a pin is refused", async () => {
    mockPin.mockRejectedValue(new Error("400"));
    renderPage(overview());

    await userEvent.type(await screen.findByRole("textbox", { name: "JIRA key or MAG-n" }), "nonsense");
    await userEvent.click(screen.getByRole("button", { name: "Pin" }));

    expect((await screen.findByRole("alert")).textContent).toBe("Not a JIRA key or an issue in this workspace");
    expect(screen.getByText("Nothing pinned")).toBeTruthy();
  });
});
