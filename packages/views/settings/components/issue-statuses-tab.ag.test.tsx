/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { IssueStatusEntry } from "@multica/core/types";
import en from "../../locales/en/settings.json";
import { IssueStatusesTab } from "./issue-statuses-tab";
import { ApiError } from "@multica/core/api/client";

const archiveMutate = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({ issues: () => "/dev/issues" }),
  useCurrentWorkspace: () => ({ id: "ws-1", settings: {} }),
}));
vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/acme/settings",
    searchParams: new URLSearchParams("tab=issue-statuses"),
  }),
}));
vi.mock("../../issues/surface/issue-surface", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../issues/surface/issue-surface")>()),
  IssueSurfaceWithStore: ({ modes }: { modes: string[] }) =>
    <div data-testid="inspection-list" data-modes={modes.join(",")} />,
}));
let catalog: IssueStatusEntry[] = [];

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey: readonly unknown[] }) => ({
    data: options.queryKey[0] === "issue-statuses" ? catalog : [{ user_id: "u-1", role: "owner" }],
    isLoading: false,
  }),
  useQueryClient: () => ({ setQueryData: vi.fn() }),
}));
vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { id: "u-1" } }),
}));
vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  memberListOptions: () => ({ queryKey: ["members", "ws-1"] }),
}));
vi.mock("@multica/core/issue-statuses/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issue-statuses/queries")>()),
  issueStatusListOptions: () => ({ queryKey: ["issue-statuses", "ws-1"] }),
}));
vi.mock("@multica/core/issue-statuses/mutations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issue-statuses/mutations")>()),
  useCreateIssueStatus: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateIssueStatus: () => ({ mutate: vi.fn(), isPending: false }),
  useArchiveIssueStatus: () => ({ mutate: archiveMutate, isPending: false }),
  useReorderIssueStatuses: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../i18n", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../i18n")>()),
  useT: () => ({
    t: (accessor: (dict: unknown) => string, params?: Record<string, unknown>) => {
      const template = accessor(en);
      if (!params) return template;
      return template.replace(/\{\{(\w+)\}\}/g, (_, k: string) => String(params[k] ?? ""));
    },
  }),
}));

function entry(overrides: Partial<IssueStatusEntry>): IssueStatusEntry {
  return {
    id: overrides.key ?? "id",
    workspace_id: "ws-1",
    key: "custom",
    name: "Custom",
    description: "",
    category: "started",
    color: "#ff0000",
    is_system: false,
    position: 1,
    archived_at: null,
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
  archiveMutate.mockReset();
  catalog = [];
});

describe("IssueStatusesTab", () => {
  it("AG patch: the status inspection list never offers the Tree layout", async () => {
    catalog = [entry({ key: "shipped", name: "Shipped", category: "done" })];
    archiveMutate.mockImplementation((_id, options) => options.onError(new ApiError("in use", 409, "Conflict", {
      code: "issue_status_in_use", issue_count: 120,
    })));
    render(<IssueStatusesTab />);
    await userEvent.click(screen.getByLabelText(en.issue_statuses.actions.open.replace("{{name}}", "Shipped")));
    await userEvent.click(await screen.findByRole("menuitem", { name: en.issue_statuses.actions.archive }));
    await userEvent.click(screen.getByRole("button", { name: en.issue_statuses.archive_dialog.confirm }));
    const dialog = screen.getByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: en.issue_statuses.archive_dialog.view_issues }));
    expect(screen.getByTestId("inspection-list")).toHaveAttribute("data-modes", "list");
  });
});
