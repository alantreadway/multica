"use client";

import { LABEL_DISPLAY_VALUES, type LabelDisplay } from "@multica/core/issues/stores/ag-label-display";
import { Toggle } from "@multica/ui/components/ui/toggle";
import { useT } from "../i18n";

/**
 * AG patch: the Off / Compact / Full chooser that replaces the on/off
 * "Labels" card-property toggle in the Display popover and the save-view
 * dialog. Same chip styling as the neighbouring card-property toggles.
 */
export function AgLabelDisplayControl({
  value,
  onChange,
  hideTitle = false,
}: {
  value: LabelDisplay;
  onChange: (next: LabelDisplay) => void;
  /** The caller already renders a "Labels" row label beside the control. */
  hideTitle?: boolean;
}) {
  const { t } = useT("issues");
  const names: Record<LabelDisplay, string> = {
    off: t(($) => $.display.labels_off),
    compact: t(($) => $.display.labels_compact),
    full: t(($) => $.display.labels_full),
  };
  return (
    <div
      role="radiogroup"
      aria-label={t(($) => $.display.card_labels)}
      className="flex flex-wrap items-center gap-1"
    >
      {!hideTitle && (
        <span className="mr-1 text-caption text-muted-foreground">
          {t(($) => $.display.card_labels)}
        </span>
      )}
      {LABEL_DISPLAY_VALUES.map((option) => (
        <Toggle
          key={option}
          size="sm"
          variant="outline"
          role="radio"
          aria-checked={value === option}
          pressed={value === option}
          onPressedChange={() => onChange(option)}
          className="h-6 rounded-md px-2 text-caption text-muted-foreground hover:bg-muted/60 aria-pressed:border-transparent aria-pressed:bg-accent aria-pressed:text-accent-foreground aria-pressed:hover:bg-accent/80"
        >
          {names[option]}
        </Toggle>
      ))}
    </div>
  );
}
