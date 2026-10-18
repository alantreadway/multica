"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Lock } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  runtimeQuota,
  useAGRuntimeSettings,
  type QuotaWindow,
  type RuntimeQuota,
} from "@multica/core/runtimes/ag-quota";
import type { AgentRuntime } from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { useLocale } from "../../i18n/use-locale";

// AG patch: Claude quota section. Renders nothing without a quota report, so an
// unpatched daemon or a non-Claude runtime shows no section.

const MAX_BACKUP_CONCURRENT = 100;

function formatReset(date: Date, locale: string, withDay: boolean): string {
  return new Intl.DateTimeFormat(locale, {
    ...(withDay ? { weekday: "short", day: "numeric", month: "short" } : {}),
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(date);
}

function WindowRow({
  label,
  window: w,
  withDay,
}: {
  label: string;
  window: QuotaWindow | null;
  withDay: boolean;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  const used = w?.utilization ?? null;
  const percent = used === null ? null : Math.round(used * 100);
  const tone =
    used === null
      ? "bg-faint-foreground"
      : used >= 0.95
        ? "bg-destructive"
        : used >= 0.8
          ? "bg-warning"
          : "bg-brand";
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between gap-2 text-caption">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground">
          {percent === null ? "—" : `${percent}%`}
          {w?.resetsAt && (
            <>
              {" · "}
              {t(($) => $.quota.resets, {
                time: formatReset(w.resetsAt, locale, withDay),
              })}
            </>
          )}
        </span>
      </div>
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent === null ? undefined : Math.min(percent, 100)}
        className="h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div
          className={cn("h-full rounded-full", tone)}
          style={{ width: `${Math.min(Math.max(percent ?? 0, 0), 100)}%` }}
        />
      </div>
    </div>
  );
}

function BackupStatus({ quota }: { quota: RuntimeQuota }) {
  const { t } = useT("runtimes");
  const { backup } = quota;
  if (backup.valid) {
    return <span className="text-success">{t(($) => $.quota.backup_ready)}</span>;
  }
  if (!backup.detected) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.quota.backup_not_found)}
      </span>
    );
  }
  return (
    <span className="text-warning">
      {t(($) => $.quota.backup_invalid)}
      {" · "}
      {backup.reason === "not_logged_in"
        ? t(($) => $.quota.reason.not_logged_in)
        : backup.reason === "same_account"
          ? t(($) => $.quota.reason.same_account)
          : backup.reason === "check_failed"
            ? t(($) => $.quota.reason.check_failed)
            : t(($) => $.quota.reason.unknown)}
    </span>
  );
}

function QuotaBody({
  runtime,
  quota,
  canEdit,
}: {
  runtime: AgentRuntime;
  quota: RuntimeQuota;
  canEdit: boolean;
}) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const save = useAGRuntimeSettings(wsId);
  const { setting } = quota.backup;
  const online = runtime.status === "online";
  const max = Math.min(
    quota.max !== null && quota.max >= 1 ? Math.floor(quota.max) : MAX_BACKUP_CONCURRENT,
    MAX_BACKUP_CONCURRENT,
  );

  // The input edits a draft; the switch and the saved value always show server state.
  const [draft, setDraft] = useState(String(setting.maxConcurrent));
  useEffect(() => {
    setDraft(String(setting.maxConcurrent));
  }, [setting.maxConcurrent]);

  const editable = canEdit && quota.backup.valid && !save.isPending;

  const submit = (next: { enabled: boolean; maxConcurrent: number }) => {
    save.mutate(
      {
        runtimeId: runtime.id,
        backup_enabled: next.enabled,
        backup_max_concurrent: next.maxConcurrent,
      },
      {
        onError: () => {
          setDraft(String(setting.maxConcurrent));
          toast.error(t(($) => $.quota.save_failed));
        },
      },
    );
  };

  const commitDraft = () => {
    const value = Number(draft);
    if (!Number.isInteger(value) || value < 1 || value > max) {
      setDraft(String(setting.maxConcurrent));
      return;
    }
    if (value !== setting.maxConcurrent) {
      submit({ enabled: setting.enabled, maxConcurrent: value });
    }
  };

  return (
    <div className="space-y-4 px-4 py-3">
      <div className="space-y-3">
        <WindowRow
          label={t(($) => $.quota.five_hour)}
          window={quota.fiveHour}
          withDay={false}
        />
        <WindowRow
          label={t(($) => $.quota.seven_day)}
          window={quota.sevenDay}
          withDay
        />
      </div>

      {online && (
        <div className="space-y-0.5 text-caption">
          {quota.holdUntil && (
            <p className="text-warning">
              {t(($) => $.quota.hold, {
                time: formatReset(quota.holdUntil, locale, false),
              })}
            </p>
          )}
          {quota.effectiveCap !== null && quota.max !== null && (
            <p className="text-muted-foreground">
              {t(($) => $.quota.cap, { cap: quota.effectiveCap, max: quota.max })}
            </p>
          )}
        </div>
      )}

      <div className="space-y-2 border-t pt-3">
        <div className="flex items-center justify-between gap-2 text-caption">
          <span className="font-medium">{t(($) => $.quota.backup_title)}</span>
          <BackupStatus quota={quota} />
        </div>
        <div className="flex items-center justify-between gap-3">
          <label
            htmlFor={`ag-backup-enabled-${runtime.id}`}
            className="text-caption"
          >
            {t(($) => $.quota.backup_enable)}
          </label>
          <Switch
            id={`ag-backup-enabled-${runtime.id}`}
            size="sm"
            checked={setting.enabled}
            disabled={!editable}
            onCheckedChange={(checked) =>
              submit({ enabled: checked, maxConcurrent: setting.maxConcurrent })
            }
          />
        </div>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.quota.backup_urgent_only)}
        </p>
        <div className="flex items-center justify-between gap-3">
          <label
            htmlFor={`ag-backup-max-${runtime.id}`}
            className="text-caption"
          >
            {t(($) => $.quota.backup_max)}
          </label>
          <Input
            id={`ag-backup-max-${runtime.id}`}
            type="number"
            inputMode="numeric"
            min={1}
            max={max}
            value={draft}
            disabled={!editable || !setting.enabled}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={commitDraft}
            onKeyDown={(e) => {
              if (e.key === "Enter") commitDraft();
            }}
            className="h-7 w-20 text-caption"
          />
        </div>
      </div>
    </div>
  );
}

export function QuotaSection({
  runtime,
  canEdit,
}: {
  runtime: AgentRuntime;
  canEdit: boolean;
}) {
  const { t } = useT("runtimes");
  const quota = runtimeQuota(runtime);
  if (!quota) return null;
  return (
    <div className="rounded-lg border" data-testid="ag-quota-section">
      <div className="flex items-center justify-between border-b px-4 py-2.5">
        <span className="text-caption font-semibold">
          {t(($) => $.quota.title)}
        </span>
        <span className="inline-flex items-center gap-2 text-caption">
          {quota.account === "backup" && (
            <span className="rounded-xs bg-warning/10 px-1 text-micro font-medium text-warning">
              {t(($) => $.quota.on_backup)}
            </span>
          )}
          {!canEdit && (
            <span className="inline-flex items-center gap-1 text-muted-foreground">
              <Lock className="h-3 w-3" />
              {t(($) => $.detail.read_only)}
            </span>
          )}
        </span>
      </div>
      <QuotaBody runtime={runtime} quota={quota} canEdit={canEdit} />
    </div>
  );
}
