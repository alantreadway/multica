"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { issueAncestryOptions } from "@multica/core/issues/queries";
import type { Issue } from "@multica/core/types";
import {
  orderLanesAsTree,
  type AncestryMap,
  type AncestryNode,
  type TreeLane,
  type TreeLaneInput,
} from "./swimlane-tree";

const EMPTY_IDS: string[] = [];

/**
 * Tree-order the parent-grouping lanes (see `orderLanesAsTree`).
 *
 * Server path (`serverLanes`): lanes only carry their immediate parent, so the
 * ancestry comes from `issueAncestryOptions` and lanes keep today's order
 * until it resolves. Client path: loaded issues supply `parent_issue_id`, and
 * the same query only covers ancestors that aren't loaded.
 *
 * Does nothing (and sends no request) unless `enabled`.
 */
export function useSwimlaneTreeLanes<L extends TreeLaneInput>({
  lanes,
  enabled,
  serverLanes,
  wsId,
  loadedIssues,
  storedOrder,
}: {
  lanes: L[];
  enabled: boolean;
  serverLanes: boolean;
  wsId: string;
  loadedIssues: readonly Issue[];
  storedOrder: readonly string[];
}): (L | TreeLane<L>)[] {
  const local = useMemo(() => {
    const map = new Map<string, AncestryNode>();
    if (!enabled || serverLanes) return map;
    for (const issue of loadedIssues) {
      map.set(issue.id, {
        id: issue.id,
        identifier: issue.identifier,
        parentId: issue.parent_issue_id,
      });
    }
    return map;
  }, [enabled, serverLanes, loadedIssues]);

  const startIds = useMemo(() => {
    if (!enabled) return EMPTY_IDS;
    const ids = new Set<string>();
    for (const lane of lanes) {
      if (!lane.parentIssue) continue;
      if (serverLanes) {
        ids.add(lane.parentIssue.id);
        continue;
      }
      // Climb through loaded issues; the first id we can't see is what the
      // query has to resolve.
      const seen = new Set<string>();
      let cursor: string | null = lane.parentIssue.id;
      while (cursor !== null && !seen.has(cursor)) {
        seen.add(cursor);
        const node = local.get(cursor);
        if (!node) {
          ids.add(cursor);
          break;
        }
        cursor = node.parentId;
      }
    }
    return ids.size === 0 ? EMPTY_IDS : [...ids];
  }, [enabled, serverLanes, lanes, local]);

  const { data: fetched } = useQuery(issueAncestryOptions(wsId, startIds));

  return useMemo(() => {
    if (!enabled) return lanes;
    if (serverLanes && !fetched) return lanes;
    const ancestry: AncestryMap = fetched
      ? new Map([...local, ...fetched.map((n): [string, AncestryNode] => [n.id, n])])
      : local;
    return orderLanesAsTree(lanes, ancestry, storedOrder);
  }, [enabled, serverLanes, lanes, fetched, local, storedOrder]);
}
