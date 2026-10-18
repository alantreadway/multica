// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import {
  isAGDaemonRefresh,
  quotaSidebarState,
  runtimeQuota,
} from "./ag-quota";

function runtime(
  metadata: Record<string, unknown>,
  status: AgentRuntime["status"] = "online",
): Pick<AgentRuntime, "status" | "metadata"> {
  return { status, metadata };
}

const report = {
  account: "primary",
  hold: { until: "2026-10-18T21:10:00Z", window: "five_hour" },
  effective_cap: 3,
  max: 5,
  windows: {
    five_hour: {
      utilization: 0.84,
      resets_at: "2026-10-18T21:10:00Z",
      observed_at: "2026-10-18T19:00:00Z",
    },
    seven_day: { utilization: 0.4, resets_at: "2026-10-22T00:00:00Z" },
  },
  backup: {
    detected: true,
    valid: true,
    reason: "",
    enabled: false,
    max_concurrent: 2,
  },
  backup_windows: {},
};

describe("AG patch: runtimeQuota", () => {
  it("AG patch: no ag_quota key is silent", () => {
    expect(runtimeQuota(runtime({}))).toBeNull();
    expect(runtimeQuota(runtime({ ag_quota: null }))).toBeNull();
    expect(runtimeQuota({ metadata: null as never })).toBeNull();
  });

  it("AG patch: garbage ag_quota is silent", () => {
    for (const ag_quota of ["x", 7, true, ["primary"]]) {
      expect(runtimeQuota(runtime({ ag_quota }))).toBeNull();
    }
  });

  it("AG patch: reads a full report", () => {
    const quota = runtimeQuota(runtime({ ag_quota: report }));
    expect(quota).toMatchObject({
      account: "primary",
      effectiveCap: 3,
      max: 5,
      fiveHour: { utilization: 0.84 },
      sevenDay: { utilization: 0.4 },
      backup: {
        detected: true,
        valid: true,
        reason: null,
        setting: { enabled: false, maxConcurrent: 2 },
      },
    });
    expect(quota?.holdUntil?.toISOString()).toBe("2026-10-18T21:10:00.000Z");
    expect(quota?.fiveHour?.resetsAt?.toISOString()).toBe(
      "2026-10-18T21:10:00.000Z",
    );
  });

  it("AG patch: an empty report falls back to unknowns", () => {
    expect(runtimeQuota(runtime({ ag_quota: {} }))).toEqual({
      account: "primary",
      holdUntil: null,
      effectiveCap: null,
      max: null,
      fiveHour: null,
      sevenDay: null,
      backup: {
        detected: false,
        valid: false,
        reason: "unknown",
        setting: { enabled: false, maxConcurrent: 2 },
      },
    });
  });

  it("AG patch: omitted window fields stay unknown", () => {
    const quota = runtimeQuota(
      runtime({
        ag_quota: { windows: { five_hour: {}, seven_day: { utilization: 0.1 } } },
      }),
    );
    expect(quota?.fiveHour).toEqual({ utilization: null, resetsAt: null });
    expect(quota?.sevenDay).toEqual({ utilization: 0.1, resetsAt: null });
  });

  it("AG patch: unknown account reads as primary and unknown reason as unknown", () => {
    const quota = runtimeQuota(
      runtime({
        ag_quota: {
          account: "tertiary",
          backup: { detected: true, valid: false, reason: "new_reason" },
        },
      }),
    );
    expect(quota?.account).toBe("primary");
    expect(quota?.backup.reason).toBe("unknown");
  });

  it("AG patch: keeps known invalid reasons", () => {
    for (const reason of ["not_logged_in", "same_account", "check_failed"]) {
      const quota = runtimeQuota(
        runtime({ ag_quota: { backup: { detected: true, valid: false, reason } } }),
      );
      expect(quota?.backup.reason).toBe(reason);
    }
  });

  it("AG patch: bad field types degrade one field, not the report", () => {
    const quota = runtimeQuota(
      runtime({
        ag_quota: {
          account: 4,
          hold: "soon",
          effective_cap: "3",
          max: 5,
          windows: { five_hour: { utilization: "high", resets_at: "not a date" } },
          backup: "yes",
        },
      }),
    );
    expect(quota).toMatchObject({
      account: "primary",
      holdUntil: null,
      effectiveCap: null,
      max: 5,
      fiveHour: { utilization: null, resetsAt: null },
      backup: { detected: false, valid: false },
    });
  });

  it("AG patch: an unparseable hold time is no hold", () => {
    const quota = runtimeQuota(
      runtime({ ag_quota: { hold: { until: "tomorrow" } } }),
    );
    expect(quota?.holdUntil).toBeNull();
  });

  it("AG patch: the stored setting beats the daemon's echo", () => {
    const quota = runtimeQuota(
      runtime({
        ag_quota: report,
        ag_settings: { backup_enabled: true, backup_max_concurrent: 4 },
      }),
    );
    expect(quota?.backup.setting).toEqual({ enabled: true, maxConcurrent: 4 });
  });

  it("AG patch: a malformed stored setting falls back to the echo", () => {
    const quota = runtimeQuota(
      runtime({ ag_quota: report, ag_settings: { backup_enabled: "yes" } }),
    );
    expect(quota?.backup.setting).toEqual({ enabled: false, maxConcurrent: 2 });
  });
});

describe("AG patch: quotaSidebarState", () => {
  const held = (until: string) => runtime({ ag_quota: { hold: { until } } });

  it("AG patch: nothing to show without reports", () => {
    expect(quotaSidebarState([])).toEqual({ backup: false, holdUntil: null });
    expect(quotaSidebarState([runtime({}), runtime({ ag_quota: report })])).toEqual(
      { backup: false, holdUntil: new Date("2026-10-18T21:10:00Z") },
    );
  });

  it("AG patch: flags a runtime on the backup account", () => {
    expect(
      quotaSidebarState([runtime({ ag_quota: { account: "backup" } })]).backup,
    ).toBe(true);
  });

  it("AG patch: takes the earliest hold end", () => {
    expect(
      quotaSidebarState([
        held("2026-10-18T23:00:00Z"),
        held("2026-10-18T21:10:00Z"),
      ]).holdUntil,
    ).toEqual(new Date("2026-10-18T21:10:00Z"));
  });

  it("AG patch: ignores offline runtimes", () => {
    expect(
      quotaSidebarState([
        runtime({ ag_quota: { account: "backup", hold: { until: "2026-10-18T21:10:00Z" } } }, "offline"),
      ]),
    ).toEqual({ backup: false, holdUntil: null });
  });
});

describe("AG patch: isAGDaemonRefresh", () => {
  it("AG patch: recognises the three AG refresh actions", () => {
    for (const action of ["quota", "drain", "ag_settings"]) {
      expect(isAGDaemonRefresh({ action })).toBe(true);
    }
  });

  it("AG patch: a registration is not an AG refresh", () => {
    expect(isAGDaemonRefresh({ runtime_id: "rt-1" })).toBe(false);
    expect(isAGDaemonRefresh({ action: "register" })).toBe(false);
    expect(isAGDaemonRefresh(null)).toBe(false);
    expect(isAGDaemonRefresh(undefined)).toBe(false);
    expect(isAGDaemonRefresh("quota")).toBe(false);
  });
});
