import { createStore } from "zustand/vanilla";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { renderWithI18n } from "../../test/i18n";
import { DraftDefinitionFields } from "./save-view-dialog";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/issue-views/mutations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issue-views/mutations")>()),
  useCreateIssueView: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateIssueView: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("./issues-header", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./issues-header")>()),
  IssueFilterMenu: ({ trigger }: { trigger: React.ReactNode }) => trigger,
}));

vi.mock("./filter-chips-bar", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./filter-chips-bar")>()),
  FilterChipList: ({ trailing }: { trailing?: React.ReactNode }) => trailing,
}));

function renderFields(sortBy: IssueViewState["sortBy"]) {
  const store = createStore<IssueViewState>()(viewStoreSlice);
  store.setState({ sortBy, sortDirection: "asc" });
  renderWithI18n(
    <ViewStoreProvider store={store}>
      <DraftDefinitionFields />
    </ViewStoreProvider>,
  );
  return store;
}

describe("AG patch: saved-view layout picker", () => {
  it("offers Tree as a layout and stores it on the draft", async () => {
    const user = userEvent.setup();
    const store = renderFields("position");

    await user.click(screen.getByRole("button", { name: /Default display/ }));
    await user.click(screen.getByRole("combobox", { name: /Layout/ }));
    await user.click(await screen.findByRole("option", { name: "Tree" }));

    expect(store.getState().viewMode).toBe("tree");
  });
});
