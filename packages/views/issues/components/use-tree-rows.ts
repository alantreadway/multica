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

  // Parents whose children are being fetched. Grows when a node opens or a
  // loaded row turns out to be open already (restored state).
  const [activeParents, setActiveParents] = useState<ReadonlySet<string>>(
    () => new Set(),
  );

  // Cursors belong to one query; a changed filter or focus starts over.
  const identity = useMemo(() => hashKey([query, focusId]), [query, focusId]);
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

  const queries = useMemo(
    () =>
      targets.map((target) => ({
        ...issueTableRowPageOptions(wsId, {
          query,
          group: { kind: "none" },
          group_key: null,
          hierarchy: { enabled: true },
          parent_id: target.parentId,
          page: { limit: TREE_PAGE_SIZE, cursor: target.cursor },
        }),
        enabled,
      })),
    [enabled, query, targets, wsId],
  );
  const combine = useCallback(
    (results: Array<UseQueryResult<IssueTableRowsResponse, Error>>) => {
      const byKey: Record<string, TreeBranch> = {};
      targets.forEach((target, index) => {
        const result = results[index];
        if (!result) return;
        const branch: TreeBranch = byKey[target.key] ?? {
          rows: [],
          nextCursor: null,
          loading: false,
          loadingMore: false,
          error: false,
        };
        const page = result.data;
        if (page) {
          branch.rows = [...branch.rows, ...page.rows];
          branch.nextCursor = page.next_cursor;
        } else if (target.cursor === null) {
          branch.loading = result.isPending;
        } else {
          branch.loadingMore = result.isPending;
        }
        branch.error ||= result.isError;
        byKey[target.key] = branch;
      });
      return byKey;
    },
    [targets],
  );
  const branches = useQueries({ queries, combine });

  // A restored-open row needs its children without a click.
  useEffect(() => {
    if (!enabled) return;
    const missing: string[] = [];
    for (const branch of Object.values(branches)) {
      for (const row of branch.rows) {
        const id = row.issue.id;
        if (
          row.direct_child_count > 0 &&
          expandedIds.has(id) &&
          id !== focusId &&
          !activeParents.has(id)
        ) {
          missing.push(id);
        }
      }
    }
    if (missing.length > 0) {
      setActiveParents((prev) => new Set([...prev, ...missing]));
    }
  }, [activeParents, branches, enabled, expandedIds, focusId]);

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
    if (focusId !== null && !focusIssue) return [];
    return flattenTree({ focusIssue, branches, expanded: expandedIds, ancestry });
  }, [ancestry, branches, enabled, expandedIds, focusId, focusIssue]);

  const issueIds = useMemo(
    () => rows.flatMap((row) => (row.kind === "issue" ? [row.issue.id] : [])),
    [rows],
  );

  const toggle = useCallback(
    (issueId: string) => {
      if (!expandedIds.has(issueId)) {
        setActiveParents((prev) => new Set(prev).add(issueId));
      }
      toggleExpanded(surfaceKey, issueId);
    },
    [expandedIds, surfaceKey, toggleExpanded],
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
      setActiveParents((prev) => new Set([...prev, ...targetIds]));
      setExpanded(surfaceKey, [...expandedIds, ...targetIds]);
      return;
    }
    if (!anyBranchLoading) setExpandingAll(false);
  }, [anyBranchLoading, expandedIds, expandingAll, rows, setExpanded, surfaceKey]);
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
      void queryClient.refetchQueries({
        queryKey: issueKeys.tableRows(
          wsId,
          query,
          { kind: "none" },
          null,
          true,
          branchKey === TREE_ROOT_KEY ? null : branchKey,
        ),
      });
    },
    [query, queryClient, wsId],
  );

  const focusPath = useMemo(
    () => (focusIssue ? ancestorPathOf(focusIssue.parent_issue_id, ancestry) : []),
    [ancestry, focusIssue],
  );

  const rootSettled = rootBranch !== undefined && !rootBranch.loading;
  return {
    rows,
    issueIds,
    isLoading:
      enabled &&
      (focusId !== null
        ? !focusIssue
        : rootBranch === undefined || rootBranch.loading),
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
