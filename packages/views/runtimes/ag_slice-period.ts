import type { RuntimeUsage } from "@multica/core/types";
import {
  agPeriodEndOffset,
  YESTERDAY,
  type AgPeriod,
  type Yesterday,
} from "../common/ag_period";
import { addDaysIso, sliceWindow, todayIso } from "./utils";

// AG patch: the runtime detail page's 1d and Yesterday periods.
//
// sliceWindow keeps upstream's N+1-day window for the trailing ranges (see
// sinceFromDays in server/internal/handler/runtime.go). A single-day period
// instead means one calendar day in the viewer's tz, as on the Analytics
// page: 1d is today, Yesterday is yesterday, and the prior window for the
// KPI delta is the day before it.
export function agSlicePeriod(
  usage: readonly RuntimeUsage[],
  period: AgPeriod,
  tz: string,
): { filtered: RuntimeUsage[]; prevFiltered: RuntimeUsage[] } {
  if (!agIsSingleDay(period)) return sliceWindow(usage, period, tz);
  const day = addDaysIso(todayIso(tz), -agPeriodEndOffset(period));
  const prevDay = addDaysIso(day, -1);
  return {
    filtered: usage.filter((u) => u.date === day),
    prevFiltered: usage.filter((u) => u.date === prevDay),
  };
}

/** The per-agent rollup behind Cost by agent only takes a trailing `days`
 *  count with the N+1 cutoff, so it cannot be narrowed to one calendar day. */
export function agIsSingleDay(period: AgPeriod): period is 1 | Yesterday {
  return period === YESTERDAY || period === 1;
}
