import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { LabelsTab } from "./labels-tab";
import { LabelManager } from "../../labels/label-manager";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

const queried = vi.hoisted(() => ({
  keys: [] as unknown[][],
  // One stable array per render, like a real query result.
  data: [] as unknown[],
}));
vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: (options: { queryKey: unknown[] }) => {
    queried.keys.push(options.queryKey);
    return { data: queried.data, isLoading: false };
  },
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

vi.mock("@multica/core/labels", () => ({
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
    queried.keys = [];
    queried.data = [];
  });

  // Agent labels were removed from the product (MUL-5600) even though the
  // backend still models the `agent` resource type.
  it("switches between the issue and skill catalogs, never agent labels", async () => {
    const user = userEvent.setup();
    renderWithI18n(<LabelsTab />);

    const issues = screen.getByRole("tab", { name: /Issues/ });
    expect(issues).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: /Skills/ })).toHaveAttribute("aria-selected", "false");
    expect(screen.queryByRole("tab", { name: /Agents/ })).toBeNull();

    queried.keys = [];
    await user.click(screen.getByRole("tab", { name: /Skills/ }));

    expect(screen.getByRole("tab", { name: /Skills/ })).toHaveAttribute("aria-selected", "true");
    expect(queried.keys).toContainEqual(["labels", "workspace-1", "list", "skill"]);
    expect(queried.keys.some((key) => key.includes("agent"))).toBe(false);
  });

  it("manages the skill catalog where it is mounted for skills", () => {
    renderWithI18n(<LabelManager scope="skill" />);

    expect(queried.keys).toContainEqual(["labels", "workspace-1", "list", "skill"]);
    expect(screen.getByRole("button", { name: /New label/ })).toBeInTheDocument();
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
