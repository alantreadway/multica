"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/** Expanded issue ids kept per surface, so reopening a surface restores them. */
export const MAX_EXPANDED_PER_SURFACE = 500;

interface TreeViewState {
  /** Expanded issue ids by surface key (the issue scope key). Collapsed is the
   *  default, so only expanded ids are stored. */
  expanded: Record<string, string[]>;
  toggleExpanded: (surfaceKey: string, issueId: string) => void;
  setExpanded: (surfaceKey: string, issueIds: readonly string[]) => void;
}

const EMPTY: string[] = [];

export const useTreeViewStore = create<TreeViewState>()(
  persist(
    (set) => ({
      expanded: {},
      toggleExpanded: (surfaceKey, issueId) =>
        set((state) => {
          const current = state.expanded[surfaceKey] ?? EMPTY;
          const next = current.includes(issueId)
            ? current.filter((id) => id !== issueId)
            : [...current, issueId].slice(-MAX_EXPANDED_PER_SURFACE);
          return { expanded: { ...state.expanded, [surfaceKey]: next } };
        }),
      setExpanded: (surfaceKey, issueIds) =>
        set((state) => ({
          expanded: {
            ...state.expanded,
            [surfaceKey]: [...new Set(issueIds)].slice(-MAX_EXPANDED_PER_SURFACE),
          },
        })),
    }),
    {
      name: "multica_issue_tree_view",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

/** The surface's expanded ids; a stable empty array until one is expanded. */
export function selectTreeExpanded(surfaceKey: string) {
  return (state: TreeViewState): readonly string[] =>
    state.expanded[surfaceKey] ?? EMPTY;
}

registerForWorkspaceRehydration(() => useTreeViewStore.persist.rehydrate());
