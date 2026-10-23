// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  CHART_LEVEL_GAP,
  CHART_NODE_HEIGHT,
  CHART_NODE_WIDTH,
  CHART_PADDING,
  CHART_SIBLING_GAP,
  CHART_VIRTUAL_ROOT_ID,
  TREE_CHART_NODE_CAP,
  buildChartTree,
  countChartIssues,
  layoutChartTree,
  type ChartNode,
} from "./tree-chart-layout";
import type { TreeRow } from "./tree-rows";

// AG patch: org-chart layout invariants over many generated trees. The shape of
// the tree must never decide whether nodes overlap, parents sit over their
// children or the connectors reach their nodes.

/** Small deterministic generator so a failure reproduces from its seed. */
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s / 0x100000000;
  };
}

function randomTree(seed: number, maxNodes: number): ChartNode {
  const next = rng(seed);
  let count = 0;
  const make = (depth: number): ChartNode => {
    const id = `n${count++}`;
    const children: ChartNode[] = [];
    if (depth < 6) {
      const want = Math.floor(next() * 5);
      for (let i = 0; i < want && count < maxNodes; i++) children.push(make(depth + 1));
    }
    return { id, kind: "issue", row: null, children };
  };
  return make(0);
}

const issueRow = (key: string, depth: number): TreeRow =>
  ({ kind: "issue", key, parentId: null, depth }) as unknown as TreeRow;
const placeholderRow = (kind: "more" | "status", key: string, depth: number): TreeRow =>
  ({ kind, key, parentId: null, depth }) as unknown as TreeRow;

describe("AG patch: org chart layout invariants", () => {
  for (let seed = 1; seed <= 40; seed += 1) {
    it(`holds for generated tree ${seed}`, () => {
      const root = randomTree(seed, seed % 4 === 0 ? 200 : 40);
      const layout = layoutChartTree(root);
      const byId = new Map(layout.nodes.map((n) => [n.id, n]));
      const slot = CHART_NODE_HEIGHT + CHART_LEVEL_GAP;

      // One placed node per tree node, one edge per non-root node.
      const total = (function count(n: ChartNode): number {
        return 1 + n.children.reduce((sum, c) => sum + count(c), 0);
      })(root);
      expect(layout.nodes).toHaveLength(total);
      expect(layout.edges).toHaveLength(total - 1);

      for (const n of layout.nodes) {
        // Rows are one fixed step per depth, inside the padded drawing.
        expect(n.y).toBe(CHART_PADDING + n.depth * slot);
        expect(n.x).toBeGreaterThanOrEqual(CHART_PADDING);
        expect(n.x + CHART_NODE_WIDTH).toBeLessThanOrEqual(layout.width - CHART_PADDING);
        expect(n.y + CHART_NODE_HEIGHT).toBeLessThanOrEqual(layout.height - CHART_PADDING);

        // A parent is centred over its first and last child.
        const kids = n.node.children.map((c) => byId.get(c.id)!);
        if (kids.length > 0) {
          expect(n.x).toBe((kids[0]!.x + kids[kids.length - 1]!.x) / 2);
        }
        // Siblings keep their order, left to right, a full gap apart.
        for (let i = 1; i < kids.length; i += 1) {
          expect(kids[i]!.x - kids[i - 1]!.x).toBeGreaterThanOrEqual(
            CHART_NODE_WIDTH + CHART_SIBLING_GAP,
          );
        }
      }

      // Nodes on one row never touch, whichever subtree they belong to.
      const byDepth = new Map<number, number[]>();
      for (const n of layout.nodes) byDepth.set(n.depth, [...(byDepth.get(n.depth) ?? []), n.x]);
      for (const xs of byDepth.values()) {
        xs.sort((a, b) => a - b);
        for (let i = 1; i < xs.length; i += 1) {
          expect(xs[i]! - xs[i - 1]!).toBeGreaterThanOrEqual(CHART_NODE_WIDTH + CHART_SIBLING_GAP);
        }
      }

      // Every connector starts at its parent's bottom centre and ends on its
      // child's top centre.
      for (const edge of layout.edges) {
        const parent = byId.get(edge.fromId)!;
        const child = byId.get(edge.toId)!;
        const start = `M${parent.x + CHART_NODE_WIDTH / 2} ${parent.y + CHART_NODE_HEIGHT}`;
        expect(edge.path.startsWith(start)).toBe(true);
        expect(edge.path.endsWith(`V${child.y}`)).toBe(true);
        expect(edge.path).toContain(`${child.x + CHART_NODE_WIDTH / 2}`);
      }
    });
  }

  it("lays out the same tree identically twice", () => {
    const a = layoutChartTree(randomTree(7, 120));
    const b = layoutChartTree(randomTree(7, 120));
    expect(a.nodes.map((n) => [n.id, n.x, n.y])).toEqual(b.nodes.map((n) => [n.id, n.x, n.y]));
    expect(a.edges.map((e) => e.path)).toEqual(b.edges.map((e) => e.path));
  });

  it("lays out a tree at the node cap with a deep chain and a wide level", () => {
    const wide: ChartNode = {
      id: "r",
      kind: "issue",
      row: null,
      children: Array.from({ length: TREE_CHART_NODE_CAP - 1 }, (_, i) => ({
        id: `c${i}`,
        kind: "issue" as const,
        row: null,
        children: [],
      })),
    };
    const layout = layoutChartTree(wide);
    expect(layout.nodes).toHaveLength(TREE_CHART_NODE_CAP);
    expect(layout.width).toBe(
      (TREE_CHART_NODE_CAP - 1) * CHART_NODE_WIDTH +
        (TREE_CHART_NODE_CAP - 2) * CHART_SIBLING_GAP +
        2 * CHART_PADDING,
    );

    let chain: ChartNode = { id: "d0", kind: "issue", row: null, children: [] };
    for (let i = 1; i < TREE_CHART_NODE_CAP; i += 1) {
      chain = { id: `d${i}`, kind: "issue", row: null, children: [chain] };
    }
    const column = layoutChartTree(chain);
    expect(new Set(column.nodes.map((n) => n.x)).size).toBe(1);
    expect(column.height).toBe(
      TREE_CHART_NODE_CAP * CHART_NODE_HEIGHT +
        (TREE_CHART_NODE_CAP - 1) * CHART_LEVEL_GAP +
        2 * CHART_PADDING,
    );
  });

  it("draws a straight connector when the child is directly below its parent", () => {
    const { edges, nodes } = layoutChartTree({
      id: "r",
      kind: "issue",
      row: null,
      children: [{ id: "a", kind: "issue", row: null, children: [] }],
    });
    const x = nodes[0]!.x + CHART_NODE_WIDTH / 2;
    expect(edges[0]!.path).toBe(
      `M${x} ${nodes[0]!.y + CHART_NODE_HEIGHT}V${nodes[1]!.y}`,
    );
  });
});

