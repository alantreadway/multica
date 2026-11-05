import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { WSClient } from "../api/ws-client";

// AG patch: Decision Desk. Events carry no data, so a change refetches.

export const agDecisionKeys = {
  all: () => ["ag-decisions"] as const,
  list: (wsId: string) => ["ag-decisions", wsId, "list"] as const,
  count: (wsId: string) => ["ag-decisions", wsId, "count"] as const,
};

/** Polls because a sync that changes no card publishes nothing, and the staleness warning reads its timestamp. */
const LIST_REFRESH_MS = 60_000;

export function agDecisionListOptions(wsId: string) {
  return queryOptions({
    queryKey: agDecisionKeys.list(wsId),
    queryFn: () => api.listAGDecisions(),
    refetchInterval: LIST_REFRESH_MS,
    // A stock backend answers 404.
    retry: false,
  });
}

export function agDecisionCountOptions(wsId: string) {
  return queryOptions({
    queryKey: agDecisionKeys.count(wsId),
    queryFn: () => api.getAGDecisionCount(),
    retry: false,
  });
}

export function subscribeAGDecisions(ws: WSClient, qc: QueryClient): () => void {
  return ws.on("ag:decision_changed", () => {
    void qc.invalidateQueries({ queryKey: agDecisionKeys.all() });
  });
}
