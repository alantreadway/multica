// AG patch: per-view label display (Off / Compact / Full). Replaces the
// boolean `CardProperties.labels`; both Compact and Full show every label.

export const LABEL_DISPLAY_VALUES = ["off", "compact", "full"] as const;
export type LabelDisplay = (typeof LABEL_DISPLAY_VALUES)[number];

/** A fresh view keeps labels hidden, as before the patch. */
export const DEFAULT_LABEL_DISPLAY: LabelDisplay = "off";

/**
 * Normalise a persisted `cardProperties.labels` value. The old boolean maps
 * `true` -> `full` (the chips it used to draw) and `false` -> `off`; anything
 * else (missing, malformed, unknown future value) falls back to `fallback`.
 */
export function normalizeLabelDisplay(
  value: unknown,
  fallback: LabelDisplay = DEFAULT_LABEL_DISPLAY,
): LabelDisplay {
  if (value === true) return "full";
  if (value === false) return "off";
  return LABEL_DISPLAY_VALUES.includes(value as LabelDisplay)
    ? (value as LabelDisplay)
    : fallback;
}

/**
 * Returns `cardProperties` with `labels` normalised, leaving every other key
 * untouched. Applied wherever persisted or server-stored card properties are
 * read back (local persist, saved-view seeds).
 */
export function normalizeCardPropertiesLabels<T extends { labels?: unknown }>(
  cardProperties: T,
  fallback: LabelDisplay = DEFAULT_LABEL_DISPLAY,
): Omit<T, "labels"> & { labels: LabelDisplay } {
  return {
    ...cardProperties,
    labels: normalizeLabelDisplay(cardProperties.labels, fallback),
  };
}
