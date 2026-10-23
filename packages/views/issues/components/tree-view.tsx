"use client";

import { memo, useEffect, useMemo } from "react";
import { Virtuoso } from "react-virtuoso";
import {
  ChevronRight,
  ChevronsDownUp,
  ChevronsUpDown,
  Crosshair,
  Loader2,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { VIRTUOSO_SEED_COUNT } from "../../common/virtuoso-seed";
import { useT } from "../../i18n";
import { IssueActionsContextMenu } from "../actions";
import {
  PEEKED_ROW_CLASS,
  PEEK_TARGET_ATTR,
  useIsIssuePeeked,
  useIssuePeekActions,
  useIssuePeekLinkProps,
} from "../surface/peek-context";
import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";
import type { ChildProgress } from "./list-row";
import { ProgressRing } from "./progress-ring";
import { StatusIcon } from "./status-icon";
import type {
  TreeAncestor,
  TreeIssueRow,
  TreeMoreRow,
  TreeRow,
  TreeStatusRow,
} from "./tree-rows";
import type { TreeRowsState } from "./use-tree-rows";

// AG patch: outline tree view. One compact row per issue with ├ └ connectors,
// per-node expand / collapse and a focus action that makes a node the root.

/** Width of one indent level; the connector cell and chevron share it. */
const INDENT_PX = 16;
const ROW_CLASS = "h-[var(--issue-row-height)]";

function Connectors({
  depth,
  guides,
  isLast,
  withElbow,
}: {
  depth: number;
  guides: boolean[];
  isLast: boolean;
  withElbow: boolean;
}) {
  if (depth === 0) return null;
  return (
    <span aria-hidden className="flex h-full shrink-0">
      {guides.map((continues, level) => {
        const elbow = withElbow && level === depth - 1;
        return (
          <span
            key={level}
            data-slot="tree-connector"
            className="relative h-full shrink-0"
            style={{ width: INDENT_PX }}
          >
            {elbow ? (
              <>
                <span
                  className={cn(
                    "absolute left-1/2 top-0 w-px bg-border",
                    isLast ? "h-1/2" : "h-full",
                  )}
                />
                <span className="absolute left-1/2 top-1/2 h-px w-1/2 bg-border" />
              </>
            ) : (
              continues && (
                <span className="absolute left-1/2 top-0 h-full w-px bg-border" />
              )
            )}
          </span>
        );
      })}
    </span>
  );
}

function AncestorPath({
  path,
  onFocus,
}: {
  path: TreeAncestor[];
  onFocus: (issueId: string) => void;
}) {
  const { t } = useT("ag");
  if (path.length === 0) return null;
  return (
    <span
      data-slot="tree-ancestor-path"
      className="flex shrink-0 items-center gap-1 text-caption text-faint-foreground"
      title={t(($) => $.issues.tree.hidden_ancestors)}
    >
      {path.map((ancestor) => (
        <span key={ancestor.id} className="flex items-center gap-1">
          <button
            type="button"
            title={ancestor.title}
            onClick={() => onFocus(ancestor.id)}
            className="rounded-xs hover:text-foreground hover:underline"
          >
            {ancestor.identifier}
          </button>
          <span aria-hidden>›</span>
        </span>
      ))}
    </span>
  );
}

const IssueTreeRow = memo(function IssueTreeRow({
  row,
  childProgress,
  onToggle,
  onFocus,
}: {
  row: TreeIssueRow;
  childProgress?: ChildProgress;
  onToggle: (issueId: string) => void;
  onFocus: (issueId: string) => void;
}) {
  const { t } = useT("ag");
  const { issue } = row;
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const catalog = useIssueStatuses(wsId);
  const peeked = useIsIssuePeeked(issue.id);
  const peekLinkProps = useIssuePeekLinkProps(issue.id);
  const hasChildren = row.childCount > 0;
  const progressLabel = childProgress
    ? t(($) => $.issues.tree.sub_issue_progress, {
        done: childProgress.done,
        total: childProgress.total,
      })
    : undefined;

  return (
    <IssueActionsContextMenu issue={issue}>
      <div
        role="treeitem"
        aria-level={row.depth + 1}
        aria-expanded={hasChildren ? row.expanded : undefined}
        data-slot="issue-list-row"
        data-tree-depth={row.depth}
        {...{ [PEEK_TARGET_ATTR]: issue.id }}
        data-peeked={peeked ? "" : undefined}
        className={cn(
          "group/row flex items-center gap-1 px-4 text-body transition-colors hover:not-data-[popup-open]:bg-surface-hover data-[popup-open]:bg-surface-hover",
          ROW_CLASS,
          PEEKED_ROW_CLASS,
        )}
      >
        <Connectors
          depth={row.depth}
          guides={row.guides}
          isLast={row.isLast}
          withElbow
        />
        {hasChildren ? (
          <button
            type="button"
            aria-label={
              row.expanded ? t(($) => $.issues.tree.collapse) : t(($) => $.issues.tree.expand)
            }
            onClick={() => onToggle(issue.id)}
            className="flex size-4 shrink-0 items-center justify-center rounded-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <ChevronRight
              className={cn(
                "size-3.5 transition-transform",
                row.expanded && "rotate-90",
              )}
            />
          </button>
        ) : (
          <span aria-hidden className="size-4 shrink-0" />
        )}
        <StatusIcon
          status={issue.status}
          category={catalog.categoryOf(issue.status)}
          color={catalog.colorOf(issue.status)}
          icon={catalog.iconOf(issue.status)}
          className="size-3.5 shrink-0"
        />
        <AncestorPath path={row.ancestorPath} onFocus={onFocus} />
        <AppLink
          href={paths.issueDetail(issue.id)}
          newTabTitle={issue.identifier}
          className="flex min-w-0 flex-1 items-center gap-2"
          {...peekLinkProps}
        >
          <span className="shrink-0 text-caption text-muted-foreground">
            {issue.identifier}
          </span>
          <IssueAgentActivityIndicator issueId={issue.id} />
          <span className="min-w-0 flex-1 truncate">{issue.title}</span>
          {childProgress && childProgress.total > 0 && (
            <span
              title={progressLabel}
              className="inline-flex shrink-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5"
            >
              <ProgressRing
                done={childProgress.done}
                total={childProgress.total}
                size={14}
              />
              <span className="text-micro font-medium tabular-nums text-muted-foreground">
                {childProgress.done}/{childProgress.total}
              </span>
            </span>
          )}
        </AppLink>
        <button
          type="button"
          aria-label={t(($) => $.issues.tree.focus)}
          title={t(($) => $.issues.tree.focus)}
          onClick={() => onFocus(issue.id)}
          className="flex size-5 shrink-0 items-center justify-center rounded-xs text-muted-foreground opacity-0 hover:bg-accent hover:text-foreground focus-visible:opacity-100 group-hover/row:opacity-100 group-focus-within/row:opacity-100 [@media(hover:none)]:opacity-100"
        >
          <Crosshair className="size-3.5" />
        </button>
        {issue.assignee_type && issue.assignee_id && (
          <ActorAvatar
            actorType={issue.assignee_type}
            actorId={issue.assignee_id}
            size="sm"
            enableHoverCard
          />
        )}
      </div>
    </IssueActionsContextMenu>
  );
});

function MoreTreeRow({
  row,
  onLoadMore,
  onRetry,
}: {
  row: TreeMoreRow;
  onLoadMore: (branchKey: string) => void;
  onRetry: (branchKey: string) => void;
}) {
  const { t } = useT("ag");
  return (
    <div className={cn("flex items-center gap-1 px-4", ROW_CLASS)}>
      <Connectors
        depth={row.depth}
        guides={row.guides}
        isLast
        withElbow
      />
      {row.error ? (
        // The failed page's cursor is already requested, so "Load more" would
        // do nothing; offer the retry that refetches it.
        <span className="flex items-center gap-1 text-caption text-muted-foreground">
          <span role="alert">{t(($) => $.issues.tree.load_error)}</span>
          <Button variant="ghost" size="xs" onClick={() => onRetry(row.branchKey)}>
            {t(($) => $.issues.tree.retry)}
          </Button>
        </span>
      ) : (
        <Button
          variant="ghost"
          size="xs"
          disabled={row.loading}
          onClick={() => onLoadMore(row.branchKey)}
          className="text-muted-foreground"
        >
          {row.loading && <Loader2 className="size-3 animate-spin" />}
          {t(($) => $.issues.tree.load_more)}
        </Button>
      )}
    </div>
  );
}

function StatusTreeRow({
  row,
  onRetry,
}: {
  row: TreeStatusRow;
  onRetry: (branchKey: string) => void;
}) {
  const { t } = useT("ag");
  return (
    <div
      className={cn(
        "flex items-center gap-1 px-4 text-caption text-muted-foreground",
        ROW_CLASS,
      )}
    >
      <Connectors
        depth={row.depth}
        guides={row.guides}
        isLast
        withElbow
      />
      {row.state === "loading" ? (
        <>
          <Loader2 className="size-3 animate-spin" />
          {t(($) => $.issues.tree.loading_children)}
        </>
      ) : (
        <>
          <span role="alert">{t(($) => $.issues.tree.load_error)}</span>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => onRetry(row.branchKey)}
          >
            {t(($) => $.issues.tree.retry)}
          </Button>
        </>
      )}
    </div>
  );
}

