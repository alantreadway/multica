// @vitest-environment jsdom

/**
 * AG patch: the Outline | Chart choice in the Display menu. Real Base UI menu
 * primitives, like the Tree entry test beside it.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createStore } from "zustand/vanilla";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import {
  type IssueViewState,
  type ViewMode,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { renderWithI18n } from "../../test/i18n";
import { IssueDisplayControls } from "./issues-header";
import { TreeLayoutContext, type TreeLayoutControl } from "./tree-layout-context";

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

const never = () => new Promise<never>(() => {});

function renderControls(opts: {
  viewMode: ViewMode;
  control: TreeLayoutControl | null;
  allowTree?: boolean;
}) {
  setApiInstance({
    listIssueStatuses: async () => ({ statuses: [], categories: [], total: 0 }),
    listProperties: async () => ({ properties: [] }),
    listProjects: never,
    listLabels: never,
  } as unknown as ApiClient);
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const store = createStore<IssueViewState>()(viewStoreSlice);
  store.getState().setViewMode(opts.viewMode);
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <ViewStoreProvider store={store}>
        <TreeLayoutContext.Provider value={opts.control}>
          <IssueDisplayControls scopedIssues={[]} allowTree={opts.allowTree ?? true} />
        </TreeLayoutContext.Provider>
      </ViewStoreProvider>
    </QueryClientProvider>,
  );
}

const control = (over: Partial<TreeLayoutControl> = {}): TreeLayoutControl => ({
  layout: "outline",
  chartAvailability: "focus",
  setLayout: vi.fn(),
  ...over,
});

async function openViewMenu(name: RegExp = /^Tree$/) {
  fireEvent.click(await screen.findByRole("button", { name }));
  await screen.findByRole("menuitemradio", { name: "Board" });
}

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("AG patch: Display menu layout choice", () => {
  it("offers Outline and Chart in tree mode, with the current layout selected", async () => {
    renderControls({ viewMode: "tree", control: control({ layout: "chart" }) });
    await openViewMenu();

    expect(screen.getByRole("menuitemradio", { name: "Outline" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(screen.getByRole("menuitemradio", { name: "Chart" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(document.querySelector('[data-slot="tree-chart-hint"]')).toBeNull();
  });

  it("switches layout from the menu", async () => {
    const c = control();
    renderControls({ viewMode: "tree", control: c });
    await openViewMenu();

    fireEvent.click(screen.getByRole("menuitemradio", { name: "Chart" }));
    await waitFor(() => expect(c.setLayout).toHaveBeenCalledWith("chart"));
  });

  it("disables Chart with a hint where nothing is focused and no project is open", async () => {
    const c = control({ chartAvailability: null });
    renderControls({ viewMode: "tree", control: c });
    await openViewMenu();

    const chart = screen.getByRole("menuitemradio", { name: "Chart" });
    expect(chart).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Focus on an issue to see it as a chart.")).toBeInTheDocument();
    fireEvent.click(chart);
    expect(c.setLayout).not.toHaveBeenCalled();
  });

  it("enables Chart without a focused node on a project page", async () => {
    const c = control({ chartAvailability: "project" });
    renderControls({ viewMode: "tree", control: c });
    await openViewMenu();

    const chart = screen.getByRole("menuitemradio", { name: "Chart" });
    expect(chart).not.toHaveAttribute("aria-disabled", "true");
    expect(document.querySelector('[data-slot="tree-chart-hint"]')).toBeNull();
    fireEvent.click(chart);
    await waitFor(() => expect(c.setLayout).toHaveBeenCalledWith("chart"));
  });

  it("returns to the outline from the chart", async () => {
    const c = control({ layout: "chart", chartAvailability: "focus" });
    renderControls({ viewMode: "tree", control: c });
    await openViewMenu();

    fireEvent.click(screen.getByRole("menuitemradio", { name: "Outline" }));
    await waitFor(() => expect(c.setLayout).toHaveBeenCalledWith("outline"));
  });

  it("leaves the layout section out of the other view modes", async () => {
    renderControls({ viewMode: "list", control: control() });
    await openViewMenu(/^List$/);
    expect(screen.queryByRole("menuitemradio", { name: "Outline" })).toBeNull();
    expect(screen.queryByRole("menuitemradio", { name: "Chart" })).toBeNull();
  });

  it("leaves it out where the surface does not offer tree mode", async () => {
    renderControls({ viewMode: "tree", control: null, allowTree: false });
    await openViewMenu(/^List$/);
    expect(screen.queryByRole("menuitemradio", { name: "Chart" })).toBeNull();
  });

  it("renders nothing for the layout when no surface provides it", async () => {
    renderControls({ viewMode: "tree", control: null });
    await openViewMenu();
    expect(screen.queryByRole("menuitemradio", { name: "Chart" })).toBeNull();
  });
});
