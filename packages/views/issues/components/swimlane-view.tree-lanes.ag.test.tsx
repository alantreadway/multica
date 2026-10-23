import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  Issue,
  IssueTableGroupDescriptor,
} from "@multica/core/types";
import { SwimLaneView } from "./swimlane-view";
import { IssueContextMenuProvider } from "../actions";
import type { IssueGroupBranches } from "../surface/use-issue-group-branches";
import { RESOURCES } from "../../test/i18n";

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

// Mock the API so childrenByParentsOptions doesn't fire real HTTP.
const mockListChildrenByParents = vi.hoisted(() =>
  vi.fn().mockResolvedValue({ issues: [] }),
);
const mockGetAgentTaskSnapshot = vi.hoisted(() =>
  vi.fn().mockResolvedValue([]),
);
// AG patch: ancestry walk for tree-ordered parent lanes.
const mockListIssues = vi.hoisted(() =>
  vi.fn().mockResolvedValue({ issues: [], total: 0 }),
);
vi.mock("@multica/core/api", async (importOriginal) => {
  const api = {
    listChildrenByParents: mockListChildrenByParents,
    getAgentTaskSnapshot: mockGetAgentTaskSnapshot,
    listIssues: mockListIssues,
  };
  return {
    ...(await importOriginal<typeof import("@multica/core/api")>()),
    api,
    getApi: () => api,
    setApiInstance: vi.fn(),
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspaceSlug: () => "acme",
    useRequiredWorkspaceSlug: () => "acme",
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

// useActorName must return a stable reference (production wraps it in
// useMemo); a fresh object per render loops the swimlane's cell effect.
const { mockActorNameResult } = vi.hoisted(() => ({
  mockActorNameResult: {
    getActorName: (_type: string, _id: string) => "Mock Actor",
    getActorInitials: () => "MA",
    getActorAvatarUrl: () => null,
    getMemberName: () => "Mock Member",
    getAgentName: () => "Mock Agent",
    getSquadName: () => "Mock Squad",
  },
}));
vi.mock("@multica/core/workspace/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/hooks")>()),
  useActorName: () => mockActorNameResult,
}));

const mockAuthUser = { id: "user-1", email: "test@test.com", name: "Test User" };
vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: Object.assign(
    (selector?: (state: unknown) => unknown) => {
      const state = { user: mockAuthUser, isAuthenticated: true };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: mockAuthUser, isAuthenticated: true }) },
  ),
}));

vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/issues" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
}));

type SwimlaneGroupingMock = "parent" | "project" | "assignee";

// Mock view store. Lane order and collapsed lanes are mutable per grouping so
// tests can simulate persisted state.
const mockViewState: {
  sortBy: "position";
  sortDirection: "asc";
  cardProperties: Record<string, boolean>;
  swimlaneGrouping: SwimlaneGroupingMock;
  swimlaneOrders: Record<SwimlaneGroupingMock, string[]>;
  collapsedSwimlanes: Record<SwimlaneGroupingMock, string[]>;
  setSwimlaneGrouping: (g: SwimlaneGroupingMock) => void;
  setSwimlaneOrder: (order: string[]) => void;
  toggleSwimlaneCollapsed: (key: string) => void;
  hideStatus: (s: string) => void;
  showStatus: (s: string) => void;
  hiddenStatuses: string[];
  priorityFilters?: string[];
  assigneeFilters?: unknown[];
  includeNoAssignee?: boolean;
  creatorFilters?: unknown[];
  projectFilters?: string[];
  includeNoProject?: boolean;
  labelFilters?: string[];
  propertyFilters?: Record<string, string[]>;
  cardPropertyIds?: string[];
  agentRunningFilter?: boolean;
} = {
  sortBy: "position",
  sortDirection: "asc",
  cardProperties: { priority: true, description: true, assignee: true, dueDate: true, project: true, childProgress: true, labels: true },
  swimlaneGrouping: "parent",
  swimlaneOrders: { parent: [], project: [], assignee: [] },
  collapsedSwimlanes: { parent: [], project: [], assignee: [] },
  setSwimlaneGrouping: vi.fn(),
  setSwimlaneOrder: vi.fn(),
  toggleSwimlaneCollapsed: vi.fn(),
  hideStatus: vi.fn(),
  showStatus: vi.fn(),
  hiddenStatuses: [],
  priorityFilters: [],
  assigneeFilters: [],
  includeNoAssignee: false,
  creatorFilters: [],
  projectFilters: [],
  includeNoProject: false,
  labelFilters: [],
  propertyFilters: {},
  cardPropertyIds: [],
  agentRunningFilter: false,
};
const mockToggleSwimlaneCollapsed = mockViewState.toggleSwimlaneCollapsed as ReturnType<typeof vi.fn>;

