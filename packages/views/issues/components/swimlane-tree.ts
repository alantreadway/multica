/**
 * Tree ordering for the parent-grouping swimlane.
 *
 * Both lane builders (`buildParentLanes` / `buildServerLanes`) produce a flat
 * list with one lane per immediate parent. This module turns that list into a
 * depth-first tree so a sub-issue's lane sits directly under its parent's
 * lane, and derives the ancestor path shown in the lane header. It is pure:
 * ancestry arrives as a lookup map, so the same code serves the server path
 * (ancestry fetched by `issueAncestryOptions`) and the client path (ancestry
 * read from loaded issues).
 */

/** One issue in the ancestry lookup: just enough to build a path. */
export interface AncestryNode {
  id: string;
  identifier: string;
  parentId: string | null;
}

export type AncestryMap = ReadonlyMap<string, AncestryNode>;

/** The slice of a lane this module reads. `LaneGroup` satisfies it. */
export interface TreeLaneInput {
  rawId: string;
  isPinned: boolean;
  identifier: string;
  parentIssue: { id: string } | null;
}

export interface SwimlaneTreeInfo {
  /** 0 for pinned lanes and roots; parent's depth + 1 otherwise. */
  depth: number;
  /** `rawId` of the nearest ancestor that has a lane of its own, or null. */
  treeParentRawId: string | null;
  /**
   * Identifier path from the root ancestor down to (and including) this
   * lane's parent issue. Ancestors without a lane are included. Length 1
   * means the lane's parent is a top-level issue.
   */
  path: { id: string; identifier: string }[];
  /** Lanes below this one at any depth. */
  descendantCount: number;
  /** `rawId` of the root lane of this lane's tree (itself for a root); null when pinned. */
  rootRawId: string | null;
  /** Position of the tree among root trees, 0-based; null when pinned. Drives the alternating band. */
  rootOrdinal: number | null;
}

export type TreeLane<L> = L & { tree: SwimlaneTreeInfo };

/**
 * Walk up from `issueId`, returning the ids of its ancestors, nearest first.
 * Stops at a root, at an id the map doesn't know, or on a cycle.
 */
function ancestorIds(issueId: string, ancestry: AncestryMap): string[] {
  const out: string[] = [];
  const seen = new Set<string>([issueId]);
  let cursor = ancestry.get(issueId)?.parentId ?? null;
  while (cursor !== null && !seen.has(cursor)) {
    out.push(cursor);
    seen.add(cursor);
    cursor = ancestry.get(cursor)?.parentId ?? null;
  }
  return out;
}

/**
 * Order lanes depth-first by parent/child relationship.
 *
 * - Pinned lanes ("No parent", "Other parents") stay first, unchanged.
 * - A lane attaches under its closest ancestor that has a lane; with none it
 *   is a root.
 * - Siblings sort by `storedOrder` (raw parent ids), then incoming order, so a
 *   stored order can never lift a child above its parent.
 * - Cycles are broken at the first lane (in incoming order) the walk reaches.
 */
