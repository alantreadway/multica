import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { Issue, Label } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { PillButton } from "../../common/pill-button";
import { IssuePropertyPills } from "./issue-property-pills";

const NAMES = ["Bug", "Frontend", "Needs design", "Customer-reported"];

const labels: Label[] = NAMES.map((name, i) => ({
  id: `label-${i}`,
  workspace_id: "ws-1",
  name,
  color: "#3b82f6",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: labels }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ settings: () => "/acme/settings" }),
}));

vi.mock("@multica/core/labels", () => ({
  labelListOptions: () => ({ queryKey: ["labels", "list"] }),
  issueLabelsOptions: () => ({ queryKey: ["labels", "issue"] }),
  useAttachLabel: () => ({ mutate: vi.fn() }),
  useDetachLabel: () => ({ mutate: vi.fn() }),
  useCreateLabel: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: vi.fn() }),
}));

// Only the label pill is under test; the sibling pickers pull in members,
// agents, projects and statuses.
vi.mock("./pickers", async () => {
  const actual = await vi.importActual<typeof import("./pickers")>("./pickers");
  return {
    ...actual,
    StatusPicker: () => null,
    PriorityPicker: () => null,
    AssigneePicker: () => null,
    StartDatePicker: () => null,
    DueDatePicker: () => null,
  };
});

vi.mock("../../projects/components/project-picker", () => ({
  ProjectPicker: () => null,
}));

const issue = {
  id: "issue-1",
  start_date: null,
  due_date: null,
  labels,
} as unknown as Issue;

describe("AG patch: detail-header label pill", () => {
  it("shows every label and lets the pill wrap at full width instead of clipping", () => {
    renderWithI18n(<IssuePropertyPills issue={issue} onUpdate={vi.fn()} />);

    for (const name of NAMES) {
      expect(screen.getByText(name)).toBeInTheDocument();
    }
    const pill = screen.getByText(NAMES[0]!).closest("button")!;
    const classes = pill.className.split(/\s+/);
    // PillButton's base `max-w-56 overflow-hidden` no longer applies here.
    expect(classes).toContain("max-w-full");
    expect(classes).toContain("flex-wrap");
    expect(classes).toContain("overflow-visible");
    expect(classes).not.toContain("max-w-56");
    expect(classes).not.toContain("overflow-hidden");
    // All four chips are inside the one pill.
    for (const name of NAMES) {
      expect(pill).toContainElement(screen.getByText(name));
    }
  });

  it("leaves PillButton's own base class capped for every other pill", () => {
    renderWithI18n(<PillButton>Other</PillButton>);
    const classes = screen.getByRole("button", { name: "Other" }).className.split(/\s+/);
    expect(classes).toContain("max-w-56");
    expect(classes).toContain("overflow-hidden");
  });
});