vi.mock("@multica/core/issues/stores/view-store-context", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issues/stores/view-store-context")>()),
  ViewStoreProvider: ({ children }: { children: React.ReactNode }) => children,
  useViewStore: (selector?: (state: typeof mockViewState) => unknown) =>
    selector ? selector(mockViewState) : mockViewState,
  useViewStoreApi: () => ({ getState: () => mockViewState, setState: vi.fn(), subscribe: vi.fn() }),
}));

vi.mock("@multica/core/modals", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/modals")>()),
  useModalStore: Object.assign(
    () => ({ open: vi.fn() }),
    { getState: () => ({ open: vi.fn() }) },
  ),
}));

// dnd-kit is inert here: these tests render lanes, they don't drag.
vi.mock("@dnd-kit/core", () => ({
  DndContext: ({ children }: { children: React.ReactNode }) => children,
  DragOverlay: () => null,
  PointerSensor: class {},
  useSensor: () => ({}),
  useSensors: () => [],
  useDroppable: () => ({ setNodeRef: vi.fn(), isOver: false }),
  pointerWithin: vi.fn(),
  closestCenter: vi.fn(),
}));

vi.mock("@dnd-kit/sortable", () => ({
  SortableContext: ({ children }: { children: React.ReactNode }) => children,
  verticalListSortingStrategy: {},
  arrayMove: <T,>(arr: T[], from: number, to: number): T[] => {
    const copy = arr.slice();
    const [item] = copy.splice(from, 1);
    copy.splice(to, 0, item!);
    return copy;
  },
  useSortable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    transform: null,
    transition: null,
    isDragging: false,
  }),
}));

vi.mock("@dnd-kit/utilities", () => ({
  CSS: { Transform: { toString: () => undefined } },
}));

// jsdom has no layout, so the real Virtuoso renders nothing (and throws on
// its resize plumbing). Render every lane inline.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ data, itemContent, components }: any) => (
    <div data-testid="virtuoso-mock">
      {(data ?? []).map((item: any, i: number) => (
        <div key={i}>{itemContent(i, item)}</div>
      ))}
      {components?.Footer ? <components.Footer /> : null}
    </div>
  ),
}));

