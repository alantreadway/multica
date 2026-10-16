/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { pruneIssueSurfaceViewStates } from "@multica/core/issues/stores/surface-view-store";
import { useTreeLayoutStore } from "@multica/core/issues/stores/tree-layout-store";
import { useTreeViewStore } from "@multica/core/issues/stores/tree-view-store";
import type {
  Issue,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
  ListIssuesParams,
} from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueDisplayControls } from "../components/issues-header";
import { IssueSurface } from "./issue-surface";
import type { IssueSurfaceProps } from "./types";

// AG patch: the Outline | Chart choice through the real IssueSurface. Choosing
// Chart in the Display menu must mount the chart, choosing Outline must mount
// the outline again, and the layout context the menu reads must come from the
// surface rather than from a hand-built value.

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
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: (state: unknown) => unknown) => {
      const state = { user: { id: "user-1" }, isAuthenticated: true };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: { id: "user-1" }, isAuthenticated: true }) },
  ),
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));
vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Test WS", slug: "test" }),
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
}));
// The outline virtualizes its rows; jsdom has no layout, so render them inline.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count, estimateSize }: any) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        start: index * estimateSize(),
        end: (index + 1) * estimateSize(),
      })),
    getTotalSize: () => count * estimateSize(),
    measureElement: () => {},
  }),
}));
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ data, itemContent }: any) => (
    <div>
      {(data ?? []).map((item: any, i: number) => (
        <div key={i}>{itemContent(i, item)}</div>
      ))}
    </div>
  ),
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
    project_id: "p1",
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as Issue;
}

const never = <T,>() => new Promise<T>(() => {});

const chart = () => document.querySelector('[data-slot="tree-chart"]');
const outlineRows = () => document.querySelectorAll('[data-slot="issue-list-row"]');
const chartNodes = () => document.querySelectorAll('[data-slot="tree-chart-node"]');

