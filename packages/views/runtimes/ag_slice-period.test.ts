// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeUsage } from "@multica/core/types";
import { agWithYesterday } from "../common/ag_period";
import { agIsSingleDay, agSlicePeriod } from "./ag_slice-period";
import { addDaysIso, sliceWindow, todayIso } from "./utils";

const TZ = "Asia/Tokyo";

function row(daysAgo: number, input: number): RuntimeUsage {
  return {
    runtime_id: "r-1",
    date: addDaysIso(todayIso(TZ), -daysAgo),
    provider: "anthropic",
    model: "claude-sonnet-4-6",
    input_tokens: input,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
  };
}

const USAGE = [row(0, 1), row(1, 10), row(2, 100), row(3, 1_000)];
const inputs = (rows: RuntimeUsage[]) => rows.map((r) => r.input_tokens);

describe("AG patch: runtime single-day periods", () => {
  it("AG patch: 1d is today, compared with yesterday", () => {
    const { filtered, prevFiltered } = agSlicePeriod(USAGE, 1, TZ);
    expect(inputs(filtered)).toEqual([1]);
    expect(inputs(prevFiltered)).toEqual([10]);
  });

  it("AG patch: Yesterday is yesterday, compared with the day before", () => {
    const { filtered, prevFiltered } = agSlicePeriod(USAGE, "yesterday", TZ);
    expect(inputs(filtered)).toEqual([10]);
    expect(inputs(prevFiltered)).toEqual([100]);
  });

  it("AG patch: trailing ranges keep upstream's window", () => {
    expect(agSlicePeriod(USAGE, 7, TZ)).toEqual(sliceWindow(USAGE, 7, TZ));
  });

  it("AG patch: only 1d and Yesterday are single-day", () => {
    expect([1, "yesterday", 7, 30].map((p) => agIsSingleDay(p as 1))).toEqual([
      true,
      true,
      false,
      false,
    ]);
  });

  it("AG patch: inserts Yesterday after 1d and not on the weekly grain", () => {
    const daily = [1, 7, 30].map((value) => ({ label: `${value}d`, value }));
    expect(agWithYesterday(daily, { label: "Yesterday" }).map((o) => o.label)).toEqual([
      "1d",
      "Yesterday",
      "7d",
      "30d",
    ]);
    const weekly = [30, 90].map((value) => ({ label: `${value}d`, value }));
    expect(agWithYesterday(weekly, { label: "Yesterday" })).toEqual(weekly);
  });
});
