import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, act, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { closestCenter, pointerWithin } from "@dnd-kit/core";
import { SwimLaneView } from "./swimlane-view";
import { IssueContextMenuProvider } from "../actions";
import { ScrollRestorationProvider } from "../../platform";
import type {
  Issue,
  IssueTableGroupDescriptor,
} from "@multica/core/types";
import type { IssueGroupBranches } from "../surface/use-issue-group-branches";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

// Mock hooks
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// Mock the API so childrenByParentsOptions doesn't fire real HTTP.
// Individual tests can override listChildrenByParents via mockResolvedValueOnce.
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
vi.mock("@multica/core/api", () => ({
  api: {
    listChildrenByParents: mockListChildrenByParents,
    getAgentTaskSnapshot: mockGetAgentTaskSnapshot,
    listIssues: mockListIssues,
  },
  getApi: () => ({
    listChildrenByParents: mockListChildrenByParents,
    getAgentTaskSnapshot: mockGetAgentTaskSnapshot,
    listIssues: mockListIssues,
  }),
  setApiInstance: vi.fn(),
}));

// Mock paths
vi.mock("@multica/core/paths", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/paths")>(
    "@multica/core/paths",
  );
  return {
    ...actual,
    useWorkspaceSlug: () => "acme",
    useRequiredWorkspaceSlug: () => "acme",
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

// Stub backend-bound hooks that the swimlane invokes for assignee groupings.
// The hook MUST return a stable reference each call
// — production `useActorName` wraps its returns in `useMemo`, and the
// swimlane feeds the result into a `useMemo(..., [getActorName, ...])`
// that then drives a `useEffect(setLocalCells, [cells])` chain. A fresh
// object per render therefore loops the effect indefinitely.
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
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => mockActorNameResult,
}));

// Mock @multica/core/auth
const mockAuthUser = { id: "user-1", email: "test@test.com", name: "Test User" };
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: any) => {
      const state = { user: mockAuthUser, isAuthenticated: true };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ user: mockAuthUser, isAuthenticated: true }) },
  ),
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));

// Mock navigation
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href, ...props }: any) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/issues" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
  NavigationProvider: ({ children }: { children: React.ReactNode }) => children,
}));

// Mock issue config
// Use the real status configuration so category fixtures cannot drift.

type SwimlaneGroupingMock = "parent" | "project" | "assignee";

// Mock view store. The lane order and collapsed-lane fields are mutable
// records on the captured object so tests can simulate persisted state
// (per grouping) and assert that `setSwimlaneOrder` was called by drag-end
// handlers. The store actions operate on `swimlaneGrouping` — tests that
// flip grouping must set both `swimlaneGrouping` and the matching slice
// in `swimlaneOrders` / `collapsedSwimlanes`.
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
  priorityFilters?: string[];
  assigneeFilters?: any[];
  includeNoAssignee?: boolean;
  creatorFilters?: any[];
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

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  ViewStoreProvider: ({ children }: { children: React.ReactNode }) => children,
  useViewStore: (selector?: any) => (selector ? selector(mockViewState) : mockViewState),
  useViewStoreApi: () => ({ getState: () => mockViewState, setState: vi.fn(), subscribe: vi.fn() }),
}));

