// AG patch: fork-only ApiClient methods. client.ts installs them onto the
// class with one hook, and the module augmentation below types them on every
// ApiClient instance, so callers use `api.reorderLabels(...)` as usual.
import type { ListLabelsResponse, ReorderLabelsRequest } from "../types";
import type { ApiClient } from "./client";
import {
  parseAGDecisionCount,
  parseAGDecisionList,
  type AGDecisionIntentInput,
  type AGDecisionList,
} from "../decisions/ag-decisions";
import { parseAGTokenRateList, type AGTokenRate } from "../runtimes/ag-token-rate";
import { parseAGOverview, parseAGOverviewPin, type AGOverview, type AGOverviewPin } from "../overview/ag-overview";
import { parseWithFallback } from "./schema";
import { EMPTY_LIST_LABELS_RESPONSE, ListLabelsResponseSchema } from "./schemas";

declare module "./client" {
  interface ApiClient {
    reorderLabels(data: ReorderLabelsRequest): Promise<ListLabelsResponse>;
    // Backup account and pacing settings for a Claude runtime (see runtimes/ag-quota.ts).
    updateAGRuntimeSettings(
      runtimeId: string,
      body: {
        backup_enabled: boolean;
        backup_max_concurrent: number;
        pace_off: boolean;
        backup_urgent_until?: string;
      },
    ): Promise<{
      backup_enabled: boolean;
      backup_max_concurrent: number;
      pace_off: boolean;
      backup_urgent_until?: string;
    }>;
    // Live token rates of the current workspace (see runtimes/ag-token-rate.ts).
    listAGTokenRates(): Promise<AGTokenRate[]>;
    // Decision Desk (see decisions/ag-decisions.ts).
    listAGDecisions(): Promise<AGDecisionList>;
    getAGDecisionCount(): Promise<number>;
    createAGDecisionIntent(cardId: string, intent: AGDecisionIntentInput): Promise<void>;
    cancelAGDecisionIntent(intentId: string): Promise<void>;
    // Overview dashboard (see overview/ag-overview.ts).
    getAGOverview(): Promise<AGOverview>;
    createAGOverviewPin(ref: string): Promise<AGOverviewPin | null>;
    deleteAGOverviewPin(pinId: string): Promise<void>;
  }
}

// ApiClient's `fetch` is private. The fork's methods reuse it (base URL, auth
// headers, logging, ApiError) through this one structural cast.
type ClientFetch = { fetch<T>(path: string, init?: RequestInit): Promise<T> };
function clientFetch<T>(client: ApiClient, path: string, init?: RequestInit): Promise<T> {
  return (client as unknown as ClientFetch).fetch<T>(path, init);
}

type AgApiMethodName =
  | "reorderLabels"
  | "updateAGRuntimeSettings"
  | "listAGTokenRates"
  | "listAGDecisions"
  | "getAGDecisionCount"
  | "createAGDecisionIntent"
  | "cancelAGDecisionIntent"
  | "getAGOverview"
  | "createAGOverviewPin"
  | "deleteAGOverviewPin";

const agApiMethods: Pick<ApiClient, AgApiMethodName> & ThisType<ApiClient> = {
  async reorderLabels(data) {
    const raw = await clientFetch<unknown>(this, `/api/labels/order`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, ListLabelsResponseSchema, EMPTY_LIST_LABELS_RESPONSE, {
      endpoint: "PUT /api/labels/order",
    });
  },

  async updateAGRuntimeSettings(runtimeId, body) {
    return clientFetch(this, `/api/runtimes/${runtimeId}/ag-settings`, {
      method: "PUT",
      body: JSON.stringify(body),
    });
  },

  async listAGTokenRates() {
    return parseAGTokenRateList(await clientFetch<unknown>(this, `/api/runtimes/ag-token-rates`));
  },

  async listAGDecisions() {
    return parseAGDecisionList(await clientFetch<unknown>(this, `/api/ag/decisions`));
  },

  async getAGDecisionCount() {
    return parseAGDecisionCount(await clientFetch<unknown>(this, `/api/ag/decisions/count`));
  },

  async createAGDecisionIntent(cardId, intent) {
    await clientFetch<unknown>(this, `/api/ag/decisions/${cardId}/intents`, {
      method: "POST",
      body: JSON.stringify(intent),
    });
  },

  async cancelAGDecisionIntent(intentId) {
    await clientFetch<unknown>(this, `/api/ag/decisions/intents/${intentId}`, { method: "DELETE" });
  },

  async getAGOverview() {
    return parseAGOverview(await clientFetch<unknown>(this, `/api/ag/overview`));
  },

  async createAGOverviewPin(ref) {
    return parseAGOverviewPin(
      await clientFetch<unknown>(this, `/api/ag/overview/pins`, { method: "POST", body: JSON.stringify({ ref }) }),
    );
  },

  async deleteAGOverviewPin(pinId) {
    await clientFetch<unknown>(this, `/api/ag/overview/pins/${encodeURIComponent(pinId)}`, { method: "DELETE" });
  },
};

export function installAgApiMethods(cls: typeof ApiClient): void {
  for (const [name, method] of Object.entries(agApiMethods)) {
    // Non-enumerable, like a method declared in the class body.
    Object.defineProperty(cls.prototype, name, {
      value: method,
      writable: true,
      configurable: true,
    });
  }
}
