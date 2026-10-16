"use client";

import { useMemo } from "react";
import type { AgentRuntime } from "@multica/core/types";
import { runtimeDrainState } from "@multica/core/runtimes/ag-drain";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

// AG patch: drain badge. Renders nothing unless the daemon is draining.
export function DrainBadge({
  runtime,
}: {
  runtime: Pick<AgentRuntime, "status" | "metadata">;
}) {
  const { t } = useT("runtimes");
  const { status, metadata } = runtime;
  const drain = useMemo(
    () => runtimeDrainState({ status, metadata }),
    [status, metadata],
  );
  if (!drain) return null;

  const label = drain.idle
    ? t(($) => $.drain.idle)
    : drain.activeTasks === null
      ? t(($) => $.drain.draining_unknown)
      : t(($) => $.drain.draining, { running: drain.activeTasks });
  const hint = drain.idle
    ? t(($) => $.drain.idle_hint)
    : t(($) => $.drain.draining_hint);
  const tone = drain.idle
    ? "bg-success/10 text-success"
    : "bg-warning/10 text-warning";

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className={`inline-flex shrink-0 items-center rounded-xs px-1 text-micro font-medium ${tone}`}
          >
            {label}
          </span>
        }
      />
      <TooltipContent>{hint}</TooltipContent>
    </Tooltip>
  );
}
