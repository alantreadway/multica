import { queryOptions, useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { WSClient } from "../api/ws-client";
import type { AGDecisionIntentInput } from "./ag-decisions";

// AG patch: Decision Desk. Events carry no data, so a change refetches.

export const agDecisionKeys = {
  all: () => ["ag-decisions"] as const,
  list: (wsId: string) => ["ag-decisions", wsId, "list"] as const,
  count: (wsId: string) => ["ag-decisions", wsId, "count"] as const,
};

/** Polls because a sync that changes no card publishes nothing, and the staleness warning reads its timestamp. */
const LIST_REFRESH_MS = 30_000;

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

/** Waits for the server: the card shows Queued only once the intent exists. */
export function useCreateAGDecisionIntent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ cardId, intent }: { cardId: string; intent: AGDecisionIntentInput }) =>
      api.createAGDecisionIntent(cardId, intent),
    onSettled: () => qc.invalidateQueries({ queryKey: agDecisionKeys.all() }),
  });
}

export function useCancelAGDecisionIntent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (intentId: string) => api.cancelAGDecisionIntent(intentId),
    onSettled: () => qc.invalidateQueries({ queryKey: agDecisionKeys.all() }),
  });
}
