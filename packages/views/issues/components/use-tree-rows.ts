"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  hashKey,
  useQueries,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import {
  issueAncestryOptions,
  issueDetailOptions,
  issueKeys,
  issueTableRowPageOptions,
} from "@multica/core/issues/queries";
import {
  selectTreeExpanded,
  useTreeViewStore,
} from "@multica/core/issues/stores/tree-view-store";
import type {
  Issue,
  IssueTableQuerySpec,
  IssueTableRowsResponse,
} from "@multica/core/types";
import {
  TREE_EXPAND_ALL_MAX_NODES,
  TREE_PAGE_SIZE,
  TREE_ROOT_KEY,
  ancestorPathOf,
  expandAllTargets,
  flattenTree,
  treeBranchKey,
  type TreeAncestor,
  type TreeBranch,
  type TreeRow,
} from "./tree-rows";

// AG patch: data + state behind the outline tree view. It sits beside the
// surface controller (not inside TreeView) so the focus and expand state
// survive the surface swapping the view for a skeleton, and so a follow-on
// layout (org chart) can read the same rows with `focusId === null`.

const EMPTY_IDS: string[] = [];
const EMPTY_CURSORS: Record<string, string[]> = {};
const EMPTY_PARENTS: ReadonlySet<string> = new Set();

/** A branch plus what the hook needs to keep its cursor pages honest. */
interface HookBranch extends TreeBranch {
  /** `dataUpdatedAt` of the first page; changes when the head is refreshed. */
  headUpdatedAt: number;
  headFetching: boolean;
  /** Rows are the previous query's, shown while the new one loads. */
  placeholder: boolean;
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) return false;
  for (const id of a) if (!b.has(id)) return false;
  return true;
}

interface BranchTarget {
  parentId: string | null;
  key: string;
  cursor: string | null;
}

export interface TreeRowsState {
  /** Visible rows in display order. */
  rows: TreeRow[];
  /** Issue ids in display order, for the peek's J / K stepping. */
  issueIds: string[];
  /** Top level has not answered yet. */
  isLoading: boolean;
  /** Top level answered with nothing (never true while focused). */
  isEmpty: boolean;
  focusId: string | null;
  focusIssue: Issue | null;
  /** Ancestors of the focused issue, topmost first. */
  focusPath: TreeAncestor[];
  focus: (issueId: string) => void;
  clearFocus: () => void;
  expandedIds: ReadonlySet<string>;
  toggle: (issueId: string) => void;
  expandAll: () => void;
  collapseAll: () => void;
  loadMore: (branchKey: string) => void;
  retry: (branchKey: string) => void;
}

/**
 * The tree's rows, from the same server query as Table with hierarchy on.
 * Filtering, sort and cache invalidation therefore match the other modes.
 * Only the top level loads up front; a parent's children load when it opens.
 */
