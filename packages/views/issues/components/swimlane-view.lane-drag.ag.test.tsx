import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, act, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { closestCenter, pointerWithin } from "@dnd-kit/core";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Issue } from "@multica/core/types";
import { SwimLaneView } from "./swimlane-view";
import { IssueContextMenuProvider } from "../actions";
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
// Ancestry walk for tree-ordered parent lanes.
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
const mockSetSwimlaneOrder = mockViewState.setSwimlaneOrder as ReturnType<typeof vi.fn>;
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

// Mock dnd-kit
const mockDragHandlePointerDown = vi.hoisted(() => vi.fn());
let lastOnDragEnd: any = null;
let lastOnDragOver: any = null;
let lastOnDragStart: any = null;
let lastCollisionDetection: any = null;

vi.mock("@dnd-kit/core", () => ({
  DndContext: ({ children, onDragEnd, onDragOver, onDragStart, collisionDetection }: any) => {
    lastOnDragStart = onDragStart;
    lastCollisionDetection = collisionDetection;
    lastOnDragEnd = onDragEnd;
    lastOnDragOver = onDragOver;
    return children;
  },
  DragOverlay: () => null,
  PointerSensor: class {},
  useSensor: () => ({}),
  useSensors: () => [],
  useDroppable: () => ({ setNodeRef: vi.fn(), isOver: false }),
  pointerWithin: vi.fn(),
  closestCenter: vi.fn(),
}));

const mockSortableContext = vi.hoisted(() => vi.fn());
vi.mock("@dnd-kit/sortable", () => ({
  SortableContext: ({ children, items }: any) => {
    mockSortableContext(items);
    return children;
  },
  verticalListSortingStrategy: {},
  // Real arrayMove implementation — the production code uses this both for
  // card reordering and lane reordering, so returning undefined would break
  // every reorder assertion.
  arrayMove: <T,>(arr: T[], from: number, to: number): T[] => {
    const copy = arr.slice();
    const [item] = copy.splice(from, 1);
    copy.splice(to, 0, item!);
    return copy;
  },
  useSortable: () => ({
    attributes: {},
    // AG patch: spy for the drag handle's pointer sensor.
    listeners: { onPointerDown: mockDragHandlePointerDown },
    setNodeRef: vi.fn(),
    transform: null,
    transition: null,
    isDragging: false,
  }),
}));

vi.mock("@dnd-kit/utilities", () => ({
  CSS: { Transform: { toString: () => undefined } },
}));

// Mock react-virtuoso: jsdom has no layout, so the real Virtuoso renders
// nothing (and throws on its resize plumbing). Render every lane inline so the
// virtualized swimlane exposes its lanes/cells/cards to the DOM, matching the
// non-virtualized behavior these tests assert.
// AG patch: the handle's scrollToIndex is captured for the jump-to-lane chip.
const mockScrollToIndex = vi.hoisted(() => vi.fn());
vi.mock("react-virtuoso", async () => {
  const React = await import("react");
  return {
    Virtuoso: React.forwardRef(function VirtuosoMock(
      { data, itemContent, components }: any,
      ref,
    ) {
      React.useImperativeHandle(ref, () => ({ scrollToIndex: mockScrollToIndex }));
      return (
        <div data-testid="virtuoso-mock">
          {(data ?? []).map((item: any, i: number) => (
            <div key={i}>{itemContent(i, item)}</div>
          ))}
          {components?.Footer ? <components.Footer /> : null}
        </div>
      );
    }),
  };
});

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


