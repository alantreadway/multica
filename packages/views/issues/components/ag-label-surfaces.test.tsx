import { render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DndContext } from "@dnd-kit/core";
import { SortableContext } from "@dnd-kit/sortable";
import type { Issue } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { DraggableBoardCard } from "./board-card";
import { ListRow } from "./list-row";

// What the view store hands the cards. Tests set `labels` per case; a legacy
// boolean is allowed because persisted state can still hold one until it is
// rehydrated.
const view = vi.hoisted(() => ({ labels: "off" as unknown }));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/properties", () => ({
  propertyListOptions: () => ({ queryKey: ["properties"] }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/acme/issues/${id}` }),
}));

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  useViewStore: (selector: (state: unknown) => unknown) =>
    selector({
      viewMode: "board",
      grouping: "status",
      swimlaneGrouping: "assignee",
      cardProperties: { labels: view.labels },
      cardPropertyIds: [],
    }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Someone" }),
}));

vi.mock("../../i18n", () => ({
  useLocale: () => "en",
  useT: () => ({ t: () => "Translated" }),
  useTimeAgo: () => () => "now",
}));

vi.mock("../actions", () => ({
  IssueActionsContextMenu: ({ children }: { children: ReactNode }) => children,
}));

vi.mock("./issue-agent-activity-indicator", () => ({
  IssueAgentActivityIndicator: () => null,
}));

vi.mock("./custom-status-chip", () => ({
  CustomStatusChip: () => null,
  useIsCustomStatus: () => false,
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => `https://app.example${path}`,
};

const NAMES = ["Bug", "Frontend", "Needs design", "Customer-reported", "Regression"];

function makeIssue(labelCount: number): Issue {
  return {
    id: "issue-1",
    identifier: "MUL-1",
    title: "Labelled issue",
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    labels: NAMES.slice(0, labelCount).map((name, i) => ({
      id: `label-${i}`,
      workspace_id: "ws-1",
      name,
      color: "#3b82f6",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    })),
    properties: {},
    start_date: null,
    due_date: null,
    updated_at: "2026-09-27T00:00:00Z",
  } as unknown as Issue;
}

function renderCard(issue: Issue) {
  return render(
    <NavigationProvider value={navigation}>
      <DndContext>
        <SortableContext items={[issue.id]}>
          <DraggableBoardCard issue={issue} />
        </SortableContext>
      </DndContext>
    </NavigationProvider>,
  );
}

function renderRow(issue: Issue) {
  return render(
    <NavigationProvider value={navigation}>
      <ListRow issue={issue} />
    </NavigationProvider>,
  );
}

const SURFACES = [
  ["board card", renderCard],
  ["list row", renderRow],
] as const;

const labelRow = (container: HTMLElement) =>
  container.querySelector<HTMLElement>('[data-slot="ag-label-row"]');

describe("AG patch: list row height with wrapped labels", () => {
  it("lets the row grow (min-h) instead of fixing its height", () => {
    view.labels = "full";
    const { container } = renderRow(makeIssue(4));
    const row = container.querySelector<HTMLElement>('[data-slot="issue-list-row"]')!;
    expect(row.className).toContain("min-h-[var(--issue-row-height)]");
    expect(row.className).not.toMatch(/(?:^|\s)h-\[var\(--issue-row-height\)\]/);
  });
});

describe.each(SURFACES)("AG patch: %s labels", (_name, renderSurface) => {
  beforeEach(() => {
    vi.clearAllMocks();
    view.labels = "off";
  });

  it.each(["compact", "full"] as const)(
    "shows all four labels in %s mode, with no +N",
    (display) => {
      view.labels = display;
      const { container } = renderSurface(makeIssue(4));
      const row = labelRow(container)!;
      expect(row.dataset.display).toBe(display);
      for (const name of NAMES.slice(0, 4)) {
        expect(within(row).getByText(name)).toBeInTheDocument();
      }
      expect(container.textContent).not.toMatch(/\+\d/);
    },
  );

  it("shows more labels than the old cap, not just the first two or three", () => {
    view.labels = "full";
    const { container } = renderSurface(makeIssue(5));
    expect(labelRow(container)!.children).toHaveLength(5);
  });

  it("hides every label when Off", () => {
    view.labels = "off";
    const { container } = renderSurface(makeIssue(4));
    expect(labelRow(container)).toBeNull();
    for (const name of NAMES.slice(0, 4)) {
      expect(screen.queryByText(name)).toBeNull();
    }
  });

  it("renders no label row for an issue without labels", () => {
    view.labels = "full";
    const { container } = renderSurface(makeIssue(0));
    expect(labelRow(container)).toBeNull();
  });

  it("still treats an un-migrated persisted boolean as Full / Off", () => {
    view.labels = true;
    const on = renderSurface(makeIssue(4));
    expect(labelRow(on.container)!.dataset.display).toBe("full");
    on.unmount();

    view.labels = false;
    const off = renderSurface(makeIssue(4));
    expect(labelRow(off.container)).toBeNull();
  });
});
