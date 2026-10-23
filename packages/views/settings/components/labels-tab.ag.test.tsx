import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { LabelManager } from "../../labels/label-manager";

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "workspace-1",
}));

// One stable array per render, like a real query result.
const queried = vi.hoisted(() => ({ data: [] as unknown[] }));
vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: () => ({ data: queried.data, isLoading: false }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

vi.mock("@multica/core/labels", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/labels")>()),
  labelListOptions: (wsId: string, resourceType: string) => ({
    queryKey: ["labels", wsId, "list", resourceType],
  }),
  useCreateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useReorderLabels: () => ({ mutate: vi.fn(), isPending: false }),
}));

const label = (id: string, name: string, position: number) => ({
  id,
  workspace_id: "workspace-1",
  resource_type: "issue",
  name,
  description: "",
  color: "#3b82f6",
  position,
  usage_count: 0,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
});

describe("Label catalogs", () => {
  afterEach(() => {
    cleanup();
    queried.data = [];
  });

  it("offers a drag handle per label, but not while the list is filtered", async () => {
    queried.data = [label("l1", "bug", 0), label("l2", "feature", 1)];
    const user = userEvent.setup();
    renderWithI18n(<LabelManager scope="issue" />);

    expect(screen.getByRole("button", { name: "Reorder bug" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reorder feature" })).toBeInTheDocument();

    await user.type(screen.getByRole("textbox"), "bug");
    expect(screen.queryByRole("button", { name: /Reorder/ })).toBeNull();
  });

  it("has nothing to reorder with a single label", () => {
    queried.data = [label("l1", "bug", 0)];
    renderWithI18n(<LabelManager scope="issue" />);

    expect(screen.queryByRole("button", { name: /Reorder/ })).toBeNull();
  });
});
