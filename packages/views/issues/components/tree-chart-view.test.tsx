import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import {
  IssuePeekActionsContext,
  IssuePeekIdContext,
  type IssuePeekActions,
} from "../surface/peek-context";
import { TREE_CHART_NODE_CAP } from "./tree-chart-layout";
import { TreeChartView } from "./tree-chart-view";
import type { TreeIssueRow, TreeRow } from "./tree-rows";
import type { TreeRowsState } from "./use-tree-rows";

// AG patch: org-chart rendering. The rows are handed in directly; the data
// hook and the geometry have their own tests.

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/acme/issues/${id}` }),
}));
vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => ({
    categoryOf: () => "unstarted",
    colorOf: () => null,
    iconOf: () => null,
  }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectDetailOptions: (_ws: string, id: string) => ({
    queryKey: ["project", id],
    queryFn: async () => ({ id, title: "Roadmap", icon: "🗺" }),
  }),
}));
vi.mock("../../i18n", () => ({
  useT: () => ({
    t: (select: (r: unknown) => unknown, options?: Record<string, unknown>) => {
      const key = String(select(new Proxy({}, { get: (_t, p) => keyPath(p as string) })));
      return options ? `${key}:${JSON.stringify(options)}` : key;
    },
  }),
}));
// Records the accessed key path ($.tree.expand -> "tree.expand").
function keyPath(first: string): unknown {
  const parts = [first];
  const handler: ProxyHandler<object> = {
    get: (_t, p) => {
      if (p === Symbol.toPrimitive || p === "toString") return () => parts.join(".");
      parts.push(String(p));
      return proxy;
    },
  };
  const proxy: object = new Proxy({}, handler);
  return proxy;
}
vi.mock("../actions", () => ({
  IssueActionsContextMenu: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("./issue-agent-activity-indicator", () => ({
  IssueAgentActivityIndicator: ({ issueId }: { issueId: string }) => (
    <span data-testid={`agent-dot-${issueId}`} />
  ),
}));
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid={`avatar-${actorId}`} />
  ),
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => `https://app.example${path}`,
};
const peek: IssuePeekActions = {
  open: vi.fn(),
  toggle: vi.fn(),
  close: vi.fn(),
  publishColumns: vi.fn(),
};

function issue(id: string, extra: Partial<Issue> = {}): Issue {
  return {
    id,
    identifier: `MAG-${id}`,
    title: `Title ${id}`,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    parent_issue_id: null,
    labels: [],
    properties: {},
    ...extra,
  } as unknown as Issue;
}

function state(rows: TreeRow[], overrides: Partial<TreeRowsState> = {}): TreeRowsState {
  return {
    rows,
    issueIds: rows.flatMap((r) => (r.kind === "issue" ? [r.issue.id] : [])),
    isLoading: false,
    isEmpty: false,
    focusId: null,
    focusIssue: null,
    focusPath: [],
    focus: vi.fn(),
    clearFocus: vi.fn(),
    expandedIds: new Set(),
    toggle: vi.fn(),
    expandAll: vi.fn(),
    collapseAll: vi.fn(),
    layout: "chart",
    chartAvailability: "focus",
    setLayout: vi.fn(),
    resetChart: vi.fn(),
    chartDepthOffset: 0,
    loadMore: vi.fn(),
    retry: vi.fn(),
    ...overrides,
  };
}

const row = (id: string, depth: number, over: Partial<TreeIssueRow> = {}): TreeIssueRow => ({
  kind: "issue",
  key: id,
  parentId: depth === 0 ? null : "1",
  depth,
  guides: [],
  issue: issue(id),
  childCount: 0,
  expanded: false,
  isLast: false,
  ancestorPath: [],
  ...over,
});

function renderChart(tree: TreeRowsState, projectId?: string, peekedId: string | null = null) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <NavigationProvider value={navigation}>
        <IssuePeekActionsContext.Provider value={peek}>
          <IssuePeekIdContext.Provider value={peekedId}>
            <TreeChartView
              tree={tree}
              childProgressMap={new Map([["1", { done: 1, total: 3 }]])}
              projectId={projectId}
            />
          </IssuePeekIdContext.Provider>
        </IssuePeekActionsContext.Provider>
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

const nodes = () =>
  [...document.querySelectorAll<HTMLElement>('[data-slot="tree-chart-node"]')];

