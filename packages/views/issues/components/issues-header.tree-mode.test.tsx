// @vitest-environment jsdom

/**
 * AG patch: the "Tree" layout of the Display panel is opt-in per surface, like
 * Gantt. Real Base UI primitives, same as the display-layout test.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
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

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

const never = () => new Promise<never>(() => {});

function renderControls(opts: { allowTree?: boolean; viewMode: ViewMode }) {
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
        <IssueDisplayControls scopedIssues={[]} allowTree={opts.allowTree} />
      </ViewStoreProvider>
    </QueryClientProvider>,
  );
  return store;
}

async function openLayouts() {
  fireEvent.click(screen.getByRole("button", { name: "Display" }));
  return screen.findByRole("group", { name: "View" });
}

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("AG patch: Display panel Tree layout", () => {
  it("offers Tree where the surface renders it and switches to it", async () => {
    const store = renderControls({ allowTree: true, viewMode: "list" });
    const layouts = await openLayouts();

    fireEvent.click(within(layouts).getByRole("button", { name: "Tree" }));
    await waitFor(() => expect(store.getState().viewMode).toBe("tree"));
  });

  it("marks Tree as the current layout when that mode is active", async () => {
    renderControls({ allowTree: true, viewMode: "tree" });
    const layouts = await openLayouts();
    expect(within(layouts).getByRole("button", { name: "Tree" })).toHaveAttribute("aria-pressed", "true");
  });

  it("hides Tree on surfaces that do not render it", async () => {
    renderControls({ viewMode: "list" });
    const layouts = await openLayouts();
    expect(within(layouts).queryByRole("button", { name: "Tree" })).toBeNull();
  });

  it("shows List as current, not Tree, when a stored tree mode meets such a surface", async () => {
    renderControls({ viewMode: "tree" });
    const layouts = await openLayouts();
    expect(within(layouts).getByRole("button", { name: "List" })).toHaveAttribute("aria-pressed", "true");
    expect(within(layouts).queryByRole("button", { name: "Tree" })).toBeNull();
  });
});
