/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { getIssueSurfaceViewStore } from "@multica/core/issues/stores/surface-view-store";
import type { Issue, IssueTableQuerySpec } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueSurfaceSelectionProvider } from "../surface/selection-context";
import type { IssueSurfaceSelection } from "../surface/selection-context";
import type { ChildProgress } from "./list-row";
import { TableView } from "./table-view";

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

// jsdom has no layout, so the real row virtualizer sees a 0-height viewport
// and renders nothing. Render every row inline instead.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: {
    count: number;
    getItemKey?: (index: number) => unknown;
  }) => ({
    getVirtualItems: () =>
      Array.from({ length: options.count }, (_, index) => ({
        index,
        key: options.getItemKey?.(index) ?? index,
        start: index * 41,
        end: (index + 1) * 41,
        size: 41,
        lane: 0,
      })),
    getTotalSize: () => options.count * 41,
    measureElement: () => {},
  }),
}));

vi.mock("@multica/core/workspace/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/hooks")>()),
  useActorName: () => ({ getActorName: () => "Someone" }),
  buildActorNameResolver: () => () => "Someone",
}));

const mockAuthUser = { id: "user-1", email: "t@t.co", name: "Tester" };
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
  AppLink: ({ children, ...props }: React.ComponentProps<"a">) => (
    <a {...props}>{children}</a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/" }),
  useIntentNavigate: () => () => {},
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});

class ObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

function makeIssue(id: string, title: string, status: Issue["status"]): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title,
    description: null,
    status,
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: null,
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

const selection: IssueSurfaceSelection = {
  selectedIds: new Set<string>(),
  toggle: () => {},
  select: () => {},
  deselect: () => {},
  clear: () => {},
};

const serverQuery: IssueTableQuerySpec = {
  scope: { kind: "workspace" },
  filters: {},
  sort: { field: "position", direction: "asc" },
};

let serverIssues: Issue[] = [];

function Harness({
  childProgressMap,
  surfaceKey,
}: {
  childProgressMap: Map<string, ChildProgress>;
  surfaceKey: string;
}) {
  return (
    <ViewStoreProvider store={getIssueSurfaceViewStore(surfaceKey)}>
      <IssueSurfaceSelectionProvider selection={selection}>
        <TableView
          serverQuery={serverQuery}
          childProgressMap={childProgressMap}
          search=""
          onSearchChange={() => {}}
          onLoadedIssuesChange={() => {}}
          onCreateIssue={() => {}}
          exportIssues={() => Promise.resolve(serverIssues)}
          resolveExportLookups={() =>
            Promise.resolve({
              projectMap: new Map(),
              childProgressMap: new Map(),
            })
          }
        />
      </IssueSurfaceSelectionProvider>
    </ViewStoreProvider>
  );
}

describe("TableView cell editors under data refresh", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    vi.stubGlobal("IntersectionObserver", ObserverStub);
    vi.stubGlobal("ResizeObserver", ObserverStub);
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
    setApiInstance({
      listProperties: async () => ({ properties: [] }),
      listMembers: async () => [],
      listAgents: async () => [],
      listSquads: async () => [],
      getAssigneeFrequency: async () => [],
      listIssueStatuses: async () => ({ statuses: [] }),
      listIssueTableRows: async () => ({
        query_fingerprint: "test",
        group_key: null,
        parent_id: null,
        total: serverIssues.length,
        rows: serverIssues.map((issue) => ({
          issue,
          direct_child_count: 0,
        })),
        branch_total: serverIssues.length,
        next_cursor: null,
      }),
    } as unknown as ApiClient);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("AG patch: the label cell shows every label, wrapping, with no +N", async () => {
    const names = ["Bug", "Frontend", "Needs design", "Customer-reported", "Regression"];
    serverIssues = [
      {
        ...makeIssue("l", "Many labels", "todo"),
        labels: names.map((name, i) => ({
          id: `label-${i}`,
          workspace_id: "ws-1",
          name,
          color: "#3b82f6",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        })),
      },
    ];

    renderWithI18n(
      <QueryClientProvider client={queryClient}>
        <Harness
          childProgressMap={new Map()}
          surfaceKey={`test-label-cell-${Math.floor(Math.random() * 1e9)}`}
        />
      </QueryClientProvider>,
    );

    const row = (await screen.findByText("MUL-l")).closest("tr")!;
    for (const name of names) {
      expect(within(row).getByText(name)).toBeInTheDocument();
    }
    expect(row.textContent).not.toMatch(/\+\d/);
    const labelRow = row.querySelector<HTMLElement>('[data-slot="ag-label-row"]')!;
    expect(labelRow.dataset.display).toBe("full");
    expect(labelRow.className).toContain("flex-wrap");
    expect(Array.from(labelRow.children).map((c) => c.getAttribute("aria-label"))).toEqual(names);
  });
});
