import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import {
  IssuePeekActionsContext,
  IssuePeekIdContext,
  type IssuePeekActions,
} from "../surface/peek-context";
import { TreeView } from "./tree-view";
import type { TreeIssueRow, TreeRow } from "./tree-rows";
import type { TreeRowsState } from "./use-tree-rows";

// AG patch: outline tree view rendering. The rows are handed in directly; the
// data hook has its own tests.

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
    loadMore: vi.fn(),
    retry: vi.fn(),
    ...overrides,
  };
}

function renderTree(tree: TreeRowsState, peekedId: string | null = null) {
  return render(
    <NavigationProvider value={navigation}>
      <IssuePeekActionsContext.Provider value={peek}>
        <IssuePeekIdContext.Provider value={peekedId}>
          <TreeView tree={tree} childProgressMap={new Map([["1", { done: 1, total: 3 }]])} />
        </IssuePeekIdContext.Provider>
      </IssuePeekActionsContext.Provider>
    </NavigationProvider>,
  );
}

const root = (id: string, over: Partial<TreeIssueRow> = {}): TreeIssueRow => ({
  kind: "issue",
  key: id,
  parentId: null,
  depth: 0,
  guides: [],
  issue: issue(id),
  childCount: 0,
  expanded: false,
  isLast: false,
  ancestorPath: [],
  ...over,
});

