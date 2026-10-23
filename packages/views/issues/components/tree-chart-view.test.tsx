import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { useIssueOpeningStore } from "@multica/core/issues/stores/issue-opening-store";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import {
  IssuePeekActionsContext,
  IssuePeekIdContext,
  type IssuePeekActions,
} from "../surface/peek-context";
import { TREE_CHART_NODE_CAP } from "./tree-chart-layout";
import { TreeChartView } from "./tree-chart-view";
import { fitViewport } from "./tree-chart-viewport";
import type { TreeIssueRow, TreeRow } from "./tree-rows";
import type { TreeRowsState } from "./use-tree-rows";

// AG patch: org-chart rendering. The rows are handed in directly; the data
// hook and the geometry have their own tests.

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/acme/issues/${id}` }),
}));
vi.mock("@multica/core/issue-statuses/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issue-statuses/hooks")>()),
  useIssueStatuses: () => ({
    categoryOf: () => "unstarted",
    colorOf: () => null,
    iconOf: () => null,
  }),
}));
vi.mock("@multica/core/projects/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/projects/queries")>()),
  projectDetailOptions: (_ws: string, id: string) => ({
    queryKey: ["project", id],
    queryFn: async () => ({ id, title: "Roadmap", icon: "🗺" }),
  }),
}));
vi.mock("../../i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../i18n")>()),
  useT: () => ({
    t: (select: (r: unknown) => unknown, options?: Record<string, unknown>) => {
      const key = String(select(new Proxy({}, { get: (_t, p) => keyPath(p as string) })));
      return options ? `${key}:${JSON.stringify(options)}` : key;
    },
  }),
}));
// Records the accessed key path ($.issues.tree.expand -> "issues.tree.expand").
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
vi.mock("./tree-chart-viewport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./tree-chart-viewport")>();
  return { ...actual, fitViewport: vi.fn(actual.fitViewport) };
});
vi.mock("../actions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../actions")>()),
  IssueActionsContextMenu: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("./issue-agent-activity-indicator", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./issue-agent-activity-indicator")>()),
  IssueAgentActivityIndicator: ({ issueId }: { issueId: string }) => (
    <span data-testid={`agent-dot-${issueId}`} />
  ),
}));
vi.mock("../../common/actor-avatar", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../common/actor-avatar")>()),
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
    identifier: `ISS-${id}`,
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

function chartElement(
  qc: QueryClient,
  tree: TreeRowsState,
  projectId?: string,
  peekedId: string | null = null,
) {
  return (
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
    </QueryClientProvider>
  );
}

function renderChart(tree: TreeRowsState, projectId?: string, peekedId: string | null = null) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(chartElement(qc, tree, projectId, peekedId));
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
    expect(within(first!).getByText("ISS-1")).toBeInTheDocument();
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
    const chip = screen.getByRole("button", { name: "issues.tree.collapse" });
    expect(chip).toHaveTextContent("4");
    expect(chip).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(chip);
    expect(tree.toggle).toHaveBeenCalledWith("1");

    fireEvent.click(screen.getAllByRole("button", { name: "issues.tree.focus" })[1]!);
    expect(tree.focus).toHaveBeenCalledWith("2");
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("offers no count chip on a leaf", () => {
    renderChart(state([row("1", 0)]));
    expect(screen.queryByRole("button", { name: "issues.tree.expand" })).toBeNull();
    expect(screen.queryByRole("button", { name: "issues.tree.collapse" })).toBeNull();
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
    fireEvent.click(screen.getByRole("button", { name: "issues.tree.load_more" }));
    expect(tree.loadMore).toHaveBeenCalledWith("1");
    fireEvent.click(screen.getByRole("button", { name: "issues.tree.retry" }));
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
    fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_use_outline" }));
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

  it("zooms with its controls and the keyboard", () => {
    const { container } = renderChart(state([row("1", 0)]));
    const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
    const scale = () => /scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]!;
    expect(scale()).toBe("1");

    fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_zoom_in" }));
    expect(Number(scale())).toBeGreaterThan(1);
    fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_zoom_out" }));
    expect(Number(scale())).toBeCloseTo(1);

    const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
    fireEvent.keyDown(surface, { key: "-" });
    expect(Number(scale())).toBeLessThan(1);
  });

  describe("fit and keyboard", () => {
    // jsdom has no layout: give the view a size so Fit has something to fit to.
    beforeEach(() => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(400);
    });
    afterEach(() => vi.restoreAllMocks());

    const wide = () => {
      // Twelve siblings are far wider than the 600px view.
      const kids = Array.from({ length: 12 }, (_, i) => row(`c${i}`, 1));
      return state([row("1", 0, { childCount: 12, expanded: true }), ...kids], { focusId: "1" });
    };
    const parts = (container: HTMLElement) => {
      const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      const [, x, y] = /translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([\d.]+)\)/
        .exec(canvas.style.transform)!;
      return {
        x: Number(x),
        y: Number(y),
        scale: Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]),
      };
    };

    it("Fit to view returns a zoomed and panned chart to the fitted view", () => {
      const { container } = renderChart(wide());
      const fitted = parts(container);
      expect(fitted.scale).toBeLessThan(1);

      const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
      fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_zoom_in" }));
      fireEvent.keyDown(surface, { key: "ArrowLeft" });
      expect(parts(container)).not.toEqual(fitted);

      fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_fit" }));
      expect(parts(container)).toEqual(fitted);
    });

    it("the 0 key fits the chart as the Fit button does", () => {
      const { container } = renderChart(wide());
      const fitted = parts(container);
      const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;

      fireEvent.keyDown(surface, { key: "+" });
      fireEvent.keyDown(surface, { key: "ArrowDown" });
      expect(parts(container)).not.toEqual(fitted);

      fireEvent.keyDown(surface, { key: "0" });
      expect(parts(container)).toEqual(fitted);
    });

    it("pans with the arrow keys", () => {
      const { container } = renderChart(state([row("1", 0)]));
      const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
      const start = parts(container);

      fireEvent.keyDown(surface, { key: "ArrowLeft" });
      const left = parts(container);
      expect(left.x).toBeGreaterThan(start.x);
      expect(left.y).toBe(start.y);

      fireEvent.keyDown(surface, { key: "ArrowRight" });
      expect(parts(container)).toEqual(start);

      fireEvent.keyDown(surface, { key: "ArrowUp" });
      expect(parts(container).y).toBeGreaterThan(start.y);
      fireEvent.keyDown(surface, { key: "ArrowDown" });
      expect(parts(container)).toEqual(start);
    });

    it("ignores keys pressed inside a node", () => {
      const { container } = renderChart(state([row("1", 0)]));
      const before = parts(container);
      fireEvent.keyDown(within(nodes()[0]!).getByRole("link"), { key: "ArrowLeft" });
      expect(parts(container)).toEqual(before);
    });
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
    expect(screen.getByRole("navigation")).toHaveTextContent("issues.tree.focus_root");
    expect(document.querySelector('[data-slot="tree-filters-paused"]')).toBeInTheDocument();
  });

  describe("opening a node", () => {
    afterEach(() => useIssueOpeningStore.setState({ openMode: "page" }));

    it("peeks the issue on a plain click when issues open in a peek, as in the outline", () => {
      useIssueOpeningStore.setState({ openMode: "peek" });
      renderChart(state([row("1", 0, { childCount: 1, expanded: true }), row("2", 1)]));
      fireEvent.click(within(nodes()[1]!).getByRole("link"));
      expect(peek.open).toHaveBeenCalledWith("2");
      expect(peek.toggle).not.toHaveBeenCalled();
    });

    it("opens the full page on a shift-click when issues open in a peek", () => {
      useIssueOpeningStore.setState({ openMode: "peek" });
      renderChart(state([row("1", 0)]));
      fireEvent.click(within(nodes()[0]!).getByRole("link"), { shiftKey: true });
      expect(navigation.push).toHaveBeenCalledWith("/acme/issues/1");
      expect(peek.open).not.toHaveBeenCalled();
    });

    it("previews on a shift-click and leaves a plain click to the page when issues open as pages", () => {
      useIssueOpeningStore.setState({ openMode: "page" });
      renderChart(state([row("1", 0)]));
      const link = within(nodes()[0]!).getByRole("link");
      fireEvent.click(link);
      expect(peek.open).not.toHaveBeenCalled();
      expect(peek.toggle).not.toHaveBeenCalled();
      fireEvent.click(link, { shiftKey: true });
      expect(peek.toggle).toHaveBeenCalledWith("1");
    });

    it("does not open the issue when the click ends a pan", () => {
      useIssueOpeningStore.setState({ openMode: "peek" });
      const { container } = renderChart(state([row("1", 0)]));
      const surface = container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
      const link = within(nodes()[0]!).getByRole("link");
      fireEvent.pointerDown(surface, { button: 0, clientX: 10, clientY: 10, pointerId: 1 });
      fireEvent.pointerMove(surface, { clientX: 90, clientY: 60, pointerId: 1 });
      fireEvent.pointerUp(surface, { pointerId: 1 });
      fireEvent.click(link);
      expect(peek.open).not.toHaveBeenCalled();
    });
  });

  describe("auto-fit", () => {
    // jsdom has no layout: give the view a size so the fit can run.
    const viewSize = (width: number, height: number) => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(width);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(height);
    };
    afterEach(() => vi.restoreAllMocks());
    const scale = (container: HTMLElement) => {
      const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      return Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]);
    };
    const loadingRow = (): TreeRow => ({
      kind: "status",
      key: "status:1",
      parentId: "1",
      depth: 1,
      guides: [],
      branchKey: "1",
      state: "loading",
    });

    it("fits a focused root once its children have arrived, not on the loading placeholder", () => {
      viewSize(600, 400);
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const root = row("1", 0, { expanded: true });
      const { container, rerender } = render(
        chartElement(qc, state([root, loadingRow()], { focusId: "1" })),
      );
      expect(scale(container)).toBe(1);

      // Twelve children are far wider than the 600px view: the fit must still
      // happen, and must shrink the chart to show them all.
      const children = Array.from({ length: 12 }, (_, i) => row(`c${i}`, 1));
      rerender(
        chartElement(
          qc,
          state([{ ...root, childCount: 12 }, ...children], { focusId: "1" }),
        ),
      );
      expect(scale(container)).toBeLessThan(1);
    });

    it("fits a project chart once the project's root issues have arrived", () => {
      viewSize(600, 400);
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const project = { focusId: null, chartAvailability: "project" as const, chartDepthOffset: 1 };
      const { container, rerender } = render(
        chartElement(
          qc,
          state([{ ...loadingRow(), parentId: null, depth: 0 }], project),
          "p1",
        ),
      );
      expect(scale(container)).toBe(1);

      const roots = Array.from({ length: 12 }, (_, i) => row(`r${i}`, 0));
      rerender(chartElement(qc, state(roots, project), "p1"));
      expect(scale(container)).toBeLessThan(1);
    });
  });

  describe("once the user has adjusted the view", () => {
    // A focused root whose children are still loading, fitted at scale 1. Each
    // case adjusts the view one way, then lets twelve children arrive: the
    // chart must stay exactly where the user left it.
    const loadingRow = (): TreeRow => ({
      kind: "status",
      key: "status:1",
      parentId: "1",
      depth: 1,
      guides: [],
      branchKey: "1",
      state: "loading",
    });
    const root = row("1", 0, { expanded: true });
    const loading = () => state([root, loadingRow()], { focusId: "1" });
    const arrived = () =>
      state(
        [{ ...root, childCount: 12 }, ...Array.from({ length: 12 }, (_, i) => row(`c${i}`, 1))],
        { focusId: "1" },
      );
    const setup = () => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(400);
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const view = render(chartElement(qc, loading()));
      const surface = view.container.querySelector('[data-slot="tree-chart"]') as HTMLElement;
      const canvas = view.container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      const scale = () => Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]);
      expect(scale()).toBe(1);
      const fitted = canvas.style.transform;
      const branchLands = () => view.rerender(chartElement(qc, arrived()));
      return { surface, canvas, scale, fitted, branchLands };
    };
    afterEach(() => vi.restoreAllMocks());

    it("keeps a wheel pan when a branch lands", () => {
      const { surface, canvas, fitted, branchLands } = setup();
      fireEvent.wheel(surface, { deltaX: 0, deltaY: 40 });
      const panned = canvas.style.transform;
      expect(panned).not.toBe(fitted);
      branchLands();
      expect(canvas.style.transform).toBe(panned);
    });

    it("keeps a ctrl + wheel zoom when a branch lands", () => {
      const { surface, canvas, scale, branchLands } = setup();
      fireEvent.wheel(surface, { deltaY: -100, ctrlKey: true });
      const zoomed = canvas.style.transform;
      expect(scale()).toBeGreaterThan(1);
      branchLands();
      expect(canvas.style.transform).toBe(zoomed);
    });

    it("keeps a drag pan when a branch lands", () => {
      const { surface, canvas, fitted, branchLands } = setup();
      fireEvent.pointerDown(surface, { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
      fireEvent.pointerMove(surface, { clientX: 130, clientY: 90, pointerId: 1 });
      fireEvent.pointerUp(surface, { pointerId: 1 });
      const panned = canvas.style.transform;
      expect(panned).not.toBe(fitted);
      branchLands();
      expect(canvas.style.transform).toBe(panned);
    });

    it("does not count a press that never moves past the drag threshold", () => {
      const { surface, scale, branchLands } = setup();
      fireEvent.pointerDown(surface, { button: 0, clientX: 100, clientY: 100, pointerId: 1 });
      fireEvent.pointerMove(surface, { clientX: 101, clientY: 100, pointerId: 1 });
      fireEvent.pointerUp(surface, { pointerId: 1 });
      branchLands();
      expect(scale()).toBeLessThan(1);
    });

    // The toolbar buttons only call the rows hook (mocked here), so the view
    // does not change by itself: the layout stays put until the branch lands.
    it("keeps the view when Show next level was pressed before a branch lands", () => {
      const { canvas, scale, branchLands } = setup();
      fireEvent.click(screen.getByRole("button", { name: /tree\.chart_expand_level/ }));
      const before = canvas.style.transform;
      branchLands();
      expect(canvas.style.transform).toBe(before);
      expect(scale()).toBe(1);
    });

    it("keeps the view when toolbar Collapse all was pressed before a branch lands", () => {
      const { canvas, scale, branchLands } = setup();
      fireEvent.click(screen.getByRole("button", { name: /tree\.collapse_all/ }));
      const before = canvas.style.transform;
      branchLands();
      expect(canvas.style.transform).toBe(before);
      expect(scale()).toBe(1);
    });
  });

  describe("auto-fit over the node cap", () => {
    const issues = (n: number) => Array.from({ length: n }, (_, i) => row(`c${i}`, 1));
    const root = row("1", 0, { expanded: true });
    const overCap = () => state([root, ...issues(TREE_CHART_NODE_CAP)], { focusId: "1" });
    const narrow = () => state([root, row("c0", 1)], { focusId: "1" });
    const wide = () =>
      state([{ ...root, childCount: 12 }, ...issues(12)], { focusId: "1" });
    beforeEach(() => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(400);
      vi.mocked(fitViewport).mockClear();
    });
    afterEach(() => vi.restoreAllMocks());
    const scale = (container: HTMLElement) => {
      const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      return Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]);
    };

    it("does not count Collapse all pressed on the cap screen as an adjustment, so the chart still fits once it is under the cap", () => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const { container, rerender } = render(chartElement(qc, narrow()));
      expect(scale(container)).toBe(1);

      rerender(chartElement(qc, overCap()));
      expect(container.querySelector('[data-slot="tree-chart-too-many"]')).toBeInTheDocument();
      fireEvent.click(screen.getAllByRole("button", { name: /tree\.collapse_all/ })[0]!);

      rerender(chartElement(qc, narrow()));
      rerender(chartElement(qc, wide()));
      expect(scale(container)).toBeLessThan(1);
    });

    it("does not fit while over the cap", () => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const { rerender } = render(chartElement(qc, narrow()));
      vi.mocked(fitViewport).mockClear();

      rerender(chartElement(qc, overCap()));
      expect(fitViewport).not.toHaveBeenCalled();
    });
  });

  describe("auto-fit on resize", () => {
    const observers: Array<() => void> = [];
    let width = 600;
    beforeEach(() => {
      width = 600;
      observers.length = 0;
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(400);
      vi.stubGlobal(
        "ResizeObserver",
        class {
          constructor(cb: () => void) {
            observers.push(cb);
          }
          observe() {}
          disconnect() {}
        },
      );
    });
    afterEach(() => {
      vi.restoreAllMocks();
      vi.unstubAllGlobals();
    });
    const scaleOf = (container: HTMLElement) => {
      const canvas = container.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      return Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]);
    };
    const resizeTo = (next: number) => {
      width = next;
      act(() => observers.forEach((cb) => cb()));
    };
    const wide = () =>
      state(Array.from({ length: 6 }, (_, i) => row(`r${i}`, 0)), {
        focusId: null,
        chartAvailability: "project",
        chartDepthOffset: 1,
      });

    it("re-fits when the view is resized until the user has adjusted it", () => {
      const { container } = renderChart(wide(), "p1");
      const roomy = scaleOf(container);
      resizeTo(300);
      expect(scaleOf(container)).toBeLessThan(roomy);
    });

    it("leaves the view alone on resize once the user has zoomed", () => {
      const { container } = renderChart(wide(), "p1");
      fireEvent.click(screen.getByRole("button", { name: "issues.tree.chart_zoom_out" }));
      const zoomed = scaleOf(container);
      resizeTo(300);
      expect(scaleOf(container)).toBe(zoomed);
    });
  });

  describe("node cap", () => {
    const issues = (n: number, depth: number) =>
      Array.from({ length: n }, (_, i) => row(`c${i}`, depth));

    it("counts issues only: the project's top node and load-more placeholders do not tip it over", () => {
      const rows: TreeRow[] = [
        ...issues(TREE_CHART_NODE_CAP, 0),
        { kind: "more", key: "more:root", parentId: null, depth: 0, guides: [], branchKey: "__root__", loading: false, error: false },
      ];
      const { container } = renderChart(
        state(rows, { chartAvailability: "project", chartDepthOffset: 1 }),
        "p1",
      );
      expect(container.querySelector('[data-slot="tree-chart-too-many"]')).toBeNull();
      expect(nodes()).toHaveLength(TREE_CHART_NODE_CAP);
    });

    it("asks to narrow a project chart one issue over the cap and draws nothing", () => {
      const { container } = renderChart(
        state(issues(TREE_CHART_NODE_CAP + 1, 0), { chartAvailability: "project", chartDepthOffset: 1 }),
        "p1",
      );
      expect(container.querySelector('[data-slot="tree-chart-too-many"]')).toHaveTextContent(
        `"count":${TREE_CHART_NODE_CAP + 1}`,
      );
      expect(nodes()).toHaveLength(0);
      expect(container.querySelector('[data-slot="tree-chart-edges"]')).toBeNull();
    });

    it("disables Show next level while over the cap, so it cannot fan out more fetches", () => {
      const focused = (n: number) =>
        state([row("1", 0, { expanded: true }), ...issues(n - 1, 1)], { focusId: "1" });
      const qc = new QueryClient();
      const { rerender } = render(chartElement(qc, focused(TREE_CHART_NODE_CAP + 1)));
      expect(screen.getByRole("button", { name: /tree\.chart_expand_level/ })).toBeDisabled();
      rerender(chartElement(qc, focused(TREE_CHART_NODE_CAP)));
      expect(screen.getByRole("button", { name: /tree\.chart_expand_level/ })).toBeEnabled();
    });

    it("keeps the toolbar and breadcrumb usable while asking to narrow down", () => {
      const children = issues(TREE_CHART_NODE_CAP, 1);
      const tree = state([row("1", 0, { childCount: children.length, expanded: true }), ...children], {
        focusId: "1",
        focusIssue: issue("1"),
      });
      renderChart(tree);
      expect(screen.getByRole("navigation")).toHaveTextContent("issues.tree.focus_root");
      fireEvent.click(screen.getAllByRole("button", { name: /tree\.collapse_all/ })[0]!);
      expect(tree.collapseAll).toHaveBeenCalled();
    });
  });
});
