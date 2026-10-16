// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import {
  MAX_EXPANDED_PER_SURFACE,
  selectTreeExpanded,
  useTreeViewStore,
} from "./tree-view-store";
import { cardPropertyOptionsForView } from "./view-store";

describe("AG patch: tree view store", () => {
  beforeEach(() => {
    useTreeViewStore.setState({ expanded: {} });
  });

  it("starts collapsed and toggles an id on and off per surface", () => {
    const { toggleExpanded } = useTreeViewStore.getState();
    expect(selectTreeExpanded("project:p1")(useTreeViewStore.getState())).toEqual([]);

    toggleExpanded("project:p1", "a");
    toggleExpanded("project:p1", "b");
    toggleExpanded("workspace:all", "a");
    expect(selectTreeExpanded("project:p1")(useTreeViewStore.getState())).toEqual(["a", "b"]);
    expect(selectTreeExpanded("workspace:all")(useTreeViewStore.getState())).toEqual(["a"]);

    toggleExpanded("project:p1", "a");
    expect(selectTreeExpanded("project:p1")(useTreeViewStore.getState())).toEqual(["b"]);
  });

  it("returns one stable empty array for an untouched surface", () => {
    const select = selectTreeExpanded("never-opened");
    expect(select(useTreeViewStore.getState())).toBe(select(useTreeViewStore.getState()));
  });

  it("replaces a surface's ids without duplicates, and caps how many it keeps", () => {
    const { setExpanded } = useTreeViewStore.getState();
    setExpanded("s", ["a", "b", "a"]);
    expect(selectTreeExpanded("s")(useTreeViewStore.getState())).toEqual(["a", "b"]);

    const many = Array.from({ length: MAX_EXPANDED_PER_SURFACE + 5 }, (_, i) => `id-${i}`);
    setExpanded("s", many);
    const kept = selectTreeExpanded("s")(useTreeViewStore.getState());
    expect(kept).toHaveLength(MAX_EXPANDED_PER_SURFACE);
    expect(kept.at(-1)).toBe(`id-${MAX_EXPANDED_PER_SURFACE + 4}`);

    setExpanded("s", []);
    expect(selectTreeExpanded("s")(useTreeViewStore.getState())).toEqual([]);
  });
});

describe("AG patch: tree mode card properties", () => {
  it("offers no card property toggles, like Table and Gantt", () => {
    expect(cardPropertyOptionsForView("tree")).toEqual([]);
  });
});
