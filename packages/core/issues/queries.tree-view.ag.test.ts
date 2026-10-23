// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { Issue, ListIssuesParams, ListIssuesResponse } from "../types";
import { issueAncestryOptions } from "./queries";

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

// Tree view mode: ancestors carry their own title and status for the hidden-path crumbs.
describe("AG patch: issueAncestryOptions", () => {
  let qc: QueryClient;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("keeps each ancestor's own title and status for the tree's hidden-path crumbs", async () => {
    const all = [
      makeIssue(1, { id: "epic", identifier: "MUL-1", title: "The epic", status: "in_progress", parent_issue_id: null }),
      makeIssue(2, { id: "story", identifier: "MUL-2", title: "The story", status: "done", parent_issue_id: "epic" }),
    ];
    const listIssues = vi
      .fn<(params?: ListIssuesParams) => Promise<ListIssuesResponse>>()
      .mockImplementation(async (params) => {
        const issues = all.filter((i) => params?.ids?.includes(i.id));
        return { issues, total: issues.length };
      });
    setApiInstance({ listIssues } as unknown as ApiClient);

    const nodes = await qc.fetchQuery(issueAncestryOptions(WS_ID, ["story"]));

    expect(nodes).toHaveLength(2);
    expect(nodes.find((n) => n.id === "epic")).toEqual({
      id: "epic",
      identifier: "MUL-1",
      parentId: null,
      title: "The epic",
      status: "in_progress",
    });
    expect(nodes.find((n) => n.id === "story")).toEqual({
      id: "story",
      identifier: "MUL-2",
      parentId: "epic",
      title: "The story",
      status: "done",
    });
  });
});
