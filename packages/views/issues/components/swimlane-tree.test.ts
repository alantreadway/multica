// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  hiddenLaneKeys,
  orderLanesAsTree,
  type AncestryMap,
  type AncestryNode,
  type TreeLaneInput,
} from "./swimlane-tree";

// AG patch: pure tree ordering for parent swimlanes.

const lane = (id: string, over: Partial<TreeLaneInput> = {}): TreeLaneInput & {
  key: string;
} => ({
  key: `parent:${id}`,
  rawId: id,
  isPinned: false,
  identifier: `ID-${id}`,
  parentIssue: { id },
  ...over,
});

const ancestryOf = (edges: Record<string, string | null>): AncestryMap =>
  new Map<string, AncestryNode>(
    Object.entries(edges).map(([id, parentId]) => [
      id,
      { id, identifier: `ID-${id}`, parentId },
    ]),
  );

const summary = (lanes: ReturnType<typeof orderLanesAsTree>) =>
  lanes.map((l) => `${l.rawId}@${l.tree.depth}`);

describe("orderLanesAsTree", () => {
  it("orders A→B→C→D depth-first with siblings in stored, then incoming, order", () => {
    // Incoming order is deliberately scrambled; E, F, G are siblings under A.
    const lanes = ["d", "g", "c", "f", "b", "e", "a"].map((id) => lane(id));
    const ancestry = ancestryOf({
      a: null,
      b: "a",
      c: "b",
      d: "c",
      e: "a",
      f: "a",
      g: "a",
    });

    const out = orderLanesAsTree(lanes, ancestry, ["f"]);

    // Under A: stored f first, then incoming order g, b, e.
    // Each lane is followed by its own subtree (d under c under b).
    expect(summary(out)).toEqual([
      "a@0",
      "f@1",
      "g@1",
      "b@1",
      "c@2",
      "d@3",
      "e@1",
    ]);
    expect(out.find((l) => l.rawId === "a")!.tree.descendantCount).toBe(6);
    expect(out.find((l) => l.rawId === "b")!.tree.descendantCount).toBe(2);
    expect(out.find((l) => l.rawId === "d")!.tree.treeParentRawId).toBe("c");
  });

  it("keeps pinned lanes first and unchanged, ahead of the tree", () => {
    const none = lane("none", { isPinned: true, parentIssue: null });
    const orphans = lane("__orphans__", { isPinned: true, parentIssue: null });
    const out = orderLanesAsTree(
      [none, orphans, lane("b"), lane("a")],
      ancestryOf({ a: null, b: "a" }),
      [],
    );

    expect(summary(out)).toEqual(["none@0", "__orphans__@0", "a@0", "b@1"]);
    expect(out[0]!.tree.path).toEqual([]);
  });

  it("attaches a lane to its nearest laned ancestor and keeps unlaned ancestors in the path", () => {
    // B has no lane of its own: C's lane hangs under A and the path still
    // runs A > B > C.
    const out = orderLanesAsTree(
      [lane("c"), lane("a")],
      ancestryOf({ a: null, b: "a", c: "b" }),
      [],
    );

    const c = out.find((l) => l.rawId === "c")!;
    expect(summary(out)).toEqual(["a@0", "c@1"]);
    expect(c.tree.treeParentRawId).toBe("a");
    expect(c.tree.path.map((p) => p.identifier)).toEqual([
      "ID-a",
      "ID-b",
      "ID-c",
    ]);
  });

  it("makes a lane a root when no ancestor has a lane, and a top-level path has length 1", () => {
    const out = orderLanesAsTree(
      [lane("c"), lane("a")],
      ancestryOf({ a: null, c: "b", b: "x", x: null }),
      [],
    );

    expect(summary(out)).toEqual(["c@0", "a@0"]);
    expect(out.find((l) => l.rawId === "a")!.tree.path).toHaveLength(1);
  });

  it("breaks a cyclic chain instead of dropping or looping on its lanes", () => {
    const out = orderLanesAsTree(
      [lane("a"), lane("b"), lane("c")],
      ancestryOf({ a: "c", b: "a", c: "b" }),
      [],
    );

    expect(out.map((l) => l.rawId).sort()).toEqual(["a", "b", "c"]);
    // First lane in incoming order becomes the root; the rest nest below it.
    expect(out[0]!.rawId).toBe("a");
    expect(out[0]!.tree.depth).toBe(0);
    expect(out[0]!.tree.treeParentRawId).toBeNull();
    // Paths terminate despite the cycle.
    for (const l of out) expect(l.tree.path.length).toBeLessThanOrEqual(3);
  });

  it("never lets a stored order move a child above its parent", () => {
    const out = orderLanesAsTree(
      [lane("a"), lane("b"), lane("c")],
      ancestryOf({ a: null, b: "a", c: "b" }),
      ["c", "b", "a"],
    );

    expect(summary(out)).toEqual(["a@0", "b@1", "c@2"]);
  });
});

describe("hiddenLaneKeys", () => {
  const tree = orderLanesAsTree(
    [lane("a"), lane("b"), lane("c"), lane("x")],
    ancestryOf({ a: null, b: "a", c: "b", x: null }),
    [],
  );

  it("hides every descendant of a collapsed lane but not the lane itself", () => {
    expect([...hiddenLaneKeys(tree, new Set(["parent:a"]))].sort()).toEqual([
      "parent:b",
      "parent:c",
    ]);
  });

  it("hides only the subtree below a collapsed mid-level lane", () => {
    expect([...hiddenLaneKeys(tree, new Set(["parent:b"]))]).toEqual([
      "parent:c",
    ]);
  });

  it("returns an empty set when nothing is collapsed", () => {
    expect(hiddenLaneKeys(tree, new Set()).size).toBe(0);
  });
});
