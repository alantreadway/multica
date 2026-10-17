/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "@multica/core/issues/stores/surface-view-store";
import { issueKeys } from "@multica/core/issues/queries";
import { useTreeViewStore } from "@multica/core/issues/stores/tree-view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import type {
  Issue,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
  ListIssuesParams,
} from "@multica/core/types";
import { useIssueSurfaceController } from "./use-issue-surface-controller";
import type { TreeIssueRow } from "../components/tree-rows";
import type { IssueSurfaceMode } from "./types";

// AG patch: tree view mode. The hook is driven through the surface controller,
// the way the page uses it, against an in-memory issue graph.

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useUpdateIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useBatchUpdateIssues: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useBatchDeleteIssues: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@multica/core/modals", () => ({
  useModalStore: { getState: () => ({ open: vi.fn() }) },
}));
vi.mock("../../i18n", () => ({
  useT: () => ({ t: () => "translated" }),
}));

function makeIssue(id: string, parent: string | null = null): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: Number.parseInt(id, 10) || 1,
    identifier: `MAG-${id}`,
    title: `Issue ${id}`,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: parent,
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as Issue;
}

const never = <T,>() => new Promise<T>(() => {});

describe("AG patch: tree view mode", () => {
  let qc: QueryClient;
  // Every issue by id. `visibleIds` is what the filters let through; a child
  // whose parent is not visible is promoted to a root, as the server does.
  // Only requests carrying a priority filter are filtered, so a request the
  // client sends without view filters sees the whole graph.
  let graph: Map<string, Issue>;
  let visibleIds: Set<string> | null;
  let listIssueTableRows: ReturnType<typeof vi.fn>;
  let listIssues: ReturnType<typeof vi.fn>;
  let getIssue: ReturnType<typeof vi.fn>;
  let listIssueTableFacets: ReturnType<typeof vi.fn>;

  const visibleFor = (request: IssueTableRowsRequest) =>
    request.query.filters.priorities?.length ? visibleIds : null;
  const childrenOf = (request: IssueTableRowsRequest) => {
    const parent = request.parent_id;
    const visible = visibleFor(request);
    // The server returns a branch only when its parent passes the filters too
    // (issue_table_rows.go: the `EXISTS (... membership parent ...)` check).
    if (parent !== null && visible && !visible.has(parent)) return [];
    return [...graph.values()].filter((issue) => {
      if (visible && !visible.has(issue.id)) return false;
      const parentId = issue.parent_issue_id;
      if (parent === null) {
        return parentId === null || (visible !== null && !visible.has(parentId));
      }
      return parentId === parent;
    });
  };
  const directChildCount = (id: string, request: IssueTableRowsRequest) => {
    const visible = visibleFor(request);
    return [...graph.values()].filter(
      (issue) => issue.parent_issue_id === id && (!visible || visible.has(issue.id)),
    ).length;
  };

  function wrapper(surfaceKey: string) {
    const store = getIssueSurfaceViewStore(surfaceKey);
    return function Wrapper({ children }: { children: ReactNode }) {
      return (
        <QueryClientProvider client={qc}>
          <ViewStoreProvider store={store}>{children}</ViewStoreProvider>
        </QueryClientProvider>
      );
    };
  }

  function useController(modes: IssueSurfaceMode[] = ["list", "table", "tree"]) {
    return useIssueSurfaceController({ scope: { type: "workspace" }, modes });
  }

  const treeRows = (result: { current: ReturnType<typeof useController> }) =>
    result.current.tree.rows.filter((row): row is TreeIssueRow => row.kind === "issue");

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    graph = new Map();
    visibleIds = null;
    listIssueTableRows = vi.fn(async (request: IssueTableRowsRequest) => {
      const all = childrenOf(request);
      const start = request.page?.cursor ? Number(request.page.cursor) : 0;
      const limit = request.page?.limit ?? 50;
      const page = all.slice(start, start + limit);
      const next = start + limit < all.length ? String(start + limit) : null;
      return {
        query_fingerprint: "fp",
        group_key: null,
        parent_id: request.parent_id,
        total: all.length,
        rows: page.map((issue) => ({
          issue,
          direct_child_count: directChildCount(issue.id, request),
        })),
        branch_total: all.length,
        next_cursor: next,
      } satisfies IssueTableRowsResponse;
    });
    listIssues = vi.fn(async (params?: ListIssuesParams) => {
      const issues = (params?.ids ?? []).flatMap((id) => graph.get(id) ?? []);
      return { issues, total: issues.length };
    });
    getIssue = vi.fn(async (id: string) => graph.get(id)!);
    listIssueTableFacets = vi.fn(async () => ({
      query_fingerprint: "fp",
      total: 7,
      facets: [{ kind: "priority", values: [{ key: "high", count: 7 }] }],
    }));
    setApiInstance({
      listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
      listIssues,
      getIssue,
      listIssueTableRows,
      listIssueTableFacets,
      listIssueTableGroups: vi.fn(() => never()),
      listGroupedIssues: vi.fn(() => never()),
      listProjects: vi.fn(() => never()),
      listProperties: vi.fn(() => never()),
      getAgentTaskSnapshot: vi.fn(() => never()),
      getWorkspaceWorkingAgents: vi.fn(async () => []),
      getChildIssueProgress: vi.fn(() => never()),
    } as unknown as ApiClient);
    pruneIssueSurfaceViewStates([]);
    useTreeViewStore.setState({ expanded: {} });
  });

  afterEach(() => {
    cleanup();
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    vi.restoreAllMocks();
  });

  it("forces sub-issues on in the server query even when the toggle is off", async () => {
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");
    if (store.getState().showSubIssues) store.getState().toggleShowSubIssues();
    expect(store.getState().showSubIssues).toBe(false);

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());

    expect(result.current.viewMode).toBe("tree");
    expect(result.current.allowTree).toBe(true);
    expect(result.current.tableQuerySpec.filters.include_sub_issues).toBe(true);
  });

  it("leaves the sub-issue toggle alone in other modes", async () => {
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("table");
    if (store.getState().showSubIssues) store.getState().toggleShowSubIssues();

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });

    expect(result.current.viewMode).toBe("table");
    expect(result.current.tableQuerySpec.filters.include_sub_issues).toBe(false);
  });

  it("loads only the root page on mount, with hierarchy on and no status branches", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(result.current.tree.isLoading).toBe(false));

    expect(listIssueTableRows).toHaveBeenCalledTimes(1);
    expect(listIssueTableRows).toHaveBeenCalledWith(
      expect.objectContaining({
        group: { kind: "none" },
        group_key: null,
        hierarchy: { enabled: true },
        parent_id: null,
      }),
    );
    expect(treeRows(result).map((row) => row.issue.id)).toEqual(["1"]);
    expect(treeRows(result)[0]).toMatchObject({ childCount: 1, expanded: false });
  });

  it("loads a parent's children when it is expanded and remembers the expansion", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "1"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));

    act(() => result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(3));

    expect(listIssueTableRows).toHaveBeenLastCalledWith(
      expect.objectContaining({ parent_id: "1" }),
    );
    expect(treeRows(result).map((row) => [row.issue.id, row.depth])).toEqual([
      ["1", 0],
      ["2", 1],
      ["3", 1],
    ]);
    expect(useTreeViewStore.getState().expanded["workspace:all"]).toEqual(["1"]);

    act(() => result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    expect(useTreeViewStore.getState().expanded["workspace:all"]).toEqual([]);
  });

  it("restores persisted expansion by fetching those branches without a click", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    useTreeViewStore.setState({ expanded: { "workspace:all": ["1"] } });
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result).map((r) => r.issue.id)).toEqual(["1", "2"]));
  });

  it("shows a promoted root's hidden ancestor path, and a crumb focuses that ancestor", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    visibleIds = new Set(["3"]);
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");
    store.getState().togglePriorityFilter("high");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() =>
      expect(treeRows(result)[0]?.ancestorPath.map((a) => a.identifier)).toEqual([
        "MAG-1",
        "MAG-2",
      ]),
    );
    expect(treeRows(result)[0]!.issue.id).toBe("3");

    // The filter stays on: "2" is hidden by it, so only the focus query
    // (which drops the filters) can return its subtree.
    act(() => result.current.tree.focus("2"));
    await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("2"));
    await waitFor(() =>
      expect(treeRows(result).map((r) => [r.issue.id, r.depth])).toEqual([
        ["2", 0],
        ["3", 1],
      ]),
    );
    expect(result.current.tree.focusPath.map((a) => a.identifier)).toEqual(["MAG-1"]);
    const focusRequest = listIssueTableRows.mock.calls.at(-1)![0] as IssueTableRowsRequest;
    expect(focusRequest.parent_id).toBe("2");
    expect(focusRequest.query.filters).toEqual({ include_sub_issues: true });
    expect(focusRequest.query.scope).toEqual({ kind: "workspace" });

    // Leaving focus restores the filtered tree.
    act(() => result.current.tree.clearFocus());
    await waitFor(() => expect(result.current.tree.focusId).toBeNull());
    await waitFor(() => expect(treeRows(result)[0]?.issue.id).toBe("3"));
    expect(treeRows(result)[0]!.ancestorPath.map((a) => a.identifier)).toEqual([
      "MAG-1",
      "MAG-2",
    ]);
  });

  it("pages a branch with a load-more cursor", async () => {
    for (let i = 1; i <= 60; i += 1) graph.set(String(i), makeIssue(String(i)));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(50));
    expect(result.current.tree.rows.at(-1)).toMatchObject({ kind: "more" });

    act(() => result.current.tree.loadMore("__root__"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(60));
    expect(result.current.tree.rows.some((row) => row.kind === "more")).toBe(false);
  });

  it("expand all stops at depth 2 and collapse all closes everything", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    graph.set("4", makeIssue("4", "3"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));

    act(() => result.current.tree.expandAll());
    await waitFor(() =>
      expect(treeRows(result).map((r) => [r.issue.id, r.depth])).toEqual([
        ["1", 0],
        ["2", 1],
        ["3", 2],
      ]),
    );
    // "3" has a child but sits at the depth cap, so it stays closed.
    expect(treeRows(result).at(-1)).toMatchObject({ childCount: 1, expanded: false });
    expect(listIssueTableRows).not.toHaveBeenCalledWith(
      expect.objectContaining({ parent_id: "3" }),
    );

    act(() => result.current.tree.collapseAll());
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
  });

  it("reports an empty tree so the surface can show its empty state", async () => {
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(result.current.isEmpty).toBe(true));
    expect(result.current.isLoading).toBe(false);
  });

  it("falls back to the first mode when a persisted tree mode is not offered here", async () => {
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");

    const { result } = renderHook(() => useController(["list", "table"]), {
      wrapper: wrapper("workspace"),
    });

    expect(result.current.viewMode).toBe("list");
    expect(result.current.allowTree).toBe(false);
    await waitFor(() => expect(store.getState().viewMode).toBe("list"));
    // No tree request was made on a surface that does not offer the mode.
    expect(
      listIssueTableRows.mock.calls.some(
        ([request]) => (request as IssueTableRowsRequest).group.kind === "none",
      ),
    ).toBe(false);
  });

  it("applies filters and sort to the tree's row requests, and re-queries when they change", async () => {
    graph.set("1", makeIssue("1"));
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");
    store.getState().togglePriorityFilter("high");
    store.getState().setSortBy("created_at");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));

    const lastRequest = () =>
      listIssueTableRows.mock.calls.at(-1)![0] as IssueTableRowsRequest;
    expect(lastRequest().query.filters).toMatchObject({ priorities: ["high"] });
    expect(lastRequest().query.sort.field).toBe("created_at");

    const callsBefore = listIssueTableRows.mock.calls.length;
    act(() => store.getState().togglePriorityFilter("urgent"));
    await waitFor(() =>
      expect(lastRequest().query.filters.priorities).toEqual(["high", "urgent"]),
    );
    expect(listIssueTableRows.mock.calls.length).toBeGreaterThan(callsBefore);
    expect(lastRequest().query.filters.include_sub_issues).toBe(true);
  });

  it("keeps the filter-bar counts on the server facets, evaluated over the same query", async () => {
    graph.set("1", makeIssue("1"));
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");
    store.getState().togglePriorityFilter("high");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    // Counts are not recounted from the loaded rows.
    expect(result.current.facetCountsExact).toBe(false);

    act(() => result.current.setActiveTableFacet({ kind: "priority" }));
    await waitFor(() => expect(result.current.tableFacetCounts?.total).toBe(7));
    const request = listIssueTableFacets.mock.calls.at(-1)![0];
    expect(request.facets).toEqual(expect.arrayContaining([{ kind: "priority" }]));
    expect(request.query.filters).toMatchObject({
      priorities: ["high"],
      include_sub_issues: true,
    });
  });

  it("makes no tree requests in other modes", async () => {
    graph.set("1", makeIssue("1"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("list");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    expect(result.current.tree.rows).toEqual([]);
    expect(
      listIssueTableRows.mock.calls.some(
        ([request]) => (request as IssueTableRowsRequest).group.kind === "none",
      ),
    ).toBe(false);
  });

  it("works on a project surface with no focus, scoped to that project", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    getIssueSurfaceViewStore("project:p1").getState().setViewMode("tree");

    const { result } = renderHook(
      () =>
        useIssueSurfaceController({
          scope: { type: "project", projectId: "p1" },
          modes: ["list", "tree"],
        }),
      { wrapper: wrapper("project:p1") },
    );
    await waitFor(() => expect(treeRows(result).map((r) => r.issue.id)).toEqual(["1"]));

    expect(result.current.viewMode).toBe("tree");
    expect(result.current.tree.focusId).toBeNull();
    expect(result.current.tree.focusIssue).toBeNull();
    expect(JSON.stringify(listIssueTableRows.mock.calls.at(-1)![0].query.scope)).toContain("p1");
  });

  it("keeps each surface's expanded nodes separate", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    getIssueSurfaceViewStore("project:p1").getState().setViewMode("tree");

    const workspace = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(workspace.result)).toHaveLength(1));
    act(() => workspace.result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(workspace.result)).toHaveLength(2));

    const project = renderHook(
      () =>
        useIssueSurfaceController({
          scope: { type: "project", projectId: "p1" },
          modes: ["list", "tree"],
        }),
      { wrapper: wrapper("project:p1") },
    );
    await waitFor(() => expect(treeRows(project.result)).toHaveLength(1));
    expect(treeRows(project.result)[0]).toMatchObject({ expanded: false });
    expect(treeRows(workspace.result)).toHaveLength(2);
  });

  it("falls back to the surface's first mode, not List, when tree is not offered", async () => {
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");

    const { result } = renderHook(() => useController(["board", "list"]), {
      wrapper: wrapper("workspace"),
    });

    expect(result.current.viewMode).toBe("board");
    expect(result.current.allowTree).toBe(false);
    await waitFor(() => expect(store.getState().viewMode).toBe("board"));
  });

  it("focus drops view filters, search and scope but keeps the sort", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    // "2" is filtered out, so a filtered request for its branch is empty.
    visibleIds = new Set(["1", "3"]);
    const store = getIssueSurfaceViewStore("project:p1");
    store.getState().setViewMode("tree");
    store.getState().togglePriorityFilter("high");
    store.getState().setSortBy("created_at");

    const { result } = renderHook(
      () =>
        useIssueSurfaceController({
          scope: { type: "project", projectId: "p1" },
          modes: ["list", "tree"],
        }),
      { wrapper: wrapper("project:p1") },
    );
    await waitFor(() => expect(treeRows(result).map((r) => r.issue.id)).toEqual(["1", "3"]));

    act(() => result.current.tree.focus("2"));
    await waitFor(() =>
      expect(treeRows(result).map((r) => [r.issue.id, r.depth])).toEqual([
        ["2", 0],
        ["3", 1],
      ]),
    );
    const request = listIssueTableRows.mock.calls.at(-1)![0] as IssueTableRowsRequest;
    expect(request.query.scope).toEqual({ kind: "workspace" });
    expect(request.query.filters).toEqual({ include_sub_issues: true });
    expect(request.query.search).toBeUndefined();
    expect(request.query.sort.field).toBe("created_at");
    // The controller's own query, and so the filter counts, is untouched.
    expect(result.current.tableQuerySpec.filters.priorities).toEqual(["high"]);
    expect(result.current.tableQuerySpec.scope.kind).toBe("project");
  });

  it("focus does not fetch the unfocused tree's open branches with the focus query", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    act(() => result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(2));

    listIssueTableRows.mockClear();
    act(() => result.current.tree.focus("2"));
    await waitFor(() => expect(treeRows(result).map((r) => r.issue.id)).toEqual(["2", "3"]));

    expect(
      listIssueTableRows.mock.calls.map(([request]) => (request as IssueTableRowsRequest).parent_id),
    ).toEqual(["2"]);
  });

  it("trims load-more cursors when a realtime refresh refetches the head", async () => {
    for (let i = 1; i <= 60; i += 1) graph.set(String(i), makeIssue(String(i)));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(50));
    act(() => result.current.tree.loadMore("__root__"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(60));

    // A new row sorts to the front, shifting every page boundary by one.
    const first = [...graph.entries()];
    graph.clear();
    graph.set("0", makeIssue("0"));
    for (const [id, issue] of first) graph.set(id, issue);
    listIssueTableRows.mockClear();
    await act(async () => {
      await qc.invalidateQueries({ queryKey: issueKeys.tableAll("ws-1") });
    });

    await waitFor(() => {
      const ids = treeRows(result).map((r) => r.issue.id);
      expect(ids).toHaveLength(50);
      expect(new Set(ids).size).toBe(ids.length);
    });
    expect(result.current.tree.rows.at(-1)).toMatchObject({ kind: "more" });
  });

  it("stops refetching a branch once it is collapsed", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    act(() => result.current.tree.expandAll());
    await waitFor(() => expect(treeRows(result)).toHaveLength(3));

    act(() => result.current.tree.collapseAll());
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    listIssueTableRows.mockClear();
    await act(async () => {
      await qc.invalidateQueries({ queryKey: issueKeys.tableAll("ws-1") });
    });
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());

    expect(
      listIssueTableRows.mock.calls.map(([request]) => (request as IssueTableRowsRequest).parent_id),
    ).toEqual([null]);
  });

  it("collapsing a parent stops refetching its open descendants too", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    act(() => result.current.tree.expandAll());
    await waitFor(() => expect(treeRows(result)).toHaveLength(3));

    // "2" stays in the persisted open set, but it is no longer on screen.
    act(() => result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    expect(useTreeViewStore.getState().expanded["workspace:all"]).toContain("2");
    listIssueTableRows.mockClear();
    await act(async () => {
      await qc.invalidateQueries({ queryKey: issueKeys.tableAll("ws-1") });
    });
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());

    expect(
      listIssueTableRows.mock.calls.map(([request]) => (request as IssueTableRowsRequest).parent_id),
    ).toEqual([null]);
  });

  it("keeps the tree on screen while a filter change loads, instead of a skeleton", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    const store = getIssueSurfaceViewStore("workspace");
    store.getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));
    act(() => result.current.tree.toggle("1"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(2));

    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const impl = listIssueTableRows.getMockImplementation() as (
      request: IssueTableRowsRequest,
    ) => Promise<IssueTableRowsResponse>;
    listIssueTableRows.mockImplementation(async (request: IssueTableRowsRequest) => {
      await gate;
      return impl(request);
    });
    act(() => store.getState().togglePriorityFilter("high"));
    await waitFor(() => expect(listIssueTableRows).toHaveBeenCalled());

    // Both the root and the open branch still show their previous rows.
    expect(result.current.tree.isLoading).toBe(false);
    expect(result.current.isLoading).toBe(false);
    expect(treeRows(result).map((r) => r.issue.id)).toEqual(["1", "2"]);

    await act(async () => release());
    await waitFor(() =>
      expect(listIssueTableRows.mock.calls.at(-1)![0].query.filters.priorities).toEqual(["high"]),
    );
    expect(treeRows(result).map((r) => r.issue.id)).toEqual(["1", "2"]);
  });

  it("keeps the tree on screen while focus loads", async () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(1));

    const impl = listIssueTableRows.getMockImplementation() as (
      request: IssueTableRowsRequest,
    ) => Promise<IssueTableRowsResponse>;
    listIssueTableRows.mockImplementation(() => never());
    getIssue.mockImplementation(() => never());
    act(() => result.current.tree.focus("1"));

    expect(result.current.isLoading).toBe(false);
    expect(result.current.tree.rows).toEqual([
      expect.objectContaining({ kind: "status", state: "loading" }),
    ]);
    getIssue.mockImplementation(async (id: string) => graph.get(id)!);
    listIssueTableRows.mockImplementation(impl);
  });

  it("recovers from a failed load-more page with a retry on the more row", async () => {
    for (let i = 1; i <= 60; i += 1) graph.set(String(i), makeIssue(String(i)));
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");
    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() => expect(treeRows(result)).toHaveLength(50));

    const impl = listIssueTableRows.getMockImplementation() as (
      request: IssueTableRowsRequest,
    ) => Promise<IssueTableRowsResponse>;
    listIssueTableRows.mockImplementation(async (request: IssueTableRowsRequest) => {
      if (request.page?.cursor) throw new Error("boom");
      return impl(request);
    });
    act(() => result.current.tree.loadMore("__root__"));
    await waitFor(() =>
      expect(result.current.tree.rows.at(-1)).toMatchObject({ kind: "more", error: true }),
    );
    // The loaded rows stay; only the tail row reports the failure.
    expect(treeRows(result)).toHaveLength(50);

    listIssueTableRows.mockImplementation(impl);
    act(() => result.current.tree.retry("__root__"));
    await waitFor(() => expect(treeRows(result)).toHaveLength(60));
    expect(result.current.tree.rows.some((row) => row.kind === "more")).toBe(false);
  });
});
