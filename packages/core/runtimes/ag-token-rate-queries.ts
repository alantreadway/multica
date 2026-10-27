import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { WSClient } from "../api/ws-client";
import {
  mergeAGTokenRate,
  parseAGTokenRateEvent,
  type AGTokenRate,
} from "./ag-token-rate";

// AG patch: live token rate. The list seeds the cache; `ag:runtime_token_rate`
// events patch it. See ag-token-rate.ts.

export const agTokenRateKeys = {
  list: (wsId: string) => ["runtimes", "ag-token-rates", wsId] as const,
};

export function agTokenRateListOptions(wsId: string) {
  return queryOptions({
    queryKey: agTokenRateKeys.list(wsId),
    queryFn: () => api.listAGTokenRates(),
    // A stock backend answers 404; the sidebar then shows a dash.
    retry: false,
  });
}

/**
 * Patches the workspace's cached list from each event. Before the list has
 * loaded there is nothing to patch: seeding it from one event would read as
 * the whole list and skip the fetch.
 */
export function subscribeAGTokenRates(ws: WSClient, qc: QueryClient): () => void {
  return ws.on("ag:runtime_token_rate", (payload) => {
    const rate = parseAGTokenRateEvent(payload);
    if (!rate?.workspaceId) return;
    qc.setQueryData<AGTokenRate[]>(agTokenRateKeys.list(rate.workspaceId), (old) =>
      old ? mergeAGTokenRate(old, rate) : old,
    );
  });
}