describe("AG patch: org chart tree from rows", () => {
  it("keeps placeholder rows as leaves under their branch and out of the issue count", () => {
    const root = buildChartTree(
      [
        issueRow("1", 0),
        issueRow("2", 1),
        placeholderRow("status", "status:2", 2),
        placeholderRow("more", "more:1", 1),
      ],
      false,
    )!;
    expect(root.children.map((c) => [c.id, c.kind])).toEqual([
      ["2", "issue"],
      ["more:1", "more"],
    ]);
    expect(root.children[0]!.children.map((c) => c.kind)).toEqual(["status"]);
    expect(countChartIssues(root)).toBe(2);
  });

  it("does not count the project's virtual node as an issue", () => {
    const rows = Array.from({ length: TREE_CHART_NODE_CAP }, (_, i) => issueRow(`i${i}`, 0));
    const root = buildChartTree(rows, true)!;
    expect(root.id).toBe(CHART_VIRTUAL_ROOT_ID);
    expect(countChartIssues(root)).toBe(TREE_CHART_NODE_CAP);
    expect(layoutChartTree(root).nodes).toHaveLength(TREE_CHART_NODE_CAP + 1);
  });

  it("re-parents a row that jumps back up several levels", () => {
    const root = buildChartTree(
      [issueRow("1", 0), issueRow("2", 1), issueRow("3", 2), issueRow("4", 3), issueRow("5", 1)],
      false,
    )!;
    expect(root.children.map((c) => c.id)).toEqual(["2", "5"]);
    expect(root.children[0]!.children[0]!.children.map((c) => c.id)).toEqual(["4"]);
  });

  it("counts nothing for no tree", () => {
    expect(countChartIssues(null)).toBe(0);
  });
});
