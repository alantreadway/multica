// AG patch: fork-only ApiClient methods. client.ts installs them onto the
// class with one hook, and the module augmentation below types them on every
// ApiClient instance, so callers use `api.reorderLabels(...)` as usual.
import type { ListLabelsResponse, ReorderLabelsRequest } from "../types";
import type { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import { EMPTY_LIST_LABELS_RESPONSE, ListLabelsResponseSchema } from "./schemas";

declare module "./client" {
  interface ApiClient {
    reorderLabels(data: ReorderLabelsRequest): Promise<ListLabelsResponse>;
    // Backup account setting for a Claude runtime (see runtimes/ag-quota.ts).
    updateAGRuntimeSettings(
      runtimeId: string,
      body: { backup_enabled: boolean; backup_max_concurrent: number },
    ): Promise<{ backup_enabled: boolean; backup_max_concurrent: number }>;
  }
}

// ApiClient's `fetch` is private. The fork's methods reuse it (base URL, auth
// headers, logging, ApiError) through this one structural cast.
type ClientFetch = { fetch<T>(path: string, init?: RequestInit): Promise<T> };
function clientFetch<T>(client: ApiClient, path: string, init?: RequestInit): Promise<T> {
  return (client as unknown as ClientFetch).fetch<T>(path, init);
}

type AgApiMethodName = "reorderLabels" | "updateAGRuntimeSettings";

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
