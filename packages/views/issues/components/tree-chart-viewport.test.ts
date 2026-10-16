// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  CHART_MAX_SCALE,
  CHART_MIN_SCALE,
  IDENTITY_VIEWPORT,
  clampScale,
  fitViewport,
  panBy,
  zoomAt,
} from "./tree-chart-viewport";

// AG patch: pan and zoom maths of the org chart.

/** Chart-space point under a screen point. */
const chartPoint = (vp: { x: number; y: number; k: number }, sx: number, sy: number) => ({
  x: (sx - vp.x) / vp.k,
  y: (sy - vp.y) / vp.k,
});

describe("AG patch: org chart viewport", () => {
  it("zooms about a point and keeps that point still", () => {
    const start = { x: 40, y: -20, k: 1 };
    const before = chartPoint(start, 300, 200);
    const next = zoomAt(start, 1.5, 300, 200);
    expect(next.k).toBeCloseTo(1.5);
    const after = chartPoint(next, 300, 200);
    expect(after.x).toBeCloseTo(before.x);
    expect(after.y).toBeCloseTo(before.y);
  });

  it("clamps the scale and leaves the viewport alone once at a limit", () => {
    expect(clampScale(50)).toBe(CHART_MAX_SCALE);
    expect(clampScale(0.001)).toBe(CHART_MIN_SCALE);
    const atMax = { x: 5, y: 6, k: CHART_MAX_SCALE };
    expect(zoomAt(atMax, 2, 10, 10)).toBe(atMax);
    expect(zoomAt({ x: 0, y: 0, k: 1 }, 100, 0, 0).k).toBe(CHART_MAX_SCALE);
  });

  it("pans by a screen delta without touching the scale", () => {
    expect(panBy({ x: 1, y: 2, k: 0.5 }, 10, -4)).toEqual({ x: 11, y: -2, k: 0.5 });
  });

  it("fits a large drawing down and centres a small one at full size", () => {
    const big = fitViewport({ width: 2000, height: 500 }, { width: 1000, height: 800 });
    expect(big.k).toBe(0.5);
    expect(big.x).toBe(0);
    expect(big.y).toBe(0);

    const small = fitViewport({ width: 400, height: 200 }, { width: 1000, height: 800 });
    expect(small).toEqual({ k: 1, x: 300, y: 0 });
  });

  it("falls back to the identity before anything is measured", () => {
    expect(fitViewport({ width: 0, height: 0 }, { width: 100, height: 100 })).toBe(IDENTITY_VIEWPORT);
    expect(fitViewport({ width: 100, height: 100 }, { width: 0, height: 0 })).toBe(IDENTITY_VIEWPORT);
  });
});
