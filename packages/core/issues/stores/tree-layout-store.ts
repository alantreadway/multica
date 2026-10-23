"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

// AG patch: which layout tree mode draws, per surface. Kept apart from the
// expand state so the outline's open nodes survive a trip through the chart.

export type TreeLayout = "outline" | "chart";

interface TreeLayoutState {
  /** Chosen layout by surface key (the issue scope key). Outline is the default,
   *  so only a chart choice is stored. */
  layouts: Record<string, TreeLayout>;
  setLayout: (surfaceKey: string, layout: TreeLayout) => void;
}

export const useTreeLayoutStore = create<TreeLayoutState>()(
  persist(
    (set) => ({
      layouts: {},
      setLayout: (surfaceKey, layout) =>
        set((state) => {
          const { [surfaceKey]: _previous, ...rest } = state.layouts;
          return { layouts: layout === "chart" ? { ...rest, [surfaceKey]: layout } : rest };
        }),
    }),
    {
      name: "multica_issue_tree_layout",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

/** The surface's chosen layout; an unknown stored value reads as outline. */
export function selectTreeLayout(surfaceKey: string) {
  return (state: TreeLayoutState): TreeLayout =>
    state.layouts[surfaceKey] === "chart" ? "chart" : "outline";
}

registerForWorkspaceRehydration(() => useTreeLayoutStore.persist.rehydrate());
