import type { Issue } from "@multica/core/types";

// AG patch: pure row model behind the outline tree view. Kept free of React so
// the flattening, connector and ancestor-path rules are unit-testable and a
// follow-on layout (org chart) can reuse the same branch data.

/** Branch key of the top level (no focus). */
export const TREE_ROOT_KEY = "__root__";

/** Rows tree mode asks for per page, per parent. */
export const TREE_PAGE_SIZE = 50;

/** Expand-all stops at this depth: depth 0 and 1 expand, so rows show to depth 2. */
export const TREE_EXPAND_ALL_DEPTH = 2;

/** Upper bound on nodes one expand-all opens (each opened node is a request). */
export const TREE_EXPAND_ALL_MAX_NODES = 100;

export function treeBranchKey(parentId: string | null): string {
  return parentId ?? TREE_ROOT_KEY;
}

/** One loaded parent's children, concatenated across its cursor pages. */
export interface TreeBranch {
  rows: ReadonlyArray<{ issue: Issue; direct_child_count: number }>;
  nextCursor: string | null;
  /** First page has not answered yet. */
  loading: boolean;
  /** A later page is in flight. */
  loadingMore: boolean;
  error: boolean;
}

export interface TreeAncestor {
  id: string;
  identifier: string;
  parentId: string | null;
  title: string;
  status: string;
}

export type TreeAncestry = ReadonlyMap<string, TreeAncestor>;

interface TreeRowBase {
  key: string;
  /** Parent issue id (`null` at the top level). */
  parentId: string | null;
  depth: number;
  /** Connector columns, `depth` of them. Column c < depth - 1 is true when the
   *  ancestor at depth c + 1 has later siblings, so its vertical line runs
   *  through this row. The last column is this row's own elbow slot. */
  guides: boolean[];
}

export interface TreeIssueRow extends TreeRowBase {
  kind: "issue";
  issue: Issue;
  childCount: number;
  expanded: boolean;
  /** Last among its siblings: draws └ rather than ├. */
  isLast: boolean;
  /** Hidden ancestors, topmost first. Only on top-level rows whose parent the
   *  filters removed. */
  ancestorPath: TreeAncestor[];
}

export interface TreeMoreRow extends TreeRowBase {
  kind: "more";
  branchKey: string;
  loading: boolean;
}

export interface TreeStatusRow extends TreeRowBase {
  kind: "status";
  branchKey: string;
  state: "loading" | "error";
}

export type TreeRow = TreeIssueRow | TreeMoreRow | TreeStatusRow;

export interface FlattenTreeInput {
  /** `null` for the whole tree, or the focused issue. */
  focusIssue: Issue | null;
  branches: Readonly<Record<string, TreeBranch | undefined>>;
  expanded: ReadonlySet<string>;
  ancestry?: TreeAncestry;
}

/**
 * Hidden ancestors of `parentId`, topmost first. Stops at a missing node or a
 * cycle, so a partial ancestry still yields the part that is known.
 */
export function ancestorPathOf(
  parentId: string | null,
  ancestry: TreeAncestry | undefined,
): TreeAncestor[] {
  if (!parentId || !ancestry) return [];
  const path: TreeAncestor[] = [];
  const seen = new Set<string>();
  let cursor: string | null = parentId;
  while (cursor !== null && !seen.has(cursor)) {
    seen.add(cursor);
    const node = ancestry.get(cursor);
    if (!node) break;
    path.push(node);
    cursor = node.parentId;
  }
  return path.reverse();
}

/**
 * Flatten the visible part of the tree: top-level rows, then each expanded
 * parent's children directly after it. A focused issue becomes the single
 * root, always open.
 */
export function flattenTree({
  focusIssue,
  branches,
  expanded,
  ancestry,
}: FlattenTreeInput): TreeRow[] {
  const out: TreeRow[] = [];
  const onPath = new Set<string>();

  const emitBranch = (
    parentId: string | null,
    depth: number,
    guides: boolean[],
  ) => {
    const key = treeBranchKey(parentId);
    const branch = branches[key];
    if (!branch || (branch.loading && branch.rows.length === 0)) {
      out.push({
        kind: "status",
        key: `status:${key}`,
        parentId,
        depth,
        guides,
        branchKey: key,
        state: "loading",
      });
      return;
    }
    if (branch.error && branch.rows.length === 0) {
      out.push({
        kind: "status",
        key: `status:${key}`,
        parentId,
        depth,
        guides,
        branchKey: key,
        state: "error",
      });
      return;
    }
    const hasMoreRow = branch.nextCursor !== null;
    branch.rows.forEach((row, index) => {
      const { issue } = row;
      if (onPath.has(issue.id)) return;
      const isLast = index === branch.rows.length - 1 && !hasMoreRow;
      const isOpen = expanded.has(issue.id) && row.direct_child_count > 0;
      out.push({
        kind: "issue",
        key: issue.id,
        parentId,
        depth,
        guides,
        issue,
        childCount: row.direct_child_count,
        expanded: isOpen,
        isLast,
        ancestorPath:
          parentId === null && depth === 0
            ? ancestorPathOf(issue.parent_issue_id, ancestry)
            : [],
      });
      if (isOpen) {
        onPath.add(issue.id);
        // Column c < depth holds the connector line for the ancestor at depth
        // c + 1; the last column is the child's own elbow slot.
        emitBranch(
          issue.id,
          depth + 1,
          depth === 0 ? [false] : [...guides.slice(0, depth - 1), !isLast, false],
        );
        onPath.delete(issue.id);
      }
    });
    if (hasMoreRow) {
      out.push({
        kind: "more",
        key: `more:${key}`,
        parentId,
        depth,
        guides,
        branchKey: key,
        loading: branch.loadingMore,
      });
    }
  };

  if (focusIssue) {
    const children = branches[treeBranchKey(focusIssue.id)];
    out.push({
      kind: "issue",
      key: focusIssue.id,
      parentId: null,
      depth: 0,
      guides: [],
      issue: focusIssue,
      childCount: children?.rows.length ?? 0,
      expanded: true,
      isLast: true,
      ancestorPath: [],
    });
    onPath.add(focusIssue.id);
    emitBranch(focusIssue.id, 1, [false]);
    return out;
  }

  emitBranch(null, 0, []);
  return out;
}

/**
 * Ids an "expand all" should open: every loaded issue above the depth cap that
 * has children and is still closed, shallowest first, up to the node cap.
 */
export function expandAllTargets(
  rows: readonly TreeRow[],
  expanded: ReadonlySet<string>,
  maxDepth = TREE_EXPAND_ALL_DEPTH,
  maxNodes = TREE_EXPAND_ALL_MAX_NODES,
): string[] {
  const targets: Array<{ id: string; depth: number }> = [];
  for (const row of rows) {
    if (row.kind !== "issue") continue;
    if (row.depth >= maxDepth || row.childCount === 0) continue;
    if (expanded.has(row.issue.id)) continue;
    targets.push({ id: row.issue.id, depth: row.depth });
  }
  targets.sort((a, b) => a.depth - b.depth);
  return targets.slice(0, maxNodes).map((target) => target.id);
}