export function orderLanesAsTree<L extends TreeLaneInput>(
  lanes: readonly L[],
  ancestry: AncestryMap,
  storedOrder: readonly string[],
): TreeLane<L>[] {
  const pinned: TreeLane<L>[] = [];
  const treeLanes: L[] = [];
  for (const lane of lanes) {
    if (lane.isPinned) {
      pinned.push({
        ...lane,
        tree: {
          depth: 0,
          treeParentRawId: null,
          path: [],
          descendantCount: 0,
          rootRawId: null,
          rootOrdinal: null,
        },
      });
    } else {
      treeLanes.push(lane);
    }
  }

  const incomingIndex = new Map<string, number>();
  treeLanes.forEach((lane, i) => incomingIndex.set(lane.rawId, i));
  const storedIndex = new Map<string, number>();
  storedOrder.forEach((id, i) => storedIndex.set(id, i));
  const byParentIssueId = new Map<string, L>();
  for (const lane of treeLanes) {
    if (lane.parentIssue) byParentIssueId.set(lane.parentIssue.id, lane);
  }

  const compareSiblings = (a: L, b: L) => {
    const ai = storedIndex.get(a.rawId);
    const bi = storedIndex.get(b.rawId);
    if (ai !== undefined && bi !== undefined) return ai - bi;
    if (ai !== undefined) return -1;
    if (bi !== undefined) return 1;
    return incomingIndex.get(a.rawId)! - incomingIndex.get(b.rawId)!;
  };

  const treeParentOf = new Map<string, string>();
  const paths = new Map<string, { id: string; identifier: string }[]>();
  for (const lane of treeLanes) {
    if (!lane.parentIssue) continue;
    const ownId = lane.parentIssue.id;
    const ancestors = ancestorIds(ownId, ancestry);
    const near = ancestors.find((id) => byParentIssueId.has(id));
    if (near !== undefined) {
      treeParentOf.set(lane.rawId, byParentIssueId.get(near)!.rawId);
    }
    const path = [...ancestors].reverse().flatMap((id) => {
      const node = ancestry.get(id);
      return node ? [{ id, identifier: node.identifier }] : [];
    });
    path.push({
      id: ownId,
      identifier: lane.identifier || ancestry.get(ownId)?.identifier || "",
    });
    paths.set(lane.rawId, path);
  }

  const children = new Map<string | null, L[]>();
  const attach = (parentKey: string | null, lane: L) => {
    const list = children.get(parentKey);
    if (list) list.push(lane);
    else children.set(parentKey, [lane]);
  };
  for (const lane of treeLanes) {
    attach(treeParentOf.get(lane.rawId) ?? null, lane);
  }
  for (const list of children.values()) list.sort(compareSiblings);

  const result: TreeLane<L>[] = [];
  const emitted = new Set<string>();

  let rootCount = 0;
  const emit = (
    lane: L,
    depth: number,
    treeParent: string | null,
    rootRawId: string,
    rootOrdinal: number,
  ): number => {
    emitted.add(lane.rawId);
    const slot = result.length;
    result.push(undefined as unknown as TreeLane<L>);
    let below = 0;
    for (const child of children.get(lane.rawId) ?? []) {
      // `emitted` also guards a cycle reached from a root through a loop.
      if (emitted.has(child.rawId)) continue;
      below += 1 + emit(child, depth + 1, lane.rawId, rootRawId, rootOrdinal);
    }
    result[slot] = {
      ...lane,
      tree: {
        depth,
        treeParentRawId: treeParent,
        path: paths.get(lane.rawId) ?? [],
        descendantCount: below,
        rootRawId,
        rootOrdinal,
      },
    };
    return below;
  };

  for (const root of children.get(null) ?? []) {
    emit(root, 0, null, root.rawId, rootCount++);
  }
  // Anything left sits on a cycle (every member has a lane parent): start at
  // the first one in incoming order and treat it as a root.
  for (const lane of treeLanes) {
    if (!emitted.has(lane.rawId)) emit(lane, 0, null, lane.rawId, rootCount++);
  }

  return [...pinned, ...result];
}

/**
 * Keys of lanes hidden because an ancestor lane is collapsed. `collapsed`
 * holds full lane keys (`parent:<id>`); a lane collapsed itself is not in the
 * result (it stays visible so it can be expanded).
 */
export function hiddenLaneKeys(
  lanes: readonly { key: string; rawId: string; tree?: SwimlaneTreeInfo }[],
  collapsed: ReadonlySet<string>,
): Set<string> {
  const hidden = new Set<string>();
  if (collapsed.size === 0) return hidden;
  const byRawId = new Map(lanes.map((lane) => [lane.rawId, lane]));
  for (const lane of lanes) {
    const seen = new Set<string>([lane.rawId]);
    let parentId = lane.tree?.treeParentRawId ?? null;
    while (parentId !== null && !seen.has(parentId)) {
      seen.add(parentId);
      const ancestor = byRawId.get(parentId);
      if (!ancestor) break;
      if (collapsed.has(ancestor.key)) {
        hidden.add(lane.key);
        break;
      }
      parentId = ancestor.tree?.treeParentRawId ?? null;
    }
  }
  return hidden;
}
