// AG patch: a hold on the weekly window can end days away, so a bare time misleads.

export function formatHoldUntil(until: Date, locale: string, now: Date = new Date()): string {
  const sameDay =
    until.getFullYear() === now.getFullYear() &&
    until.getMonth() === now.getMonth() &&
    until.getDate() === now.getDate();
  return new Intl.DateTimeFormat(locale, {
    ...(sameDay ? {} : { weekday: "short", day: "numeric", month: "short" }),
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(until);
}
