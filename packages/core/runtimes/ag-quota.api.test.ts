// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

afterEach(() => vi.unstubAllGlobals());

describe("AG patch: updateAGRuntimeSettings", () => {
  it("AG patch: PUTs both fields to the runtime's ag-settings endpoint and returns the stored setting", async () => {
    const stored = { backup_enabled: true, backup_max_concurrent: 3 };
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(stored), { status: 200 }));
    vi.stubGlobal("fetch", fetch);

    const result = await new ApiClient("https://api.example.test").updateAGRuntimeSettings(
      "rt-9",
      stored,
    );

    expect(result).toEqual(stored);
    const [url, init] = fetch.mock.calls[0]!;
    expect(new URL(url).pathname).toBe("/api/runtimes/rt-9/ag-settings");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body)).toEqual(stored);
  });

  it("AG patch: a server rejection rejects the call", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "not a claude runtime" }), { status: 400 }),
      ),
    );
    await expect(
      new ApiClient("https://api.example.test").updateAGRuntimeSettings("rt-9", {
        backup_enabled: true,
        backup_max_concurrent: 2,
      }),
    ).rejects.toBeTruthy();
  });
});
