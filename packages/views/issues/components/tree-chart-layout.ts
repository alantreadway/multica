import type { TreeRow } from "./tree-rows";

// AG patch: pure geometry behind the org-chart layout of tree mode. It turns the
// outline's visible rows back into a tree, places the nodes with a hand-rolled
// tidy-tree pass (no d3-hierarchy) and routes elbow connectors. React-free so
// positions, overlaps and collapse are unit-testable.

/** Node box. Every node shares it, which keeps the contour pass simple. */
export const CHART_NODE_WIDTH = 208;
export const CHART_NODE_HEIGHT = 72;
/** Space between sibling subtrees, and between one level and the next. */
export const CHART_SIBLING_GAP = 20;
export const CHART_LEVEL_GAP = 44;
/** Margin around the drawing. */
export const CHART_PADDING = 24;

/** Above this many visible issues the chart asks the user to narrow it down. */
export const TREE_CHART_NODE_CAP = 200;

/** Nodes at this chart depth and below start collapsed: depth 0, 1 open, so
 *  nodes show to depth 2. */
export const TREE_CHART_DEFAULT_OPEN_DEPTH = 2;

/** Default-open only reaches this many nodes of one branch, so opening the
 *  chart never fans out into a request per sibling of a wide level. */
export const TREE_CHART_DEFAULT_OPEN_PER_BRANCH = 20;

/** How far "Expand a level" can push the open depth. */
export const TREE_CHART_MAX_OPEN_DEPTH = 8;

export const CHART_VIRTUAL_ROOT_ID = "__chart_root__";

export interface ChartNode {
  id: string;
  /** `root` is the project's virtual top node. */
  kind: "root" | "issue" | "more" | "status";
  /** The outline row behind the node; absent on the virtual root. */
  row: TreeRow | null;
  children: ChartNode[];
}

/**
 * Rebuild the tree from the outline's flat rows. Rows come in depth-first
 * order, so a row's parent is the latest row one level up. With a virtual root
 * the top-level rows hang under it; otherwise the first row (the focused
 * issue) is the root.
 */
export function buildChartTree(
  rows: readonly TreeRow[],
  virtualRoot: boolean,
): ChartNode | null {
  const toNode = (row: TreeRow): ChartNode => ({
    id: row.key,
    kind: row.kind,
    row,
    children: [],
  });
  const offset = virtualRoot ? 1 : 0;
  const root: ChartNode | null = virtualRoot
    ? { id: CHART_VIRTUAL_ROOT_ID, kind: "root", row: null, children: [] }
    : null;
  let top: ChartNode | null = root;
  const stack: ChartNode[] = [];
  if (root) stack[0] = root;
  for (const row of rows) {
    const node = toNode(row);
    const depth = row.depth + offset;
    if (depth === 0) {
      top ??= node;
      stack.length = 0;
      stack[0] = node;
      continue;
    }
    const parent = stack[depth - 1] ?? top;
    if (!parent) continue;
    parent.children.push(node);
    stack.length = depth;
    stack[depth] = node;
  }
  return top;
}

/** Issue nodes only: the figure the node cap is checked against. */
export function countChartIssues(root: ChartNode | null): number {
  if (!root) return 0;
  let count = root.kind === "issue" ? 1 : 0;
  for (const child of root.children) count += countChartIssues(child);
  return count;
}

/**
 * The chart has something to fit once nothing in it is still loading: the
 * focused issue is fetched, not cached, so until it lands the only row is a
 * placeholder. Every open branch (the default-open levels included) shows a
 * loading row until its children arrive, so none left means the tree is drawn.
 */
export function isChartRootSettled(root: ChartNode | null): boolean {
  if (!root || root.kind === "status") return false;
  const loading = (node: ChartNode): boolean =>
    (node.row?.kind === "status" && node.row.state === "loading") ||
    node.children.some(loading);
  return !loading(root);
}

export interface ChartPlacedNode {
  id: string;
  node: ChartNode;
  parentId: string | null;
  depth: number;
  /** Top-left corner, in chart space. */
  x: number;
  y: number;
}

export interface ChartEdge {
  id: string;
  fromId: string;
  toId: string;
  /** SVG path: down from the parent, across, down into the child. */
  path: string;
}

export interface ChartLayout {
  nodes: ChartPlacedNode[];
  edges: ChartEdge[];
  width: number;
  height: number;
}

interface Contour {
  left: number[];
  right: number[];
}

interface Subtree {
  contour: Contour;
  /** Child centres relative to this subtree's root centre. */
  childOffsets: number[];
}

