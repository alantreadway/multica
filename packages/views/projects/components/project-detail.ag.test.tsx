import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Project } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { ProjectDetail } from "./project-detail";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    switch (options.queryKey?.[0]) {
      case "project-detail":
        return { data: PROJECT, isLoading: false };
      case "members":
        return {
          data: [{ user_id: "user-1", name: "User One", role: "admin" }],
          isLoading: false,
        };
      case "agents":
      case "pins":
        return { data: [], isLoading: false };
      default:
        return { data: undefined, isLoading: false };
    }
  },
}));

vi.mock("../../workflows/project-workflow-menu", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../workflows/project-workflow-menu")>()),
  ProjectWorkflowMenu: () => null,
}));

vi.mock("@multica/core/projects/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/projects/queries")>()),
  projectDetailOptions: () => ({ queryKey: ["project-detail"] }),
}));

vi.mock("@multica/core/projects/mutations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/projects/mutations")>()),
  useUpdateProject: () => ({ mutate: vi.fn() }),
  useDeleteProject: () => ({ mutate: vi.fn() }),
}));

vi.mock("@multica/core/pins", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/pins")>()),
  pinListOptions: () => ({ queryKey: ["pins"] }),
  useCreatePin: () => ({ mutate: vi.fn() }),
  useDeletePin: () => ({ mutate: vi.fn() }),
}));

vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  memberListOptions: () => ({ queryKey: ["members"] }),
  agentListOptions: () => ({ queryKey: ["agents"] }),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    projects: () => "/test-workspace/projects",
  }),
}));

vi.mock("../../editor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../editor")>()),
  TitleEditor: ({ defaultValue }: { defaultValue: string }) => (
    <div>{defaultValue}</div>
  ),
  ContentEditor: () => null,
}));

const surfaceModes = vi.hoisted(() => ({ seen: [] as string[][] }));
vi.mock("../../issues/surface/issue-surface", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../issues/surface/issue-surface")>()),
  IssueSurface: ({ modes }: { modes: string[] }) => {
    surfaceModes.seen.push(modes);
    return null;
  },
}));

const PROJECT: Project = {
  id: "project-1",
  workspace_id: "workspace-1",
  title: "Launch Plan",
  description: null,
  icon: null,
  status: "in_progress",
  priority: "high",
  lead_type: null,
  lead_id: null,
  start_date: null,
  due_date: null,
  created_at: "2026-06-01T00:00:00Z",
  updated_at: "2026-06-01T00:00:00Z",
  issue_count: 3,
  done_count: 1,
  resource_count: 0,
};

function renderProjectDetail() {
  const adapter: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/test-workspace/projects/project-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => `https://app.example${path}`,
  };

  renderWithI18n(
    <QueryClientProvider client={new QueryClient()}>
      <NavigationProvider value={adapter}>
        <ProjectDetail projectId={PROJECT.id} />
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

describe("AG patch: ProjectDetail layouts", () => {
  it("offers Tree on the project page alongside the existing layouts", () => {
    surfaceModes.seen = [];
    renderProjectDetail();
    expect(surfaceModes.seen.at(-1)).toEqual(
      expect.arrayContaining(["board", "list", "table", "swimlane", "gantt", "tree"]),
    );
  });
});
