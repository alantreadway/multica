import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// AG patch: live token rate. A patched daemon reports each Claude/Codex
// runtime's tokens per second (60s average) on its heartbeat; the server keeps
// the latest in memory, serves it from GET /api/runtimes/ag-token-rates and
// publishes `ag:runtime_token_rate`. A stock backend has neither, which the
// sidebar shows as a dash. No API import here: api/ag-client.ts imports this.

/** Matches the server's TTL: three missed 15s heartbeats. */
export const TOKEN_RATE_TTL_MS = 45_000;

export interface AGTokenRate {
  runtimeId: string;
  workspaceId: string;
  /** Uncached input plus cache writes: new context sent. */
  inputPerSec: number;
  outputPerSec: number;
  cacheReadPerSec: number;
  activeRuns: number;
  observedAt: Date | null;
}

const perSec = z.number().finite().nonnegative().catch(0);

const rateSchema = z.object({
  runtime_id: z.string().min(1),
  workspace_id: z.string().catch(""),
  input_per_sec: perSec,
  output_per_sec: perSec,
  cache_read_per_sec: perSec,
  active_runs: z.number().finite().nonnegative().catch(0),
  observed_at: z.string().catch(""),
});

function toRate(raw: z.infer<typeof rateSchema>): AGTokenRate {
  const at = raw.observed_at ? new Date(raw.observed_at) : null;
  return {
    runtimeId: raw.runtime_id,
    workspaceId: raw.workspace_id,
    inputPerSec: raw.input_per_sec,
    outputPerSec: raw.output_per_sec,
    cacheReadPerSec: raw.cache_read_per_sec,
    activeRuns: raw.active_runs,
    observedAt: at && !Number.isNaN(at.getTime()) ? at : null,
  };
}

/** The list endpoint's body; an unreadable element drops only itself. */
export function parseAGTokenRateList(raw: unknown): AGTokenRate[] {
  const items = parseWithFallback<unknown[]>(raw, z.array(z.unknown()), [], {
    endpoint: "GET /api/runtimes/ag-token-rates",
  });
  return items.flatMap((item) => {
    const parsed = rateSchema.safeParse(item);
    return parsed.success ? [toRate(parsed.data)] : [];
  });
}

/** One `ag:runtime_token_rate` payload, or null when unreadable. */
export function parseAGTokenRateEvent(raw: unknown): AGTokenRate | null {
  const parsed = rateSchema.safeParse(raw);
  return parsed.success ? toRate(parsed.data) : null;
}

/** Replaces the runtime's entry, keeping the list order stable. */
export function mergeAGTokenRate(list: readonly AGTokenRate[], rate: AGTokenRate): AGTokenRate[] {
  const index = list.findIndex((r) => r.runtimeId === rate.runtimeId);
  if (index === -1) return [...list, rate];
  const next = list.slice();
  next[index] = rate;
  return next;
}

export interface AGTokenRateSummary {
  inputPerSec: number;
  outputPerSec: number;
  cacheReadPerSec: number;
  /** Runtimes heard from within the TTL, busiest output first. */
  runtimes: AGTokenRate[];
}

/** Totals over rates heard within the TTL, or null when there are none. */
export function summarizeAGTokenRates(
  rates: readonly AGTokenRate[] | undefined,
  now: number,
): AGTokenRateSummary | null {
  const live = (rates ?? []).filter(
    (r) => r.observedAt && now - r.observedAt.getTime() <= TOKEN_RATE_TTL_MS,
  );
  if (live.length === 0) return null;
  return {
    inputPerSec: live.reduce((sum, r) => sum + r.inputPerSec, 0),
    outputPerSec: live.reduce((sum, r) => sum + r.outputPerSec, 0),
    cacheReadPerSec: live.reduce((sum, r) => sum + r.cacheReadPerSec, 0),
    runtimes: live.slice().sort((a, b) => b.outputPerSec - a.outputPerSec),
  };
}
