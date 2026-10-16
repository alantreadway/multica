// AG patch: pan and zoom maths for the org chart. The chart is drawn once at
// scale 1 and moved with a CSS transform, so these are plain numbers.

export interface ChartViewport {
  /** Translation of the chart's origin, in screen pixels. */
  x: number;
  y: number;
  /** Scale. */
  k: number;
}

export const CHART_MIN_SCALE = 0.25;
export const CHART_MAX_SCALE = 2;
export const CHART_ZOOM_STEP = 1.2;

export const IDENTITY_VIEWPORT: ChartViewport = { x: 0, y: 0, k: 1 };

export function clampScale(k: number): number {
  return Math.min(CHART_MAX_SCALE, Math.max(CHART_MIN_SCALE, k));
}

/** Zoom by `factor`, keeping the chart point under (px, py) where it is. */
export function zoomAt(
  viewport: ChartViewport,
  factor: number,
  px: number,
  py: number,
): ChartViewport {
  const k = clampScale(viewport.k * factor);
  if (k === viewport.k) return viewport;
  const ratio = k / viewport.k;
  return { k, x: px - (px - viewport.x) * ratio, y: py - (py - viewport.y) * ratio };
}

export function panBy(viewport: ChartViewport, dx: number, dy: number): ChartViewport {
  return { ...viewport, x: viewport.x + dx, y: viewport.y + dy };
}

/** Scale the whole drawing into the view (never above 1), centred across and
 *  pinned to the top so a tall chart starts at its root. */
export function fitViewport(
  content: { width: number; height: number },
  view: { width: number; height: number },
): ChartViewport {
  if (content.width <= 0 || content.height <= 0 || view.width <= 0 || view.height <= 0) {
    return IDENTITY_VIEWPORT;
  }
  const k = clampScale(Math.min(1, view.width / content.width, view.height / content.height));
  return { k, x: (view.width - content.width * k) / 2, y: 0 };
}
