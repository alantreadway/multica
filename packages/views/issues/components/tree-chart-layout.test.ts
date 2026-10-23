// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import {
  CHART_LEVEL_GAP,
  CHART_NODE_HEIGHT,
  CHART_NODE_WIDTH,
  CHART_SIBLING_GAP,
  CHART_VIRTUAL_ROOT_ID,
  buildChartTree,
  countChartIssues,
  elbowPath,
  layoutChartTree,
  type ChartNode,
  type ChartPlacedNode,
} from "./tree-chart-layout";
import {
  flattenTree,
  type TreeBranch,
  type TreeExpandRule,
} from "./tree-rows";

// AG patch: org-chart geometry. Positions, overlap, collapse and the elbow
// connectors are pure, so they are checked without a DOM.

const issue = (id: string, parent: string | null = null): Issue =>
  ({ id, identifier: `ISS-${id}`, title: id, parent_issue_id: parent }) as unknown as Issue;

/** `shape` maps a parent id (or "__root__") to its child ids. */
function branches(shape: Record<string, string[]>): Record<string, TreeBranch> {
  const out: Record<string, TreeBranch> = {};
  for (const [parent, ids] of Object.entries(shape)) {
    out[parent] = {
      rows: ids.map((id) => ({
        issue: issue(id, parent === "__root__" ? null : parent),
        direct_child_count: (shape[id] ?? []).length,
      })),
      nextCursor: null,
      loading: false,
      loadingMore: false,
      error: false,
    };
  }
  return out;
}

function leaf(id: string): ChartNode {
  return { id, kind: "issue", row: null, children: [] };
}
function node(id: string, ...children: ChartNode[]): ChartNode {
  return { id, kind: "issue", row: null, children };
}

function place(root: ChartNode) {
  const layout = layoutChartTree(root);
  const byId = new Map(layout.nodes.map((n) => [n.id, n]));
  return { layout, byId };
}

const overlaps = (a: ChartPlacedNode, b: ChartPlacedNode) =>
  a.x < b.x + CHART_NODE_WIDTH &&
  b.x < a.x + CHART_NODE_WIDTH &&
  a.y < b.y + CHART_NODE_HEIGHT &&
  b.y < a.y + CHART_NODE_HEIGHT;

