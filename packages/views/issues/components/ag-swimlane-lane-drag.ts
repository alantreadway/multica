"use client";

/**
 * AG patch: lane drag rules and the jump-to-lane chip for the tree-ordered
 * parent swimlane. See ag-patches/PATCHES.md.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import {
  closestCenter,
  pointerWithin,
  type Collision,
  type CollisionDetection,
} from "@dnd-kit/core";
import type { VirtuosoHandle } from "react-virtuoso";
import { lanesToExpand, type SwimlaneTreeInfo } from "./swimlane-tree";

/**
 * Lane drags only target sibling lanes. A pointer over another level gives no
 * target, so the lane under it never moves aside. `laneTreeParents` maps each
 * draggable lane id to its tree parent's raw id.
 */
export function agSiblingLaneCollision(
  args: Parameters<CollisionDetection>[0],
  laneTreeParents: ReadonlyMap<string, string | null>,
): Collision[] {
  const activeId = args.active.id as string;
  const isSiblingLane = (c: { id: unknown }) =>
    laneTreeParents.get(c.id as string) === laneTreeParents.get(activeId);
  const laneHits = pointerWithin(args).filter((c) =>
    (c.id as string).startsWith("lane:"),
  );
  if (laneHits.length > 0) return laneHits.filter(isSiblingLane);
  return closestCenter(args).filter(isSiblingLane);
}

interface JumpLane {
  key: string;
  rawId: string;
  parentIssue: { id: string } | null;
  tree?: SwimlaneTreeInfo;
}

/**
 * Jump from a card that heads a lane to that lane: expand it and its
 * collapsed ancestors, then scroll to it once it is mounted and open.
 */
export function useAgJumpToLane({
  laneGroups,
  orderedLanes,
  collapsedLanes,
  toggleLane,
  scrollEl,
}: {
  laneGroups: readonly JumpLane[];
  orderedLanes: readonly JumpLane[];
  collapsedLanes: ReadonlySet<string>;
  toggleLane: (laneKey: string) => void;
  scrollEl: HTMLElement | null;
}) {
  const lanesVirtuosoRef = useRef<VirtuosoHandle>(null);
  const [pendingJumpKey, setPendingJumpKey] = useState<string | null>(null);
  const jumpToLane = useCallback(
    (issueId: string) => {
      const target = laneGroups.find((g) => g.parentIssue?.id === issueId);
      if (!target) return;
      for (const key of lanesToExpand(laneGroups, target.rawId, collapsedLanes)) {
        toggleLane(key);
      }
      setPendingJumpKey(target.key);
    },
    [laneGroups, collapsedLanes, toggleLane],
  );

  useEffect(() => {
    if (pendingJumpKey === null) return;
    const index = orderedLanes.findIndex((g) => g.key === pendingJumpKey);
    if (index === -1 || collapsedLanes.has(pendingJumpKey)) return;
    setPendingJumpKey(null);
    const header = scrollEl?.querySelector<HTMLElement>("[data-swimlane-status-header]");
    lanesVirtuosoRef.current?.scrollToIndex({
      index,
      align: "start",
      offset: -(header?.offsetHeight ?? 0),
    });
  }, [pendingJumpKey, orderedLanes, collapsedLanes, scrollEl]);

  return { lanesVirtuosoRef, jumpToLane };
}
