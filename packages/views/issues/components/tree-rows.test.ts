// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import {
  TREE_ROOT_KEY,
  ancestorPathOf,
  expandAllTargets,
  flattenTree,
  type TreeAncestor,
  type TreeBranch,
  type TreeIssueRow,
} from "./tree-rows";

function issue(id: string, parent: string | null = null): Issue {
  return {
    id,
    identifier: `MAG-${id}`,
    title: `Issue ${id}`,
    status: "todo",
    parent_issue_id: parent,
  } as unknown as Issue;
}

function branch(
  rows: Array<[Issue, number]>,
  overrides: Partial<TreeBranch> = {},
): TreeBranch {
  return {
    rows: rows.map(([i, direct_child_count]) => ({ issue: i, direct_child_count })),
    nextCursor: null,
    loading: false,
    loadingMore: false,
    error: false,
    ...overrides,
  };
}

const issueRows = (rows: ReturnType<typeof flattenTree>) =>
  rows.filter((row): row is TreeIssueRow => row.kind === "issue");

describe("AG patch: flattenTree", () => {
  it("lists only the top level until a parent is expanded", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 2], [issue("2"), 0]]),
        "1": branch([[issue("3", "1"), 0], [issue("4", "1"), 0]]),
      },
      expanded: new Set(),
    });
    expect(issueRows(rows).map((r) => r.issue.id)).toEqual(["1", "2"]);
    expect(issueRows(rows)[0]!.expanded).toBe(false);
  });

  it("places an expanded parent's children directly after it", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 2], [issue("2"), 0]]),
        "1": branch([[issue("3", "1"), 0], [issue("4", "1"), 0]]),
      },
      expanded: new Set(["1"]),
    });
    const flat = issueRows(rows);
    expect(flat.map((r) => [r.issue.id, r.depth])).toEqual([
      ["1", 0],
      ["3", 1],
      ["4", 1],
      ["2", 0],
    ]);
    expect(flat.map((r) => r.isLast)).toEqual([false, false, true, true]);
  });

  it("carries the vertical connector through rows below a non-last ancestor", () => {
    const rows = issueRows(
      flattenTree({
        focusIssue: null,
        branches: {
          [TREE_ROOT_KEY]: branch([[issue("1"), 1]]),
          "1": branch([[issue("2", "1"), 1], [issue("5", "1"), 0]]),
          "2": branch([[issue("3", "2"), 0]]),
        },
        expanded: new Set(["1", "2"]),
      }),
    );
    const byId = Object.fromEntries(rows.map((r) => [r.issue.id, r]));
    expect(byId["1"]!.guides).toEqual([]);
    expect(byId["2"]!.guides).toEqual([false]);
    // "3" sits under "2", which has a later sibling ("5"): column 0 keeps its line.
    expect(byId["3"]!.guides).toEqual([true, false]);
    expect(byId["5"]!.guides).toEqual([false]);
  });

  it("does not expand a parent without children, or one whose id is not expanded", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: { [TREE_ROOT_KEY]: branch([[issue("1"), 0]]) },
      expanded: new Set(["1"]),
    });
    expect(issueRows(rows)[0]!.expanded).toBe(false);
    expect(rows).toHaveLength(1);
  });

  it("shows loading and error rows for a branch that has no rows yet", () => {
    const loading = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 3]]),
        "1": branch([], { loading: true }),
      },
      expanded: new Set(["1"]),
    });
    expect(loading[1]).toMatchObject({ kind: "status", state: "loading", depth: 1 });

    const errored = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 3]]),
        "1": branch([], { error: true }),
      },
      expanded: new Set(["1"]),
    });
    expect(errored[1]).toMatchObject({ kind: "status", state: "error", branchKey: "1" });
  });

  it("adds a load-more row while a branch has a next cursor, and keeps the last row open-ended", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 0]], { nextCursor: "c1" }),
      },
      expanded: new Set(),
    });
    expect(rows.map((r) => r.kind)).toEqual(["issue", "more"]);
    expect((rows[0] as TreeIssueRow).isLast).toBe(false);
    expect(rows[1]).toMatchObject({ kind: "more", branchKey: TREE_ROOT_KEY, loading: false });
  });

  it("flags the load-more row when a page of its branch failed", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 0]], { nextCursor: "c1", error: true }),
      },
      expanded: new Set(),
    });
    expect(rows[1]).toMatchObject({ kind: "more", error: true });
  });

  it("makes the focused issue the single open root", () => {
    const focus = issue("9", "1");
    const rows = flattenTree({
      focusIssue: focus,
      branches: {
        "9": branch([[issue("10", "9"), 0], [issue("11", "9"), 0]]),
      },
      expanded: new Set(),
    });
    expect(issueRows(rows).map((r) => [r.issue.id, r.depth, r.expanded])).toEqual([
      ["9", 0, true],
      ["10", 1, false],
      ["11", 1, false],
    ]);
    expect(issueRows(rows)[0]!.ancestorPath).toEqual([]);
  });

  it("attaches the hidden ancestor path to a promoted top-level row only", () => {
    const ancestry = new Map<string, TreeAncestor>([
      ["1", { id: "1", identifier: "MAG-1", parentId: null, title: "Epic", status: "todo" }],
      ["2", { id: "2", identifier: "MAG-2", parentId: "1", title: "Story", status: "todo" }],
    ]);
    const rows = issueRows(
      flattenTree({
        focusIssue: null,
        branches: {
          [TREE_ROOT_KEY]: branch([[issue("3", "2"), 1]]),
          "3": branch([[issue("4", "3"), 0]]),
        },
        expanded: new Set(["3"]),
        ancestry,
      }),
    );
    expect(rows[0]!.ancestorPath.map((a) => a.identifier)).toEqual(["MAG-1", "MAG-2"]);
    expect(rows[1]!.ancestorPath).toEqual([]);
  });

  it("stops at a cycle instead of recursing forever", () => {
    const rows = flattenTree({
      focusIssue: null,
      branches: {
        [TREE_ROOT_KEY]: branch([[issue("1"), 1]]),
        "1": branch([[issue("1", "1"), 1]]),
      },
      expanded: new Set(["1"]),
    });
    expect(issueRows(rows).map((r) => r.issue.id)).toEqual(["1"]);
  });
});

