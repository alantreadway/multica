import { createStore } from "zustand/vanilla";
import { describe, expect, it, vi } from "vitest";
import type { IssueView } from "@multica/core/api/schemas";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { renderWithI18n } from "../../test/i18n";
import { DraftDefinitionFields, SaveViewDialog } from "./save-view-dialog";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "workspace-1",
}));

const mutations = vi.hoisted(() => ({ create: vi.fn(), update: vi.fn() }));

vi.mock("@multica/core/issue-views/mutations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/issue-views/mutations")>()),
  useCreateIssueView: () => ({ mutate: mutations.create, isPending: false }),
  useUpdateIssueView: () => ({ mutate: mutations.update, isPending: false }),
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

describe("AG patch: saved-view label display", () => {
  const editableView = (cardProperties?: Record<string, unknown>) =>
    ({
      id: "view-1",
      name: "Triage",
      visibility: "private",
      scope_type: "workspace",
      scope_id: null,
      scope_variant: null,
      definition_version: 1,
      revision: 3,
      query: {},
      display: cardProperties ? { cardProperties } : {},
    }) as unknown as IssueView;

  const renderEdit = (cardProperties?: Record<string, unknown>) => {
    mutations.update.mockReset();
    return renderWithI18n(
      <ViewStoreProvider store={createStore<IssueViewState>()(viewStoreSlice)}>
        <SaveViewDialog
          open
          onOpenChange={() => {}}
          scope={{ kind: "workspace" }}
          editView={editableView(cardProperties)}
          seedFromDefinition
        />
      </ViewStoreProvider>,
    );
  };

  const checked = () =>
    screen
      .getAllByRole("radio")
      .filter((r) => r.getAttribute("aria-checked") === "true")
      .map((r) => r.textContent);

  it("offers Off / Compact / Full for Labels in the default display and stores the choice on the draft", async () => {
    const user = userEvent.setup();
    const store = renderFields("created_at");

    await user.click(screen.getByRole("button", { name: /Default display/ }));
    expect(
      screen.getAllByRole("radio").map((r) => r.textContent),
    ).toEqual(["Off", "Compact", "Full"]);
    expect(checked()).toEqual(["Off"]);

    await user.click(screen.getByRole("radio", { name: "Compact" }));
    expect(store.getState().cardProperties.labels).toBe("compact");
    expect(checked()).toEqual(["Compact"]);

    await user.click(screen.getByRole("radio", { name: "Full" }));
    expect(store.getState().cardProperties.labels).toBe("full");
  });

  it("no longer offers Labels as an on/off toggle among the card properties", async () => {
    const user = userEvent.setup();
    renderFields("created_at");
    await user.click(screen.getByRole("button", { name: /Default display/ }));
    expect(screen.queryByRole("button", { name: "Labels" })).not.toBeInTheDocument();
  });

  it.each([
    [{ labels: true }, "Full"],
    [{ labels: false }, "Off"],
    [{}, "Off"],
    [undefined, "Off"],
    [{ labels: "compact" }, "Compact"],
  ] as const)(
    "editing a view whose stored cardProperties are %j shows %s",
    async (stored, expected) => {
      const user = userEvent.setup();
      renderEdit(stored);
      await user.click(screen.getByRole("button", { name: /Default display/ }));
      expect(checked()).toEqual([expected]);
    },
  );

  it("saves a legacy boolean back as the normalised string, without touching the other toggles", async () => {
    const user = userEvent.setup();
    renderEdit({ labels: true, description: true, priority: false });

    await user.click(screen.getByRole("button", { name: "Save changes" }));

    expect(mutations.update).toHaveBeenCalledTimes(1);
    const { display } = mutations.update.mock.calls[0]![0];
    expect(display.cardProperties.labels).toBe("full");
    expect(display.cardProperties.description).toBe(true);
    expect(display.cardProperties.priority).toBe(false);
  });

  it("saves the chosen display when creating a view", async () => {
    const user = userEvent.setup();
    mutations.create.mockReset();
    renderWithI18n(
      <ViewStoreProvider store={createStore<IssueViewState>()(viewStoreSlice)}>
        <SaveViewDialog open onOpenChange={() => {}} scope={{ kind: "workspace" }} />
      </ViewStoreProvider>,
    );
    await user.type(screen.getByLabelText("Name"), "Compact labels");
    await user.click(screen.getByRole("button", { name: /Default display/ }));
    await user.click(screen.getByRole("radio", { name: "Compact" }));
    await user.click(screen.getByRole("button", { name: "Create view" }));

    expect(mutations.create).toHaveBeenCalledTimes(1);
    expect(mutations.create.mock.calls[0]![0].display.cardProperties.labels).toBe("compact");
  });
});