function FocusBreadcrumb({ tree }: { tree: TreeRowsState }) {
  const { t } = useT("ag");
  if (!tree.focusId) return null;
  return (
    <nav
      aria-label={t(($) => $.issues.tree.breadcrumb_label)}
      className="flex min-w-0 items-center gap-1 text-caption text-muted-foreground"
    >
      <button
        type="button"
        onClick={tree.clearFocus}
        className="shrink-0 rounded-xs hover:text-foreground hover:underline"
      >
        {t(($) => $.issues.tree.focus_root)}
      </button>
      {tree.focusPath.map((ancestor) => (
        <span key={ancestor.id} className="flex min-w-0 items-center gap-1">
          <span aria-hidden>›</span>
          <button
            type="button"
            title={ancestor.title}
            onClick={() => tree.focus(ancestor.id)}
            className="shrink-0 rounded-xs hover:text-foreground hover:underline"
          >
            {ancestor.identifier}
          </button>
        </span>
      ))}
      {tree.focusIssue && (
        <span className="flex min-w-0 items-center gap-1">
          <span aria-hidden>›</span>
          <span className="truncate text-foreground">
            {tree.focusIssue.identifier}
          </span>
        </span>
      )}
    </nav>
  );
}

export function TreeView({
  tree,
  childProgressMap,
}: {
  tree: TreeRowsState;
  childProgressMap: Map<string, ChildProgress>;
}) {
  const { t } = useT("ag");
  const peek = useIssuePeekActions();
  const { rows, issueIds } = tree;

  useEffect(() => {
    peek?.publishColumns([issueIds]);
  }, [peek, issueIds]);
  useEffect(() => () => peek?.publishColumns(null), [peek]);

  const itemContent = useMemo(
    () =>
      function renderRow(_index: number, row: TreeRow) {
        if (row.kind === "issue") {
          return (
            <IssueTreeRow
              row={row}
              childProgress={childProgressMap.get(row.issue.id)}
              onToggle={tree.toggle}
              onFocus={tree.focus}
            />
          );
        }
        if (row.kind === "more") {
          return <MoreTreeRow
              row={row}
              onLoadMore={tree.loadMore}
              onRetry={tree.retry}
            />;
        }
        return <StatusTreeRow row={row} onRetry={tree.retry} />;
      },
    [childProgressMap, tree.focus, tree.loadMore, tree.retry, tree.toggle],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b px-4">
        <div className="min-w-0 flex-1">
          <FocusBreadcrumb tree={tree} />
        </div>
        {tree.focusId && (
          <span
            data-slot="tree-filters-paused"
            className="flex shrink-0 items-center gap-1 text-caption text-muted-foreground"
          >
            {t(($) => $.issues.tree.filters_paused)}
            <Button
              variant="ghost"
              size="xs"
              onClick={tree.clearFocus}
              className="text-foreground"
            >
              {t(($) => $.issues.tree.clear_focus)}
            </Button>
          </span>
        )}
        <Button
          variant="ghost"
          size="xs"
          onClick={tree.expandAll}
          className="text-muted-foreground"
        >
          <ChevronsUpDown className="size-3.5" />
          <span className="hidden md:inline">{t(($) => $.issues.tree.expand_all)}</span>
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={tree.collapseAll}
          className="text-muted-foreground"
        >
          <ChevronsDownUp className="size-3.5" />
          <span className="hidden md:inline">
            {t(($) => $.issues.tree.collapse_all)}
          </span>
        </Button>
      </div>
      <div
        role="tree"
        aria-label={t(($) => $.issues.tree.aria_label)}
        className="relative min-h-0 flex-1"
      >
        <Virtuoso
          style={{ position: "absolute", inset: 0 }}
          data={rows}
          computeItemKey={(_index, row) => row.key}
          initialItemCount={Math.min(rows.length, VIRTUOSO_SEED_COUNT)}
          increaseViewportBy={{ top: 400, bottom: 400 }}
          itemContent={itemContent}
        />
      </div>
    </div>
  );
}
