"use client";

import { ListTree, Network } from "lucide-react";
import {
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import type { TreeLayout } from "@multica/core/issues/stores/tree-layout-store";
import { useT } from "../../i18n";
import { useTreeLayoutControl } from "./tree-layout-context";

// AG patch: the Outline | Chart choice, as a section of the Display menu. It
// renders only while tree mode is the active view.

export function TreeLayoutMenuSection({ onChosen }: { onChosen: () => void }) {
  const { t } = useT("ag");
  const control = useTreeLayoutControl();
  if (!control) return null;
  const chartDisabled = control.chartAvailability === null;
  return (
    <>
      <DropdownMenuSeparator />
      <DropdownMenuGroup>
        <DropdownMenuLabel>{t(($) => $.issues.tree.layout_section)}</DropdownMenuLabel>
      </DropdownMenuGroup>
      <DropdownMenuRadioGroup
        value={control.layout}
        onValueChange={(value) => {
          control.setLayout(value as TreeLayout);
          onChosen();
        }}
      >
        <DropdownMenuRadioItem value="outline">
          <ListTree />
          {t(($) => $.issues.tree.layout_outline)}
        </DropdownMenuRadioItem>
        <DropdownMenuRadioItem value="chart" disabled={chartDisabled}>
          <Network />
          {t(($) => $.issues.tree.layout_chart)}
        </DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
      {chartDisabled && (
        <p
          data-slot="tree-chart-hint"
          className="max-w-48 px-2 pb-1.5 text-caption text-muted-foreground"
        >
          {t(($) => $.issues.tree.chart_hint)}
        </p>
      )}
    </>
  );
}
