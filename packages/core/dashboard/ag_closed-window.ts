// AG patch: windows that end before today ("Yesterday") for the dateless
// per-agent dashboard rollups.
//
// Those endpoints only take a trailing `days` count, closed at exactly N
// calendar days in the viewer's tz (parseExactSinceParamInTZ), so the window
// [today - endOffset - (days - 1), today - endOffset] is the difference of two
// trailing windows: `days + endOffset` minus `endOffset`. Every numeric field
// in these rows is a count or a sum, so it subtracts; the string fields
// (agent, provider, model, failure reason) identify the row.

type Row = object;

/** Query-key part for a window: plain `days` for a trailing window, so keys
 *  for the existing ranges are unchanged. */
export function agWindowKey(days: number, endOffset: number): number | string {
  return endOffset > 0 ? `${days}-${endOffset}` : days;
}

export async function agFetchWindow<T extends Row>(
  fetchTrailing: (days: number) => Promise<T[]>,
  days: number,
  endOffset: number,
): Promise<T[]> {
  if (endOffset <= 0) return fetchTrailing(days);
  const [wide, recent] = await Promise.all([
    fetchTrailing(days + endOffset),
    fetchTrailing(endOffset),
  ]);
  return agSubtractRows(wide, recent);
}

function rowKey(row: Row): string {
  return JSON.stringify(
    Object.entries(row)
      .filter(([, v]) => typeof v === "string")
      .sort(([a], [b]) => a.localeCompare(b)),
  );
}

/** `wide` minus `recent`, matched on the non-numeric fields. The two requests
 *  are not one snapshot, so a count can come out negative when today's runs
 *  land between them; clamp at zero and drop rows left with nothing. */
export function agSubtractRows<T extends Row>(wide: T[], recent: T[]): T[] {
  const recentByKey = new Map<string, T>();
  for (const r of recent) recentByKey.set(rowKey(r), r);

  const out: T[] = [];
  for (const w of wide) {
    const r = recentByKey.get(rowKey(w)) as Record<string, unknown> | undefined;
    const next: Record<string, unknown> = {
      ...(w as Record<string, unknown>),
    };
    let any = false;
    for (const [field, value] of Object.entries(w)) {
      if (typeof value !== "number") continue;
      const sub = typeof r?.[field] === "number" ? (r[field] as number) : 0;
      const diff = Math.max(0, value - sub);
      next[field] = diff;
      if (diff > 0) any = true;
    }
    if (any) out.push(next as T);
  }
  return out;
}
