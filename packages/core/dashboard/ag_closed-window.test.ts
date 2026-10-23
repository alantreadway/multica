// @vitest-environment node
import { describe, expect, it, vi } from "vitest";

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  api: {
    getDashboardUsageByAgent: vi.fn(),
    getDashboardAgentRunTime: vi.fn(),
    getDashboardFailuresByAgent: vi.fn(),
  },
}));

import { api } from "../api";
import { agFetchWindow, agSubtractRows, agWindowKey } from "./ag_closed-window";
import {
  dashboardAgentRunTimeOptions,
  dashboardFailuresByAgentOptions,
  dashboardUsageByAgentOptions,
} from "./queries";

describe("AG patch: closed dashboard windows", () => {
  it("AG patch: keeps the plain day count as the key for a trailing window", () => {
    expect(agWindowKey(30, 0)).toBe(30);
    expect(agWindowKey(1, 1)).toBe("1-1");
  });

  it("AG patch: subtracts matching rows field by field", () => {
    const wide = [
      { agent_id: "a", model: "m1", input_tokens: 500, task_count: 5 },
      { agent_id: "a", model: "m2", input_tokens: 70, task_count: 1 },
      { agent_id: "b", model: "m1", input_tokens: 10, task_count: 2 },
    ];
    const recent = [
      { agent_id: "a", model: "m1", input_tokens: 200, task_count: 2 },
      { agent_id: "b", model: "m1", input_tokens: 10, task_count: 2 },
    ];
    expect(agSubtractRows(wide, recent)).toEqual([
      { agent_id: "a", model: "m1", input_tokens: 300, task_count: 3 },
      { agent_id: "a", model: "m2", input_tokens: 70, task_count: 1 },
    ]);
  });

  it("AG patch: matches failure rows on the reason as well as the agent", () => {
    const wide = [
      { agent_id: "a", failure_reason: "", task_count: 9 },
      { agent_id: "a", failure_reason: "timeout", task_count: 4 },
    ];
    const recent = [{ agent_id: "a", failure_reason: "timeout", task_count: 1 }];
    expect(agSubtractRows(wide, recent)).toEqual([
      { agent_id: "a", failure_reason: "", task_count: 9 },
      { agent_id: "a", failure_reason: "timeout", task_count: 3 },
    ]);
  });

  it("AG patch: clamps at zero when today grew between the two requests", () => {
    const wide = [{ agent_id: "a", total_seconds: 100, task_count: 2 }];
    const recent = [{ agent_id: "a", total_seconds: 150, task_count: 1 }];
    expect(agSubtractRows(wide, recent)).toEqual([
      { agent_id: "a", total_seconds: 0, task_count: 1 },
    ]);
  });

  it("AG patch: ignores missing optional fields when matching", () => {
    const wide = [{ agent_id: "a", cost_usd_ticks: null, task_count: 3 }];
    const recent = [{ agent_id: "a", task_count: 1 }];
    expect(agSubtractRows(wide, recent)).toEqual([
      { agent_id: "a", cost_usd_ticks: null, task_count: 2 },
    ]);
  });

  it("AG patch: fetches once for a trailing window", async () => {
    const fetch = vi.fn(async (days: number) => [{ id: "x", n: days }]);
    await expect(agFetchWindow(fetch, 30, 0)).resolves.toEqual([
      { id: "x", n: 30 },
    ]);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("AG patch: fetches days+offset and offset for a window ending earlier", async () => {
    const fetch = vi.fn(async (days: number) => [{ id: "x", n: days * 10 }]);
    await expect(agFetchWindow(fetch, 1, 1)).resolves.toEqual([
      { id: "x", n: 10 },
    ]);
    expect(fetch.mock.calls.map(([d]) => d).sort()).toEqual([1, 2]);
  });

  it.each([
    ["by-agent", dashboardUsageByAgentOptions, api.getDashboardUsageByAgent],
    ["agent-runtime", dashboardAgentRunTimeOptions, api.getDashboardAgentRunTime],
    [
      "failures-by-agent",
      dashboardFailuresByAgentOptions,
      api.getDashboardFailuresByAgent,
    ],
  ] as const)(
    "AG patch: %s queries yesterday as two trailing windows",
    async (kind, build, endpoint) => {
      const fn = vi.mocked(endpoint as (p: { days?: number }) => Promise<unknown[]>);
      fn.mockReset();
      fn.mockImplementation(async (p) => [
        { agent_id: "a", task_count: p.days === 2 ? 5 : 2 },
      ]);

      const options = build("ws-1", 1, "p-1", "UTC", 1);
      expect(options.queryKey).toEqual(["dashboard", "ws-1", kind, "1-1", "p-1", "UTC"]);
      const rows = await (options.queryFn as () => Promise<unknown>)();

      expect(rows).toEqual([{ agent_id: "a", task_count: 3 }]);
      expect(fn.mock.calls.map(([p]) => p).sort((a, b) => a.days! - b.days!)).toEqual([
        { days: 1, project_id: "p-1", tz: "UTC" },
        { days: 2, project_id: "p-1", tz: "UTC" },
      ]);
    },
  );

  it("AG patch: leaves the trailing-window key unchanged", () => {
    expect(dashboardUsageByAgentOptions("ws-1", 30, null, "UTC").queryKey).toEqual([
      "dashboard",
      "ws-1",
      "by-agent",
      30,
      null,
      "UTC",
    ]);
  });
});
