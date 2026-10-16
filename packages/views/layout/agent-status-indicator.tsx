"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspacePresenceMap } from "@multica/core/agents";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { runtimeDrainState } from "@multica/core/runtimes/ag-drain";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@multica/ui/components/ui/hover-card";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@multica/ui/components/ui/sidebar";
import { WorkingAgentsHoverContent } from "../issues/components/workspace-agent-working-chip";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

// AG patch: sidebar agent status. One compact row above Analytics: how many
// agents are working, plus a drain marker while a patched daemon is draining.
// It reads only queries the workspace presence prefetch already keeps warm, so
// it adds no requests and no polling.

interface DrainingRuntime {
  id: string;
  name: string;
  idle: boolean;
  activeTasks: number | null;
}

export function AgentStatusIndicator({ wsId }: { wsId: string | undefined }) {
  const { t } = useT("layout");
  const p = useWorkspacePaths();
  const { byAgent, loading } = useWorkspacePresenceMap(wsId);
  // Same key as the presence hook's runtime read, so this is a cache hit.
  const { data: runtimes } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });

  const { working, running, queued } = useMemo(() => {
    const working: { id: string; running_task_count: number }[] = [];
    let running = 0;
    let queued = 0;
    for (const [id, detail] of byAgent) {
      running += detail.runningCount;
      queued += detail.queuedCount;
      if (detail.runningCount > 0) {
        working.push({ id, running_task_count: detail.runningCount });
      }
    }
    return { working, running, queued };
  }, [byAgent]);

  const draining = useMemo(() => {
    const out: DrainingRuntime[] = [];
    for (const runtime of runtimes ?? []) {
      const state = runtimeDrainState(runtime);
      if (state) out.push({ id: runtime.id, name: runtime.name, ...state });
    }
    return out;
  }, [runtimes]);

  if (!wsId) return null;

  const isDraining = draining.length > 0;
  const drained = isDraining && draining.every((r) => r.idle);
  const unknown = loading;
  const isRunning = !unknown && running > 0;

  let summary: string;
  if (unknown) {
    summary = "—";
  } else if (working.length === 0) {
    summary =
      queued > 0
        ? t(($) => $.agent_status.none_queued, { count: queued })
        : t(($) => $.agent_status.none);
  } else {
    summary = [
      t(($) => $.agent_status.agents, { count: working.length }),
      t(($) => $.agent_status.running, { count: running }),
      ...(queued > 0 ? [t(($) => $.agent_status.queued, { count: queued })] : []),
    ].join(" · ");
  }

  let drainLabel: string | null = null;
  if (isDraining) {
    if (drained) {
      drainLabel = t(($) => $.agent_status.drained);
    } else {
      const counts = draining.map((r) => r.activeTasks);
      drainLabel = counts.some((c) => c === null)
        ? t(($) => $.agent_status.draining_unknown)
        : t(($) => $.agent_status.draining, {
            running: counts.reduce<number>((sum, c) => sum + (c ?? 0), 0),
          });
    }
  }

  const trigger = (
    <SidebarMenuButton
      render={<AppLink href={isDraining ? p.runtimes() : p.agents()} />}
      aria-label={unknown ? t(($) => $.agent_status.unknown) : undefined}
      className={cn(
        "h-auto py-1.5 text-muted-foreground hover:bg-sidebar-accent/70",
        (unknown || (!isRunning && !isDraining)) && "opacity-70",
      )}
    >
      <span className="flex size-4 shrink-0 items-center justify-center">
        <span
          data-testid="agent-status-dot"
          className={cn(
            "size-2 rounded-full",
            isRunning ? "animate-pulse bg-brand" : "bg-muted-foreground/40",
          )}
        />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-caption leading-snug">{summary}</span>
        {drainLabel && (
          <span
            data-testid="agent-status-drain"
            className={cn(
              "flex items-center gap-1.5 truncate text-caption",
              drained ? "text-success" : "text-warning",
            )}
          >
            <span
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                drained ? "bg-success" : "bg-warning",
              )}
            />
            <span className="truncate">{drainLabel}</span>
          </span>
        )}
      </span>
    </SidebarMenuButton>
  );

  return (
    <SidebarMenu className="gap-0.5">
      <SidebarMenuItem>
        <HoverCard>
          <HoverCardTrigger render={trigger} />
          <HoverCardContent side="right" align="end" className="w-72">
            <div className="flex flex-col gap-3">
              <WorkingAgentsHoverContent agents={unknown ? undefined : working} />
              {isDraining && (
                <div className="flex flex-col gap-1">
                  <div className="text-caption font-medium text-muted-foreground">
                    {t(($) => $.agent_status.hover_runtimes_header)}
                  </div>
                  {draining.map((r) => (
                    <div key={r.id} className="truncate text-caption">
                      {r.idle
                        ? t(($) => $.agent_status.hover_runtime_drained, { name: r.name })
                        : r.activeTasks === null
                          ? t(($) => $.agent_status.hover_runtime_draining_unknown, {
                              name: r.name,
                            })
                          : t(($) => $.agent_status.hover_runtime_draining, {
                              name: r.name,
                              running: r.activeTasks,
                            })}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </HoverCardContent>
        </HoverCard>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