export function useTreeRows({
  wsId,
  surfaceKey,
  query,
  enabled,
}: {
  wsId: string;
  /** Persistence key for expand state, one per surface. */
  surfaceKey: string;
  query: IssueTableQuerySpec;
  enabled: boolean;
}): TreeRowsState {
  const queryClient = useQueryClient();
  const expandedList = useTreeViewStore(selectTreeExpanded(surfaceKey));
  const toggleExpanded = useTreeViewStore((s) => s.toggleExpanded);
  const setExpanded = useTreeViewStore((s) => s.setExpanded);
  const expandedIds = useMemo<ReadonlySet<string>>(
    () => new Set(expandedList),
    [expandedList],
  );

  const [focusState, setFocusState] = useState<{
    surfaceKey: string;
    id: string | null;
  }>({ surfaceKey, id: null });
  const focusId = focusState.surfaceKey === surfaceKey ? focusState.id : null;
  const focus = useCallback(
    (issueId: string) => setFocusState({ surfaceKey, id: issueId }),
    [surfaceKey],
  );
  const clearFocus = useCallback(
    () => setFocusState({ surfaceKey, id: null }),
    [surfaceKey],
  );

  // While a node is focused its subtree is fetched without the view filters,
  // search and surface scope (sort is kept, sub-issues stay on). The server
  // only returns a branch whose parent passes the filters, so a hidden
  // ancestor, or a node under a filtered-out parent, would come back empty.
  const activeQuery = useMemo<IssueTableQuerySpec>(
    () =>
      focusId === null
        ? query
        : {
            scope: { kind: "workspace" },
            filters: { include_sub_issues: true },
            sort: query.sort,
          },
    [focusId, query],
  );

  // Parents whose children are fetched: exactly the open rows on screen, so a
  // collapsed branch stops refetching. Owned by one surface and focus, so a
  // focus change never fetches the old tree's branches with the new query.
  const parentsScope = `${surfaceKey}|${focusId ?? ""}`;
  const [activeState, setActiveState] = useState<{
    scope: string;
    ids: ReadonlySet<string>;
  }>({ scope: parentsScope, ids: EMPTY_PARENTS });
  const activeParents =
    activeState.scope === parentsScope ? activeState.ids : EMPTY_PARENTS;
  const addActiveParents = useCallback(
    (ids: readonly string[]) =>
      setActiveState((prev) => ({
        scope: parentsScope,
        ids: new Set([
          ...(prev.scope === parentsScope ? prev.ids : EMPTY_PARENTS),
          ...ids,
        ]),
      })),
    [parentsScope],
  );

  // Cursors belong to one query; a changed filter or focus starts over.
  const identity = useMemo(
    () => hashKey([activeQuery, focusId]),
    [activeQuery, focusId],
  );
  const [cursorState, setCursorState] = useState<{
    identity: string;
    byKey: Record<string, string[]>;
  }>({ identity, byKey: EMPTY_CURSORS });
  const cursorsByKey =
    cursorState.identity === identity ? cursorState.byKey : EMPTY_CURSORS;

  const focusQuery = useQuery({
    ...issueDetailOptions(wsId, focusId ?? ""),
    enabled: enabled && focusId !== null,
  });
  const focusIssue = focusId !== null ? (focusQuery.data ?? null) : null;
  // A focus on an issue that no longer exists would leave an empty tree.
  useEffect(() => {
    if (focusId !== null && focusQuery.isError) clearFocus();
  }, [clearFocus, focusId, focusQuery.isError]);

  const targets = useMemo<BranchTarget[]>(() => {
    const parents: Array<string | null> = [focusId];
    for (const id of activeParents) if (id !== focusId) parents.push(id);
    return parents.flatMap((parentId) => {
      const key = treeBranchKey(parentId);
      return [null, ...(cursorsByKey[key] ?? EMPTY_IDS)].map((cursor) => ({
        parentId,
        key,
        cursor,
      }));
    });
  }, [activeParents, cursorsByKey, focusId]);

  // QueriesObserver replaces observers by query hash, so keepPreviousData
  // cannot bridge a changed filter, sort or search inside useQueries. Keep the
  // last settled head of each branch and hand it back as placeholder data, so
  // the old tree stays painted (and scrolled) until the new one arrives.
  const placeholderRef = useRef(new Map<string, IssueTableRowsResponse>());
  const queries = useMemo(
    () =>
      targets.map((target) => {
        const placeholder =
          target.cursor === null
            ? placeholderRef.current.get(`${wsId}:${target.key}`)
            : undefined;
        return {
          ...issueTableRowPageOptions(wsId, {
            query: activeQuery,
            group: { kind: "none" },
            group_key: null,
            hierarchy: { enabled: true },
            parent_id: target.parentId,
            page: { limit: TREE_PAGE_SIZE, cursor: target.cursor },
          }),
          // A value, not a closure: the observer only reuses its placeholder
          // result while this compares equal by reference between renders.
          ...(placeholder ? { placeholderData: placeholder } : {}),
          enabled,
        };
      }),
    [activeQuery, enabled, targets, wsId],
  );
  const combine = useCallback(
    (results: Array<UseQueryResult<IssueTableRowsResponse, Error>>) => {
      const byKey: Record<string, HookBranch> = {};
      const seen = new Map<string, Set<string>>();
      targets.forEach((target, index) => {
        const result = results[index];
        if (!result) return;
        const branch: HookBranch = byKey[target.key] ?? {
          rows: [],
          nextCursor: null,
          loading: false,
          loadingMore: false,
          error: false,
          headUpdatedAt: 0,
          headFetching: false,
          placeholder: false,
        };
        const ids = seen.get(target.key) ?? new Set<string>();
        seen.set(target.key, ids);
        const page = result.data;
        if (page) {
          // Pages fetched at different moments can overlap when rows move
          // between them; a duplicate id would be a duplicate React key.
          const fresh = page.rows.filter((row) => !ids.has(row.issue.id));
          for (const row of fresh) ids.add(row.issue.id);
          branch.rows = [...branch.rows, ...fresh];
          branch.nextCursor = page.next_cursor;
        } else if (target.cursor === null) {
          branch.loading = result.isPending;
        } else {
          branch.loadingMore = result.isPending;
        }
        if (target.cursor === null) {
          branch.headUpdatedAt = result.dataUpdatedAt;
          branch.headFetching = result.isFetching;
        }
        branch.error ||= result.isError;
        branch.placeholder ||= result.isPlaceholderData;
        byKey[target.key] = branch;
      });
      return byKey;
    },
    [targets],
  );
  const branches = useQueries({ queries, combine });

  useEffect(() => {
    const next = new Map<string, IssueTableRowsResponse>();
    for (const target of targets) {
      const key = `${wsId}:${target.key}`;
      const branch = branches[target.key];
      if (!branch || branch.placeholder || branch.loading || branch.error) {
        const previous = placeholderRef.current.get(key);
        if (previous) next.set(key, previous);
        continue;
      }
      next.set(key, {
        query_fingerprint: "__tree_placeholder__",
        group_key: null,
        parent_id: target.parentId,
        total: branch.rows.length,
        rows: [...branch.rows],
        branch_total: branch.rows.length,
        next_cursor: null,
      });
    }
    // Bounded to the branches on screen, so a long session cannot accumulate.
    placeholderRef.current = next;
  }, [branches, targets, wsId]);

  // A broad invalidation (any realtime event) refetches every active page.
  // Once a head is refreshing, the cursors after it are obsolete: drop them
  // before their responses can duplicate or skip a row. The revision check is
  // the second line of defence for a head restored without a fetching frame.
  const headRevisionRef = useRef<Record<string, number>>({});
  useEffect(() => {
    const previous = headRevisionRef.current;
    const nextRevisions: Record<string, number> = {};
    const stale: string[] = [];
    for (const [key, branch] of Object.entries(branches)) {
      if (branch.headUpdatedAt === 0) continue;
      nextRevisions[key] = branch.headUpdatedAt;
      const seenRevision = previous[key];
      if (
        (cursorsByKey[key]?.length ?? 0) > 0 &&
        (branch.headFetching ||
          (seenRevision !== undefined && seenRevision !== branch.headUpdatedAt))
      ) {
        stale.push(key);
      }
    }
    headRevisionRef.current = nextRevisions;
    if (stale.length === 0) return;
    setCursorState((prev) => {
      if (prev.identity !== identity) return prev;
      const byKey = { ...prev.byKey };
      for (const key of stale) delete byKey[key];
      return { identity, byKey };
    });
  }, [branches, cursorsByKey, identity]);

  const rootBranch = branches[treeBranchKey(focusId)];
  const rootIssues = useMemo(
    () => (focusId === null ? (rootBranch?.rows ?? []).map((r) => r.issue) : []),
    [focusId, rootBranch],
  );

  // Parents the filters removed: fetched without view filters, so the dimmed
  // path can name issues that are not in the tree.
  const ancestryStart = useMemo(() => {
    if (!enabled) return EMPTY_IDS;
    if (focusId !== null) {
      return focusIssue?.parent_issue_id ? [focusIssue.parent_issue_id] : EMPTY_IDS;
    }
    const ids = new Set<string>();
    for (const issue of rootIssues) {
      if (issue.parent_issue_id) ids.add(issue.parent_issue_id);
    }
    return ids.size > 0 ? [...ids] : EMPTY_IDS;
  }, [enabled, focusId, focusIssue, rootIssues]);
  const { data: ancestryNodes } = useQuery(
    issueAncestryOptions(wsId, ancestryStart),
  );
  const ancestry = useMemo(
    () =>
      new Map<string, TreeAncestor>(
        (ancestryNodes ?? []).map((node) => [node.id, node]),
      ),
    [ancestryNodes],
  );

  const rows = useMemo<TreeRow[]>(() => {
    if (!enabled) return [];
    if (focusId !== null && !focusIssue) {
      return [
        {
          kind: "status",
          key: "status:focus",
          parentId: null,
          depth: 0,
          guides: [],
          branchKey: treeBranchKey(focusId),
          state: "loading",
        },
      ];
    }
    return flattenTree({ focusIssue, branches, expanded: expandedIds, ancestry });
  }, [ancestry, branches, enabled, expandedIds, focusId, focusIssue]);

  // Fetch exactly the open rows. Restored open state and fresh toggles both
  // show up here, so nothing outside the visible tree keeps refetching.
  useEffect(() => {
    if (!enabled) return;
    const open = new Set<string>();
    for (const row of rows) {
      if (row.kind === "issue" && row.expanded && row.issue.id !== focusId) {
        open.add(row.issue.id);
      }
    }
    setActiveState((prev) =>
      prev.scope === parentsScope && sameSet(prev.ids, open)
        ? prev
        : { scope: parentsScope, ids: open },
    );
  }, [enabled, focusId, parentsScope, rows]);

  const issueIds = useMemo(
    () => rows.flatMap((row) => (row.kind === "issue" ? [row.issue.id] : [])),
    [rows],
  );

  const toggle = useCallback(
    (issueId: string) => {
      if (!expandedIds.has(issueId)) addActiveParents([issueId]);
      toggleExpanded(surfaceKey, issueId);
    },
    [addActiveParents, expandedIds, surfaceKey, toggleExpanded],
  );

  // Expand-all opens level by level: each pass opens what is loaded, then the
  // next pass sees the children that arrived. It stops at the depth cap or the
  // node cap, whichever comes first.
  const [expandingAll, setExpandingAll] = useState(false);
  const expandedByAll = useRef(0);
  const anyBranchLoading = Object.values(branches).some((b) => b.loading);
  useEffect(() => {
    if (!expandingAll) return;
    const room = TREE_EXPAND_ALL_MAX_NODES - expandedByAll.current;
    const targetIds =
      room > 0 ? expandAllTargets(rows, expandedIds).slice(0, room) : [];
    if (targetIds.length > 0) {
      expandedByAll.current += targetIds.length;
      addActiveParents(targetIds);
      setExpanded(surfaceKey, [...expandedIds, ...targetIds]);
      return;
    }
    if (!anyBranchLoading) setExpandingAll(false);
  }, [
    addActiveParents,
    anyBranchLoading,
    expandedIds,
    expandingAll,
    rows,
    setExpanded,
    surfaceKey,
  ]);
  const expandAll = useCallback(() => {
    expandedByAll.current = 0;
    setExpandingAll(true);
  }, []);
  const collapseAll = useCallback(() => {
    setExpandingAll(false);
    setExpanded(surfaceKey, EMPTY_IDS);
  }, [setExpanded, surfaceKey]);

  const loadMore = useCallback(
    (branchKey: string) => {
      const branch = branches[branchKey];
      if (!branch?.nextCursor) return;
      const cursor = branch.nextCursor;
      setCursorState((prev) => {
        const byKey = prev.identity === identity ? prev.byKey : EMPTY_CURSORS;
        const current = byKey[branchKey] ?? EMPTY_IDS;
        if (current.includes(cursor)) return prev;
        return { identity, byKey: { ...byKey, [branchKey]: [...current, cursor] } };
      });
    },
    [branches, identity],
  );

  const retry = useCallback(
    (branchKey: string) => {
      // Only the failed pages: refetching a healthy head would make the hook
      // drop the cursors that follow it.
      void queryClient.refetchQueries({
        predicate: (q) => q.state.status === "error",
        queryKey: issueKeys.tableRows(
          wsId,
          activeQuery,
          { kind: "none" },
          null,
          true,
          branchKey === TREE_ROOT_KEY ? null : branchKey,
        ),
      });
    },
    [activeQuery, queryClient, wsId],
  );

  const focusPath = useMemo(
    () => (focusIssue ? ancestorPathOf(focusIssue.parent_issue_id, ancestry) : []),
    [ancestry, focusIssue],
  );

  const rootSettled = rootBranch !== undefined && !rootBranch.loading;
  // Only the first load is "loading" to the surface: it swaps the whole view
  // for a skeleton, which would unmount the tree (scroll, breadcrumb) on every
  // later filter, search or focus change. Those show placeholders and in-tree
  // loading rows instead.
  const firstLoadDone = useRef<string | null>(null);
  const loadKey = `${wsId}|${surfaceKey}`;
  if (enabled && focusId === null && rootSettled) firstLoadDone.current = loadKey;
  return {
    rows,
    issueIds,
    isLoading:
      enabled &&
      firstLoadDone.current !== loadKey &&
      (focusId !== null ? !focusIssue : !rootSettled),
    isEmpty:
      enabled &&
      focusId === null &&
      rootSettled &&
      !rootBranch.error &&
      rootBranch.rows.length === 0,
    focusId,
    focusIssue,
    focusPath,
    focus,
    clearFocus,
    expandedIds,
    toggle,
    expandAll,
    collapseAll,
    loadMore,
    retry,
  };
}
