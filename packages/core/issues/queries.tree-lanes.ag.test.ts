// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { Issue, ListIssuesParams, ListIssuesResponse } from "../types";
import { issueAncestryOptions, issueKeys } from "./queries";

const WS_ID = "ws-1";

function makeIssue(idx: number, overrides: Partial<Issue> = {}): Issue {
  return {
    id: `issue-${idx}`,
    workspace_id: WS_ID,
    number: idx,
    identifier: `MUL-${idx}`,
    title: `Issue ${idx}`,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: "project-1",
    position: idx,
    stage: null,
    start_date: "2026-05-01T00:00:00Z",
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

// AG patch: ancestry walk behind the tree-ordered parent swimlanes.
describe("AG patch: issueAncestryOptions", () => {
  let qc: QueryClient;

  // A chain by id: each entry is [id, parent id | null].
  const issuesFor = (edges: Record<string, string | null>) =>
    Object.entries(edges).map(([id, parent], i) =>
      makeIssue(i + 1, { id, identifier: `MUL-${id}`, parent_issue_id: parent }),
    );

  const installIssues = (edges: Record<string, string | null>) => {
    const all = issuesFor(edges);
    const calls: ListIssuesParams[] = [];
    const listIssues = vi
      .fn<(params?: ListIssuesParams) => Promise<ListIssuesResponse>>()
      .mockImplementation(async (params) => {
        calls.push(params!);
        const issues = all.filter((i) => params?.ids?.includes(i.id));
        return { issues, total: issues.length };
      });
    setApiInstance({ listIssues } as unknown as ApiClient);
    return { calls, listIssues };
  };

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("sends one sorted, de-duplicated batch per tree level and stops at roots", async () => {
    const { calls } = installIssues({
      a: null,
      b: "a",
      c: "b",
      d: "b",
      x: null,
    });

    const nodes = await qc.fetchQuery(
      issueAncestryOptions(WS_ID, ["x", "d", "c", "d"]),
    );

    expect(calls.map((c) => c.ids)).toEqual([["c", "d", "x"], ["b"], ["a"]]);
    expect(nodes.map((n) => n.id).sort()).toEqual(["a", "b", "c", "d", "x"]);
    expect(nodes.find((n) => n.id === "c")).toEqual({
      id: "c",
      identifier: "MUL-c",
      parentId: "b",
    });
    expect(nodes.find((n) => n.id === "a")!.parentId).toBeNull();
  });

  it("chunks a level at the server's 100-id cap", async () => {
    const edges: Record<string, string | null> = {};
    const ids = Array.from(
      { length: 150 },
      (_, i) => `n-${String(i).padStart(3, "0")}`,
    );
    for (const id of ids) edges[id] = null;
    const { calls } = installIssues(edges);

    await qc.fetchQuery(issueAncestryOptions(WS_ID, [...ids].reverse()));

    expect(calls).toHaveLength(2);
    expect(calls[0]!.ids).toHaveLength(100);
    expect(calls[1]!.ids).toHaveLength(50);
    expect(calls.every((c) => (c.limit ?? 0) >= c.ids!.length)).toBe(true);
    expect(calls.flatMap((c) => c.ids!)).toEqual(ids);
  });

  it("terminates on a parent cycle without re-requesting an id", async () => {
    const { calls } = installIssues({ a: "c", b: "a", c: "b" });

    const nodes = await qc.fetchQuery(issueAncestryOptions(WS_ID, ["a"]));

    expect(calls.map((c) => c.ids)).toEqual([["a"], ["c"], ["b"]]);
    expect(nodes).toHaveLength(3);
  });

  it("makes no request for an empty id set", async () => {
    const { listIssues } = installIssues({ a: null });
    const options = issueAncestryOptions(WS_ID, []);

    expect(options.enabled).toBe(false);
    const observer = new QueryObserver(qc, options);
    const unsubscribe = observer.subscribe(() => {});
    await Promise.resolve();
    unsubscribe();
    expect(listIssues).not.toHaveBeenCalled();
  });

  it("sits under tableAll so issue invalidation refreshes it", async () => {
    installIssues({ a: null, b: "a" });
    const options = issueAncestryOptions(WS_ID, ["b"]);

    expect(options.queryKey.slice(0, issueKeys.tableAll(WS_ID).length)).toEqual(
      issueKeys.tableAll(WS_ID),
    );
    // Same ids in another order share one cache entry; another workspace does not.
    expect(issueAncestryOptions(WS_ID, ["b", "a"]).queryKey).toEqual(
      issueAncestryOptions(WS_ID, ["a", "b"]).queryKey,
    );
    expect(issueAncestryOptions("ws-2", ["b"]).queryKey).not.toEqual(
      options.queryKey,
    );

    await qc.fetchQuery(options);
    expect(qc.getQueryState(options.queryKey)!.isInvalidated).toBe(false);
    await qc.invalidateQueries({
      queryKey: issueKeys.tableAll(WS_ID),
      refetchType: "none",
    });
    expect(qc.getQueryState(options.queryKey)!.isInvalidated).toBe(true);
  });
});
