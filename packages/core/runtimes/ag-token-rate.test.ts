// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  mergeAGTokenRate,
  parseAGTokenRateEvent,
  parseAGTokenRateList,
  summarizeAGTokenRates,
  TOKEN_RATE_TTL_MS,
  type AGTokenRate,
} from "./ag-token-rate";
import { agTokenRateKeys, subscribeAGTokenRates } from "./ag-token-rate-queries";
import type { WSClient } from "../api/ws-client";

const wire = (over: Record<string, unknown> = {}) => ({
  runtime_id: "rt-1",
  workspace_id: "ws-1",
  input_per_sec: 12.5,
  output_per_sec: 80,
  cache_read_per_sec: 900,
  window_seconds: 60,
  active_runs: 1,
  observed_at: "2026-10-27T09:00:00Z",
  ...over,
});

const at = Date.parse("2026-10-27T09:00:00Z");

describe("AG patch: token rate parsing", () => {
  it("AG patch: reads a well-formed rate", () => {
    expect(parseAGTokenRateEvent(wire())).toEqual({
      runtimeId: "rt-1",
      workspaceId: "ws-1",
      inputPerSec: 12.5,
      outputPerSec: 80,
      cacheReadPerSec: 900,
      activeRuns: 1,
      observedAt: new Date(at),
    });
  });

  it("AG patch: defaults bad numbers and dates, and drops a rate without a runtime", () => {
    const rate = parseAGTokenRateEvent(wire({ output_per_sec: -3, input_per_sec: "x", observed_at: "soon" }));
    expect(rate).toMatchObject({ inputPerSec: 0, outputPerSec: 0, observedAt: null });
    expect(parseAGTokenRateEvent(wire({ runtime_id: "" }))).toBeNull();
    expect(parseAGTokenRateEvent("nope")).toBeNull();
  });

  it("AG patch: a list keeps readable rates and survives a non-array body", () => {
    expect(parseAGTokenRateList([wire(), { runtime_id: 5 }, wire({ runtime_id: "rt-2" })]).map((r) => r.runtimeId)).toEqual([
      "rt-1",
      "rt-2",
    ]);
    expect(parseAGTokenRateList({ error: "not found" })).toEqual([]);
  });
});

describe("AG patch: token rate summary", () => {
  const rate = (id: string, out: number, ageMs: number): AGTokenRate => ({
    ...parseAGTokenRateEvent(wire({ runtime_id: id, output_per_sec: out }))!,
    observedAt: new Date(at - ageMs),
  });

  it("AG patch: sums rates heard within the TTL, busiest first", () => {
    const summary = summarizeAGTokenRates([rate("a", 10, 0), rate("b", 30, 20_000), rate("old", 99, TOKEN_RATE_TTL_MS + 1)], at);
    expect(summary).toMatchObject({ inputPerSec: 25, outputPerSec: 40, cacheReadPerSec: 1800 });
    expect(summary?.runtimes.map((r) => r.runtimeId)).toEqual(["b", "a"]);
  });

  it("AG patch: nothing live, or no data, is null", () => {
    expect(summarizeAGTokenRates([rate("old", 5, TOKEN_RATE_TTL_MS + 1)], at)).toBeNull();
    expect(summarizeAGTokenRates(undefined, at)).toBeNull();
    expect(summarizeAGTokenRates([], at)).toBeNull();
  });

  it("AG patch: merge replaces a runtime in place and appends a new one", () => {
    const list = [rate("a", 1, 0), rate("b", 2, 0)];
    expect(mergeAGTokenRate(list, rate("a", 7, 0)).map((r) => [r.runtimeId, r.outputPerSec])).toEqual([
      ["a", 7],
      ["b", 2],
    ]);
    expect(mergeAGTokenRate(list, rate("c", 3, 0))).toHaveLength(3);
  });
});

describe("AG patch: token rate events", () => {
  function fakeWS() {
    let handler: ((payload: unknown) => void) | undefined;
    const ws = {
      on: vi.fn((event: string, h: (payload: unknown) => void) => {
        expect(event).toBe("ag:runtime_token_rate");
        handler = h;
        return () => {
          handler = undefined;
        };
      }),
    } as unknown as WSClient;
    return { ws, emit: (p: unknown) => handler?.(p), subscribed: () => handler !== undefined };
  }

  it("AG patch: an event patches the workspace's loaded list", () => {
    const qc = new QueryClient();
    const { ws, emit } = fakeWS();
    subscribeAGTokenRates(ws, qc);
    qc.setQueryData(agTokenRateKeys.list("ws-1"), parseAGTokenRateList([wire({ runtime_id: "rt-2" })]));

    emit(wire({ output_per_sec: 5 }));
    emit(wire({ workspace_id: "ws-2" }));
    emit({ junk: true });

    const list = qc.getQueryData<AGTokenRate[]>(agTokenRateKeys.list("ws-1"));
    expect(list?.map((r) => [r.runtimeId, r.outputPerSec])).toEqual([
      ["rt-2", 80],
      ["rt-1", 5],
    ]);
    expect(qc.getQueryData(agTokenRateKeys.list("ws-2"))).toBeUndefined();
  });

  it("AG patch: an event before the list loads does not seed it", () => {
    const qc = new QueryClient();
    const { ws, emit } = fakeWS();
    subscribeAGTokenRates(ws, qc);
    emit(wire());
    expect(qc.getQueryData(agTokenRateKeys.list("ws-1"))).toBeUndefined();
  });

  it("AG patch: unsubscribing stops the handler", () => {
    const { ws, subscribed } = fakeWS();
    const unsub = subscribeAGTokenRates(ws, new QueryClient());
    expect(subscribed()).toBe(true);
    unsub();
    expect(subscribed()).toBe(false);
  });
});
