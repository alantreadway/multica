import { z } from "zod";
import { parseWithFallback } from "../api/schema";
import type { AgentRuntime } from "../types";

// AG patch: drain badge. The key is written only while a patched daemon is
// draining, so its absence is the normal case and must stay silent.

export interface RuntimeDrainState {
  /** Draining with nothing left running: safe to restart or upgrade. */
  idle: boolean;
  /** Tasks still running on the daemon; null when the report omits or garbles it. */
  activeTasks: number | null;
}

// Lenient: only `draining` gates the badge, so a bad count must not hide it.
const agDrainSchema = z.object({
  draining: z.boolean(),
  idle: z.boolean().catch(false),
  active_tasks: z.number().nullable().catch(null),
});

/** The drain state to show, or null when not draining or offline (stale key). */
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
