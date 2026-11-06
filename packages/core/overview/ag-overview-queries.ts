import { queryOptions, useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { WSClient } from "../api/ws-client";
import type { WSEventType } from "../types/events";

// AG patch: Overview dashboard. The endpoint is read-only and computed on the
// server, so live updates refetch it: task, issue and Desk events invalidate it,
// at most once per AG_OVERVIEW_REFRESH_MS.

export const agOverviewKeys = {
  all: () => ["ag-overview"] as const,
  detail: (wsId: string) => ["ag-overview", wsId] as const,
};

export const AG_OVERVIEW_REFRESH_MS = 5_000;

/** Ageing and run times move with the clock even when no event arrives. */
const POLL_MS = 60_000;

export function agOverviewOptions(wsId: string) {
  return queryOptions({
    queryKey: agOverviewKeys.detail(wsId),
    queryFn: () => api.getAGOverview(),
    refetchInterval: POLL_MS,
    // A stock backend answers 404.
    retry: false,
  });
}

export const AG_OVERVIEW_EVENTS: readonly WSEventType[] = [
  "task:queued",
  "task:dispatch",
  "task:running",
  "task:completed",
  "task:failed",
  "task:cancelled",
  "issue:created",
  "issue:updated",
  "issue:deleted",
  "ag:decision_changed",
];

/**
 * The first event schedules one refetch AG_OVERVIEW_REFRESH_MS later; events
 * inside that window ride along. Only a mounted Overview refetches.
 */
export function subscribeAGOverview(ws: WSClient, qc: QueryClient): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const schedule = () => {
    if (timer) return;
    timer = setTimeout(() => {
      timer = null;
      void qc.invalidateQueries({ queryKey: agOverviewKeys.all() });
    }, AG_OVERVIEW_REFRESH_MS);
  };
  const unsubs = AG_OVERVIEW_EVENTS.map((event) => ws.on(event, schedule));
  return () => {
    if (timer) clearTimeout(timer);
    timer = null;
    for (const unsub of unsubs) unsub();
  };
}

/** Waits for the server, then refetches: the new tree only exists once the server has built it. */
export function useCreateAGOverviewPin(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ref: string) => api.createAGOverviewPin(ref),
    onSuccess: () => qc.invalidateQueries({ queryKey: agOverviewKeys.detail(wsId) }),
  });
}

export function useDeleteAGOverviewPin(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (pinId: string) => api.deleteAGOverviewPin(pinId),
    onSuccess: () => qc.invalidateQueries({ queryKey: agOverviewKeys.detail(wsId) }),
  });
}