describe("AG patch: IssueSurface layout choice", () => {
  let qc: QueryClient;
  let graph: Map<string, Issue>;
  // Requests parked until a test releases them: `issue:<id>` for the detail
  // fetch, `rows:<parent id>` for a branch of children.
  let held: Map<string, Promise<void>>;
  let release: Map<string, () => void>;

  const hold = (key: string) => {
    held.set(
      key,
      new Promise<void>((resolve) => release.set(key, resolve)),
    );
  };
  const letGo = (key: string) => release.get(key)?.();

  const childrenOf = (parent: string | null) =>
    [...graph.values()].filter((issue) => issue.parent_issue_id === parent);

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    graph = new Map();
    held = new Map();
    release = new Map();
    // 1 > 2, 1 > 3
    graph.set("1", makeIssue("1"));
    graph.set("2", makeIssue("2", "1"));
    graph.set("3", makeIssue("3", "1"));
    setApiInstance({
      listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
      listIssues: vi.fn(async (params?: ListIssuesParams) => {
        const issues = (params?.ids ?? []).flatMap((id) => graph.get(id) ?? []);
        return { issues, total: issues.length };
      }),
      getIssue: vi.fn(async (id: string) => {
        await held.get(`issue:${id}`);
        return graph.get(id)!;
      }),
      listIssueTableRows: vi.fn(async (request: IssueTableRowsRequest) => {
        await held.get(`rows:${request.parent_id}`);
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
      }),
      listIssueTableFacets: vi.fn(async () => ({
        query_fingerprint: "fp",
        total: 0,
        facets: [],
      })),
      listIssueTableGroups: vi.fn(() => never()),
      listGroupedIssues: vi.fn(() => never()),
      listProjects: vi.fn(() => never()),
      listLabels: vi.fn(() => never()),
      listProperties: vi.fn(async () => ({ properties: [] })),
      getProject: vi.fn(async (id: string) => ({ id, title: "Roadmap", icon: null })),
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
    document.body.innerHTML = "";
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    vi.restoreAllMocks();
  });

  function mount(scope: IssueSurfaceProps["scope"]) {
    return renderWithI18n(
      <QueryClientProvider client={qc}>
        <IssueSurface
          scope={scope}
          modes={["list", "tree"]}
          batchToolbar="never"
          renderHeader={({ controller }) => (
            <IssueDisplayControls scopedIssues={[]} allowTree={controller.allowTree} />
          )}
        />
      </QueryClientProvider>,
    );
  }

  const openMenu = async (current: "List" | "Tree") =>
    fireEvent.click(await screen.findByRole("button", { name: current }));

  async function chooseTreeMode() {
    await openMenu("List");
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Tree" }));
  }

  it("mounts the outline in tree mode, then the chart when Chart is chosen from the menu", async () => {
    mount({ type: "project", projectId: "p1" });
    await chooseTreeMode();
    await waitFor(() => expect(outlineRows().length).toBeGreaterThan(0));
    expect(chart()).toBeNull();

    await openMenu("Tree");
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Chart" }));

    await waitFor(() => expect(chart()).toBeInTheDocument());
    expect(outlineRows()).toHaveLength(0);
    // Project chart: the project node over the root issue and its children.
    await waitFor(() => expect(chartNodes().length).toBeGreaterThanOrEqual(3));
    expect(screen.getByRole("group", { name: "Issue org chart" })).toBeInTheDocument();
  });

  it("returns to the outline when Outline is chosen again", async () => {
    mount({ type: "project", projectId: "p1" });
    await chooseTreeMode();
    await openMenu("Tree");
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Chart" }));
    await waitFor(() => expect(chart()).toBeInTheDocument());

    await openMenu("Tree");
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Outline" }));

    await waitFor(() => expect(chart()).toBeNull());
    await waitFor(() => expect(outlineRows().length).toBeGreaterThan(0));
  });

  it("keeps Chart disabled on a workspace surface until a node is focused, so the chart never mounts", async () => {
    mount({ type: "workspace" });
    await chooseTreeMode();
    await waitFor(() => expect(outlineRows().length).toBeGreaterThan(0));

    await openMenu("Tree");
    const item = await screen.findByRole("menuitemradio", { name: "Chart" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(item);
    expect(chart()).toBeNull();
    expect(useTreeLayoutStore.getState().layouts).toEqual({});
  });

  describe("auto-fit through the real rows hook", () => {
    // jsdom has no layout: give the chart a size so a fit has somewhere to go.
    beforeEach(() => {
      vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(400);
    });

    const scale = () => {
      const canvas = document.querySelector('[data-slot="tree-chart-canvas"]') as HTMLElement;
      return Number(/scale\(([\d.]+)\)/.exec(canvas.style.transform)![1]);
    };
    const addChildren = (parent: string, count: number) => {
      for (let i = 0; i < count; i++) {
        graph.set(`${parent}-${i}`, makeIssue(`${parent}-${i}`, parent));
      }
    };
    const chooseChart = async () => {
      await chooseTreeMode();
      await waitFor(() => expect(outlineRows().length).toBeGreaterThan(0));
      await openMenu("Tree");
      fireEvent.click(await screen.findByRole("menuitemradio", { name: "Chart" }));
      await waitFor(() => expect(chart()).toBeInTheDocument());
    };

    it("fits a re-rooted chart once the focused issue and its children have arrived, not on the lone placeholder", async () => {
      graph.clear();
      graph.set("1", makeIssue("1"));
      addChildren("1", 12);
      mount({ type: "project", projectId: "p1" });
      await chooseChart();
      await waitFor(() => expect(chartNodes().length).toBeGreaterThanOrEqual(13));

      // The focused issue's detail is a plain fetch: until it lands the chart
      // has one placeholder and nothing to fit.
      hold("issue:1");
      const node = await screen.findByLabelText("MAG-1 Issue 1");
      fireEvent.click(within(node).getByRole("button", { name: "Focus on this issue" }));
      await waitFor(() =>
        expect(document.querySelector('[data-slot="tree-chart-status"]')).toBeInTheDocument(),
      );
      expect(chartNodes()).toHaveLength(0);

      letGo("issue:1");
      await waitFor(() => expect(chartNodes().length).toBeGreaterThanOrEqual(13));
      // Twelve children are far wider than the 600px view: shrunk to fit.
      await waitFor(() => expect(scale()).toBeLessThan(1));
    });

    it("waits for the default-open level to load before fitting, so grandchildren do not spill out of the view", async () => {
      graph.clear();
      graph.set("r1", makeIssue("r1"));
      graph.set("r2", makeIssue("r2"));
      addChildren("r1", 12);
      hold("rows:r1");
      mount({ type: "project", projectId: "p1" });
      await chooseChart();
      await waitFor(() => expect(chartNodes().length).toBeGreaterThanOrEqual(2));
      await waitFor(() =>
        expect(document.querySelector('[data-slot="tree-chart-status"]')).toBeInTheDocument(),
      );
      // Two root issues alone fit at scale 1; fitting now would lock that in.
      expect(scale()).toBe(1);

      letGo("rows:r1");
      await waitFor(() => expect(chartNodes().length).toBeGreaterThanOrEqual(14));
      await waitFor(() => expect(scale()).toBeLessThan(1));
    });
  });
});
