import { z } from "zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { parseWithFallback } from "../api/schema";
import type { AgentRuntime } from "../types";
import { runtimeKeys } from "./queries";

// AG patch: Claude quota section and sidebar warning. The daemon reports
// `metadata.ag_quota` on every heartbeat and the server stores the backup
// setting in `metadata.ag_settings`. Both are absent on an unpatched daemon,
// which must stay silent.

export type QuotaAccount = "primary" | "backup";

export type BackupReason =
  | "not_logged_in"
  | "same_account"
  | "check_failed"
  | "unknown";

export interface QuotaWindow {
  /** 0..1 and above; null when the daemon has not seen one. */
  utilization: number | null;
  resetsAt: Date | null;
}

export interface BackupSetting {
  enabled: boolean;
  maxConcurrent: number;
}

export interface RuntimeQuota {
  /** Whose windows are reported; an unknown value reads as primary. */
  account: QuotaAccount;
  holdUntil: Date | null;
  holdWindow: string | null;
  effectiveCap: number | null;
  max: number | null;
  fiveHour: QuotaWindow | null;
  sevenDay: QuotaWindow | null;
  backup: {
    detected: boolean;
    valid: boolean;
    reason: BackupReason | null;
    /** The stored setting (`ag_settings`), else what the daemon last received. */
    setting: BackupSetting;
  };
}

// Every field is optional with a default: a report from a newer daemon, or one
// that omits a window, must degrade to "unknown" rather than hide the section.
const windowSchema = z.object({
  utilization: z.number().finite().nullable().catch(null),
  resets_at: z.string().nullable().catch(null),
});

const quotaSchema = z.object({
  account: z.string().catch("primary"),
  hold: z
    .object({
      until: z.string().catch(""),
      window: z.string().nullable().catch(null),
    })
    .nullable()
    .catch(null),
  effective_cap: z.number().finite().nullable().catch(null),
  max: z.number().finite().nullable().catch(null),
  windows: z
    .object({
      five_hour: windowSchema.nullable().catch(null),
      seven_day: windowSchema.nullable().catch(null),
    })
    .catch({ five_hour: null, seven_day: null }),
  backup: z
    .object({
      detected: z.boolean().catch(false),
      valid: z.boolean().catch(false),
      reason: z.string().catch(""),
      enabled: z.boolean().catch(false),
      max_concurrent: z.number().finite().catch(2),
    })
    .catch({
      detected: false,
      valid: false,
      reason: "",
      enabled: false,
      max_concurrent: 2,
    }),
});

const settingsSchema = z.object({
  backup_enabled: z.boolean(),
  backup_max_concurrent: z.number().finite(),
});

const REASONS: readonly string[] = [
  "not_logged_in",
  "same_account",
  "check_failed",
];

function toDate(value: string | null | undefined): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function toWindow(
  raw: z.infer<typeof windowSchema> | null | undefined,
): QuotaWindow | null {
  if (!raw) return null;
  return { utilization: raw.utilization, resetsAt: toDate(raw.resets_at) };
}

function metadataOf(runtime: Pick<AgentRuntime, "metadata">) {
  return (runtime.metadata ?? {}) as Record<string, unknown>;
}

/** The quota state to show, or null when the key is absent or not an object. */
export function runtimeQuota(
  runtime: Pick<AgentRuntime, "metadata">,
): RuntimeQuota | null {
  const metadata = metadataOf(runtime);
  const raw = metadata.ag_quota;
  if (raw === undefined || raw === null) return null;

  const parsed = parseWithFallback<z.infer<typeof quotaSchema> | null>(
    raw,
    quotaSchema,
    null,
    { endpoint: "runtime.metadata.ag_quota" },
  );
  if (!parsed) return null;

  // Only the stored setting is current right after a save: the echo in
  // `ag_quota.backup` lags until the daemon's next report.
  const stored =
    metadata.ag_settings == null
      ? null
      : parseWithFallback<z.infer<typeof settingsSchema> | null>(
          metadata.ag_settings,
          settingsSchema,
          null,
          { endpoint: "runtime.metadata.ag_settings" },
        );
  const enabled = stored?.backup_enabled ?? parsed.backup.enabled;
  const maxConcurrent =
    stored?.backup_max_concurrent ?? parsed.backup.max_concurrent;

  const holdUntil = toDate(parsed.hold?.until);
  const reason = parsed.backup.reason;
  return {
    account: parsed.account === "backup" ? "backup" : "primary",
    holdUntil,
    holdWindow: holdUntil ? (parsed.hold?.window ?? null) : null,
    effectiveCap: parsed.effective_cap,
    max: parsed.max,
    fiveHour: toWindow(parsed.windows.five_hour),
    sevenDay: toWindow(parsed.windows.seven_day),
    backup: {
      detected: parsed.backup.detected,
      valid: parsed.backup.valid,
      reason: parsed.backup.valid
        ? null
        : REASONS.includes(reason)
          ? (reason as BackupReason)
          : "unknown",
      setting: { enabled, maxConcurrent: Math.round(maxConcurrent) },
    },
  };
}

export interface QuotaSidebarState {
  /** Some online runtime is running on the backup account. */
  backup: boolean;
  /** The earliest hold end across online runtimes; null when none is held. */
  holdUntil: Date | null;
}

/** What the sidebar shows. Offline runtimes are skipped: their keys are stale. */
export function quotaSidebarState(
  runtimes: readonly Pick<AgentRuntime, "status" | "metadata">[],
): QuotaSidebarState {
  let backup = false;
  let holdUntil: Date | null = null;
  for (const runtime of runtimes) {
    if (runtime.status !== "online") continue;
    const quota = runtimeQuota(runtime);
    if (!quota) continue;
    if (quota.account === "backup") backup = true;
    if (quota.holdUntil && (!holdUntil || quota.holdUntil < holdUntil)) {
      holdUntil = quota.holdUntil;
    }
  }
  return { backup, holdUntil };
}

const AG_REFRESH_ACTIONS: readonly unknown[] = ["quota", "drain", "ag_settings"];

/**
 * True for the `daemon:register` refreshes the AG patches publish. They are
 * not registrations, so a dialog waiting for a new runtime must ignore them.
 */
export function isAGDaemonRefresh(payload: unknown): boolean {
  if (typeof payload !== "object" || payload === null) return false;
  return AG_REFRESH_ACTIONS.includes((payload as { action?: unknown }).action);
}

/** Saves the backup setting; the caller awaits the server, nothing is optimistic. */
export function useAGRuntimeSettings(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      runtimeId,
      ...body
    }: {
      runtimeId: string;
      backup_enabled: boolean;
      backup_max_concurrent: number;
    }) => api.updateAGRuntimeSettings(runtimeId, body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}
