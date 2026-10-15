import { z } from "zod";
import { parseWithFallback } from "../api/schema";
import type { AgentRuntime } from "../types";

// AG patch: drain badge. A patched daemon reports its drain state on every
// heartbeat and the backend stores it in runtime.metadata.ag_drain, only while
// the daemon is draining. Stock daemons and backends never write the key, so
// its absence is the normal case and must stay silent.

export interface RuntimeDrainState {
  /** Draining with nothing left running: safe to restart or upgrade. */
  idle: boolean;
  /** Tasks still running on the daemon; null when the report omits or garbles it. */
  activeTasks: number | null;
}

// Lenient on purpose: only `draining` gates the badge, so a malformed count
// or idle flag must not hide it.
const agDrainSchema = z.object({
  draining: z.boolean(),
  idle: z.boolean().catch(false),
  active_tasks: z.number().nullable().catch(null),
});

/**
 * The drain state to show for a runtime, or null when it is not draining.
 *
 * An offline runtime shows nothing: its daemon is gone, so a stored report
 * is stale until the runtime registers again and overwrites metadata.
 */
export function runtimeDrainState(
  runtime: Pick<AgentRuntime, "status" | "metadata">,
): RuntimeDrainState | null {
  if (runtime.status !== "online") return null;
  const raw = (runtime.metadata as Record<string, unknown> | null | undefined)
    ?.ag_drain;
  if (raw === undefined || raw === null) return null;

  const parsed = parseWithFallback<z.infer<typeof agDrainSchema> | null>(
    raw,
    agDrainSchema,
    null,
    { endpoint: "runtime.metadata.ag_drain" },
  );
  if (!parsed || !parsed.draining) return null;

  const count = parsed.active_tasks;
  const activeTasks =
    count !== null && Number.isFinite(count) && count >= 0
      ? Math.floor(count)
      : null;
  // Idle needs a known zero: an unknown count is never safe to restart on.
  return { idle: parsed.idle && activeTasks === 0, activeTasks };
}
