/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { pruneIssueSurfaceViewStates } from "@multica/core/issues/stores/surface-view-store";
import type {
  Issue,
  IssueTableRowsRequest,
  ListIssuesParams,
  ListIssuesResponse,
} from "@multica/core/types";
import { IssueSurface } from "./issue-surface";

const mockTranslate = vi.hoisted(() => vi.fn(() => "translated"));
vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
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

vi.mock("../../i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../i18n")>()),
  useLocale: () => "en",
  // TableView also reads `i18n.language` for its date formatting.
  useT: () => ({ t: mockTranslate, i18n: { language: "en" } }),
  useTimeAgo: () => () => "now",
}));

vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/" }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Test WS", slug: "test" }),
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});

function makeIssue(id: string, title: string, projectId: string): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: projectId,
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

function never<T>() {
  return new Promise<T>(() => {});
}

describe("IssueSurface — table pagination ownership", () => {
  let qc: QueryClient;
  let listIssues: ReturnType<
    typeof vi.fn<(params?: ListIssuesParams) => Promise<ListIssuesResponse>>
  >;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    listIssues = vi.fn(() => never<ListIssuesResponse>());
    mockTranslate.mockClear();
    pruneIssueSurfaceViewStates([]);
    // jsdom has no IntersectionObserver; the table footer sentinel constructs one on mount.
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
  });

  afterEach(() => {
    cleanup();
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  // AG patch: the real surface in tree mode. The surface renders `<TreeView>`
  // and hands `allowTree` to the header; a mode wired into the controller but
  // not into these two spots would give a blank surface or no menu entry.
  it("AG patch: renders the outline tree and a Tree menu entry in tree mode", async () => {
    const { getIssueSurfaceViewStore } = await import(
      "@multica/core/issues/stores/surface-view-store"
    );
    const store = getIssueSurfaceViewStore("project:pt-tree-mode");
    store.getState().setViewMode("tree");
    const parent = makeIssue("tree-parent", "Tree parent row", "pt-tree-mode");
    const child = makeIssue("tree-child", "Tree child row", "pt-tree-mode");
    child.parent_issue_id = parent.id;
    const listIssueTableRows = vi.fn(async (request: IssueTableRowsRequest) => ({
      query_fingerprint: "sha256:tree",
      group_key: null,
      parent_id: request.parent_id,
      total: 1,
      rows:
        request.parent_id === null
          ? [{ issue: parent, direct_child_count: 1 }]
          : [{ issue: child, direct_child_count: 0 }],
      branch_total: 1,
      next_cursor: null,
    }));
    setApiInstance({
      listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
      listIssues,
      listIssueTableRows,
      listIssueTableFacets: vi.fn(() => never()),
      listGroupedIssues: vi.fn(() => never()),
      listProjects: vi.fn(() => Promise.resolve([])),
      getAgentTaskSnapshot: vi.fn(() => Promise.resolve([])),
      getChildIssueProgress: vi.fn(() => Promise.resolve([])),
      listProperties: vi.fn(() => Promise.resolve({ properties: [] })),
      listMembers: vi.fn(() => Promise.resolve([])),
      listAgents: vi.fn(() => Promise.resolve([])),
      listSquads: vi.fn(() => Promise.resolve([])),
      listLabels: vi.fn(() => never()),
    } as unknown as ApiClient);
    // Name each control by its i18n key path ($.issues.view.tree -> "issues.view.tree").
    const keyPath = (select: (r: unknown) => unknown) => {
      const parts: string[] = [];
      const proxy: object = new Proxy(
        {},
        {
          get: (_t, p) => {
            if (p === Symbol.toPrimitive || p === "toString") {
              return () => parts.join(".");
            }
            parts.push(String(p));
            return proxy;
          },
        },
      );
      return String(select(proxy));
    };
    mockTranslate.mockImplementation(keyPath as never);

    render(
      <QueryClientProvider client={qc}>
        <IssueSurface
          scope={{ type: "project", projectId: "pt-tree-mode" }}
          modes={["list", "tree"]}
          batchToolbar="never"
        />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Tree parent row")).toBeInTheDocument();
    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getAllByRole("treeitem")).toHaveLength(1);
    // Children arrive when the parent opens.
    fireEvent.click(screen.getByRole("button", { name: "issues.tree.expand" }));
    expect(await screen.findByText("Tree child row")).toBeInTheDocument();

    // The header offers the Tree layout, so it can be left and re-entered.
    fireEvent.click(await screen.findByRole("button", { name: /issues\.view\.tree/ }));
    expect(await screen.findByRole("menuitemradio", { name: /issues\.view\.tree/ })).toBeInTheDocument();
    mockTranslate.mockImplementation((() => "translated") as never);
  });
});
