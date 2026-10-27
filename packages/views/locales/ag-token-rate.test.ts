// @vitest-environment node
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

// A missing key silently falls back to English; a dropped placeholder renders
// "{{input}}/s in".
const SIDEBAR: Record<string, readonly string[]> = {
  tokens: ["{{input}}", "{{output}}"],
  tokens_unknown: [],
  hover_tokens_header: [],
  hover_tokens_runtime: ["{{name}}", "{{input}}", "{{output}}", "{{cache}}"],
  hover_tokens_unlisted: [],
};

type Bundle = { ag: { layout: { agent_status?: Record<string, unknown> } } };

describe("AG patch: token rate translations", () => {
  for (const locale of Object.keys(RESOURCES)) {
    it(`AG patch: ${locale} defines every token rate string with its placeholders`, () => {
      const status = (RESOURCES as unknown as Record<string, Bundle>)[locale]!.ag.layout.agent_status;
      for (const [key, placeholders] of Object.entries(SIDEBAR)) {
        const value = status?.[key];
        expect(typeof value, key).toBe("string");
        expect((value as string).trim().length, key).toBeGreaterThan(0);
        for (const p of placeholders) expect(value, key).toContain(p);
      }
    });
  }
});
