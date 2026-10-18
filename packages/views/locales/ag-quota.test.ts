// @vitest-environment node
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

// A missing key silently falls back to English; a dropped placeholder renders
// "On hold until {{time}}".
const QUOTA_STRINGS: Record<string, readonly string[]> = {
  title: [],
  five_hour: [],
  seven_day: [],
  resets: ["{{time}}"],
  hold: ["{{time}}"],
  cap: ["{{cap}}", "{{max}}"],
  on_backup: [],
  backup_title: [],
  backup_ready: [],
  backup_not_found: [],
  backup_invalid: [],
  backup_enable: [],
  backup_urgent_only: [],
  backup_max: [],
  save_failed: [],
};
const REASONS = ["not_logged_in", "same_account", "check_failed", "unknown"];
const SIDEBAR: Record<string, readonly string[]> = {
  backup_urgent: [],
  quota_hold: ["{{time}}"],
};

type Bundle = {
  runtimes: { quota?: Record<string, unknown> };
  layout: { agent_status?: Record<string, unknown> };
};

describe("AG patch: quota translations", () => {
  const locales = Object.keys(RESOURCES);

  it("AG patch: ships en and every translated locale", () => {
    expect(locales).toContain("en");
    expect(locales.length).toBeGreaterThan(1);
  });

  for (const locale of locales) {
    const bundle = (RESOURCES as unknown as Record<string, Bundle>)[locale]!;

    it(`AG patch: ${locale} defines every quota string with its placeholders`, () => {
      const quota = bundle.runtimes.quota;
      expect(quota).toBeDefined();
      for (const [key, placeholders] of Object.entries(QUOTA_STRINGS)) {
        const value = quota?.[key];
        expect(typeof value, key).toBe("string");
        expect((value as string).trim().length, key).toBeGreaterThan(0);
        for (const p of placeholders) expect(value, key).toContain(p);
      }
      const reason = quota?.reason as Record<string, unknown> | undefined;
      for (const key of REASONS) {
        expect(typeof reason?.[key], key).toBe("string");
        expect((reason?.[key] as string).trim().length, key).toBeGreaterThan(0);
      }
    });

    it(`AG patch: ${locale} defines the sidebar quota strings`, () => {
      const status = bundle.layout.agent_status;
      for (const [key, placeholders] of Object.entries(SIDEBAR)) {
        const value = status?.[key];
        expect(typeof value, key).toBe("string");
        expect((value as string).trim().length, key).toBeGreaterThan(0);
        for (const p of placeholders) expect(value, key).toContain(p);
      }
    });
  }

  it("AG patch: the English wording matches the spec", () => {
    const en = RESOURCES.en as unknown as Bundle;
    expect(en.layout.agent_status?.backup_urgent).toBe("Backup account · urgent only");
    expect(en.layout.agent_status?.quota_hold).toBe("Claude on hold until {{time}}");
    expect(en.runtimes.quota?.backup_urgent_only).toBe("Only urgent issues run on the backup.");
  });
});
