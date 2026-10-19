import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { act, cleanup, fireEvent, screen, within } from "@testing-library/react";
import type { DragEndEvent } from "@dnd-kit/core";
import { renderWithI18n } from "../test/i18n";
import { LabelManager } from "./label-manager";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

const state = vi.hoisted(() => ({
  data: [] as unknown[],
  mutate: undefined as unknown as Mock<(...args: any[]) => void>,
  // Captured so a test can finish a drag without real pointer geometry,
  // which jsdom does not lay out.
  onDragEnd: undefined as ((event: DragEndEvent) => void) | undefined,
  toastError: undefined as unknown as Mock<(...args: any[]) => void>,
}));

vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: () => ({ data: state.data, isLoading: false }),
}));

vi.mock("sonner", () => ({
  toast: { error: (...args: unknown[]) => state.toastError(...args) },
}));

vi.mock("@dnd-kit/core", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@dnd-kit/core")>();
  return {
    ...actual,
    DndContext: (props: React.ComponentProps<typeof actual.DndContext>) => {
      state.onDragEnd = props.onDragEnd as (event: DragEndEvent) => void;
      return <actual.DndContext {...props} />;
    },
  };
});

vi.mock("@multica/core/labels", () => ({
  labelListOptions: (wsId: string, resourceType: string) => ({
    queryKey: ["labels", wsId, "list", resourceType],
  }),
  useCreateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLabel: () => ({ mutate: vi.fn(), isPending: false }),
  useReorderLabels: () => ({ mutate: (...args: unknown[]) => state.mutate(...args), isPending: false }),
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

const drop = (active: string, over: string | null) =>
  act(() => {
    state.onDragEnd!({ active: { id: active }, over: over ? { id: over } : null } as DragEndEvent);
  });

// dnd-kit attaches its keyboard listeners and measures rows on timers, so a
// keyboard drag needs real time to pass between keys.
const tick = () => act(() => new Promise<void>((resolve) => setTimeout(resolve, 50)));

// Row order as the user sees it: the reorder handle names, top to bottom.
const visibleOrder = () =>
  screen
    .getAllByRole("button", { name: /^Reorder / })
    .map((b) => b.getAttribute("aria-label")!.replace("Reorder ", ""));

describe("Manage labels drag-to-reorder", () => {
  beforeEach(() => {
    state.data = [label("a", "alpha", 0), label("b", "beta", 1), label("c", "gamma", 2)];
    state.mutate = vi.fn();
    state.toastError = vi.fn();
  });
  afterEach(cleanup);

  it("lists labels in the order the server returned them", () => {
    state.data = [label("c", "gamma", 0), label("a", "alpha", 1), label("b", "beta", 2)];
    renderWithI18n(<LabelManager scope="issue" />);

    expect(visibleOrder()).toEqual(["gamma", "alpha", "beta"]);
  });

  it("sends the full new order for the managed catalog and shows it at once", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("a", "c");

    expect(state.mutate).toHaveBeenCalledTimes(1);
    expect(state.mutate.mock.calls[0]![0]).toEqual({
      resource_type: "issue",
      label_ids: ["b", "c", "a"],
    });
    expect(visibleOrder()).toEqual(["beta", "gamma", "alpha"]);
  });

  it("moves a label up as well as down", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("c", "a");

    expect(state.mutate.mock.calls[0]![0].label_ids).toEqual(["c", "a", "b"]);
  });

  it("reorders only the skill catalog when managing skill labels", () => {
    renderWithI18n(<LabelManager scope="skill" />);

    drop("a", "b");

    expect(state.mutate.mock.calls[0]![0]).toEqual({
      resource_type: "skill",
      label_ids: ["b", "a", "c"],
    });
  });

  it("ignores a drop that goes nowhere or onto the same row", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("a", null);
    drop("a", "a");
    drop("a", "not-a-label");

    expect(state.mutate).not.toHaveBeenCalled();
    expect(visibleOrder()).toEqual(["alpha", "beta", "gamma"]);
  });

  it("rolls the rows back and says so when the server rejects the order", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("a", "c");
    expect(visibleOrder()).toEqual(["beta", "gamma", "alpha"]);

    const options = state.mutate.mock.calls[0]![1] as {
      onError: (e: unknown) => void;
      onSettled: () => void;
    };
    act(() => {
      options.onError(new Error("label list is out of date"));
      options.onSettled();
    });

    expect(visibleOrder()).toEqual(["alpha", "beta", "gamma"]);
    expect(state.toastError).toHaveBeenCalledWith("label list is out of date");
  });

  it("falls back to a generic message when the failure is not an Error", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("a", "b");
    act(() => (state.mutate.mock.calls[0]![1] as { onError: (e: unknown) => void }).onError("boom"));

    expect(state.toastError).toHaveBeenCalledWith("Couldn't save the new order");
  });

  it("ignores a second drop while the first is still saving, then accepts one", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    drop("a", "b");
    drop("b", "c");
    expect(state.mutate).toHaveBeenCalledTimes(1);

    act(() => (state.mutate.mock.calls[0]![1] as { onSettled: () => void }).onSettled());
    drop("c", "b");
    expect(state.mutate).toHaveBeenCalledTimes(2);
  });

  it("picks up a changed server order", () => {
    const { rerender } = renderWithI18n(<LabelManager scope="issue" />);
    expect(visibleOrder()).toEqual(["alpha", "beta", "gamma"]);

    state.data = [label("b", "beta", 0), label("a", "alpha", 1), label("c", "gamma", 2)];
    rerender(<LabelManager scope="issue" />);

    expect(visibleOrder()).toEqual(["beta", "alpha", "gamma"]);
  });

  it("gives every handle a name that identifies its label", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    const handle = screen.getByRole("button", { name: "Reorder beta" });
    expect(within(handle.parentElement!).getByText("beta")).toBeInTheDocument();
  });

  it("makes each handle a focusable sortable control", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    for (const name of ["alpha", "beta", "gamma"]) {
      const handle = screen.getByRole("button", { name: `Reorder ${name}` });
      expect(handle).toHaveAttribute("aria-roledescription", "sortable");
      expect(handle).toHaveAttribute("tabindex", "0");
      expect(handle).toHaveAttribute("aria-describedby");
    }
  });

  it("offers no handles while the list is filtered", () => {
    renderWithI18n(<LabelManager scope="issue" />);

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "alp" } });

    expect(screen.queryByRole("button", { name: /^Reorder / })).not.toBeInTheDocument();
  });

  it("reorders from the keyboard: Space, arrow, Space", async () => {
    renderWithI18n(<LabelManager scope="issue" />);
    // jsdom lays nothing out, so give the rows stacked rects for dnd-kit's
    // keyboard coordinate getter to step between.
    for (const [i, name] of ["alpha", "beta", "gamma"].entries()) {
      const row = screen.getByRole("button", { name: `Reorder ${name}` }).parentElement!;
      row.getBoundingClientRect = () =>
        ({ top: i * 50, bottom: i * 50 + 50, left: 0, right: 300, width: 300, height: 50, x: 0, y: i * 50 }) as DOMRect;
    }
    const handle = screen.getByRole("button", { name: "Reorder alpha" });
    handle.focus();

    fireEvent.keyDown(handle, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowDown", key: "ArrowDown" });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await tick();

    expect(state.mutate).toHaveBeenCalledTimes(1);
    expect(state.mutate.mock.calls[0]![0]).toEqual({
      resource_type: "issue",
      label_ids: ["b", "a", "c"],
    });
    expect(visibleOrder()).toEqual(["beta", "alpha", "gamma"]);
  });
});
