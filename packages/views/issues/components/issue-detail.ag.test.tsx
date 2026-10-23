import { forwardRef } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue, Label } from "@multica/core/types";
import {
  DEFAULT_SUB_ISSUE_ROW_PROPERTIES,
  useSubIssueDisplayStore,
} from "@multica/core/issues/stores/sub-issue-display-store";
import { renderWithI18n } from "../../test/i18n";

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

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", name: "Test WS", slug: "test" }),
    useWorkspacePaths: () => actual.paths.workspace("test"),
  };
});

vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  AppLink: ({ children, href, ...props }: React.ComponentProps<"a">) => (
    <a href={href} {...props}>
      {children}
    </a>
  ),
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/issues/issue-1",
    getShareableUrl: (p: string) => `https://app.multica.com${p}`,
  }),
  useBackOrReplace: () => vi.fn(),
}));

// Tiptap needs a real layout; the sub-issue rows under test don't use it.
vi.mock("../../editor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../editor")>()),
  ReadonlyContent: ({ content }: { content: string }) => <div>{content}</div>,
  ContentEditor: forwardRef(function MockContentEditor() {
    return null;
  }),
  TitleEditor: forwardRef(function MockTitleEditor({ defaultValue }: { defaultValue?: string }) {
    return <div>{defaultValue}</div>;
  }),
}));

vi.mock("../../common/actor-avatar", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../common/actor-avatar")>()),
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

const mockApiObj = vi.hoisted(() => ({
  getIssue: vi.fn(),
  listTimeline: vi.fn().mockResolvedValue([]),
  listIssueSubscribers: vi.fn().mockResolvedValue([]),
  getActiveTasksForIssue: vi.fn().mockResolvedValue({ tasks: [] }),
  listTasksByIssue: vi.fn().mockResolvedValue([]),
  listChildIssues: vi.fn().mockResolvedValue({ issues: [] }),
  getChildIssueProgress: vi.fn().mockResolvedValue({ progress: [] }),
  getAgentTaskSnapshot: vi.fn().mockResolvedValue([]),
  getWorkspaceWorkingAgents: vi.fn().mockResolvedValue([]),
  listProperties: vi.fn().mockResolvedValue({ properties: [], total: 0 }),
  listIssues: vi.fn().mockResolvedValue({ issues: [], total: 0 }),
  listIssueReactions: vi.fn().mockResolvedValue([]),
  listAttachments: vi.fn().mockResolvedValue([]),
  listMembers: vi.fn().mockResolvedValue([]),
  listAgents: vi.fn().mockResolvedValue([]),
  listProjects: vi.fn().mockResolvedValue({ projects: [] }),
}));

vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: mockApiObj,
  getApi: () => mockApiObj,
}));

vi.mock("@multica/core/realtime", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/realtime")>()),
  useWSEvent: vi.fn(),
  useWSReconnect: vi.fn(),
  useWS: () => ({ subscribe: vi.fn(() => () => {}), onReconnect: vi.fn(() => () => {}) }),
}));

const mockIssue: Issue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "TES-1",
  title: "Implement authentication",
  description: "Add JWT auth to the backend",
  status: "in_progress",
  priority: "high",
  assignee_type: "member",
  assignee_id: "user-1",
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  stage: null,
  start_date: null,
  due_date: "2026-06-01T00:00:00Z",
  metadata: {},
  properties: {},
  created_at: "2026-01-15T00:00:00Z",
  updated_at: "2026-01-20T00:00:00Z",
  revision: 3,
};

import { IssueDetail } from "./issue-detail";

function renderIssueDetail(issueId = "issue-1") {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <IssueDetail issueId={issueId} />
    </QueryClientProvider>,
  );
}

describe("IssueDetail (shared)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApiObj.getIssue.mockResolvedValue(mockIssue);
  });

  describe("sub-issues list", () => {
    beforeEach(() => {
      useSubIssueDisplayStore.setState({
        rowProperties: { ...DEFAULT_SUB_ISSUE_ROW_PROPERTIES },
        rowPropertyIds: [],
      });
    });

    const label = (id: string, name: string): Label => ({
      id,
      workspace_id: "ws-1",
      resource_type: "issue",
      name,
      color: "#3b82f6",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    });

    const subIssue = (overrides: Partial<Issue>): Issue => ({
      ...mockIssue,
      parent_issue_id: "issue-1",
      assignee_type: null,
      assignee_id: null,
      due_date: null,
      priority: "none",
      ...overrides,
    });

    it("AG patch: sub-issue rows show every label as a wrapping Full row, with no +N", async () => {
      const names = ["backend", "auth", "billing", "urgent-fix", "regression"];
      mockApiObj.listChildIssues.mockResolvedValue({
        issues: [
          subIssue({
            id: "child-1",
            number: 11,
            identifier: "TES-11",
            title: "Fix login flow",
            labels: names.map((name, i) => label(`l${i}`, name)),
          }),
        ],
      });

      renderIssueDetail();

      const rowLink = (await screen.findByText("Fix login flow")).closest("a")!;
      for (const name of names) {
        expect(within(rowLink).getByText(name)).toBeInTheDocument();
      }
      expect(rowLink.textContent).not.toMatch(/\+\d/);
      const labelRow = rowLink.querySelector<HTMLElement>('[data-slot="ag-label-row"]')!;
      // The sub-issue display preference stays a boolean and renders Full.
      expect(labelRow.dataset.display).toBe("full");
      expect(labelRow.className).toContain("flex-wrap");
      expect(labelRow.className).not.toContain("overflow-hidden");
      expect(Array.from(labelRow.children).map((c) => c.getAttribute("aria-label"))).toEqual(names);
    });
  });
});
