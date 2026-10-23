"use client";

import { ListTree, Network } from "@multica/ui/icons";
import { Toggle } from "@multica/ui/components/ui/toggle";
import type { TreeLayout } from "@multica/core/issues/stores/tree-layout-store";
import { useT } from "../../i18n";
import { useTreeLayoutControl } from "./tree-layout-context";

// AG patch: the Outline | Chart choice, as a row of the Display popover. It
// renders only while tree mode is the active view.

const LAYOUTS: { value: TreeLayout; Icon: typeof ListTree }[] = [
  { value: "outline", Icon: ListTree },
  { value: "chart", Icon: Network },
];

export function TreeLayoutMenuSection() {
  const { t } = useT("ag");
  const control = useTreeLayoutControl();
  if (!control) return null;
  const chartDisabled = control.chartAvailability === null;
  const label = (layout: TreeLayout) =>
    layout === "outline"
      ? t(($) => $.issues.tree.layout_outline)
      : t(($) => $.issues.tree.layout_chart);
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-3">
        <span className="text-caption font-medium text-muted-foreground">
          {t(($) => $.issues.tree.layout_section)}
        </span>
        <div
          role="group"
          aria-label={t(($) => $.issues.tree.layout_section)}
          className="flex gap-0.5 rounded-lg bg-muted p-0.5"
        >
          {LAYOUTS.map(({ value, Icon }) => (
            <Toggle
              key={value}
              pressed={control.layout === value}
              disabled={value === "chart" && chartDisabled}
              onPressedChange={() => control.setLayout(value)}
              className="h-6 gap-1 rounded-md px-2 text-caption font-normal text-muted-foreground hover:bg-transparent hover:text-foreground aria-pressed:bg-background aria-pressed:text-foreground aria-pressed:shadow-sm aria-pressed:hover:bg-background"
            >
              <Icon className="size-3.5" />
              {label(value)}
            </Toggle>
          ))}
        </div>
      </div>
      {chartDisabled && (
        <p data-slot="tree-chart-hint" className="text-caption text-muted-foreground">
          {t(($) => $.issues.tree.chart_hint)}
        </p>
      )}
    </div>
  );
}
