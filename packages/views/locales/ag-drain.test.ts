// @vitest-environment node
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

// A missing key silently falls back to English; a dropped placeholder renders
// "Draining · {{running}}".
const DRAIN_KEYS = [
  "draining",
  "draining_unknown",
  "idle",
  "draining_hint",
  "idle_hint",
] as const;

describe("AG patch: drain badge translations", () => {
  const locales = Object.keys(RESOURCES);

  it("AG patch: ships en and every translated locale", () => {
    expect(locales).toContain("en");
    expect(locales.length).toBeGreaterThan(1);
  });

  for (const locale of locales) {
    describe(locale, () => {
      const drain = (
        RESOURCES as Record<
          string,
          { runtimes: { drain?: Record<string, unknown> } }
        >
      )[locale]?.runtimes.drain;

      it(`AG patch: ${locale} defines every drain string`, () => {
        expect(drain).toBeDefined();
        for (const key of DRAIN_KEYS) {
          expect(typeof drain?.[key], key).toBe("string");
          expect((drain?.[key] as string).trim().length, key).toBeGreaterThan(0);
        }
      });

      it(`AG patch: ${locale} keeps the running-count placeholder`, () => {
        expect(drain?.draining).toContain("{{running}}");
        expect(drain?.draining_unknown).not.toContain("{{");
        expect(drain?.idle).not.toContain("{{");
      });
    });
  }

  it("AG patch: the English wording matches the spec", () => {
    const drain = (
      RESOURCES.en as unknown as {
        runtimes: { drain: Record<string, string> };
      }
    ).runtimes.drain;
    expect(drain.draining).toBe("Draining · {{running}} running");
    expect(drain.idle).toBe("Idle");
  });
});
