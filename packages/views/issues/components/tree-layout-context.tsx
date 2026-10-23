"use client";

import { createContext, useContext } from "react";
import type { TreeLayout } from "@multica/core/issues/stores/tree-layout-store";
import type { TreeChartAvailability } from "./use-tree-rows";

// AG patch: hands the surface's tree layout to the Display menu without
// threading props through every page header. Absent outside surfaces that
// offer tree mode.

export interface TreeLayoutControl {
  layout: TreeLayout;
  chartAvailability: TreeChartAvailability;
  setLayout: (layout: TreeLayout) => void;
}

export const TreeLayoutContext = createContext<TreeLayoutControl | null>(null);

export function useTreeLayoutControl(): TreeLayoutControl | null {
  return useContext(TreeLayoutContext);
}
