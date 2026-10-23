/**
 * @vitest-environment jsdom
 */
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { issueKeys } from "../issues/queries";
import { workspaceKeys } from "../workspace/queries";
import type { Label, ListLabelsResponse } from "../types";
import { useReorderLabels } from "./mutations";
import { labelKeys } from "./queries";

vi.mock("../hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

const labelA: Label = {
  id: "label-a",
  workspace_id: "ws-1",
  name: "bug",
  color: "#ef4444",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const labelB: Label = {
  id: "label-b",
  workspace_id: "ws-1",
  name: "feature",
  color: "#22c55e",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("useReorderLabels", () => {
  afterEach(() => vi.restoreAllMocks());

  const seed = (qc: QueryClient) =>
    qc.setQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"), {
      labels: [
        { ...labelA, position: 0 },
        { ...labelB, position: 1 },
      ],
      total: 2,
    });

  it("reorders the cached list optimistically", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seed(qc);
    const write = deferred<ListLabelsResponse>();
    setApiInstance({ reorderLabels: vi.fn(() => write.promise) } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    act(() =>
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] }),
    );
    await waitFor(() =>
      expect(
        qc.getQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"))?.labels.map((l) => l.id),
      ).toEqual([labelB.id, labelA.id]),
    );
    await act(async () => {
      write.resolve({ labels: [], total: 0 });
    });
  });

  it("rolls back to the previous order when the server rejects it", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seed(qc);
    const before = qc.getQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"));
    setApiInstance({
      reorderLabels: vi.fn(() => Promise.reject(new Error("label list is out of date"))),
    } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    await act(async () => {
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] });
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(qc.getQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"))).toEqual(before);
  });

  it("refetches the catalog and the rows that embed labels once it settles", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seed(qc);
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    setApiInstance({
      reorderLabels: vi.fn(() => Promise.reject(new Error("label list is out of date"))),
    } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    await act(async () => {
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] });
    });
    await waitFor(() => expect(result.current.isError).toBe(true));

    const keys = invalidate.mock.calls.map(([filters]) => (filters as { queryKey: unknown[] }).queryKey);
    expect(keys).toContainEqual(labelKeys.all("ws-1"));
    expect(keys).toContainEqual(issueKeys.all("ws-1"));
    expect(keys).toContainEqual(workspaceKeys.skills("ws-1"));
  });

  it("sends the new order and leaves other catalogs alone", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    seed(qc);
    const skills = {
      labels: [
        { ...labelA, id: "skill-a", position: 0 },
        { ...labelB, id: "skill-b", position: 1 },
      ],
      total: 2,
    };
    qc.setQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "skill"), skills);
    const reorderLabels = vi.fn(() => Promise.resolve({ labels: [], total: 0 }));
    setApiInstance({ reorderLabels } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    await act(async () => {
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(reorderLabels).toHaveBeenCalledWith({
      resource_type: "issue",
      label_ids: [labelB.id, labelA.id],
    });
    expect(qc.getQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "skill"))).toEqual(skills);
  });

  it("renumbers positions to match the requested order", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    // Positions that are not 0..n-1, as after deleting a label.
    qc.setQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"), {
      labels: [
        { ...labelA, position: 4 },
        { ...labelB, position: 9 },
      ],
      total: 2,
    });
    const write = deferred<ListLabelsResponse>();
    setApiInstance({ reorderLabels: vi.fn(() => write.promise) } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    act(() =>
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] }),
    );
    await waitFor(() =>
      expect(
        qc.getQueryData<ListLabelsResponse>(labelKeys.list("ws-1", "issue"))?.labels.map((l) => [l.id, l.position]),
      ).toEqual([[labelB.id, 0], [labelA.id, 1]]),
    );
    await act(async () => {
      write.resolve({ labels: [], total: 0 });
    });
  });

  it("does not invent a list when the catalog was never loaded", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    setApiInstance({
      reorderLabels: vi.fn(() => Promise.resolve({ labels: [], total: 0 })),
    } as unknown as ApiClient);
    const { result } = renderHook(() => useReorderLabels(), { wrapper: wrapper(qc) });

    await act(async () => {
      result.current.mutate({ resource_type: "issue", label_ids: [labelB.id, labelA.id] });
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(qc.getQueryData(labelKeys.list("ws-1", "issue"))).toBeUndefined();
  });
});
