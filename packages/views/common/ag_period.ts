// AG patch: a "Yesterday" period beside the trailing-N-day ranges on the
// Analytics page and the runtime detail page.
//
// A period is either a trailing day count (`1` is today, from 00:00 in the
// viewer's tz) or YESTERDAY, the previous calendar day. Yesterday is a 1-day
// window that ends one day back, so the pages keep reading `days` and add
// `endOffset` where a window has to stop before today.

export const YESTERDAY = "yesterday";
export type Yesterday = typeof YESTERDAY;
export type AgPeriod<D extends number = number> = D | Yesterday;

export function agPeriodDays(period: AgPeriod): number {
  return period === YESTERDAY ? 1 : period;
}

export function agPeriodEndOffset(period: AgPeriod): number {
  return period === YESTERDAY ? 1 : 0;
}

/** Parses a range-selector value written as `String(period)`. */
export function agParsePeriod<D extends number>(value: string): AgPeriod<D> {
  return value === YESTERDAY ? YESTERDAY : (Number(value) as D);
}

/** Inserts the Yesterday option after the 1-day one. Returns the options
 *  unchanged when no 1-day option is offered (weekly grain). */
export function agWithYesterday<T extends { value: AgPeriod }>(
  options: readonly T[],
  yesterday: Omit<T, "value">,
): (T | (Omit<T, "value"> & { value: Yesterday }))[] {
  const at = options.findIndex((o) => o.value === 1);
  if (at < 0) return [...options];
  return [
    ...options.slice(0, at + 1),
    { ...yesterday, value: YESTERDAY },
    ...options.slice(at + 1),
  ];
}
