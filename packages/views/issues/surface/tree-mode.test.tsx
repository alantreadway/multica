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
  let graph: Map<string, Issue>;
  let visibleIds: Set<string> | null;
  let listIssueTableRows: ReturnType<typeof vi.fn>;
  let listIssues: ReturnType<typeof vi.fn>;
  let getIssue: ReturnType<typeof vi.fn>;

  const childrenOf = (parent: string | null) =>
    [...graph.values()].filter((issue) => {
      if (visibleIds && !visibleIds.has(issue.id)) return false;
      const parentId = issue.parent_issue_id;
      if (parent === null) {
        return parentId === null || (visibleIds !== null && !visibleIds.has(parentId));
      }
      return parentId === parent;
    });
  const directChildCount = (id: string) =>
    [...graph.values()].filter(
      (issue) => issue.parent_issue_id === id && (!visibleIds || visibleIds.has(issue.id)),
    ).length;

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

  function useController(modes: Array<"list" | "table" | "tree"> = ["list", "table", "tree"]) {
    return useIssueSurfaceController({ scope: { type: "workspace" }, modes });
  }

  const treeRows = (result: { current: ReturnType<typeof useController> }) =>
    result.current.tree.rows.filter((row): row is TreeIssueRow => row.kind === "issue");

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    graph = new Map();
    visibleIds = null;
    listIssueTableRows = vi.fn(async (request: IssueTableRowsRequest) => {
      const all = childrenOf(request.parent_id);
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
          direct_child_count: directChildCount(issue.id),
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
    setApiInstance({
      listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
      listIssues,
      getIssue,
      listIssueTableRows,
      listIssueTableFacets: async () => ({
        query_fingerprint: "fp",
        total: 0,
        facets: [],
      }),
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
    getIssueSurfaceViewStore("workspace").getState().setViewMode("tree");

    const { result } = renderHook(useController, { wrapper: wrapper("workspace") });
    await waitFor(() =>
      expect(treeRows(result)[0]?.ancestorPath.map((a) => a.identifier)).toEqual([
        "MAG-1",
        "MAG-2",
      ]),
    );
    expect(treeRows(result)[0]!.issue.id).toBe("3");

    visibleIds = null;
    act(() => result.current.tree.focus("2"));
    await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("2"));
    await waitFor(() =>
      expect(treeRows(result).map((r) => [r.issue.id, r.depth])).toEqual([
        ["2", 0],
        ["3", 1],
      ]),
    );
    expect(result.current.tree.focusPath.map((a) => a.identifier)).toEqual(["MAG-1"]);
    expect(listIssueTableRows).toHaveBeenLastCalledWith(
      expect.objectContaining({ parent_id: "2" }),
    );

    act(() => result.current.tree.clearFocus());
    await waitFor(() => expect(result.current.tree.focusId).toBeNull());
    await waitFor(() => expect(treeRows(result)[0]?.issue.id).toBe("1"));
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
});
