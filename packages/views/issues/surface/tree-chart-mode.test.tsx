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
import { useTreeLayoutStore } from "@multica/core/issues/stores/tree-layout-store";
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
import type { IssueSurfaceProps } from "./types";

// AG patch: the org-chart layout of tree mode, driven through the surface
// controller the way the page does, against an in-memory issue graph. Covers
// where the chart is available, the collapse-by-default depth, per-surface
// persistence and that the outline's focus and expand state survive the chart.

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

describe("AG patch: tree chart layout", () => {
  let qc: QueryClient;
  let graph: Map<string, Issue>;
  let listIssueTableRows: ReturnType<typeof vi.fn>;

  const childrenOf = (parent: string | null) =>
    [...graph.values()].filter((issue) => issue.parent_issue_id === parent);

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

  const workspaceScope: IssueSurfaceProps["scope"] = { type: "workspace" };
  const projectScope: IssueSurfaceProps["scope"] = { type: "project", projectId: "p1" };
  function useController(scope: IssueSurfaceProps["scope"]) {
    return useIssueSurfaceController({ scope, modes: ["list", "table", "tree"] });
  }
  const mount = (scope: IssueSurfaceProps["scope"], surfaceKey: string) => {
    getIssueSurfaceViewStore(surfaceKey).getState().setViewMode("tree");
    return renderHook(() => useController(scope), { wrapper: wrapper(surfaceKey) });
  };
  const issueRows = (result: { current: ReturnType<typeof useController> }) =>
    result.current.tree.rows.filter((row): row is TreeIssueRow => row.kind === "issue");
  const ids = (result: { current: ReturnType<typeof useController> }) =>
    issueRows(result).map((row) => row.issue.id);

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    graph = new Map();
    listIssueTableRows = vi.fn(async (request: IssueTableRowsRequest) => {
      const all = childrenOf(request.parent_id);
      return {
        query_fingerprint: "fp",
        group_key: null,
        parent_id: request.parent_id,
        total: all.length,
        rows: all.map((issue) => ({
          issue,
          direct_child_count: childrenOf(issue.id).length,
        })),
        branch_total: all.length,
        next_cursor: null,
      } satisfies IssueTableRowsResponse;
    });
    setApiInstance({
      listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
      listIssues: vi.fn(async (params?: ListIssuesParams) => {
        const issues = (params?.ids ?? []).flatMap((id) => graph.get(id) ?? []);
        return { issues, total: issues.length };
      }),
      getIssue: vi.fn(async (id: string) => graph.get(id)!),
      listIssueTableRows,
      listIssueTableFacets: vi.fn(async () => ({
        query_fingerprint: "fp",
        total: 0,
        facets: [],
      })),
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
    useTreeLayoutStore.setState({ layouts: {} });
  });

  afterEach(() => {
    cleanup();
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    vi.restoreAllMocks();
  });

  // 1 > 2 > 3 > 4 > 5, plus a sibling 6 under 1.
  const chain = () => {
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "2"));
    graph.set("4", makeIssue("4", "3"));
    graph.set("5", makeIssue("5", "4"));
    graph.set("6", makeIssue("6", "1"));
  };

  describe("availability", () => {
    it("is off on the workspace and My Issues surfaces until a node is focused", async () => {
      chain();
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));

      expect(result.current.tree.chartAvailability).toBeNull();
      // A stored chart choice does not apply while it is unavailable.
      act(() => result.current.tree.setLayout("chart"));
      expect(useTreeLayoutStore.getState().layouts).toEqual({});
      expect(result.current.tree.layout).toBe("outline");
    });

    it("is off on My Issues until a node is focused, then on", async () => {
      chain();
      const myScope: IssueSurfaceProps["scope"] = {
        type: "my",
        userId: "user-1",
        relation: "assigned",
      } as IssueSurfaceProps["scope"];
      const { result } = mount(myScope, "my:assigned");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      expect(result.current.tree.chartAvailability).toBeNull();
      act(() => result.current.tree.setLayout("chart"));
      expect(result.current.tree.layout).toBe("outline");

      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      expect(result.current.tree.chartAvailability).toBe("focus");
    });

    it("is on from a focused node, anywhere", async () => {
      chain();
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));

      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      expect(result.current.tree.chartAvailability).toBe("focus");
      act(() => result.current.tree.setLayout("chart"));
      expect(result.current.tree.layout).toBe("chart");

      // Clearing focus takes the workspace surface back to the outline.
      act(() => result.current.tree.clearFocus());
      await waitFor(() => expect(result.current.tree.focusId).toBeNull());
      expect(result.current.tree.chartAvailability).toBeNull();
      expect(result.current.tree.layout).toBe("outline");
    });

    it("is on for a project page without focusing anything", async () => {
      chain();
      const { result } = mount(projectScope, "project:p1");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));

      expect(result.current.tree.chartAvailability).toBe("project");
      act(() => result.current.tree.setLayout("chart"));
      expect(result.current.tree.layout).toBe("chart");
      expect(result.current.tree.chartDepthOffset).toBe(1);
    });
  });

  describe("collapse by default below depth 2", () => {
    it("draws a focused root three levels deep, with the third level collapsed", async () => {
      chain();
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      act(() => result.current.tree.setLayout("chart"));

      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));
      expect(issueRows(result).find((r) => r.issue.id === "3")).toMatchObject({
        depth: 2,
        childCount: 1,
        expanded: false,
      });
      // Nothing below the collapsed node was requested.
      const asked = listIssueTableRows.mock.calls.map(
        (call) => (call[0] as IssueTableRowsRequest).parent_id,
      );
      expect(asked).not.toContain("3");
    });

    it("opens only the first 20 children of a wide branch", async () => {
      graph.set("1", makeIssue("1"));
      for (let i = 0; i < 25; i += 1) {
        const id = `c${String(i).padStart(2, "0")}`;
        graph.set(id, makeIssue(id, "1"));
        graph.set(`${id}-g`, makeIssue(`${id}-g`, id));
      }
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      act(() => result.current.tree.setLayout("chart"));

      // 1 + 25 children + a grandchild under each of the first 20.
      await waitFor(() => expect(issueRows(result)).toHaveLength(46));
      const children = issueRows(result).filter((r) => r.depth === 1);
      expect(children).toHaveLength(25);
      expect(children.filter((r) => r.expanded).map((r) => r.issue.id)).toEqual(
        children.slice(0, 20).map((r) => r.issue.id),
      );
      expect(children.slice(20).every((r) => !r.expanded && r.childCount === 1)).toBe(true);
      const asked = listIssueTableRows.mock.calls.map(
        (call) => (call[0] as IssueTableRowsRequest).parent_id,
      );
      expect(asked).toContain("c19");
      expect(asked).not.toContain("c20");
    });

    it("hangs a project's root issues under the virtual node, one level less deep", async () => {
      chain();
      graph.set("7", makeIssue("7"));
      const { result } = mount(projectScope, "project:p1");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.setLayout("chart"));

      // Roots are chart depth 1 (open), their children depth 2 (collapsed).
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6", "7"]));
      expect(issueRows(result).find((r) => r.issue.id === "2")).toMatchObject({
        expanded: false,
        childCount: 1,
      });
    });

    it("opens a node on toggle, closes it again, and resets or collapses all", async () => {
      chain();
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      act(() => result.current.tree.setLayout("chart"));
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));

      act(() => result.current.tree.toggle("3"));
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "4", "6"]));

      act(() => result.current.tree.toggle("2"));
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6"]));

      act(() => result.current.tree.resetChart());
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));

      act(() => result.current.tree.collapseAll());
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6"]));

      // "Show next level" pushes the open depth one step at a time.
      act(() => result.current.tree.expandAll());
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));
      act(() => result.current.tree.expandAll());
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "4", "6"]));
    });
  });

  describe("fetch bound above the node cap", () => {
    it("opens no further branch once the loaded chart is over its cap", async () => {
      // 10 roots with 20 children each: 210 issues on screen at the default
      // depth, every child with a grandchild waiting a level down.
      for (let r = 0; r < 10; r += 1) {
        graph.set(`r${r}`, makeIssue(`r${r}`));
        for (let c = 0; c < 20; c += 1) {
          graph.set(`c${r}-${c}`, makeIssue(`c${r}-${c}`, `r${r}`));
          graph.set(`g${r}-${c}`, makeIssue(`g${r}-${c}`, `c${r}-${c}`));
        }
      }
      const { result } = mount(projectScope, "project:p1");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.setLayout("chart"));
      await waitFor(() => expect(issueRows(result)).toHaveLength(210));

      // "Show next level" would open every child; over the cap it fetches none.
      act(() => result.current.tree.expandAll());
      act(() => result.current.tree.expandAll());
      await new Promise((resolve) => setTimeout(resolve, 50));
      const asked = listIssueTableRows.mock.calls.map(
        (call) => (call[0] as IssueTableRowsRequest).parent_id,
      );
      expect(asked.filter((id) => id?.startsWith("c"))).toEqual([]);
      expect(issueRows(result)).toHaveLength(210);
    });
  });

  describe("outline state", () => {
    it("keeps focus and the outline's expanded nodes across a trip through the chart", async () => {
      chain();
      useTreeViewStore.setState({ expanded: { workspace: [], "workspace:all": ["1"] } });
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6"]));

      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      act(() => result.current.tree.setLayout("chart"));
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));
      act(() => result.current.tree.toggle("3"));
      await waitFor(() => expect(ids(result)).toContain("4"));

      act(() => result.current.tree.setLayout("outline"));
      expect(result.current.tree.focusId).toBe("1");
      // The chart's toggle did not leak into the outline's expand state.
      expect(useTreeViewStore.getState().expanded["workspace:all"]).toEqual(["1"]);
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6"]));
    });

    it("starts a new root from the default depth again", async () => {
      chain();
      const { result } = mount(workspaceScope, "workspace");
      await waitFor(() => expect(result.current.tree.isLoading).toBe(false));
      act(() => result.current.tree.focus("1"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("1"));
      act(() => result.current.tree.setLayout("chart"));
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "3", "6"]));
      act(() => result.current.tree.collapseAll());
      await waitFor(() => expect(ids(result)).toEqual(["1", "2", "6"]));

      act(() => result.current.tree.focus("2"));
      await waitFor(() => expect(result.current.tree.focusIssue?.id).toBe("2"));
      await waitFor(() => expect(ids(result)).toEqual(["2", "3", "4"]));
    });
  });

  describe("persistence", () => {
    it("remembers the layout per surface", async () => {
      chain();
      const project = mount(projectScope, "project:p1");
      await waitFor(() => expect(project.result.current.tree.isLoading).toBe(false));
      act(() => project.result.current.tree.setLayout("chart"));
      expect(useTreeLayoutStore.getState().layouts).toEqual({ "project:p1": "chart" });
      project.unmount();

      // Same surface later: still the chart. Another surface: the outline.
      const again = mount(projectScope, "project:p1");
      await waitFor(() => expect(again.result.current.tree.isLoading).toBe(false));
      expect(again.result.current.tree.layout).toBe("chart");
      again.unmount();

      const other = mount(workspaceScope, "workspace");
      await waitFor(() => expect(other.result.current.tree.isLoading).toBe(false));
      expect(other.result.current.tree.layout).toBe("outline");
    });
  });
});