describe("AG patch: TreeChartView", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it("draws one node per issue with identifier, title, progress, assignee and agent dot", () => {
    renderChart(
      state([
        row("1", 0, {
          childCount: 2,
          expanded: true,
          issue: issue("1", { assignee_type: "member", assignee_id: "user-1" }),
        }),
        row("2", 1),
        row("3", 1),
      ]),
    );
    const [first, second, third] = nodes();
    expect(within(first!).getByText("MAG-1")).toBeInTheDocument();
    expect(within(first!).getByText("Title 1")).toBeInTheDocument();
    expect(within(first!).getByText("1/3")).toBeInTheDocument();
    expect(within(first!).getByTestId("avatar-user-1")).toBeInTheDocument();
    expect(within(first!).getByTestId("agent-dot-1")).toBeInTheDocument();
    // Parent above its children, children side by side.
    const top = (el: HTMLElement) => Number.parseFloat(el.style.top);
    const left = (el: HTMLElement) => Number.parseFloat(el.style.left);
    expect(top(second!)).toBeGreaterThan(top(first!));
    expect(top(second!)).toBe(top(third!));
    expect(left(third!)).toBeGreaterThan(left(second!));
  });

  it("joins each child to its parent with an elbow connector", () => {
    const { container } = renderChart(
      state([row("1", 0, { childCount: 2, expanded: true }), row("2", 1), row("3", 1)]),
    );
    const paths = container.querySelectorAll('[data-slot="tree-chart-edges"] path');
    expect(paths).toHaveLength(2);
    for (const path of paths) expect(path.getAttribute("d")).toMatch(/^M[\d.]+ [\d.]+V[\d.]+H[\d.]+V[\d.]+$/);
  });

  it("toggles a node from its count chip and re-roots from its focus action", () => {
    const tree = state([row("1", 0, { childCount: 4, expanded: true }), row("2", 1)]);
    renderChart(tree);
    const chip = screen.getByRole("button", { name: "tree.collapse" });
    expect(chip).toHaveTextContent("4");
    expect(chip).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(chip);
    expect(tree.toggle).toHaveBeenCalledWith("1");

    fireEvent.click(screen.getAllByRole("button", { name: "tree.focus" })[1]!);
    expect(tree.focus).toHaveBeenCalledWith("2");
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("offers no count chip on a leaf", () => {
    renderChart(state([row("1", 0)]));
    expect(screen.queryByRole("button", { name: "tree.expand" })).toBeNull();
    expect(screen.queryByRole("button", { name: "tree.collapse" })).toBeNull();
  });

  it("links a node to its issue so it opens and peeks as in the outline", () => {
    renderChart(state([row("1", 0)]), undefined, "1");
    const node = nodes()[0]!;
    expect(node).toHaveAttribute("data-peek-target", "1");
    expect(node).toHaveAttribute("data-peeked");
    expect(within(node).getByRole("link")).toHaveAttribute("href", "/acme/issues/1");
  });

  it("publishes the chart's issues for peek stepping and clears them on unmount", () => {
    const { unmount } = renderChart(state([row("1", 0, { childCount: 1, expanded: true }), row("2", 1)]));
    expect(peek.publishColumns).toHaveBeenCalledWith([["1", "2"]]);
    unmount();
    expect(peek.publishColumns).toHaveBeenLastCalledWith(null);
  });

  it("shows load-more, loading and failed branches as dashed placeholder nodes", () => {
    const tree = state([
      row("1", 0, { childCount: 9, expanded: true }),
      row("2", 1),
      { kind: "more", key: "more:1", parentId: "1", depth: 1, guides: [], branchKey: "1", loading: false, error: false },
      { kind: "status", key: "status:3", parentId: "1", depth: 1, guides: [], branchKey: "3", state: "error" },
    ]);
    renderChart(tree);
    fireEvent.click(screen.getByRole("button", { name: "tree.load_more" }));
    expect(tree.loadMore).toHaveBeenCalledWith("1");
    fireEvent.click(screen.getByRole("button", { name: "tree.retry" }));
    expect(tree.retry).toHaveBeenCalledWith("3");
  });

  it("puts the project on a virtual top node over the root issues", async () => {
    const { container } = renderChart(
      state([row("1", 0), row("2", 0)], { focusId: null, chartAvailability: "project", chartDepthOffset: 1 }),
      "p1",
    );
    expect(await screen.findByText("Roadmap")).toBeInTheDocument();
    const project = container.querySelector('[data-slot="tree-chart-project"]') as HTMLElement;
    const top = (el: HTMLElement) => Number.parseFloat(el.style.top);
    expect(nodes()).toHaveLength(2);
    for (const node of nodes()) expect(top(node)).toBeGreaterThan(top(project));
    expect(container.querySelectorAll('[data-slot="tree-chart-edges"] path')).toHaveLength(2);
  });

  it("asks the user to narrow the chart above the node cap, instead of drawing it", () => {
    const children = Array.from({ length: TREE_CHART_NODE_CAP }, (_, i) => row(`c${i}`, 1));
    const tree = state([row("1", 0, { childCount: children.length, expanded: true }), ...children], {
      focusId: "1",
    });
    const { container } = renderChart(tree);
    const message = container.querySelector('[data-slot="tree-chart-too-many"]')!;
    expect(message).toHaveTextContent(`"count":${TREE_CHART_NODE_CAP + 1}`);
    expect(message).toHaveTextContent(`"max":${TREE_CHART_NODE_CAP}`);
    expect(nodes()).toHaveLength(0);

    // The message's own button, after the toolbar's.
    fireEvent.click(screen.getAllByRole("button", { name: /tree\.collapse_all/ }).at(-1)!);
    expect(tree.collapseAll).toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "tree.chart_use_outline" }));
    expect(tree.setLayout).toHaveBeenCalledWith("outline");
  });

  it("draws exactly at the cap", () => {
    const children = Array.from({ length: TREE_CHART_NODE_CAP - 1 }, (_, i) => row(`c${i}`, 1));
    renderChart(
      state([row("1", 0, { childCount: children.length, expanded: true }), ...children], {
        focusId: "1",
      }),
    );
    expect(nodes()).toHaveLength(TREE_CHART_NODE_CAP);
  });

  it("zooms with its controls and the keyboard, and fits back", () => {
    const { container } = renderChart(state([row("1", 0)]));
    const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
    const scale = () => /scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]!;
    expect(scale()).toBe("1");

    fireEvent.click(screen.getByRole("button", { name: "tree.chart_zoom_in" }));
    expect(Number(scale())).toBeGreaterThan(1);
    fireEvent.click(screen.getByRole("button", { name: "tree.chart_zoom_out" }));
    expect(Number(scale())).toBeCloseTo(1);

    const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
    fireEvent.keyDown(surface, { key: "-" });
    expect(Number(scale())).toBeLessThan(1);
  });

  it("pans by dragging the background and ignores a drag that starts on a control", () => {
    const { container } = renderChart(state([row("1", 0)]));
    const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
    const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
    const translate = () => /translate\(([-\d.]+)px, ([-\d.]+)px\)/.exec(canvas.style.transform)!.slice(1).map(Number);

    fireEvent.pointerDown(surface, { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
    fireEvent.pointerMove(surface, { clientX: 130, clientY: 90, pointerId: 1 });
    fireEvent.pointerUp(surface, { pointerId: 1 });
    expect(translate()).toEqual([30, -10]);

    const link = within(nodes()[0]!).getByRole("link");
    fireEvent.pointerDown(link, { button: 0, clientX: 0, clientY: 0, pointerId: 2 });
    fireEvent.pointerMove(surface, { clientX: 80, clientY: 80, pointerId: 2 });
    expect(translate()).toEqual([30, -10]);
  });

  it("pans with the wheel and zooms with ctrl + wheel", () => {
    const { container } = renderChart(state([row("1", 0)]));
    const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
    const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
    fireEvent.wheel(surface, { deltaX: 0, deltaY: 40 });
    expect(canvas.style.transform).toContain("translate(0px, -40px)");
    fireEvent.wheel(surface, { deltaY: -100, ctrlKey: true });
    expect(Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1])).toBeGreaterThan(1);
  });

  it("runs the toolbar actions", () => {
    const tree = state([row("1", 0)]);
    renderChart(tree);
    fireEvent.click(screen.getByRole("button", { name: /tree\.chart_expand_level/ }));
    fireEvent.click(screen.getByRole("button", { name: /tree\.collapse_all/ }));
    fireEvent.click(screen.getByRole("button", { name: /tree\.chart_reset/ }));
    expect(tree.expandAll).toHaveBeenCalledOnce();
    expect(tree.collapseAll).toHaveBeenCalledOnce();
    expect(tree.resetChart).toHaveBeenCalledOnce();
  });

  it("keeps the breadcrumb and the filters-paused notice of the outline", () => {
    renderChart(state([row("1", 0)], { focusId: "1", focusIssue: issue("1") }));
    expect(screen.getByRole("navigation")).toHaveTextContent("tree.focus_root");
    expect(document.querySelector('[data-slot="tree-filters-paused"]')).toBeInTheDocument();
  });
});
