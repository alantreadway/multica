// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import { runtimeDrainState } from "./ag-drain";

function runtime(
  metadata: Record<string, unknown>,
  status: AgentRuntime["status"] = "online",
): Pick<AgentRuntime, "status" | "metadata"> {
  return { status, metadata };
}

describe("AG patch: runtimeDrainState", () => {
  it("AG patch: no ag_drain key is not draining", () => {
    expect(runtimeDrainState(runtime({}))).toBeNull();
    expect(runtimeDrainState(runtime({ cli_version: "v0.6.2" }))).toBeNull();
    expect(runtimeDrainState(runtime({ ag_drain: null }))).toBeNull();
  });

  it("AG patch: reads a draining state with its running count", () => {
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: false, active_tasks: 3 } }),
      ),
    ).toEqual({ idle: false, activeTasks: 3 });
  });

  it("AG patch: reads idle", () => {
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: true, active_tasks: 0 } }),
      ),
    ).toEqual({ idle: true, activeTasks: 0 });
  });

  it("AG patch: idle with tasks still running is not idle", () => {
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: true, active_tasks: 2 } }),
      ),
    ).toEqual({ idle: false, activeTasks: 2 });
  });

  it("AG patch: draining=false is not draining", () => {
    expect(
      runtimeDrainState(runtime({ ag_drain: { draining: false } })),
    ).toBeNull();
  });

  it("AG patch: an offline runtime shows no drain state", () => {
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: true, active_tasks: 0 } }, "offline"),
      ),
    ).toBeNull();
  });

  it("AG patch: malformed values fall back instead of throwing", () => {
    for (const bad of ["yes", 7, [], { draining: "true" }, { idle: true }, {}]) {
      expect(runtimeDrainState(runtime({ ag_drain: bad }))).toBeNull();
    }
  });

  it("AG patch: a malformed count or idle flag still shows the badge", () => {
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: "no", active_tasks: "2" } }),
      ),
    ).toEqual({ idle: false, activeTasks: null });
    expect(
      runtimeDrainState(runtime({ ag_drain: { draining: true } })),
    ).toEqual({ idle: false, activeTasks: null });
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, active_tasks: -5 } }),
      ),
    ).toEqual({ idle: false, activeTasks: null });
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, idle: true } }),
      ),
    ).toEqual({ idle: false, activeTasks: null });
    expect(
      runtimeDrainState(
        runtime({ ag_drain: { draining: true, active_tasks: 2.9 } }),
      ),
    ).toEqual({ idle: false, activeTasks: 2 });
  });

  it("AG patch: missing metadata does not throw", () => {
    expect(
      runtimeDrainState({ status: "online", metadata: undefined as never }),
    ).toBeNull();
  });
});
