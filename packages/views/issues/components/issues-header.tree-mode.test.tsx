// @vitest-environment jsdom

/**
 * AG patch: the "Tree" entry of the view menu is opt-in per surface, like Gantt.
 * Real Base UI menu primitives, same as the status-filter test.
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

vi.mock("@multica/core/hooks", () => ({
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

async function openViewMenu(triggerName: RegExp) {
  fireEvent.click(await screen.findByRole("button", { name: triggerName }));
  await screen.findByRole("menuitemradio", { name: "Board" });
}

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("AG patch: view menu Tree entry", () => {
  it("offers Tree where the surface renders it and switches to it", async () => {
    const store = renderControls({ allowTree: true, viewMode: "list" });
    await openViewMenu(/^List$/);

    fireEvent.click(screen.getByRole("menuitemradio", { name: "Tree" }));
    await waitFor(() => expect(store.getState().viewMode).toBe("tree"));
  });

  it("labels the trigger Tree when that mode is active", async () => {
    renderControls({ allowTree: true, viewMode: "tree" });
    expect(await screen.findByRole("button", { name: /^Tree$/ })).toBeInTheDocument();
  });

  it("hides Tree on surfaces that do not render it", async () => {
    renderControls({ viewMode: "list" });
    await openViewMenu(/^List$/);
    expect(screen.queryByRole("menuitemradio", { name: "Tree" })).toBeNull();
  });

  it("shows the List trigger, not Tree, when a stored tree mode meets such a surface", async () => {
    renderControls({ viewMode: "tree" });
    expect(await screen.findByRole("button", { name: /^List$/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Tree$/ })).toBeNull();
  });
});
