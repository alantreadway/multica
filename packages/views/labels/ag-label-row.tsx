"use client";

import type { Label } from "@multica/core/types";
import type { LabelDisplay } from "@multica/core/issues/stores/ag-label-display";
import { cn } from "@multica/ui/lib/utils";
import { LabelChip } from "./label-chip";

/**
 * AG patch: the one place issue surfaces (board card, list row, table cell,
 * sub-issue row) draw an issue's labels. Every label is
 * shown, in the order the server returned them, and the row wraps instead of
 * clipping or collapsing into a `+N` counter.
 *
 * `compact` is an outlined chip with a colour dot; `full` is the filled
 * `LabelChip`; `off` renders nothing. The compact chip's fixed overhead (padding,
 * dot, gap) must stay under `LabelChip`'s 16px so it is narrower for any name.
 */
export function AgLabelRow({
  labels,
  display,
  className,
}: {
  labels: readonly Label[];
  display: LabelDisplay;
  className?: string;
}) {
  if (display === "off" || labels.length === 0) return null;
  return (
    <span
      data-slot="ag-label-row"
      data-display={display}
      className={cn("flex min-w-0 max-w-full flex-wrap items-center gap-1", className)}
    >
      {labels.map((label) =>
        display === "compact" ? (
          <AgCompactLabelChip key={label.id} label={label} />
        ) : (
          <LabelChip key={label.id} label={label} />
        ),
      )}
    </span>
  );
}

/**
 * The dot colour is applied as an inline style, so it trusts the backend's
 * `#rrggbb` normalisation exactly like `LabelChip` does.
 */
function AgCompactLabelChip({ label }: { label: Label }) {
  return (
    <span
      // The outline is an inset ring, so it adds no width: overhead is
      // px-1 (8px) + size-1 dot (4px) + gap-0.5 (2px) = 14px.
      className="inline-flex min-w-0 max-w-full items-center gap-0.5 rounded-full px-1 py-px text-micro text-foreground ring-1 ring-inset ring-border"
      aria-label={label.name}
      title={label.name}
    >
      <span
        aria-hidden
        className="size-1 shrink-0 rounded-full"
        style={{ backgroundColor: label.color }}
      />
      <span className="truncate max-w-[12rem]">{label.name}</span>
    </span>
  );
}