describe("AG patch: ancestorPathOf", () => {
  const node = (id: string, parentId: string | null): [string, TreeAncestor] => [
    id,
    { id, identifier: `MAG-${id}`, parentId, title: id, status: "todo" },
  ];

  it("returns the chain topmost first and tolerates a cycle or a gap", () => {
    expect(
      ancestorPathOf("3", new Map([node("1", null), node("2", "1"), node("3", "2")])).map(
        (a) => a.id,
      ),
    ).toEqual(["1", "2", "3"]);
    expect(ancestorPathOf("a", new Map([node("a", "b"), node("b", "a")])).map((a) => a.id)).toEqual([
      "b",
      "a",
    ]);
    expect(ancestorPathOf("3", new Map([node("3", "missing")])).map((a) => a.id)).toEqual(["3"]);
    expect(ancestorPathOf(null, new Map())).toEqual([]);
    expect(ancestorPathOf("x", undefined)).toEqual([]);
  });
});

describe("AG patch: expandAllTargets", () => {
  const rows = flattenTree({
    focusIssue: null,
    branches: {
      [TREE_ROOT_KEY]: branch([[issue("1"), 1], [issue("2"), 1], [issue("leaf"), 0]]),
      "1": branch([[issue("3", "1"), 1]]),
      "3": branch([[issue("4", "3"), 1]]),
    },
    expanded: new Set(["1", "3"]),
  });

  it("opens loaded nodes with children above the depth cap, shallowest first", () => {
    expect(expandAllTargets(rows, new Set(["1", "3"]))).toEqual(["2"]);
    expect(expandAllTargets(rows, new Set())).toEqual(["1", "2", "3"]);
  });

  it("never opens a node at or beyond the depth cap", () => {
    expect(expandAllTargets(rows, new Set(), 1)).toEqual(["1", "2"]);
  });

  it("caps how many nodes one pass opens", () => {
    expect(expandAllTargets(rows, new Set(), 2, 2)).toEqual(["1", "2"]);
  });
});