// Mock modal store
const mockOpenModal = vi.fn();
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(
    () => ({ open: mockOpenModal }),
    { getState: () => ({ open: mockOpenModal }) },
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
  {
    id: "child-1",
    workspace_id: "ws-1",
    number: 2,
    identifier: "PROJ-2",
    title: "Child Issue 1",
    description: "Child description",
    status: "in_progress",
    priority: "medium",
    assignee_type: "member",
    assignee_id: "user-1",
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: "parent-1",
    project_id: null,
    position: 200,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "orphan-1",
    workspace_id: "ws-1",
    number: 3,
    identifier: "PROJ-3",
    title: "Orphan Issue 1",
    description: "No parent",
    status: "backlog",
    priority: "low",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 300,
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
      <I18nProvider resources={TEST_RESOURCES} locale="en">
        {/* Cards delegate their right-click menu to this surface-level
            provider; IssueSurface mounts it in production. */}
        <IssueContextMenuProvider>{ui}</IssueContextMenuProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("SwimLaneView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockViewState.swimlaneGrouping = "parent";
    mockViewState.swimlaneOrders = { parent: [], project: [], assignee: [] };
    mockViewState.collapsedSwimlanes = { parent: [], project: [], assignee: [] };
    mockViewState.priorityFilters = [];
    mockViewState.assigneeFilters = [];
    mockViewState.includeNoAssignee = false;
    mockViewState.creatorFilters = [];
    mockViewState.projectFilters = [];
    mockViewState.includeNoProject = false;
    mockViewState.labelFilters = [];
    mockViewState.agentRunningFilter = false;
    mockListChildrenByParents.mockResolvedValue({ issues: [] });
    mockGetAgentTaskSnapshot.mockResolvedValue([]);
  });

  it("renders status columns as headers", () => {
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getByText("Todo")).toBeInTheDocument();
    expect(screen.getByText("Todo")).toBeInTheDocument();
    expect(screen.getByText("In Progress")).toBeInTheDocument();
  });

  // MUL-4290: `cancelled` is a first-class default status. Status columns come
  // from `visibleStatuses` in ALL_STATUSES order, so the Cancelled column
  // renders by default (ordered last) and is only dropped when the status
  // filter narrows to a subset that excludes it.
  const cancelledOrphan: Issue = {
    id: "cancelled-orphan",
    workspace_id: "ws-1",
    number: 9,
    identifier: "PROJ-9",
    title: "Cancelled Orphan",
    description: "A cancelled orphan",
    status: "cancelled",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 400,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("renders a Cancelled column and its cards by default", () => {
    renderWithI18n(
      // No visibleStatuses prop → default (ALL_STATUSES) includes cancelled.
      <SwimLaneView
        issues={[...mockIssues, cancelledOrphan]}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(screen.getByText("Cancelled Orphan")).toBeInTheDocument();
  });

  // Cells are CATEGORIES, cards carry concrete status KEYS. Keying the cell by
  // the raw key gave a custom status a cell that does not exist, and the card
  // fell out of the grid entirely (MUL-6409).
  const customStatusOrphan: Issue = {
    ...cancelledOrphan,
    id: "custom-orphan",
    number: 10,
    identifier: "PROJ-10",
    title: "Awaiting Reporter",
    status: "awaiting_response",
    status_category: "started",
  };

  it("renders a custom-status card in its own status column", () => {
    renderWithI18n(
      <SwimLaneView
        issues={[...mockIssues, customStatusOrphan]}
        visibleStatuses={["todo", "in_progress", "awaiting_response"]}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getByText("Awaiting Reporter")).toBeInTheDocument();
  });

  it("omits the Cancelled column when the status filter narrows to a subset without cancelled", () => {
    renderWithI18n(
      <SwimLaneView
        issues={[...mockIssues, cancelledOrphan]}
        visibleStatuses={[
          "todo",
          "in_progress",
          "done",
        ]}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.queryByText("Cancelled")).not.toBeInTheDocument();
    expect(screen.queryByText("Cancelled Orphan")).not.toBeInTheDocument();
  });

  it("renders parent swimlanes and orphans section", () => {
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getAllByText("No parent").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Parent Issue 1").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("PROJ-1").length).toBeGreaterThanOrEqual(1);
  });

  it("renders cards in their corresponding cell", () => {
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getByText("Orphan Issue 1")).toBeInTheDocument();

    // parent-1 is promoted to a lane header — must not also appear as a card.
    const parentTitleMatches = screen.getAllByText("Parent Issue 1");
    expect(parentTitleMatches).toHaveLength(1);
    expect(parentTitleMatches[0]!.closest("div")?.textContent).toContain("PROJ-1");

    expect(screen.getByText("Child Issue 1")).toBeInTheDocument();
  });

  it("calls the create callback when add button is clicked", () => {
    const onCreateIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
        onCreateIssue={onCreateIssue}
      />,
    );

    const addButtons = screen.getAllByRole("button", { name: /add issue/i });
    expect(addButtons.length).toBeGreaterThan(0);

    fireEvent.click(addButtons[0]!);
    expect(onCreateIssue).toHaveBeenCalledWith(expect.any(Object));
    expect(mockOpenModal).not.toHaveBeenCalled();
  });

  it("includes project_id in the create payload when projectId prop is set", () => {
    const onCreateIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
        projectId="proj-42"
        onCreateIssue={onCreateIssue}
      />,
    );

    const addButtons = screen.getAllByRole("button", { name: /add issue/i });
    fireEvent.click(addButtons[0]!);

    expect(onCreateIssue).toHaveBeenCalledWith(
      expect.objectContaining({ project_id: "proj-42" }),
    );
    expect(mockOpenModal).not.toHaveBeenCalled();
  });

  // A child whose parent isn't in the loaded set — lands in "Other parents".
  const orphanChild: Issue = {
    id: "lonely-child",
    workspace_id: "ws-1",
    number: 99,
    identifier: "PROJ-99",
    title: "Lonely Child",
    description: null,
    status: "todo",
    priority: "medium",
    assignee_type: "member",
    assignee_id: "user-1",
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: "missing-parent",
    project_id: null,
    position: 400,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("renders children whose parent is not in the loaded set under an 'Other parents' fallback lane", () => {
    renderWithI18n(
      <SwimLaneView issues={[orphanChild]} onMoveIssue={vi.fn()} />,
    );

    expect(screen.getByText("Other parents")).toBeInTheDocument();
    expect(screen.getByText("Lonely Child")).toBeInTheDocument();
  });

  it("does not render the add-issue button inside 'Other parents' cells", () => {
    renderWithI18n(
      <SwimLaneView
        issues={[...mockIssues, orphanChild]}
        onMoveIssue={vi.fn()}
        onCreateIssue={vi.fn()}
      />,
    );

    // No parent + Parent Issue 1 each have one + per visible status column.
    // The Other parents lane must add zero.
    const realLaneCount = 2;
    const visibleStatusCount = 7; // Four lifecycle categories (closed included)
    expect(
      screen.getAllByRole("button", { name: /add issue/i }).length,
    ).toBe(realLaneCount * visibleStatusCount);
  });

  it("does not call onMoveIssue when a card is dropped onto the empty whitespace of an 'Other parents' cell", () => {
    // `over.id` is the orphan cell id — what dnd-kit emits when the
    // pointer lands on the cell's empty area.
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView
        issues={[...mockIssues, orphanChild]}
        onMoveIssue={mockOnMoveIssue}
      />,
    );

    act(() => {
      lastOnDragOver({
        active: { id: "child-1" },
        over: { id: "swim:parent:__orphans__:todo" },
      });
    });
    act(() => {
      lastOnDragEnd({
        active: { id: "child-1" },
        over: { id: "swim:parent:__orphans__:todo" },
      });
    });

    expect(mockOnMoveIssue).not.toHaveBeenCalled();
  });

  it("hides a parent lane when all its children are in hidden statuses", () => {
    // child-1 (in_progress) is not in the visible set — parent-1's lane
    // is dropped. parent-1 itself falls into the No-parent lane as a card.
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues.filter((i) => i.status === "todo")}
        unfilteredIssues={mockIssues}
        visibleStatuses={["todo"]}
        hiddenStatuses={["in_progress", "done"]}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.queryByText("Child Issue 1")).not.toBeInTheDocument();
    expect(screen.getAllByText("Parent Issue 1")).toHaveLength(1);
  });

  it("hides hidden-only server parent lanes and keeps the parent as a card", () => {
    const parent = mockIssues[0]!;
    const descriptors: IssueTableGroupDescriptor[] = [
      {
        key: "parent:parent-1",
        value: {
          kind: "parent",
          parent_id: "parent-1",
          parent: {
            id: "parent-1",
            number: 1,
            identifier: "PROJ-1",
            title: "Parent Issue 1",
            status: "todo",
          },
          value_state: "value",
        },
        count: 1,
        secondary_groups: [{
          key: "compound:cGFyZW50OnBhcmVudC0x:status:done",
          value: { kind: "status", status: "done" },
          count: 1,
        }],
      },
      {
        key: "parent:none",
        value: {
          kind: "parent",
          parent_id: null,
          parent: null,
          value_state: "unset",
        },
        count: 1,
        secondary_groups: [{
          key: "compound:cGFyZW50Om5vbmU:status:todo",
          value: { kind: "status", status: "todo" },
          count: 1,
        }],
      },
    ];

    renderWithI18n(
      <SwimLaneView
        issues={[parent]}
        visibleStatuses={["todo"]}
        hiddenStatuses={["done"]}
        groupBranches={makeServerBranches(descriptors, [parent])}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getAllByText("Parent Issue 1")).toHaveLength(1);
    expect(
      screen.queryByRole("link", { name: "Open parent issue" }),
    ).not.toBeInTheDocument();
  });

  it("does not render a server parent header again as a No-parent card", () => {
    const parent = mockIssues[0]!;
    const child = { ...mockIssues[1]!, status: "todo" as const };
    const descriptors: IssueTableGroupDescriptor[] = [
      {
        key: "parent:parent-1",
        value: {
          kind: "parent",
          parent_id: "parent-1",
          parent: {
            id: "parent-1",
            number: 1,
            identifier: "PROJ-1",
            title: "Parent Issue 1",
            status: "todo",
          },
          value_state: "value",
        },
        count: 1,
        secondary_groups: [{
          key: "compound:cGFyZW50OnBhcmVudC0x:status:todo",
          value: { kind: "status", status: "todo" },
          count: 1,
        }],
      },
      {
        key: "parent:none",
        value: {
          kind: "parent",
          parent_id: null,
          parent: null,
          value_state: "unset",
        },
        count: 1,
        secondary_groups: [{
          key: "compound:cGFyZW50Om5vbmU:status:todo",
          value: { kind: "status", status: "todo" },
          count: 1,
        }],
      },
    ];

    renderWithI18n(
      <SwimLaneView
        issues={[parent, child]}
        visibleStatuses={["todo"]}
        groupBranches={makeServerBranches(descriptors, [parent, child])}
        onMoveIssue={vi.fn()}
      />,
    );

    expect(screen.getAllByText("Parent Issue 1")).toHaveLength(1);
    expect(
      screen.getByRole("link", { name: "Open parent issue" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Child Issue 1")).toBeInTheDocument();
  });

  it("does not call onMoveIssue when a card is dragged out of 'Other parents'", () => {
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView
        issues={[...mockIssues, orphanChild]}
        onMoveIssue={mockOnMoveIssue}
      />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lonely-child" },
        over: { id: "swim:parent:parent-1:in_progress" },
      });
    });

    expect(mockOnMoveIssue).not.toHaveBeenCalled();
  });

  it("renders an open-parent link for lanes with a real parent", () => {
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
      />,
    );

    const links = screen.getAllByRole("link", { name: "Open parent issue" });
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute("href", expect.stringContaining("parent-1"));
  });

  it("renders HiddenColumnsPanel only when hiddenStatuses is non-empty", () => {
    const { unmount } = renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={vi.fn()}
      />,
    );
    expect(screen.queryByText("Hidden columns")).not.toBeInTheDocument();
    unmount();

    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        visibleStatuses={["todo", "in_progress", "cancelled"]}
        hiddenStatuses={["done"]}
        onMoveIssue={vi.fn()}
      />,
    );
    expect(screen.getByText("Hidden columns")).toBeInTheDocument();
    expect(screen.getByText("Done")).toBeInTheDocument();
  });

  it("calls onMoveIssue on drag-and-drop end", () => {
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        onMoveIssue={mockOnMoveIssue}
      />,
    );

    const targetCellId = "swim:parent:none:in_progress";

    act(() => {
      lastOnDragOver({
        active: { id: "orphan-1" },
        over: { id: targetCellId },
      });
    });

    act(() => {
      lastOnDragEnd({
        active: { id: "orphan-1" },
        over: { id: targetCellId },
      });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "orphan-1",
      {
        parent_issue_id: null,
        status: "in_progress",
        position: 300,
        before_id: null,
        after_id: null,
      },
      expect.any(Function),
    );
  });

  it("does not call onMoveIssue when drop target equals source cell (no-op)", () => {
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView issues={mockIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "parent-1" },
        over: { id: "swim:parent:none:todo" },
      });
    });

    expect(mockOnMoveIssue).not.toHaveBeenCalled();
  });

  it("emits parent_issue_id when dragging from orphan into a parent lane", () => {
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView issues={mockIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    const target = "swim:parent:parent-1:todo";
    act(() => {
      lastOnDragOver({
        active: { id: "orphan-1" },
        over: { id: target },
      });
    });
    act(() => {
      lastOnDragEnd({
        active: { id: "orphan-1" },
        over: { id: target },
      });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "orphan-1",
      expect.objectContaining({
        parent_issue_id: "parent-1",
      }),
      expect.any(Function),
    );
    // Moving lanes within Unstarted must preserve the concrete Backlog key.
    expect(mockOnMoveIssue.mock.calls[0]![1]).toHaveProperty("status", "todo");
  });

  it("renders count for hidden statuses from in-memory statusTotals", () => {
    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        visibleStatuses={["in_progress", "done", "cancelled"]}
        hiddenStatuses={["backlog"]}
        onMoveIssue={vi.fn()}
      />,
    );

    const panel = screen.getByText("Hidden columns").parentElement!.parentElement!;
    expect(panel).toHaveTextContent("Backlog");
    expect(panel).not.toHaveTextContent("In Progress");
    // The parent represented by a lane header is not counted as a card.
    expect(panel).toHaveTextContent("1");
  });

  it("hidden-column totals come from unfilteredIssues when provided", () => {
    const unfiltered: Issue[] = [
      ...mockIssues,
      {
        ...mockIssues[2]!,
        id: "blocked-1",
        identifier: "PROJ-99",
        title: "Blocked Issue",
        status: "blocked",
        position: 500,
      },
    ];

    renderWithI18n(
      <SwimLaneView
        issues={mockIssues}
        unfilteredIssues={unfiltered}
        visibleStatuses={["done", "cancelled"]}
        hiddenStatuses={["backlog", "in_progress", "blocked"]}
        onMoveIssue={vi.fn()}
      />,
    );

    const panel = screen.getByText("Hidden columns").parentElement!.parentElement!;
    expect(panel).toHaveTextContent("Backlog");
    expect(panel).toHaveTextContent("In Progress");
    const counts = [...panel.querySelectorAll("span")].map((el) => el.textContent);
    expect(counts).toContain("1");
    expect(counts.filter((count) => count === "1")).toHaveLength(3);
  });

  const multiParentIssues: Issue[] = [
    {
      id: "parent-1",
      workspace_id: "ws-1",
      number: 1,
      identifier: "PROJ-1",
      title: "Parent A",
      description: null,
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
    {
      id: "parent-2",
      workspace_id: "ws-1",
      number: 2,
      identifier: "PROJ-10",
      title: "Parent B",
      description: null,
      status: "todo",
      priority: "high",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 200,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "child-of-1",
      workspace_id: "ws-1",
      number: 3,
      identifier: "PROJ-2",
      title: "Child of A",
      description: null,
      status: "in_progress",
      priority: "medium",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: "parent-1",
      project_id: null,
      position: 300,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "child-of-2",
      workspace_id: "ws-1",
      number: 4,
      identifier: "PROJ-11",
      title: "Child of B",
      description: null,
      status: "in_progress",
      priority: "medium",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: "parent-2",
      project_id: null,
      position: 400,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  ];

  it("persists lane order via setSwimlaneOrder when a lane is dragged onto another", () => {
    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lane:parent:parent-1" },
        over: { id: "lane:parent:parent-2" },
      });
    });

    expect(mockSetSwimlaneOrder).toHaveBeenCalledWith(["parent-2", "parent-1"]);
  });

  it("appends newly-visible parents to the persisted order on first reorder", () => {
    mockViewState.swimlaneOrders = {
      ...mockViewState.swimlaneOrders,
      parent: ["parent-1"],
    };

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lane:parent:parent-1" },
        over: { id: "lane:parent:parent-2" },
      });
    });

    expect(mockSetSwimlaneOrder).toHaveBeenCalledWith(["parent-2", "parent-1"]);
  });

  it("preserves persisted entries that aren't currently visible during a reorder", () => {
    mockViewState.swimlaneOrders = {
      ...mockViewState.swimlaneOrders,
      parent: ["filtered-a", "parent-1", "filtered-b", "parent-2"],
    };

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lane:parent:parent-1" },
        over: { id: "lane:parent:parent-2" },
      });
    });

    expect(mockSetSwimlaneOrder).toHaveBeenCalledWith([
      "filtered-a",
      "parent-2",
      "filtered-b",
      "parent-1",
    ]);
  });

  it("does not call setSwimlaneOrder when a lane is dropped onto itself", () => {
    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lane:parent:parent-1" },
        over: { id: "lane:parent:parent-1" },
      });
    });

    expect(mockSetSwimlaneOrder).not.toHaveBeenCalled();
  });

  it("does not call onMoveIssue when a lane drag ends (no card mutation)", () => {
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    act(() => {
      lastOnDragEnd({
        active: { id: "lane:parent:parent-1" },
        over: { id: "lane:parent:parent-2" },
      });
    });

    expect(mockOnMoveIssue).not.toHaveBeenCalled();
  });

  it("renders parent lanes in stored swimlaneOrder when set", () => {
    mockViewState.swimlaneOrders = {
      ...mockViewState.swimlaneOrders,
      parent: ["parent-2", "parent-1"],
    };

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    const parentA = screen.getByText("Parent A");
    const parentB = screen.getByText("Parent B");
    // DOM order: "Parent B" must precede "Parent A".
    // compareDocumentPosition: bitmask, DOCUMENT_POSITION_FOLLOWING = 4
    expect(parentB.compareDocumentPosition(parentA) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps 'No parent' lane pinned at top regardless of stored order", () => {
    mockViewState.swimlaneOrders = {
      ...mockViewState.swimlaneOrders,
      parent: ["parent-2", "parent-1"],
    };

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    const noParent = screen.getByText("No parent");
    const parentB = screen.getByText("Parent B");
    expect(noParent.compareDocumentPosition(parentB) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  // ------------------------------------------------------------------
  // Persisted collapsed-lane state
  // ------------------------------------------------------------------

  it("collapses a parent lane when its id is in stored collapsedSwimlanes", () => {
    mockViewState.collapsedSwimlanes = {
      ...mockViewState.collapsedSwimlanes,
      parent: ["parent-1"],
    };

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    // The lane HEADER for Parent A is still visible…
    expect(screen.getByText("Parent A")).toBeInTheDocument();
    // …but its child card is not.
    expect(screen.queryByText("Child of A")).not.toBeInTheDocument();
    // Parent B (not collapsed) still shows its child.
    expect(screen.getByText("Child of B")).toBeInTheDocument();
  });

  it("collapses the 'No parent' lane when 'none' is in stored collapsedSwimlanes", () => {
    mockViewState.collapsedSwimlanes = {
      ...mockViewState.collapsedSwimlanes,
      parent: ["none"],
    };

    renderWithI18n(
      <SwimLaneView issues={mockIssues} onMoveIssue={vi.fn()} />,
    );

    expect(screen.getByText("No parent")).toBeInTheDocument();
    expect(screen.queryByText("Orphan Issue 1")).not.toBeInTheDocument();
  });

  it("calls toggleSwimlaneCollapsed with the raw parent id when a lane header is clicked", () => {
    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    const parentAHeader = screen.getByText("Parent A").closest("button");
    expect(parentAHeader).not.toBeNull();
    fireEvent.click(parentAHeader!);

    expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledWith("parent-1");
  });

  it("calls toggleSwimlaneCollapsed with 'none' when the No-parent lane header is clicked", () => {
    renderWithI18n(
      <SwimLaneView issues={mockIssues} onMoveIssue={vi.fn()} />,
    );

    // "No parent" appears twice (lane title + orphan-1's card description);
    // find the one inside a button.
    const matches = screen.getAllByText("No parent");
    const noParentHeader = matches.map((m) => m.closest("button")).find(Boolean);
    expect(noParentHeader).toBeDefined();
    fireEvent.click(noParentHeader!);

    expect(mockToggleSwimlaneCollapsed).toHaveBeenCalledWith("none");
  });

  // ------------------------------------------------------------------
  // Project grouping
  // ------------------------------------------------------------------

  const projectIssues: Issue[] = [
    {
      ...mockIssues[0]!,
      id: "issue-a",
      identifier: "PROJ-100",
      title: "Issue A",
      project_id: "proj-1",
      parent_issue_id: null,
      status: "todo",
    },
    {
      ...mockIssues[0]!,
      id: "issue-b",
      identifier: "PROJ-101",
      title: "Issue B",
      project_id: "proj-2",
      parent_issue_id: null,
      status: "in_progress",
    },
    {
      ...mockIssues[0]!,
      id: "issue-c",
      identifier: "PROJ-102",
      title: "Issue C",
      project_id: null,
      parent_issue_id: null,
      status: "todo",
    },
  ];

  it("groups by project when swimlaneGrouping is 'project'", () => {
    mockViewState.swimlaneGrouping = "project";

    renderWithI18n(
      <SwimLaneView issues={projectIssues} onMoveIssue={vi.fn()} />,
    );

    // No-project pinned lane is always present.
    expect(screen.getAllByText("No project").length).toBeGreaterThanOrEqual(1);
    // Both issue cards from real projects render. The component receives
    // project metadata from its parent, so this standalone test asserts on
    // card visibility rather than lane title text.
    expect(screen.getByText("Issue A")).toBeInTheDocument();
    expect(screen.getByText("Issue B")).toBeInTheDocument();
    expect(screen.getByText("Issue C")).toBeInTheDocument();
  });

  it("emits project_id when a card is dropped into a project lane", () => {
    mockViewState.swimlaneGrouping = "project";
    const mockOnMoveIssue = vi.fn();

    renderWithI18n(
      <SwimLaneView issues={projectIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    // Drop "issue-c" (no project) into proj-1's todo cell.
    const target = "swim:project:proj-1:todo";
    act(() => {
      lastOnDragOver({ active: { id: "issue-c" }, over: { id: target } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "issue-c" }, over: { id: target } });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "issue-c",
      expect.objectContaining({ project_id: "proj-1" }),
      expect.any(Function),
    );
    expect(mockOnMoveIssue.mock.calls[0]?.[1]).not.toHaveProperty("status");
  });

  it("emits null project_id when a card is dropped into the 'No project' lane", () => {
    mockViewState.swimlaneGrouping = "project";
    const mockOnMoveIssue = vi.fn();

    renderWithI18n(
      <SwimLaneView issues={projectIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    const target = "swim:project:none:in_progress";
    act(() => {
      lastOnDragOver({ active: { id: "issue-a" }, over: { id: target } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "issue-a" }, over: { id: target } });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "issue-a",
      expect.objectContaining({ project_id: null, status: "in_progress" }),
      expect.any(Function),
    );
  });

  // ------------------------------------------------------------------
  // Assignee grouping
  // ------------------------------------------------------------------

  const assigneeIssues: Issue[] = [
    {
      ...mockIssues[0]!,
      id: "issue-x",
      identifier: "PROJ-200",
      title: "Issue X",
      assignee_type: "member",
      assignee_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      status: "todo",
    },
    {
      ...mockIssues[0]!,
      id: "issue-y",
      identifier: "PROJ-201",
      title: "Issue Y",
      assignee_type: "agent",
      assignee_id: "agent-1",
      parent_issue_id: null,
      project_id: null,
      status: "in_progress",
    },
    {
      ...mockIssues[0]!,
      id: "issue-z",
      identifier: "PROJ-202",
      title: "Issue Z",
      assignee_type: null,
      assignee_id: null,
      parent_issue_id: null,
      project_id: null,
      status: "todo",
    },
  ];

  it("groups by assignee when swimlaneGrouping is 'assignee'", () => {
    mockViewState.swimlaneGrouping = "assignee";

    renderWithI18n(
      <SwimLaneView issues={assigneeIssues} onMoveIssue={vi.fn()} />,
    );

    // Unassigned pinned lane is always rendered.
    expect(screen.getAllByText("Unassigned").length).toBeGreaterThanOrEqual(1);
    // Mock actor name fallback for both member and agent.
    expect(screen.getAllByText("Mock Actor").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("Issue X")).toBeInTheDocument();
    expect(screen.getByText("Issue Y")).toBeInTheDocument();
    expect(screen.getByText("Issue Z")).toBeInTheDocument();
  });

  it("emits assignee_type + assignee_id when a card is dropped into an actor lane", () => {
    mockViewState.swimlaneGrouping = "assignee";
    const mockOnMoveIssue = vi.fn();

    renderWithI18n(
      <SwimLaneView issues={assigneeIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    const target = "swim:assignee:member:user-1:in_progress";
    act(() => {
      lastOnDragOver({ active: { id: "issue-z" }, over: { id: target } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "issue-z" }, over: { id: target } });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "issue-z",
      expect.objectContaining({
        assignee_type: "member",
        assignee_id: "user-1",
        status: "in_progress",
      }),
      expect.any(Function),
    );
  });

  it("emits null assignee when a card is dropped into the 'Unassigned' lane", () => {
    mockViewState.swimlaneGrouping = "assignee";
    const mockOnMoveIssue = vi.fn();

    renderWithI18n(
      <SwimLaneView issues={assigneeIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    const target = "swim:assignee:none:done";
    act(() => {
      lastOnDragOver({ active: { id: "issue-x" }, over: { id: target } });
    });
    act(() => {
      lastOnDragEnd({ active: { id: "issue-x" }, over: { id: target } });
    });

    expect(mockOnMoveIssue).toHaveBeenCalledWith(
      "issue-x",
      expect.objectContaining({
        assignee_type: null,
        assignee_id: null,
        status: "done",
      }),
      expect.any(Function),
    );
  });

  // ------------------------------------------------------------------
  // Batched children fetch (childrenByParentsOptions)
  // ------------------------------------------------------------------

  it("fires listChildrenByParents once with all visible parent ids on mount", async () => {
    // multiParentIssues has parent-1 (Child of A) and parent-2 (Child of B) as
    // visible parent lanes. Both ids should appear in one batched call.
    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalledTimes(1);
    });
    const [calledIds] = mockListChildrenByParents.mock.calls[0] as [string[]];
    expect(calledIds.sort()).toEqual(["parent-1", "parent-2"].sort());
  });

  it("does not fire listChildrenByParents when there are no parent lanes", async () => {
    // All issues are top-level — no parent lanes, no batch request.
    const flatIssues = mockIssues.filter((i) => i.parent_issue_id === null);
    renderWithI18n(
      <SwimLaneView issues={flatIssues} onMoveIssue={vi.fn()} />,
    );

    await act(async () => {});
    expect(mockListChildrenByParents).not.toHaveBeenCalled();
  });

  it("merges batch-fetched children into parent lanes so previously-empty cells populate", async () => {
    // Scenario: grandparent G → parent P (loaded, becomes a lane header) →
    // grandchild GC (NOT in the initial `issues` set, returned only by the
    // batch fetch). P's lane should show GC after the batch resolves.
    //
    // For the batch to include P.id, the caller must pass childProgressMap
    // signaling that P has children — without it, batchParentIds only sees
    // GP.id (from parent.parent_issue_id) and GC is never fetched.
    const grandparent: Issue = {
      id: "gp-1",
      workspace_id: "ws-1",
      number: 10,
      identifier: "PROJ-10",
      title: "Grandparent",
      description: null,
      status: "todo",
      priority: "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 10,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const parent: Issue = {
      ...grandparent,
      id: "p-1",
      number: 11,
      identifier: "PROJ-11",
      title: "Parent",
      parent_issue_id: "gp-1",
      position: 11,
    };
    const grandchild: Issue = {
      ...grandparent,
      id: "gc-1",
      number: 12,
      identifier: "PROJ-12",
      title: "Grandchild (batch only)",
      status: "in_progress",
      parent_issue_id: "p-1",
      position: 12,
    };

    mockListChildrenByParents.mockResolvedValueOnce({ issues: [grandchild] });
    const childProgressMap = new Map<string, { done: number; total: number }>([
      ["p-1", { done: 0, total: 1 }],
    ]);

    renderWithI18n(
      <SwimLaneView
        issues={[grandparent, parent]}
        childProgressMap={childProgressMap}
        onMoveIssue={vi.fn()}
      />,
    );

    // Assert the batch request actually included p-1 — without this the
    // mock would happily return GC for any request and the merge would
    // appear to work without exercising the real path.
    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });
    const [calledIds] = mockListChildrenByParents.mock.calls[0] as [string[]];
    expect(calledIds).toEqual(expect.arrayContaining(["p-1"]));

    await waitFor(() => {
      expect(screen.getByText("Grandchild (batch only)")).toBeInTheDocument();
    });
  });

  it("includes visible parents with children (via childProgressMap) in the batch request", async () => {
    // Even without any loaded child pointing at a parent, if childProgressMap
    // says the parent has children we should query it so deep-nested
    // grandchildren are discoverable.
    const parentWithUnloadedChildren: Issue = {
      id: "p-only",
      workspace_id: "ws-1",
      number: 50,
      identifier: "PROJ-50",
      title: "Standalone parent",
      description: null,
      status: "todo",
      priority: "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 50,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const childProgressMap = new Map<string, { done: number; total: number }>([
      ["p-only", { done: 0, total: 3 }],
    ]);

    renderWithI18n(
      <SwimLaneView
        issues={[parentWithUnloadedChildren]}
        childProgressMap={childProgressMap}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });
    const [calledIds] = mockListChildrenByParents.mock.calls[0] as [string[]];
    expect(calledIds).toEqual(expect.arrayContaining(["p-only"]));
  });

  it("does not fire listChildrenByParents when swimlaneGrouping is not parent", async () => {
    mockViewState.swimlaneGrouping = "project";

    renderWithI18n(
      <SwimLaneView issues={multiParentIssues} onMoveIssue={vi.fn()} />,
    );

    await act(async () => {});
    expect(mockListChildrenByParents).not.toHaveBeenCalled();
  });

  it("does not call onMoveIssue when dropping a card into a lane whose header is that card", () => {
    // parent-1 (a lane-header card in the No-parent lane) dropped onto a
    // cell inside its own lane (`swim:parent:parent-1:in_progress`) would
    // be a self-cycle. The client guard refuses before reaching the API.
    const mockOnMoveIssue = vi.fn();
    renderWithI18n(
      <SwimLaneView issues={mockIssues} onMoveIssue={mockOnMoveIssue} />,
    );

    act(() => {
      lastOnDragOver({
        active: { id: "parent-1" },
        over: { id: "swim:parent:parent-1:in_progress" },
      });
    });
    act(() => {
      lastOnDragEnd({
        active: { id: "parent-1" },
        over: { id: "swim:parent:parent-1:in_progress" },
      });
    });

    expect(mockOnMoveIssue).not.toHaveBeenCalled();
  });

  it("filters batch-fetched children using active filters", async () => {
    mockViewState.swimlaneGrouping = "parent";

    const grandparent: Issue = {
      id: "gp-2",
      workspace_id: "ws-1",
      number: 20,
      identifier: "PROJ-20",
      title: "Grandparent 2",
      description: null,
      status: "todo",
      priority: "high",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 10,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const parent: Issue = {
      ...grandparent,
      id: "p-2",
      number: 21,
      identifier: "PROJ-21",
      title: "Parent 2",
      parent_issue_id: "gp-2",
      position: 11,
    };
    const matchingGrandchild: Issue = {
      ...grandparent,
      id: "gc-matching",
      number: 22,
      identifier: "PROJ-22",
      title: "Matching Child (High Priority)",
      status: "in_progress",
      priority: "high",
      parent_issue_id: "p-2",
      position: 12,
    };
    const nonMatchingGrandchild: Issue = {
      ...grandparent,
      id: "gc-non-matching",
      number: 23,
      identifier: "PROJ-23",
      title: "Non-matching Child (Low Priority)",
      status: "in_progress",
      priority: "low",
      parent_issue_id: "p-2",
      position: 13,
    };

    mockListChildrenByParents.mockResolvedValueOnce({
      issues: [matchingGrandchild, nonMatchingGrandchild],
    });

    const childProgressMap = new Map<string, { done: number; total: number }>([
      ["p-2", { done: 0, total: 2 }],
    ]);

    renderWithI18n(
      <SwimLaneView
        issues={[grandparent, parent]}
        activeFilters={{
          priorityFilters: ["high"],
          assigneeFilters: [],
          includeNoAssignee: false,
          creatorFilters: [],
          projectFilters: [],
          includeNoProject: false,
          labelFilters: [],
        }}
        childProgressMap={childProgressMap}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });

    await waitFor(() => {
      expect(screen.getByText("Matching Child (High Priority)")).toBeInTheDocument();
      expect(screen.queryByText("Non-matching Child (Low Priority)")).toBeNull();
    });
  });

  it("filters batch-fetched children using API-derived running issue ids", async () => {
    mockViewState.swimlaneGrouping = "parent";

    const grandparent: Issue = {
      id: "gp-3",
      workspace_id: "ws-1",
      number: 30,
      identifier: "PROJ-30",
      title: "Grandparent 3",
      description: null,
      status: "todo",
      priority: "medium",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 10,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const parent: Issue = {
      ...grandparent,
      id: "p-3",
      number: 31,
      identifier: "PROJ-31",
      title: "Parent 3",
      parent_issue_id: "gp-3",
      position: 11,
    };
    const runningGrandchild: Issue = {
      ...grandparent,
      id: "gc-running",
      number: 32,
      identifier: "PROJ-32",
      title: "Running Child",
      status: "in_progress",
      assignee_type: "agent",
      assignee_id: "idle-agent",
      parent_issue_id: "p-3",
      position: 12,
    };
    const nonRunningGrandchild: Issue = {
      ...grandparent,
      id: "gc-non-running",
      number: 33,
      identifier: "PROJ-33",
      title: "Non-running Child",
      status: "in_progress",
      assignee_type: "agent",
      assignee_id: "working-agent",
      parent_issue_id: "p-3",
      position: 13,
    };

    mockListChildrenByParents.mockResolvedValueOnce({
      issues: [runningGrandchild, nonRunningGrandchild],
    });

    const childProgressMap = new Map<string, { done: number; total: number }>([
      ["p-3", { done: 0, total: 2 }],
    ]);

    renderWithI18n(
      <SwimLaneView
        issues={[grandparent, parent]}
        activeFilters={{
          priorityFilters: [],
          assigneeFilters: [],
          includeNoAssignee: false,
          agentRunningFilter: true,
          runningIssueIds: new Set(["gc-running"]),
          creatorFilters: [],
          projectFilters: [],
          includeNoProject: false,
          labelFilters: [],
        }}
        childProgressMap={childProgressMap}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });

    await waitFor(() => {
      expect(screen.getByText("Running Child")).toBeInTheDocument();
      expect(screen.queryByText("Non-running Child")).toBeNull();
    });
  });

  it("hides batch-fetched children when the running issue set is empty", async () => {
    mockViewState.swimlaneGrouping = "parent";
    const parent = mockIssues[0]!;
    const batchOnlyChild: Issue = {
      ...mockIssues[1]!,
      id: "working-empty-child",
      identifier: "PROJ-34",
      title: "Working Empty Child",
      parent_issue_id: parent.id,
    };
    mockListChildrenByParents.mockResolvedValueOnce({
      issues: [batchOnlyChild],
    });

    renderWithI18n(
      <SwimLaneView
        issues={[parent]}
        activeFilters={{
          priorityFilters: [],
          assigneeFilters: [],
          includeNoAssignee: false,
          agentRunningFilter: true,
          runningIssueIds: new Set(),
          creatorFilters: [],
          projectFilters: [],
          includeNoProject: false,
          labelFilters: [],
        }}
        childProgressMap={
          new Map([[parent.id, { done: 0, total: 1 }]])
        }
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });
    await act(async () => {});
    expect(screen.queryByText("Working Empty Child")).toBeNull();
  });

  it("hides batch-fetched children when 'Show sub-issues' is off", async () => {
    mockViewState.swimlaneGrouping = "parent";

    const grandparent: Issue = {
      id: "gp-4",
      workspace_id: "ws-1",
      number: 40,
      identifier: "PROJ-40",
      title: "Grandparent 4",
      description: null,
      status: "todo",
      priority: "medium",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 10,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const parent: Issue = {
      ...grandparent,
      id: "p-4",
      number: 41,
      identifier: "PROJ-41",
      title: "Parent 4",
      parent_issue_id: "gp-4",
      position: 11,
    };
    // Returned only by the batch fetch (not in the initial `issues` set). It is
    // a sub-issue, so with showSubIssues off it must not be merged back in.
    const batchOnlyChild: Issue = {
      ...grandparent,
      id: "gc-hidden",
      number: 42,
      identifier: "PROJ-42",
      title: "Batch Sub-issue",
      status: "in_progress",
      parent_issue_id: "p-4",
      position: 12,
    };

    mockListChildrenByParents.mockResolvedValueOnce({ issues: [batchOnlyChild] });

    const childProgressMap = new Map<string, { done: number; total: number }>([
      ["p-4", { done: 0, total: 1 }],
    ]);

    renderWithI18n(
      <SwimLaneView
        issues={[grandparent, parent]}
        activeFilters={{
          priorityFilters: [],
          assigneeFilters: [],
          includeNoAssignee: false,
          creatorFilters: [],
          projectFilters: [],
          includeNoProject: false,
          labelFilters: [],
          showSubIssues: false,
        }}
        childProgressMap={childProgressMap}
        onMoveIssue={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(mockListChildrenByParents).toHaveBeenCalled();
    });

    // Guard against a false pass: the batch request must have targeted p-4.
    const [calledIds] = mockListChildrenByParents.mock.calls[0] as [string[]];
    expect(calledIds).toEqual(expect.arrayContaining(["p-4"]));

    // Give the merge effect a chance to run, then assert the sub-issue stays hidden.
    await act(async () => {});
    expect(screen.queryByText("Batch Sub-issue")).toBeNull();
  });
});

describe("SwimLaneView tab-session scroll restoration (MUL-4741)", () => {
  it("registers the outer scroller for memento capture and restores the saved offset at attach", () => {
    const adapter = {
      get: (key: string) =>
        key === "swimlane" ? { top: 240, height: 2000 } : undefined,
    };
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <I18nProvider resources={TEST_RESOURCES} locale="en">
          <IssueContextMenuProvider>
            <ScrollRestorationProvider adapter={adapter}>
              <SwimLaneView issues={mockIssues} onMoveIssue={vi.fn()} />
            </ScrollRestorationProvider>
          </IssueContextMenuProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );

    const scroller = container.querySelector<HTMLElement>(
      '[data-tab-scroll-root="swimlane"]',
    );
    // Capture side: the coordinator scans [data-tab-scroll-root] — without
    // the marker, leaving the tab never saves the swimlane offset.
    expect(scroller).not.toBeNull();
    // Restore side: the ref-attach assignment applies the saved offset
    // before first paint (jsdom has no layout, so no clamping applies).
    expect(scroller!.scrollTop).toBe(240);
  });
});

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

  it("AG patch: the band wraps the header row and the cells row, and no cell carries a band", async () => {
    const { container } = renderServer([null, B, A, X], [C, B, Y, A, X]);

    await waitFor(() => expect(laneOf("Issue X")?.dataset.laneBand).toBe("odd"));
    const lane = laneOf("Issue X");
    // Header row, then the cells grid, both inside the band element.
    expect(lane.children).toHaveLength(2);
    expect(lane.children[0]!.querySelector("button")).not.toBeNull();
    // The band attribute and tint live on the lane element only.
    expect(container.querySelectorAll("[data-lane-band]")).toHaveLength(
      laneElements(container).filter((el) => el.dataset.laneBand).length,
    );
    const cells = lane.children[1]!.children;
    expect(cells.length).toBeGreaterThan(0);
    for (const cell of Array.from(cells)) {
      expect(cell.hasAttribute("data-lane-band")).toBe(false);
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

  describe("AG patch: drag rules and jump-to-lane chip", () => {
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
});