// Tree-ordered parent lanes (see swimlane-view.tree-lanes.ag.test.tsx). Chain
// A -> B -> C -> D -> E, plus X -> Y -> Z. Ids are single letters so a
// lane's raw id is also its collapse key.
describe("AG patch: drag rules and jump-to-lane chip", () => {
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

  // Settles the ancestry query: a rebuild between dragOver and dragEnd would
  // resync localCells.
  const renderBoard = async (
    issues: Issue[],
    onMoveIssue = vi.fn(),
    visibleStatuses = ["todo"],
  ) => {
    const result = renderWithI18n(
      <SwimLaneView
        issues={issues}
        visibleStatuses={visibleStatuses}
        onMoveIssue={onMoveIssue}
      />,
    );
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    return result;
  };

  const dropCard = (activeId: string, overId: string) => {
    // dragStart takes the drag lock so a lane rebuild can't resync localCells.
    act(() => {
      lastOnDragStart({ active: { id: activeId } });
    });
    act(() => {
      lastOnDragOver({ active: { id: activeId }, over: { id: overId } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: activeId }, over: { id: overId } });
    });
  };
  const dropLane = (activeRaw: string, overRaw: string) =>
    act(() => {
      lastOnDragEnd({
        active: { id: `lane:parent:${activeRaw}` },
        over: { id: `lane:parent:${overRaw}` },
      });
    });
  // The view store mock isn't reactive: a drag start/cancel re-renders it.
  const rerenderBoard = () => {
    act(() => {
      lastOnDragStart({ active: { id: "c" } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "c" }, over: null });
    });
  };
  const laneSortableItems = () => {
    const lists = mockSortableContext.mock.calls
      .map(([items]) => items as string[])
      .filter((items) => items.some((id) => id.startsWith("lane:")));
    return lists[lists.length - 1];
  };
  const jumpChip = (laneTitle: string) =>
    within(laneOf(laneTitle)).getByRole("button", { name: "Jump to lane" });

  afterEach(() => {
    vi.mocked(pointerWithin).mockReset();
    vi.mocked(closestCenter).mockReset();
    mockToggleSwimlaneCollapsed.mockReset();
    mockSetSwimlaneOrder.mockReset();
  });

  it("AG patch: a card dropped into a descendant's lane is refused without an optimistic move", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C, D, E], onMoveIssue);
    await screen.findAllByText("Issue D");

    // B sits in A's lane; C's lane is a child, D's lane a grandchild.
    dropCard("b", "swim:parent:c:todo");
    dropCard("b", "swim:parent:d:todo");

    expect(onMoveIssue).not.toHaveBeenCalled();
    expect(within(laneOf("Issue A")).getByText("Issue B")).toBeInTheDocument();
    expect(within(laneOf("Issue D")).queryByText("Issue B")).toBeNull();
  });

  it("AG patch: the descendant guard holds on dragOver alone, before any drop", async () => {
    await renderBoard([A, B, C, D, E]);
    await screen.findAllByText("Issue D");

    act(() => {
      lastOnDragStart({ active: { id: "b" } });
    });
    act(() => {
      lastOnDragOver({ active: { id: "b" }, over: { id: "swim:parent:d:todo" } });
    });

    expect(within(laneOf("Issue A")).getByText("Issue B")).toBeInTheDocument();
    expect(within(laneOf("Issue D")).queryByText("Issue B")).toBeNull();
  });

  it("AG patch: a drop on a descendant's lane after hovering another lane does not reparent", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C, D, E, X, Y], onMoveIssue);
    await screen.findAllByText("Issue D");

    // B hovers X's lane (allowed, moved optimistically), then C's lane
    // (refused, so B stays in X's), then drops on C's cell.
    act(() => {
      lastOnDragStart({ active: { id: "b" } });
    });
    act(() => {
      lastOnDragOver({ active: { id: "b" }, over: { id: "swim:parent:x:todo" } });
    });
    act(() => {
      lastOnDragOver({ active: { id: "b" }, over: { id: "swim:parent:c:todo" } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "b" }, over: { id: "swim:parent:c:todo" } });
    });

    expect(onMoveIssue).not.toHaveBeenCalled();
  });

  it("AG patch: a normal cross-lane drop still reparents the card", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C, D, E], onMoveIssue);
    await screen.findAllByText("Issue D");

    // D (a card in C's lane) moves up into A's lane, an ancestor's.
    dropCard("d", "swim:parent:a:todo");

    expect(onMoveIssue).toHaveBeenCalledTimes(1);
    expect(onMoveIssue.mock.calls[0]![0]).toBe("d");
    expect(onMoveIssue.mock.calls[0]![1]).toMatchObject({ parent_issue_id: "a" });
  });

  it("AG patch: a lane drops onto a sibling lane, never onto another level or parent, and never reparents", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([C, B, A, Z, Y, X], onMoveIssue);
    await waitFor(() => expect(laneOf("Issue Y")).toBeTruthy());
    expect(laneSortableItems()).toEqual([
      "lane:parent:a",
      "lane:parent:b",
      "lane:parent:x",
      "lane:parent:y",
    ]);

    // Tree order: a, b (under a), x, y (under x).
    dropLane("a", "y");
    dropLane("b", "y");
    dropLane("b", "a");
    expect(mockSetSwimlaneOrder).not.toHaveBeenCalled();

    dropLane("a", "x");
    expect(mockSetSwimlaneOrder).toHaveBeenCalledTimes(1);
    expect(mockSetSwimlaneOrder).toHaveBeenCalledWith(["b", "x", "a", "y"]);
    expect(onMoveIssue).not.toHaveBeenCalled();
  });

  it("AG patch: a lane's sub-lanes stay under it after a reorder", async () => {
    mockSetSwimlaneOrder.mockImplementation((order: string[]) => {
      mockViewState.swimlaneOrders = { ...mockViewState.swimlaneOrders, parent: order };
    });
    const { container } = await renderBoard([C, B, A, Z, Y, X]);
    await waitFor(() => expect(laneOf("Issue Y")).toBeTruthy());
    const tree = () => lanesOf(container).filter((l) => l.title !== "No parent");
    expect(tree()).toEqual([
      { title: "Issue A", depth: 0 },
      { title: "Issue B", depth: 1 },
      { title: "Issue X", depth: 0 },
      { title: "Issue Y", depth: 1 },
    ]);

    dropLane("a", "x");
    rerenderBoard();

    expect(tree()).toEqual([
      { title: "Issue X", depth: 0 },
      { title: "Issue Y", depth: 1 },
      { title: "Issue A", depth: 0 },
      { title: "Issue B", depth: 1 },
    ]);
  });

  it("AG patch: lanes hidden under a collapsed ancestor are not lane sort items and keep their stored slot", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["a"], project: [], assignee: [] };
    mockViewState.swimlaneOrders = { parent: ["a", "b", "x", "y"], project: [], assignee: [] };
    await renderBoard([C, B, A, Z, Y, X]);
    await waitFor(() => expect(laneOf("Issue X")).toBeTruthy());

    expect(laneSortableItems()).toEqual(["lane:parent:a", "lane:parent:x", "lane:parent:y"]);

    dropLane("x", "a");
    expect(mockSetSwimlaneOrder).toHaveBeenCalledWith(["x", "b", "a", "y"]);
  });

  it("AG patch: lane drags only collide with sibling lanes, card drags ignore lanes", async () => {
    await renderBoard([C, B, A, Z, Y, X]);
    await waitFor(() => expect(laneOf("Issue Y")).toBeTruthy());
    const lane = (id: string) => ({ id: `lane:parent:${id}` });
    const detect = (activeId: string) =>
      lastCollisionDetection({ active: { id: activeId } });

    // a and x are roots; b and y sit under them. A lane is its own sibling.
    vi.mocked(pointerWithin).mockReturnValue([lane("b"), lane("x"), lane("y")] as never);
    expect(detect("lane:parent:a")).toEqual([lane("x")]);
    expect(detect("lane:parent:b")).toEqual([lane("b")]);

    // A pointer over a non-sibling lane gives no target, even with a
    // sibling nearby: the lane under the pointer is the one that must move.
    vi.mocked(pointerWithin).mockReturnValue([lane("y")] as never);
    vi.mocked(closestCenter).mockReturnValue([lane("x")] as never);
    expect(detect("lane:parent:a")).toEqual([]);

    // Pointer over no lane: the closest-centre fallback is filtered the same way.
    vi.mocked(pointerWithin).mockReturnValue([{ id: "swim:parent:a:todo" }] as never);
    vi.mocked(closestCenter).mockReturnValue([lane("b"), lane("x")] as never);
    expect(detect("lane:parent:a")).toEqual([lane("x")]);

    vi.mocked(pointerWithin).mockReturnValue([lane("x"), { id: "swim:parent:a:todo" }, { id: "c" }] as never);
    expect(detect("c")).toEqual([{ id: "c" }]);
  });

  it("AG patch: a card heading a lane shows a chip that cannot start a drag or open the issue", async () => {
    await renderBoard([A, B, C, X]);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    // B's card in A's lane heads lane b; C is a plain card inside it.
    const chip = jumpChip("Issue A");
    expect(within(laneOf("Issue B")).queryByRole("button", { name: "Jump to lane" })).toBeNull();
    expect(chip.closest("a")).toBeNull();

    fireEvent.pointerDown(chip);
    expect(mockDragHandlePointerDown).not.toHaveBeenCalled();
    fireEvent.pointerDown(within(laneOf("Issue A")).getByText("Issue B"));
    expect(mockDragHandlePointerDown).toHaveBeenCalled();
  });

  it("AG patch: the No parent lane has no chip, because lane headers never render there as cards", async () => {
    await renderBoard([A, B, C, X]);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    expect(
      within(laneOf("No parent")).queryByRole("button", { name: "Jump to lane" }),
    ).toBeNull();
    // Only B's card heads a lane that is not "No parent".
    expect(screen.getAllByRole("button", { name: "Jump to lane" })).toHaveLength(1);
  });

  it("AG patch: the chip opens a collapsed lane, then scrolls to it below the status header", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["b"], project: [], assignee: [] };
    mockToggleSwimlaneCollapsed.mockImplementation((key: string) => {
      const { parent } = mockViewState.collapsedSwimlanes;
      mockViewState.collapsedSwimlanes = {
        ...mockViewState.collapsedSwimlanes,
        parent: parent.filter((k) => k !== key),
      };
    });
    const heightSpy = vi
      .spyOn(HTMLElement.prototype, "offsetHeight", "get")
      .mockImplementation(function (this: HTMLElement) {
        return this.hasAttribute("data-swimlane-status-header") ? 40 : 0;
      });
    try {
      await renderBoard([A, B, C, X]);
      await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

      fireEvent.click(jumpChip("Issue A"));

      expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledTimes(1);
      expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledWith("b");
      // Lane order: No parent, A, B.
      expect(mockScrollToIndex).toHaveBeenCalledTimes(1);
      expect(mockScrollToIndex).toHaveBeenCalledWith({ index: 2, align: "start", offset: -40 });
    } finally {
      heightSpy.mockRestore();
    }
  });

  it("AG patch: the chip waits for a collapsed lane to open before scrolling", async () => {
    mockViewState.collapsedSwimlanes = { parent: ["b"], project: [], assignee: [] };
    await renderBoard([A, B, C, X]);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    fireEvent.click(jumpChip("Issue A"));
    expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledWith("b");
    expect(mockScrollToIndex).not.toHaveBeenCalled();

    mockViewState.collapsedSwimlanes = { parent: [], project: [], assignee: [] };
    rerenderBoard();
    expect(mockScrollToIndex).toHaveBeenCalledTimes(1);
    expect(mockScrollToIndex).toHaveBeenCalledWith(expect.objectContaining({ index: 2 }));
  });

  it("AG patch: the chip on an open lane scrolls without toggling", async () => {
    await renderBoard([A, B, C, X]);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    fireEvent.click(jumpChip("Issue A"));

    expect(mockToggleSwimlaneCollapsed).not.toHaveBeenCalled();
    expect(mockScrollToIndex).toHaveBeenCalledWith(
      expect.objectContaining({ index: 2, align: "start" }),
    );
  });

  it("AG patch: a card dropped into No parent becomes top-level", async () => {
    const W = mk("w", 9, null);
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C, W], onMoveIssue);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    // B (a card in A's lane, heading lane b) moves out to the top level.
    dropCard("b", "swim:parent:none:todo");

    expect(onMoveIssue).toHaveBeenCalledTimes(1);
    expect(onMoveIssue.mock.calls[0]![0]).toBe("b");
    expect(onMoveIssue.mock.calls[0]![1]).toMatchObject({ parent_issue_id: null });
  });

  it("AG patch: a card moved to another status in its lane keeps its parent", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C], onMoveIssue, ["todo", "in_progress"]);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    // C is a card in B's lane (todo); drop it on B's in_progress cell.
    dropCard("c", "swim:parent:b:in_progress");

    expect(onMoveIssue).toHaveBeenCalledTimes(1);
    expect(onMoveIssue.mock.calls[0]![1]).toMatchObject({
      parent_issue_id: "b",
      status: "in_progress",
    });
  });

  it("AG patch: a card dropped on a lane header does nothing", async () => {
    const onMoveIssue = vi.fn();
    await renderBoard([A, B, C, X], onMoveIssue);
    await waitFor(() => expect(laneOf("Issue B")).toBeTruthy());

    act(() => {
      lastOnDragEnd({ active: { id: "b" }, over: { id: "lane:parent:x" } });
    });

    expect(onMoveIssue).not.toHaveBeenCalled();
    expect(mockSetSwimlaneOrder).not.toHaveBeenCalled();
  });
});
