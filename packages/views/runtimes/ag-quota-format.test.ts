// @vitest-environment node

import { describe, expect, it } from "vitest";
import { formatHoldUntil } from "./ag-quota-format";

const now = new Date(2026, 9, 18, 12, 0);

describe("AG patch: formatHoldUntil", () => {
  it("AG patch: a hold ending today is time only", () => {
    expect(formatHoldUntil(new Date(2026, 9, 18, 21, 10), "en", now)).toBe("21:10");
  });

  it("AG patch: a hold ending on another day names the day", () => {
    const text = formatHoldUntil(new Date(2026, 9, 22, 9, 0), "en", now);
    expect(text).toMatch(/Thu/);
    expect(text).toMatch(/09:00/);
  });

  it("AG patch: the same clock time in another month is not today", () => {
    expect(formatHoldUntil(new Date(2026, 10, 18, 21, 10), "en", now)).toMatch(/Nov/);
  });
});
