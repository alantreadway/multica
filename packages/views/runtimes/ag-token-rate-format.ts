// AG patch: token rates span 0.1 to tens of thousands per second, so they are
// shown compact ("12.4K") with at most one decimal.

export function formatTokenRate(perSec: number, locale: string): string {
  return new Intl.NumberFormat(locale, {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(perSec);
}