describe("AG patch: org chart layout", () => {
  it("places nothing for an empty tree and one node for a lone root", () => {
    expect(layoutChartTree(null)).toEqual({ nodes: [], edges: [], width: 0, height: 0 });
    const { layout } = place(leaf("a"));
    expect(layout.nodes).toHaveLength(1);
    expect(layout.edges).toEqual([]);
    expect(layout.nodes[0]).toMatchObject({ x: 24, y: 24, depth: 0, parentId: null });
    expect(layout.width).toBe(CHART_NODE_WIDTH + 48);
    expect(layout.height).toBe(CHART_NODE_HEIGHT + 48);
  });

  it("stacks levels top-down and centres a parent over its first and last child", () => {
    const { byId } = place(node("r", leaf("a"), leaf("b"), leaf("c")));
    const step = CHART_NODE_WIDTH + CHART_SIBLING_GAP;
    expect(byId.get("b")!.x - byId.get("a")!.x).toBe(step);
    expect(byId.get("c")!.x - byId.get("b")!.x).toBe(step);
    expect(byId.get("a")!.y).toBe(byId.get("r")!.y + CHART_NODE_HEIGHT + CHART_LEVEL_GAP);
    expect(byId.get("r")!.x).toBe((byId.get("a")!.x + byId.get("c")!.x) / 2);
    expect(byId.get("b")!.x).toBe(byId.get("r")!.x);
  });

  it("keeps a single child directly under its parent", () => {
    const { byId, layout } = place(node("r", node("a", leaf("b"))));
    expect(byId.get("a")!.x).toBe(byId.get("r")!.x);
    expect(byId.get("b")!.x).toBe(byId.get("r")!.x);
    expect(layout.edges.map((e) => e.path)).toEqual([
      elbowPath(byId.get("r")!, byId.get("a")!),
      elbowPath(byId.get("a")!, byId.get("b")!),
    ]);
  });

  it("never overlaps two nodes, whatever the shape", () => {
    const wide = node(
      "r",
      node("a", leaf("a1"), leaf("a2"), leaf("a3")),
      leaf("b"),
      node("c", node("c1", leaf("c11"), leaf("c12")), leaf("c2")),
      node("d", leaf("d1")),
    );
    const { layout } = place(wide);
    expect(layout.nodes).toHaveLength(13);
    for (let i = 0; i < layout.nodes.length; i += 1) {
      for (let j = i + 1; j < layout.nodes.length; j += 1) {
        expect(
          overlaps(layout.nodes[i]!, layout.nodes[j]!),
          `${layout.nodes[i]!.id} vs ${layout.nodes[j]!.id}`,
        ).toBe(false);
      }
    }
    // Everything sits inside the reported drawing.
    for (const n of layout.nodes) {
      expect(n.x).toBeGreaterThanOrEqual(0);
      expect(n.x + CHART_NODE_WIDTH).toBeLessThanOrEqual(layout.width);
      expect(n.y + CHART_NODE_HEIGHT).toBeLessThanOrEqual(layout.height);
    }
  });

  it("keeps sibling subtrees a gap apart at every depth, not only at their roots", () => {
    // `a` is deep on its right edge, `b` deep on its left: the gap must hold
    // at depth 2 even though their roots would be closer.
    const { byId } = place(node("r", node("a", leaf("a1"), leaf("a2")), node("b", leaf("b1"), leaf("b2"))));
    expect(byId.get("b1")!.x - byId.get("a2")!.x).toBeGreaterThanOrEqual(
      CHART_NODE_WIDTH + CHART_SIBLING_GAP,
    );
  });

  it("tucks a shallow subtree in beside a deep one", () => {
    // A leaf next to a three-deep column needs one slot, not the column's width.
    const { layout } = place(node("r", node("a", node("a1", leaf("a11"))), leaf("b")));
    expect(layout.width).toBe(2 * CHART_NODE_WIDTH + CHART_SIBLING_GAP + 48);
  });

  it("routes each edge from the parent's bottom centre down, across and into the child", () => {
    const { byId, layout } = place(node("r", leaf("a"), leaf("b")));
    const r = byId.get("r")!;
    const a = byId.get("a")!;
    const edge = layout.edges.find((e) => e.toId === "a")!;
    const px = r.x + CHART_NODE_WIDTH / 2;
    const py = r.y + CHART_NODE_HEIGHT;
    const ax = a.x + CHART_NODE_WIDTH / 2;
    expect(edge).toMatchObject({ fromId: "r", toId: "a" });
    expect(edge.path).toBe(`M${px} ${py}V${py + CHART_LEVEL_GAP / 2}H${ax}V${a.y}`);
    expect(layout.edges).toHaveLength(2);
  });
});

describe("AG patch: org chart from outline rows", () => {
  const shape = {
    __root__: ["1", "2"],
    "1": ["11", "12"],
    "11": ["111"],
    "2": ["21"],
  };
  const data = branches(shape);
  const flatten = (expanded: ReadonlySet<string> | TreeExpandRule, focus: Issue | null = null) =>
    flattenTree({ focusIssue: focus, branches: data, expanded });

  it("hangs the top-level rows under a virtual project node", () => {
    const root = buildChartTree(flatten(new Set(["1"])), true)!;
    expect(root).toMatchObject({ id: CHART_VIRTUAL_ROOT_ID, kind: "root" });
    expect(root.children.map((c) => c.id)).toEqual(["1", "2"]);
    expect(root.children[0]!.children.map((c) => c.id)).toEqual(["11", "12"]);
    expect(root.children[1]!.children).toEqual([]);
    expect(countChartIssues(root)).toBe(4);
  });

  it("takes the focused issue as the root when there is no virtual node", () => {
    const focus = issue("1");
    const root = buildChartTree(flatten(new Set(["11"]), focus), false)!;
    expect(root.id).toBe("1");
    expect(root.children.map((c) => c.id)).toEqual(["11", "12"]);
    expect(root.children[0]!.children.map((c) => c.id)).toEqual(["111"]);
  });

  it("has no tree for no rows without a virtual node, and a lone project node with one", () => {
    expect(buildChartTree([], false)).toBeNull();
    expect(buildChartTree([], true)).toMatchObject({ kind: "root", children: [] });
  });

  it("drops collapsed branches from the drawing", () => {
    const open = layoutChartTree(buildChartTree(flatten(new Set(["1", "11"])), true));
    const collapsed = layoutChartTree(buildChartTree(flatten(new Set()), true));
    expect(open.nodes.map((n) => n.id)).toEqual([CHART_VIRTUAL_ROOT_ID, "1", "11", "111", "12", "2"]);
    expect(collapsed.nodes.map((n) => n.id)).toEqual([CHART_VIRTUAL_ROOT_ID, "1", "2"]);
  });
});