describe("AG patch: TreeView", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders one row per issue with identifier, title, progress, assignee and agent dot", () => {
    const rows: TreeRow[] = [
      root("1", {
        childCount: 3,
        issue: issue("1", { assignee_type: "member", assignee_id: "user-1" }),
      }),
    ];
    renderTree(state(rows));
    const row = screen.getByRole("treeitem");
    expect(within(row).getByText("MAG-1")).toBeInTheDocument();
    expect(within(row).getByText("Title 1")).toBeInTheDocument();
    expect(within(row).getByText("1/3")).toBeInTheDocument();
    expect(within(row).getByTestId("avatar-user-1")).toBeInTheDocument();
    expect(within(row).getByTestId("agent-dot-1")).toBeInTheDocument();
  });

  it("nests rows with connector cells and marks aria levels", () => {
    const rows: TreeRow[] = [
      root("1", { childCount: 2, expanded: true }),
      {
        ...root("2"),
        parentId: "1",
        depth: 1,
        guides: [false],
        issue: issue("2", { parent_issue_id: "1" }),
      },
    ];
    renderTree(state(rows));
    const items = screen.getAllByRole("treeitem");
    expect(items.map((i) => i.getAttribute("aria-level"))).toEqual(["1", "2"]);
    expect(items[0]).toHaveAttribute("aria-expanded", "true");
    expect(items[0]!.querySelectorAll('[data-slot="tree-connector"]')).toHaveLength(0);
    expect(items[1]!.querySelectorAll('[data-slot="tree-connector"]')).toHaveLength(1);
  });

  it("toggles a node from its chevron without opening the issue", () => {
    const tree = state([root("1", { childCount: 2 })]);
    renderTree(tree);
    fireEvent.click(screen.getByRole("button", { name: "tree.expand" }));
    expect(tree.toggle).toHaveBeenCalledWith("1");
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("offers no chevron on a leaf", () => {
    renderTree(state([root("1")]));
    expect(screen.queryByRole("button", { name: "tree.expand" })).toBeNull();
    expect(screen.queryByRole("button", { name: "tree.collapse" })).toBeNull();
  });

  it("focuses a node from its focus action", () => {
    const tree = state([root("1")]);
    renderTree(tree);
    fireEvent.click(screen.getByRole("button", { name: "tree.focus" }));
    expect(tree.focus).toHaveBeenCalledWith("1");
  });

  it("opens and peeks an issue the way List does", () => {
    renderTree(state([root("1")]));
    fireEvent.click(screen.getByRole("link"));
    expect(navigation.push).toHaveBeenCalledWith("/acme/issues/1");
    expect(fireEvent.click(screen.getByRole("link"), { shiftKey: true })).toBe(false);
    expect(peek.toggle).toHaveBeenCalledWith("1");
  });

  it("marks the peeked row and publishes its order for J / K stepping", () => {
    const { container } = renderTree(state([root("1"), root("2")]), "2");
    expect(container.querySelector('[data-peek-target="2"]')).toHaveAttribute("data-peeked");
    expect(container.querySelector('[data-peek-target="1"]')).not.toHaveAttribute("data-peeked");
    expect(peek.publishColumns).toHaveBeenCalledWith([["1", "2"]]);
  });

  it("dims a promoted root's hidden ancestor path and focuses a clicked crumb", () => {
    const tree = state([
      root("3", {
        ancestorPath: [
          { id: "1", identifier: "MAG-1", parentId: null, title: "Epic", status: "todo" },
          { id: "2", identifier: "MAG-2", parentId: "1", title: "Story", status: "todo" },
        ],
      }),
    ]);
    const { container } = renderTree(tree);
    const path = container.querySelector('[data-slot="tree-ancestor-path"]')!;
    expect(path.className).toContain("text-faint-foreground");
    expect(path.textContent).toBe("MAG-1›MAG-2›");
    fireEvent.click(within(path as HTMLElement).getByText("MAG-2"));
    expect(tree.focus).toHaveBeenCalledWith("2");
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("shows a breadcrumb back up while focused", () => {
    const focusIssue = issue("9", { parent_issue_id: "2" });
    const tree = state([root("9", { issue: focusIssue, expanded: true })], {
      focusId: "9",
      focusIssue,
      focusPath: [
        { id: "1", identifier: "MAG-1", parentId: null, title: "Epic", status: "todo" },
        { id: "2", identifier: "MAG-2", parentId: "1", title: "Story", status: "todo" },
      ],
    });
    renderTree(tree);
    const nav = screen.getByRole("navigation");
    fireEvent.click(within(nav).getByText("MAG-1"));
    expect(tree.focus).toHaveBeenCalledWith("1");
    fireEvent.click(within(nav).getByText("tree.focus_root"));
    expect(tree.clearFocus).toHaveBeenCalled();
    expect(within(nav).getByText("MAG-9")).toBeInTheDocument();
  });

  it("virtualises: a long list mounts a window of rows, not all of them", () => {
    const rows: TreeRow[] = Array.from({ length: 2000 }, (_, i) => root(String(i + 1)));
    renderTree(state(rows));
    const mounted = screen.getAllByRole("treeitem").length;
    expect(mounted).toBeGreaterThan(0);
    expect(mounted).toBeLessThan(200);
  });

  it("has no breadcrumb when nothing is focused", () => {
    renderTree(state([root("1")]));
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("wires expand all and collapse all", () => {
    const tree = state([root("1")]);
    renderTree(tree);
    fireEvent.click(screen.getByRole("button", { name: /tree\.expand_all/ }));
    fireEvent.click(screen.getByRole("button", { name: /tree\.collapse_all/ }));
    expect(tree.expandAll).toHaveBeenCalled();
    expect(tree.collapseAll).toHaveBeenCalled();
  });

  it("renders load-more, loading and error rows for a branch", () => {
    const tree = state([
      root("1", { childCount: 5, expanded: true }),
      { kind: "status", key: "status:1", parentId: "1", depth: 1, guides: [false], branchKey: "1", state: "error" },
      { kind: "more", key: "more:__root__", parentId: null, depth: 0, guides: [], branchKey: "__root__", loading: false, error: false },
    ]);
    renderTree(tree);
    fireEvent.click(screen.getByRole("button", { name: "tree.retry" }));
    expect(tree.retry).toHaveBeenCalledWith("1");
    fireEvent.click(screen.getByRole("button", { name: "tree.load_more" }));
    expect(tree.loadMore).toHaveBeenCalledWith("__root__");
  });

  it("shows a failed load-more as an error with a retry, not a dead button", () => {
    const tree = state([
      root("1"),
      { kind: "more", key: "more:__root__", parentId: null, depth: 0, guides: [], branchKey: "__root__", loading: false, error: true },
    ]);
    renderTree(tree);
    expect(screen.getByRole("alert")).toHaveTextContent("tree.load_error");
    expect(screen.queryByRole("button", { name: "tree.load_more" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "tree.retry" }));
    expect(tree.retry).toHaveBeenCalledWith("__root__");
    expect(tree.loadMore).not.toHaveBeenCalled();
  });

  it("keeps the focus action in the tab order and visible on keyboard focus or touch", () => {
    renderTree(state([root("1")]));
    const button = screen.getByRole("button", { name: "tree.focus" });
    const classes = button.className.split(/\s+/);
    // `hidden` is display:none, which removes the button from the tab order.
    expect(classes).not.toContain("hidden");
    expect(classes).toContain("flex");
    expect(classes).toContain("opacity-0");
    expect(classes).toContain("focus-visible:opacity-100");
    expect(classes).toContain("group-focus-within/row:opacity-100");
    expect(classes).toContain("[@media(hover:none)]:opacity-100");
    button.focus();
    expect(document.activeElement).toBe(button);
  });

  it("says filters are paused while focused, and clears focus from the hint", () => {
    const focusIssue = issue("9");
    const tree = state([root("9", { issue: focusIssue, expanded: true })], {
      focusId: "9",
      focusIssue,
    });
    renderTree(tree);
    const hint = document.querySelector('[data-slot="tree-filters-paused"]') as HTMLElement;
    expect(hint).toHaveTextContent("tree.filters_paused");
    fireEvent.click(within(hint).getByRole("button", { name: "tree.clear_focus" }));
    expect(tree.clearFocus).toHaveBeenCalled();
  });

  it("has no paused-filters hint when nothing is focused", () => {
    renderTree(state([root("1")]));
    expect(document.querySelector('[data-slot="tree-filters-paused"]')).toBeNull();
  });
});
