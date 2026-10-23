// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";

const storage = vi.hoisted(() => new Map<string, string>());
vi.mock("../../platform/storage", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/storage")>()),
  defaultStorage: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  },
}));

import { setCurrentWorkspace } from "../../platform/workspace-storage";
import { selectTreeLayout, useTreeLayoutStore } from "./tree-layout-store";

// AG patch: the layout choice survives a reload, per surface.

const savedKey = () => [...storage.keys()].find((key) => key.includes("multica_issue_tree_layout"));

/** A page reload: memory is emptied (which also persists the empty state, so
 *  the saved value is put back) and the store reads storage again. */
async function reload(key: string) {
  const saved = storage.get(key)!;
  useTreeLayoutStore.setState({ layouts: {} });
  storage.set(key, saved);
  await useTreeLayoutStore.persist.rehydrate();
}

beforeEach(async () => {
  setCurrentWorkspace("acme", "ws-1");
  await Promise.resolve();
  storage.clear();
  useTreeLayoutStore.setState({ layouts: {} });
});

describe("AG patch: tree layout persistence", () => {
  it("writes a chart choice to storage and restores it on rehydration", async () => {
    useTreeLayoutStore.getState().setLayout("project:p1", "chart");
    useTreeLayoutStore.getState().setLayout("workspace:all", "chart");
    const key = savedKey()!;
    expect(key).toBeDefined();
    expect(JSON.parse(storage.get(key)!).state.layouts).toEqual({
      "project:p1": "chart",
      "workspace:all": "chart",
    });

    await reload(key);
    const state = useTreeLayoutStore.getState();
    expect(selectTreeLayout("project:p1")(state)).toBe("chart");
    expect(selectTreeLayout("workspace:all")(state)).toBe("chart");
    expect(selectTreeLayout("my-issues:mine")(state)).toBe("outline");
  });

  it("forgets a surface once it returns to the outline, also after a reload", async () => {
    const { setLayout } = useTreeLayoutStore.getState();
    setLayout("project:p1", "chart");
    setLayout("project:p2", "chart");
    setLayout("project:p1", "outline");

    await reload(savedKey()!);
    expect(useTreeLayoutStore.getState().layouts).toEqual({ "project:p2": "chart" });
  });

  it("keeps each workspace's choices apart", async () => {
    useTreeLayoutStore.getState().setLayout("project:p1", "chart");
    const acme = savedKey()!;
    setCurrentWorkspace("other", "ws-2");
    await Promise.resolve();
    useTreeLayoutStore.setState({ layouts: {} });
    await useTreeLayoutStore.persist.rehydrate();
    expect(useTreeLayoutStore.getState().layouts).toEqual({});
    expect(JSON.parse(storage.get(acme)!).state.layouts).toEqual({ "project:p1": "chart" });

    setCurrentWorkspace("acme", "ws-1");
    await Promise.resolve();
    await useTreeLayoutStore.persist.rehydrate();
    expect(selectTreeLayout("project:p1")(useTreeLayoutStore.getState())).toBe("chart");
  });

  it("does not disturb other surfaces when one changes", () => {
    const { setLayout } = useTreeLayoutStore.getState();
    setLayout("a", "chart");
    setLayout("b", "chart");
    setLayout("a", "chart");
    expect(useTreeLayoutStore.getState().layouts).toEqual({ a: "chart", b: "chart" });
  });
});
