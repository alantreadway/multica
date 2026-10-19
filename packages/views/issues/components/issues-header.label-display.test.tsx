// @vitest-environment jsdom

/**
 * AG patch: the Labels Off / Compact / Full chooser in the Display popover,
 * replacing the on/off "Labels" card-property toggle.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

function renderControls(viewMode: ViewMode, labels?: unknown) {
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
  store.getState().setViewMode(viewMode);
  if (labels !== undefined) {
    store.setState({
      cardProperties: { ...store.getState().cardProperties, labels: labels as never },
    });
  }
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <ViewStoreProvider store={store}>
        <IssueDisplayControls scopedIssues={[]} allowTree={false} />
      </ViewStoreProvider>
    </QueryClientProvider>,
  );
  return store;
}

const openDisplay = async () => {
  fireEvent.click(await screen.findByRole("button", { name: /Display/ }));
  await screen.findByText("Card properties");
};

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

describe("AG patch: Display popover label chooser", () => {
  it("offers Off / Compact / Full instead of a Labels toggle, defaulting to Off", async () => {
    renderControls("board");
    await openDisplay();

    expect(screen.getByRole("radiogroup", { name: "Labels" })).toBeInTheDocument();
    expect(screen.getAllByRole("radio").map((r) => r.textContent)).toEqual([
      "Off",
      "Compact",
      "Full",
    ]);
    expect(screen.getByRole("radio", { name: "Off" })).toHaveAttribute("aria-checked", "true");
    // The other card properties are still plain toggles; Labels is not one.
    expect(screen.getByRole("button", { name: "Priority" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Labels" })).not.toBeInTheDocument();
  });

  it("writes the choice to the view's card properties without touching the other toggles", async () => {
    const user = userEvent.setup();
    const store = renderControls("board");
    const before = { ...store.getState().cardProperties };
    await openDisplay();

    await user.click(screen.getByRole("radio", { name: "Compact" }));
    expect(store.getState().cardProperties).toEqual({ ...before, labels: "compact" });
    expect(screen.getByRole("radio", { name: "Compact" })).toHaveAttribute("aria-checked", "true");

    await user.click(screen.getByRole("radio", { name: "Full" }));
    expect(store.getState().cardProperties.labels).toBe("full");

    await user.click(screen.getByRole("radio", { name: "Off" }));
    expect(store.getState().cardProperties.labels).toBe("off");
  });

  it("is also available in the list layout", async () => {
    renderControls("list");
    await openDisplay();
    expect(screen.getByRole("radiogroup", { name: "Labels" })).toBeInTheDocument();
  });

  it("shows an un-migrated persisted boolean as Full / Off", async () => {
    renderControls("board", true);
    await openDisplay();
    expect(screen.getByRole("radio", { name: "Full" })).toHaveAttribute("aria-checked", "true");
  });

  it("is absent in layouts that have no card properties (table)", async () => {
    renderControls("table");
    fireEvent.click(await screen.findByRole("button", { name: /Display/ }));
    await screen.findByText(/Ordering/);
    expect(screen.queryByRole("radiogroup", { name: "Labels" })).not.toBeInTheDocument();
  });
});
