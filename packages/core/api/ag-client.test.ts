import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ApiClient label response schemas", () => {
  it("defaults label position to 0 when an older server omits it", async () => {
    const label = {
      id: "label-1",
      workspace_id: "ws-1",
      name: "bug",
      color: "#ef4444",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ labels: [label, { ...label, id: "label-2", position: 3 }], total: 2 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    const list = await client.listLabels();
    expect(list.labels.map((l) => l.position)).toEqual([0, 3]);

    const reordered = await client.reorderLabels({
      resource_type: "issue",
      label_ids: ["label-2", "label-1"],
    });
    expect(reordered.labels.map((l) => l.position)).toEqual([0, 3]);
    const call = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(call[0]).toBe("https://api.example.test/api/labels/order");
    expect(call[1].method).toBe("PUT");
    expect(JSON.parse(call[1].body as string)).toEqual({
      resource_type: "issue",
      label_ids: ["label-2", "label-1"],
    });
  });

  it("falls back safely when a label position is malformed", async () => {
    const label = {
      id: "label-1",
      workspace_id: "ws-1",
      name: "bug",
      color: "#ef4444",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ labels: [{ ...label, position: "first" }], total: 1 }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new ApiClient("https://api.example.test");
    await expect(client.listLabels()).resolves.toEqual({ labels: [], total: 0 });
    await expect(
      client.reorderLabels({ resource_type: "issue", label_ids: ["label-1"] }),
    ).resolves.toEqual({ labels: [], total: 0 });
    await expect(client.getLabel("label-1")).resolves.toMatchObject({ id: "" });

    // A wholly malformed catalog response falls back the same way.
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({ labels: "not-an-array", total: "not-a-number" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      ),
    );
    await expect(
      client.reorderLabels({ resource_type: "issue", label_ids: ["label-1"] }),
    ).resolves.toEqual({ labels: [], total: 0 });
  });

  it("keeps a label position of 0 and rejects a null position as malformed", async () => {
    const label = {
      id: "label-1",
      workspace_id: "ws-1",
      name: "bug",
      color: "#ef4444",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    const respond = (position: unknown) =>
      vi.stubGlobal(
        "fetch",
        vi.fn().mockImplementation(() =>
          Promise.resolve(
            new Response(JSON.stringify({ labels: [{ ...label, position }], total: 1 }), {
              status: 200,
              headers: { "Content-Type": "application/json" },
            }),
          ),
        ),
      );
    const client = new ApiClient("https://api.example.test");

    respond(0);
    expect((await client.listLabels()).labels[0]?.position).toBe(0);

    respond(null);
    await expect(client.listLabels()).resolves.toEqual({ labels: [], total: 0 });
  });

  it("surfaces a rejected reorder instead of parsing the error body as a list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: "label list is out of date" }), {
            status: 409,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      ),
    );

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.reorderLabels({ resource_type: "issue", label_ids: ["label-1"] }),
    ).rejects.toThrow();
  });
});
