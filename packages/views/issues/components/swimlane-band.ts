/**
 * Alternating band for the tree-ordered parent swimlane: every root lane and
 * its sub-lanes share one band, consecutive roots alternate. Pinned lanes and
 * lanes without tree info (server ancestry not resolved yet) have no band.
 */
import type { SwimlaneTreeInfo } from "./swimlane-tree";

export type LaneBand = "odd" | "even";

export function laneBand(tree: SwimlaneTreeInfo | undefined): LaneBand | null {
  if (!tree || tree.rootOrdinal === null) return null;
  return tree.rootOrdinal % 2 === 0 ? "even" : "odd";
}

/**
 * Class for the band element. The padding is negative-margin balanced so
 * header and cells keep the same x in every lane; only odd bands are tinted.
 */
export function laneBandClass(band: LaneBand): string {
  return band === "odd"
    ? "-mx-2 rounded-xl bg-muted/60 px-2 py-1"
    : "-mx-2 rounded-xl px-2 py-1";
}

/** Top gap above a lane: tight inside a tree, wide before a new root. */
export function laneGapClass(
  index: number,
  tree: SwimlaneTreeInfo | undefined,
): string | undefined {
  if (index === 0) return undefined;
  return tree && tree.depth > 0 ? "pt-2" : "pt-4";
}
