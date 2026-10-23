// @vitest-environment node
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

// A dropped placeholder renders "Cost · {{period}}"; a label left on the old
// placeholder renders "Cost · D" because nothing passes `days` any more.
type Strings = Record<string, string>;
type Locale = {
  ag: {
    usage: { ag_period: Strings; kpi: Strings; errors: Strings };
    runtimes: { usage: Strings };
  };
};

const USAGE_KPI = ["cost_label", "tokens_label", "run_time_label", "tasks_label"];
const USAGE_ERRORS = ["kpi_failed_label", "kpi_rate_label", "kpi_agents_label"];
const RUNTIME_KPI = ["kpi_cost_label", "kpi_cache_label", "kpi_tokens_label"];

describe("AG patch: period translations", () => {
  for (const locale of Object.keys(RESOURCES)) {
    const res = (RESOURCES as unknown as Record<string, Locale>)[locale]!;

    it(`AG patch: ${locale} defines the period strings`, () => {
      expect(res.ag.usage.ag_period.days).toContain("{{days}}");
      expect(res.ag.usage.ag_period.yesterday?.trim()).toBeTruthy();
      expect(res.ag.runtimes.usage.ag_period_days).toContain("{{days}}");
      expect(res.ag.runtimes.usage.ag_period_yesterday?.trim()).toBeTruthy();
      expect(res.ag.runtimes.usage.ag_cost_by_agent_single_day?.trim()).toBeTruthy();
    });

    it(`AG patch: ${locale} KPI labels take the period`, () => {
      const labels = [
        ...USAGE_KPI.map((k) => res.ag.usage.kpi[k]),
        ...USAGE_ERRORS.map((k) => res.ag.usage.errors[k]),
        ...RUNTIME_KPI.map((k) => res.ag.runtimes.usage[k]),
      ];
      for (const label of labels) {
        expect(label).toContain("{{period}}");
        expect(label).not.toContain("{{days}}");
      }
    });
  }
});