const mockIssues: Issue[] = [
  {
    id: "parent-1",
    workspace_id: "ws-1",
    number: 1,
    identifier: "PROJ-1",
    title: "Parent Issue 1",
    description: "Parent description",
    status: "todo",
    priority: "high",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 100,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

function makeServerBranches(
  descriptors: IssueTableGroupDescriptor[],
  issues: Issue[],
): IssueGroupBranches {
  const pagination = Object.fromEntries(
    descriptors.flatMap((descriptor) =>
      (descriptor.secondary_groups ?? []).map((cell) => [
        cell.key,
        {
          total: cell.count,
          loaded: issues.filter(
            (issue) =>
              cell.value.kind === "status" &&
              issue.status === cell.value.status,
          ).length,
          hasMore: false,
          isLoading: false,
          isFetching: false,
          isError: false,
          loadMore: vi.fn(),
          retry: vi.fn(),
        },
      ]),
    ),
  );
  return {
    enabled: true,
    descriptors,
    issues,
    pagination,
    total: issues.length,
    isLoading: false,
    isRefreshing: false,
    isError: false,
    hasMoreGroups: false,
    isLoadingMoreGroups: false,
    loadMoreGroups: vi.fn(),
    retryGroups: vi.fn(),
  };
}

function renderWithI18n(ui: React.ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={RESOURCES} locale="en">
        {/* Cards delegate their right-click menu to this surface-level
            provider; IssueSurface mounts it in production. */}
        <IssueContextMenuProvider>{ui}</IssueContextMenuProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

// AG patch: tree-ordered parent lanes. Chain A -> B -> C -> D -> E, plus
// X -> Y -> Z. Ids are single letters so a lane's raw id is also its collapse key.
describe("AG patch: SwimLaneView tree-ordered parent lanes", () => {
  const mk = (
    id: string,
    number: number,
    parent: string | null,
    overrides: Partial<Issue> = {},
  ): Issue => ({
    ...mockIssues[0]!,
    id,
    number,
    identifier: `MAG-${number}`,
    title: `Issue ${id.toUpperCase()}`,
    status: "todo",
    parent_issue_id: parent,
    position: number,
    ...overrides,
  });
  const A = mk("a", 1, null);
  const B = mk("b", 2, "a");
  const C = mk("c", 3, "b");
  const D = mk("d", 4, "c");
  const E = mk("e", 7, "d");
  const X = mk("x", 5, null);
  const Y = mk("y", 6, "x");
  const Z = mk("z", 8, "y");
  const ALL = [A, B, C, D, E, X, Y, Z];

  const parentDescriptor = (
    parent: Issue | null,
  ): IssueTableGroupDescriptor => ({
    key: `parent:${parent?.id ?? "none"}`,
    value: parent
      ? {
          kind: "parent",
          parent_id: parent.id,
          parent: {
            id: parent.id,
            number: parent.number,
            identifier: parent.identifier,
            title: parent.title,
            status: parent.status,
          },
          value_state: "value",
        }
      : { kind: "parent", parent_id: null, parent: null, value_state: "unset" },
    count: 1,
    secondary_groups: [
      {
        key: `compound:${parent?.id ?? "none"}:status:todo`,
        value: { kind: "status", status: "todo" },
        count: 1,
      },
    ],
  });

  const laneTitle = (el: HTMLElement) =>
    el.querySelector(".font-semibold")?.textContent ?? "";
  const laneElements = (root: ParentNode = document) =>
    Array.from(root.querySelectorAll<HTMLElement>("[data-lane-depth]"));
  const lanesOf = (container: HTMLElement) =>
    laneElements(container).map((el) => ({
      title: laneTitle(el),
      depth: Number(el.dataset.laneDepth),
    }));
  const laneOf = (title: string) =>
    laneElements().find((el) => laneTitle(el) === title)!;

  beforeEach(() => {
    vi.clearAllMocks();
    mockViewState.swimlaneGrouping = "parent";
    mockViewState.swimlaneOrders = { parent: [], project: [], assignee: [] };
    mockViewState.collapsedSwimlanes = { parent: [], project: [], assignee: [] };
    mockListChildrenByParents.mockResolvedValue({ issues: [] });
    mockGetAgentTaskSnapshot.mockResolvedValue([]);
    mockListIssues.mockImplementation(
      async (params: { ids?: string[] }) => {
        const issues = ALL.filter((i) => params.ids?.includes(i.id));
        return { issues, total: issues.length };
      },
    );
  });

  const renderServer = (
    parents: (Issue | null)[],
    issues: Issue[],
    extra: Partial<React.ComponentProps<typeof SwimLaneView>> = {},
  ) =>
    renderWithI18n(
      <SwimLaneView
        issues={issues}
        visibleStatuses={["todo"]}
        groupBranches={makeServerBranches(
          parents.map(parentDescriptor),
          issues,
        )}
        onMoveIssue={vi.fn()}
        {...extra}
      />,
    );

  it("AG patch: server path nests a sub-issue lane under its parent lane", async () => {
    const { container } = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() =>
      expect(lanesOf(container)).toEqual([
        { title: "No parent", depth: 0 },
        { title: "Issue A", depth: 0 },
        { title: "Issue B", depth: 1 },
        { title: "Issue X", depth: 0 },
      ]),
    );
  });

  it("AG patch: client path nests lanes from loaded issues without a listIssues call", async () => {
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[C, B, A, Y, X]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(lanesOf(container)).toEqual([
        { title: "No parent", depth: 0 },
        { title: "Issue A", depth: 0 },
        { title: "Issue B", depth: 1 },
        { title: "Issue X", depth: 0 },
      ]),
    );
    expect(mockListIssues).not.toHaveBeenCalled();
  });

  it("AG patch: three levels get contiguous depths 0, 1 and 2", async () => {
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[D, C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(lanesOf(container)).toEqual([
        { title: "No parent", depth: 0 },
        { title: "Issue A", depth: 0 },
        { title: "Issue B", depth: 1 },
        { title: "Issue C", depth: 2 },
      ]),
    );
  });

  it("AG patch: a nested lane header shows its parent as ↳ identifier, with the full path in the tooltip", async () => {
    renderWithI18n(
      <SwimLaneView
        issues={[D, C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    const parent = await screen.findByText("↳ MAG-2");
    expect(parent).toHaveAttribute("title", "MAG-1 > MAG-2 > MAG-3");
    expect(screen.queryByText(/>/)).not.toBeInTheDocument();
    // Top-level lane A keeps today's header: title plus identifier badge.
    const laneA = screen.getByText("Issue A").closest("[data-lane-depth]")!;
    expect(laneA.textContent).not.toContain("↳");
    expect(laneA.textContent).toContain("MAG-1");
  });

  it("AG patch: each root tree shares one band, roots alternate, pinned lanes have none", async () => {
    renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue X")?.dataset.laneBand).toBe("odd"));
    const band = (title: string) => laneOf(title).dataset.laneBand ?? null;
    expect(band("No parent")).toBeNull();
    expect(band("Issue A")).toBe("even");
    expect(band("Issue B")).toBe("even");
    expect(band("Issue X")).toBe("odd");
    // Tint on the odd band only, and the header row stays the lane's first child.
    expect(laneOf("Issue X").className).toContain("bg-muted");
    expect(laneOf("Issue A").className).not.toContain("bg-muted");
    expect(laneOf("No parent").className).not.toContain("-mx-2");
  });

  it("AG patch: nested lane headers have no depth offset and lanes in one tree sit closer", async () => {
    renderServer([null, C, B, A, X], [D, C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue C")?.dataset.laneBand).toBe("even"));
    for (const title of ["Issue A", "Issue B", "Issue C"]) {
      expect(laneOf(title).querySelector("span.border-l")).toBeNull();
    }
    const gap = (title: string) => laneOf(title).parentElement!.className;
    expect(gap("No parent")).toBe("");
    expect(gap("Issue A")).toBe("pt-4");
    expect(gap("Issue B")).toBe("pt-2");
    expect(gap("Issue C")).toBe("pt-2");
    expect(gap("Issue X")).toBe("pt-4");
  });

  it("AG patch: collapsing a root shrinks its band to the header row", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["a"], project: [], assignee: [] };
    const { container } = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(lanesOf(container)).toHaveLength(3));
    expect(laneOf("Issue A").dataset.laneBand).toBe("even");
    expect(laneOf("Issue A").querySelector('[class*="grid"], [style*="grid"]')).toBeNull();
    expect(laneOf("Issue X").dataset.laneBand).toBe("odd");
  });

  it("AG patch: non-parent groupings render no band", async () => {
    mockViewState.swimlaneGrouping = "project";
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[A, B]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => expect(laneElements(container).length).toBeGreaterThan(0));
    expect(container.querySelector("[data-lane-band]")).toBeNull();
    expect(container.querySelector(".-mx-2")).toBeNull();
  });

  it("AG patch: three roots alternate even / odd / even, ignoring the pinned lane ahead of them", async () => {
    const W = mk("w", 9, null);
    renderServer([null, W, B, A, X], [W, C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue B")?.dataset.laneBand).toBe("odd"));
    const bands = Object.fromEntries(
      laneElements().map((el) => [laneTitle(el), el.dataset.laneBand ?? null]),
    );
    expect(bands).toEqual({
      "No parent": null,
      "Issue W": "even",
      "Issue A": "odd",
      "Issue B": "odd",
      "Issue X": "even",
    });
    expect(laneOf("Issue A").dataset.laneBand).toBe(laneOf("Issue B").dataset.laneBand);
  });

  it("AG patch: every lane header sits at the same x whatever its depth", async () => {
    renderServer([null, C, B, A], [D, C, B, A]);

    await waitFor(() => expect(laneOf("Issue C")?.dataset.laneBand).toBe("even"));
    const headerOf = (title: string) =>
      laneOf(title).firstElementChild as HTMLElement;
    const chevronOf = (title: string) =>
      headerOf(title).querySelector("button svg") as SVGElement;
    for (const title of ["Issue A", "Issue B", "Issue C"]) {
      // The first child of the lane is the header row, with no leading rail or indent.
      expect(headerOf(title).className).toBe(headerOf("Issue A").className);
      expect(headerOf(title).querySelector(".border-l")).toBeNull();
      expect(headerOf(title).getAttribute("style")).toBeNull();
      // Grip, then the collapse button whose first icon is the chevron.
      expect(headerOf(title).children[0]?.tagName.toLowerCase()).toBe("svg");
      expect(headerOf(title).children[1]?.tagName.toLowerCase()).toBe("button");
      expect(chevronOf(title)).toBe(
        headerOf(title).children[1]!.firstElementChild,
      );
    }
    // Banding pads and pulls back identically for every band, so x is shared.
    expect(laneOf("Issue A").className).toContain("-mx-2");
    expect(laneOf("Issue C").className).toContain("-mx-2");
  });

  it("AG patch: the band tints the header row and gaps, and every cell sits on an opaque underlay", async () => {
    const { container } = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue X")?.dataset.laneBand).toBe("odd"));
    const lane = laneOf("Issue X");
    // Header row, then the cells grid, both inside the band element.
    expect(lane.children).toHaveLength(2);
    expect(lane.children[0]!.querySelector("button")).not.toBeNull();
    // The band attribute lives on lane elements only.
    expect(container.querySelectorAll("[data-lane-band]")).toHaveLength(
      laneElements(container).filter((el) => el.dataset.laneBand).length,
    );
    // The status tints are translucent, so the band would show through them.
    // Each cell therefore sits on an opaque underlay that hides the band.
    const cells = lane.children[1]!.children;
    expect(cells.length).toBeGreaterThan(0);
    for (const cell of Array.from(cells)) {
      expect(cell.hasAttribute("data-lane-band")).toBe(false);
      expect(cell.hasAttribute("data-swimlane-cell")).toBe(true);
      expect(cell.className).toMatch(/(^|\s)bg-background(\s|$)/);
      expect(cell.className).not.toContain("bg-muted/60");
    }
    // The band is inside the gap wrapper, so bands of different roots never touch.
    expect(lane.parentElement!.className).toMatch(/\bpt-[24]\b/);
  });

  it("AG patch: a root that was collapsed gets its cells and sub-lanes back once expanded, band unchanged", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["a"], project: [], assignee: [] };
    const first = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(lanesOf(first.container)).toHaveLength(3));
    expect(laneOf("Issue A").children).toHaveLength(1);
    expect(laneOf("Issue A").dataset.laneBand).toBe("even");

    mockViewState.collapsedSwimlanes = { parent: [], project: [], assignee: [] };
    first.unmount();
    const view = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue B")?.dataset.laneBand).toBe("even"));
    expect(lanesOf(view.container)).toHaveLength(4);
    expect(laneOf("Issue A").children).toHaveLength(2);
    expect(laneOf("Issue A").dataset.laneBand).toBe("even");
    expect(laneOf("Issue B").dataset.laneBand).toBe("even");
    expect(laneOf("Issue X").dataset.laneBand).toBe("odd");
  });

  it("AG patch: a sub-lane shows only its immediate parent, deeper lanes still show the full path in the tooltip", async () => {
    renderServer([null, C, B, A], [D, C, B, A]);

    const labels = await screen.findAllByText(/^↳ /);
    const byText = Object.fromEntries(
      labels.map((el) => [el.textContent, el.getAttribute("title")]),
    );
    expect(byText).toEqual({
      "↳ MAG-1": "MAG-1 > MAG-2",
      "↳ MAG-2": "MAG-1 > MAG-2 > MAG-3",
    });
    // Root lane A has no parent label.
    expect(laneOf("Issue A").textContent).not.toContain("↳");
  });

  it.each(["project", "assignee"] as const)(
    "AG patch: %s grouping keeps today's flat gap and no band",
    async (grouping) => {
      mockViewState.swimlaneGrouping = grouping;
      const { container } = renderWithI18n(
        <SwimLaneView
          issues={[A, X]}
          visibleStatuses={["todo"]}
          onMoveIssue={vi.fn()}
        />,
      );

      await waitFor(() =>
        expect(laneElements(container).length).toBeGreaterThan(0),
      );
      expect(container.querySelector("[data-lane-band]")).toBeNull();
      expect(container.querySelector(".-mx-2")).toBeNull();
      expect(container.querySelector(".pt-2")).toBeNull();
    },
  );

  it("AG patch: ancestors without a lane of their own still appear in the path", async () => {
    // Only D is visible, so only C gets a lane; B and A exist in the
    // unfiltered set and must still show up in C's header path.
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[D]}
        unfilteredIssues={[D, C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(await screen.findByText("↳ MAG-2")).toHaveAttribute(
      "title",
      "MAG-1 > MAG-2 > MAG-3",
    );
    expect(lanesOf(container)).toEqual([
      { title: "No parent", depth: 0 },
      { title: "Issue C", depth: 0 },
    ]);
  });

  it("AG patch: collapsing a lane hides its sub-lanes and toggles by raw parent id", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["a"], project: [], assignee: [] };
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[D, C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(lanesOf(container).map((l) => l.title)).toEqual([
        "No parent",
        "Issue A",
      ]),
    );
    expect(screen.getByText("+2 sub-lanes")).toBeInTheDocument();

    const laneA = screen.getByText("Issue A").closest("[data-lane-depth]")!;
    fireEvent.click(
      laneA.querySelector<HTMLElement>('button[aria-label="Toggle lane"]')!,
    );
    expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledWith("a");
  });

  it("AG patch: the ancestry walk sends one sorted batch per level and stops at roots", async () => {
    const { container } = renderServer([null, C, X], [D, C, Y, X]);

    await waitFor(() =>
      expect(lanesOf(container).map((l) => l.depth)).toEqual([0, 0, 0]),
    );
    await waitFor(() =>
      expect(mockListIssues.mock.calls.map((c) => c[0].ids)).toEqual([
        ["c", "x"],
        ["b"],
        ["a"],
      ]),
    );
  });

  it("AG patch: no ancestry request for a non-parent grouping", async () => {
    mockViewState.swimlaneGrouping = "project";
    renderWithI18n(
      <SwimLaneView
        issues={[C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await act(async () => {});
    expect(mockListIssues).not.toHaveBeenCalled();
  });

  it("AG patch: the client path asks only for ancestors that are not loaded", async () => {
    // B and A are not in the loaded set; C's lane needs them from the query.
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[D, C]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(mockListIssues.mock.calls.map((c) => c[0].ids)).toEqual([
        ["b"],
        ["a"],
      ]),
    );
    expect(await screen.findByText("↳ MAG-2")).toBeInTheDocument();
    expect(lanesOf(container).map((l) => l.title)).toContain("Issue C");
  });

  it("AG patch: lanes keep today's order, with no spinner, until ancestry resolves", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => {
      release = r;
    });
    mockListIssues.mockImplementation(async (params: { ids?: string[] }) => {
      await gate;
      const issues = ALL.filter((i) => params.ids?.includes(i.id));
      return { issues, total: issues.length };
    });

    const { container } = renderServer([null, B, A, X], [C, B, Y, A, X]);

    // Server order, flat: B is not nested yet.
    await waitFor(() =>
      expect(lanesOf(container)).toEqual([
        { title: "No parent", depth: 0 },
        { title: "Issue B", depth: 0 },
        { title: "Issue A", depth: 0 },
        { title: "Issue X", depth: 0 },
      ]),
    );
    expect(container.querySelector('[role="status"], .animate-spin')).toBeNull();

    await act(async () => {
      release();
    });
    await waitFor(() =>
      expect(lanesOf(container).map((l) => l.title)).toEqual([
        "No parent",
        "Issue A",
        "Issue B",
        "Issue X",
      ]),
    );
  });

  it("AG patch: collapsing a mid-level lane hides only the lanes below it", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["b"], project: [], assignee: [] };
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[D, C, B, A]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(lanesOf(container).map((l) => l.title)).toEqual([
        "No parent",
        "Issue A",
        "Issue B",
      ]),
    );
    expect(screen.getByText("+1 sub-lane")).toBeInTheDocument();
  });

  it("AG patch: a server-path lane header shows the path once ancestry resolves", async () => {
    renderServer([null, C, B, A], [D, C, B, A]);

    expect(await screen.findByText("↳ MAG-2")).toHaveAttribute(
      "title",
      "MAG-1 > MAG-2 > MAG-3",
    );
    // Header path text is clipped with an ellipsis, never wrapped.
    expect(screen.getByText("↳ MAG-2").className).toContain("truncate");
  });

  it("AG patch: server path sends no ancestry request for a non-parent grouping", async () => {
    mockViewState.swimlaneGrouping = "project";
    renderServer([null, B, A], [C, B, A]);

    await act(async () => {});
    expect(mockListIssues).not.toHaveBeenCalled();
  });

  it("AG patch: a parent cycle in the data still renders every lane", async () => {
    // A.parent = B and B.parent = A is corrupt data, but must not hang.
    const cyclicA = { ...A, parent_issue_id: "b" };
    const { container } = renderWithI18n(
      <SwimLaneView
        issues={[cyclicA, B]}
        visibleStatuses={["todo"]}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(lanesOf(container).map((l) => l.title).sort()).toEqual(
        ["Issue A", "Issue B", "No parent"].sort(),
      ),
    );
  });
});