/**
 * Tidy tree. Each subtree is laid out on its own and summarised by a contour
 * (the left and right extent at every depth). Siblings are then pushed apart
 * only as far as their contours require, and the parent is centred over its
 * first and last child. Subtrees never overlap and shallow ones tuck in beside
 * deep ones.
 */
export function layoutChartTree(root: ChartNode | null): ChartLayout {
  if (!root) return { nodes: [], edges: [], width: 0, height: 0 };
  const half = CHART_NODE_WIDTH / 2;
  const memo = new Map<ChartNode, Subtree>();

  const place = (node: ChartNode): Subtree => {
    const cached = memo.get(node);
    if (cached) return cached;
    const own: Contour = { left: [-half], right: [half] };
    if (node.children.length === 0) {
      const leaf = { contour: own, childOffsets: [] };
      memo.set(node, leaf);
      return leaf;
    }
    const subtrees = node.children.map(place);
    const merged: Contour = {
      left: [...subtrees[0]!.contour.left],
      right: [...subtrees[0]!.contour.right],
    };
    const offsets = [0];
    for (let i = 1; i < subtrees.length; i++) {
      const next = subtrees[i]!.contour;
      let offset = Number.NEGATIVE_INFINITY;
      const shared = Math.min(merged.right.length, next.left.length);
      for (let d = 0; d < shared; d++) {
        offset = Math.max(
          offset,
          merged.right[d]! + CHART_SIBLING_GAP - next.left[d]!,
        );
      }
      for (let d = 0; d < next.left.length; d++) {
        if (d < merged.right.length) {
          merged.right[d] = next.right[d]! + offset;
        } else {
          merged.left[d] = next.left[d]! + offset;
          merged.right[d] = next.right[d]! + offset;
        }
      }
      offsets.push(offset);
    }
    const centre = (offsets[0]! + offsets[offsets.length - 1]!) / 2;
    const childOffsets = offsets.map((offset) => offset - centre);
    const result: Subtree = {
      childOffsets,
      contour: {
        left: [-half, ...merged.left.map((v) => v - centre)],
        right: [half, ...merged.right.map((v) => v - centre)],
      },
    };
    memo.set(node, result);
    return result;
  };

  place(root);

  const centres: Array<{ node: ChartNode; parentId: string | null; depth: number; cx: number }> = [];
  const walk = (node: ChartNode, parentId: string | null, depth: number, cx: number) => {
    centres.push({ node, parentId, depth, cx });
    const { childOffsets } = memo.get(node)!;
    node.children.forEach((child, index) =>
      walk(child, node.id, depth + 1, cx + childOffsets[index]!),
    );
  };
  walk(root, null, 0, 0);

  const minLeft = Math.min(...centres.map((c) => c.cx - half));
  const maxRight = Math.max(...centres.map((c) => c.cx + half));
  const maxDepth = Math.max(...centres.map((c) => c.depth));
  const shift = CHART_PADDING - minLeft;

  const nodes: ChartPlacedNode[] = centres.map(({ node, parentId, depth, cx }) => ({
    id: node.id,
    node,
    parentId,
    depth,
    x: cx + shift - half,
    y: CHART_PADDING + depth * (CHART_NODE_HEIGHT + CHART_LEVEL_GAP),
  }));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const edges: ChartEdge[] = [];
  for (const child of nodes) {
    if (!child.parentId) continue;
    const parent = byId.get(child.parentId)!;
    edges.push({
      id: `${parent.id}>${child.id}`,
      fromId: parent.id,
      toId: child.id,
      path: elbowPath(parent, child),
    });
  }

  return {
    nodes,
    edges,
    width: maxRight - minLeft + CHART_PADDING * 2,
    height:
      (maxDepth + 1) * CHART_NODE_HEIGHT + maxDepth * CHART_LEVEL_GAP + CHART_PADDING * 2,
  };
}

/** Parent bottom-centre, down to the gap's midline, across, down to the child. */
export function elbowPath(
  parent: { x: number; y: number },
  child: { x: number; y: number },
): string {
  const px = parent.x + CHART_NODE_WIDTH / 2;
  const py = parent.y + CHART_NODE_HEIGHT;
  const cx = child.x + CHART_NODE_WIDTH / 2;
  const cy = child.y;
  const midY = py + (cy - py) / 2;
  return px === cx ? `M${px} ${py}V${cy}` : `M${px} ${py}V${midY}H${cx}V${cy}`;
}
