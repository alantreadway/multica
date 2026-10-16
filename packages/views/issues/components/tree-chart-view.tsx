"use client";

import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChevronDown,
  ChevronsDownUp,
  ChevronsUpDown,
  Crosshair,
  Loader2,
  Maximize,
  RotateCcw,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { AppLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { ProjectIcon } from "../../projects/components/project-icon";
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
import { StatusIcon } from "./status-icon";
import {
  CHART_NODE_HEIGHT,
  CHART_NODE_WIDTH,
  TREE_CHART_NODE_CAP,
  isChartRootSettled,
  buildChartTree,
  countChartIssues,
  layoutChartTree,
  type ChartLayout,
  type ChartPlacedNode,
} from "./tree-chart-layout";
import {
  CHART_ZOOM_STEP,
  IDENTITY_VIEWPORT,
  fitViewport,
  panBy,
  zoomAt,
  type ChartViewport,
} from "./tree-chart-viewport";
import type { TreeIssueRow, TreeMoreRow, TreeStatusRow } from "./tree-rows";
import { TreeToolbar } from "./tree-view";
import type { TreeRowsState } from "./use-tree-rows";

// AG patch: org-chart layout of tree mode. A top-down tidy tree with elbow
// connectors, drawn once and moved with a CSS transform (pan and zoom). It
// reads the outline's rows, so filters, focus and paging behave the same.

const EMPTY_LAYOUT: ChartLayout = { nodes: [], edges: [], width: 0, height: 0 };
const DRAG_THRESHOLD_PX = 4;
const PAN_KEY_STEP_PX = 48;

const IssueChartNode = memo(function IssueChartNode({
  row,
  placed,
  childProgress,
  onToggle,
  onFocus,
}: {
  row: TreeIssueRow;
  placed: ChartPlacedNode;
  childProgress?: ChildProgress;
  onToggle: (issueId: string) => void;
  onFocus: (issueId: string) => void;
}) {
  const { t } = useT("issues");
  const { issue } = row;
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const catalog = useIssueStatuses(wsId);
  const peeked = useIsIssuePeeked(issue.id);
  const peekLinkProps = useIssuePeekLinkProps(issue.id);
  const hasChildren = row.childCount > 0;

  return (
    <IssueActionsContextMenu issue={issue}>
      <div
        role="group"
        aria-label={`${issue.identifier} ${issue.title}`}
        data-slot="tree-chart-node"
        data-chart-depth={placed.depth}
        {...{ [PEEK_TARGET_ATTR]: issue.id }}
        data-peeked={peeked ? "" : undefined}
        className={cn(
          "group/node absolute rounded-lg border bg-card text-body text-card-foreground shadow-xs transition-colors hover:not-data-[popup-open]:bg-surface-hover data-[popup-open]:bg-surface-hover",
          PEEKED_ROW_CLASS,
        )}
        style={{
          left: placed.x,
          top: placed.y,
          width: CHART_NODE_WIDTH,
          height: CHART_NODE_HEIGHT,
        }}
      >
        <AppLink
          href={paths.issueDetail(issue.id)}
          newTabTitle={issue.identifier}
          className="flex h-full min-w-0 flex-col gap-1 px-2.5 py-2"
          {...peekLinkProps}
        >
          <span className="flex h-5 min-w-0 items-center gap-1.5">
            <StatusIcon
              status={issue.status}
              category={catalog.categoryOf(issue.status)}
              color={catalog.colorOf(issue.status)}
              icon={catalog.iconOf(issue.status)}
              className="size-3.5 shrink-0"
            />
            <span className="shrink-0 text-caption text-muted-foreground">
              {issue.identifier}
            </span>
            <IssueAgentActivityIndicator issueId={issue.id} />
            <span className="min-w-0 flex-1" />
            {childProgress && childProgress.total > 0 && (
              <span
                title={t(($) => $.tree.sub_issue_progress, {
                  done: childProgress.done,
                  total: childProgress.total,
                })}
                className="shrink-0 text-micro font-medium tabular-nums text-muted-foreground"
              >
                {childProgress.done}/{childProgress.total}
              </span>
            )}
            {issue.assignee_type && issue.assignee_id && (
              <ActorAvatar
                actorType={issue.assignee_type}
                actorId={issue.assignee_id}
                size="sm"
              />
            )}
          </span>
          <span className="line-clamp-2 min-w-0 text-caption leading-4 text-foreground">
            {issue.title}
          </span>
        </AppLink>
        <button
          type="button"
          aria-label={t(($) => $.tree.focus)}
          title={t(($) => $.tree.focus)}
          onClick={() => onFocus(issue.id)}
          className="absolute -right-2 -top-2 flex size-5 items-center justify-center rounded-full border bg-card text-muted-foreground opacity-0 shadow-xs hover:bg-accent hover:text-foreground focus-visible:opacity-100 group-hover/node:opacity-100 group-focus-within/node:opacity-100 [@media(hover:none)]:opacity-100"
        >
          <Crosshair className="size-3" />
        </button>
        {hasChildren && (
          <button
            type="button"
            aria-label={
              row.expanded ? t(($) => $.tree.collapse) : t(($) => $.tree.expand)
            }
            aria-expanded={row.expanded}
            onClick={() => onToggle(issue.id)}
            className="absolute -bottom-2.5 left-1/2 flex h-5 -translate-x-1/2 items-center gap-0.5 rounded-full border bg-card px-1.5 text-micro font-medium tabular-nums text-muted-foreground shadow-xs hover:bg-accent hover:text-foreground"
          >
            <ChevronDown
              className={cn("size-3 transition-transform", !row.expanded && "-rotate-90")}
            />
            {row.childCount}
          </button>
        )}
      </div>
    </IssueActionsContextMenu>
  );
});

function PlaceholderChartNode({
  row,
  placed,
  onLoadMore,
  onRetry,
}: {
  row: TreeMoreRow | TreeStatusRow;
  placed: ChartPlacedNode;
  onLoadMore: (branchKey: string) => void;
  onRetry: (branchKey: string) => void;
}) {
  const { t } = useT("issues");
  const failed = row.kind === "more" ? row.error : row.state === "error";
  return (
    <div
      data-slot={`tree-chart-${row.kind}`}
      className="absolute flex items-center justify-center gap-1.5 rounded-lg border border-dashed bg-background text-caption text-muted-foreground"
      style={{
        left: placed.x,
        top: placed.y,
        width: CHART_NODE_WIDTH,
        height: CHART_NODE_HEIGHT,
      }}
    >
      {failed ? (
        <>
          <span role="alert">{t(($) => $.tree.load_error)}</span>
          <Button variant="ghost" size="xs" onClick={() => onRetry(row.branchKey)}>
            {t(($) => $.tree.retry)}
          </Button>
        </>
      ) : row.kind === "more" ? (
        <Button
          variant="ghost"
          size="xs"
          disabled={row.loading}
          onClick={() => onLoadMore(row.branchKey)}
        >
          {row.loading && <Loader2 className="size-3 animate-spin" />}
          {t(($) => $.tree.load_more)}
        </Button>
      ) : (
        <>
          <Loader2 className="size-3 animate-spin" />
          {t(($) => $.tree.loading_children)}
        </>
      )}
    </div>
  );
}

function ProjectChartNode({
  placed,
  projectId,
}: {
  placed: ChartPlacedNode;
  projectId: string | undefined;
}) {
  const wsId = useWorkspaceId();
  const { data: project } = useQuery({
    ...projectDetailOptions(wsId, projectId ?? ""),
    enabled: !!projectId,
  });
  return (
    <div
      data-slot="tree-chart-project"
      className="absolute flex items-center justify-center gap-2 rounded-lg border bg-muted px-3 text-body font-medium text-foreground"
      style={{
        left: placed.x,
        top: placed.y,
        width: CHART_NODE_WIDTH,
        height: CHART_NODE_HEIGHT,
      }}
    >
      <ProjectIcon project={project} size="md" />
      <span className="min-w-0 truncate">{project?.title ?? ""}</span>
    </div>
  );
}

function TooManyNodes({
  count,
  tree,
}: {
  count: number;
  tree: TreeRowsState;
}) {
  const { t } = useT("issues");
  return (
    <div
      data-slot="tree-chart-too-many"
      role="status"
      className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center"
    >
      <p className="text-body font-medium">{t(($) => $.tree.chart_too_many_title)}</p>
      <p className="max-w-md text-caption text-muted-foreground">
        {t(($) => $.tree.chart_too_many_body, {
          count,
          max: TREE_CHART_NODE_CAP,
        })}
      </p>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" onClick={tree.collapseAll}>
          <ChevronsDownUp className="size-3.5" />
          {t(($) => $.tree.collapse_all)}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => tree.setLayout("outline")}>
          {t(($) => $.tree.chart_use_outline)}
        </Button>
      </div>
    </div>
  );
}

export function TreeChartView({
  tree,
  childProgressMap,
  projectId,
}: {
  tree: TreeRowsState;
  childProgressMap: Map<string, ChildProgress>;
  /** Names the virtual top node when the chart starts from a project. */
  projectId?: string;
}) {
  const { t } = useT("issues");
  const peek = useIssuePeekActions();
  const { rows, issueIds } = tree;
  const virtualRoot = tree.focusId === null && tree.chartAvailability === "project";

  useEffect(() => {
    peek?.publishColumns([issueIds]);
  }, [peek, issueIds]);
  useEffect(() => () => peek?.publishColumns(null), [peek]);

  const root = useMemo(() => buildChartTree(rows, virtualRoot), [rows, virtualRoot]);
  const issueCount = useMemo(() => countChartIssues(root), [root]);
  const overCap = issueCount > TREE_CHART_NODE_CAP;
  const layout = useMemo(
    () => (overCap ? EMPTY_LAYOUT : layoutChartTree(root)),
    [overCap, root],
  );

  const viewRef = useRef<HTMLDivElement>(null);
  const [viewport, setViewport] = useState<ChartViewport>(IDENTITY_VIEWPORT);
  const [viewSize, setViewSize] = useState({ width: 0, height: 0 });

  useEffect(() => {
    const el = viewRef.current;
    if (!el) return;
    const measure = () => setViewSize({ width: el.clientWidth, height: el.clientHeight });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [overCap]);

  const fit = useCallback(
    () => setViewport(fitViewport(layout, viewSize)),
    [layout, viewSize],
  );
  // Fit once per root, when its tree has drawn (not while only the focused
  // node's placeholder is up). Later expansions keep the user's view.
  const fittedFor = useRef<string | null>(null);
  const fitKey = `${tree.focusId ?? "top"}|${virtualRoot}`;
  const rootReady = isChartRootSettled(root);
  useEffect(() => {
    if (!rootReady || overCap || viewSize.width === 0) return;
    if (fittedFor.current === fitKey) return;
    fittedFor.current = fitKey;
    fit();
  }, [fit, fitKey, overCap, rootReady, viewSize.width]);

  const zoomBy = useCallback(
    (factor: number) =>
      setViewport((vp) => zoomAt(vp, factor, viewSize.width / 2, viewSize.height / 2)),
    [viewSize.height, viewSize.width],
  );

  // React registers wheel listeners as passive, which cannot stop the page
  // scrolling; ctrl / meta + wheel (and a trackpad pinch) zoom, plain wheel pans.
  useEffect(() => {
    const el = viewRef.current;
    if (!el) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      if (event.ctrlKey || event.metaKey) {
        const rect = el.getBoundingClientRect();
        const factor = Math.exp(-event.deltaY * 0.01);
        setViewport((vp) =>
          zoomAt(vp, factor, event.clientX - rect.left, event.clientY - rect.top),
        );
      } else {
        setViewport((vp) => panBy(vp, -event.deltaX, -event.deltaY));
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [overCap]);

  const drag = useRef<{ x: number; y: number; moved: boolean } | null>(null);
  const suppressClick = useRef(false);
  const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    if ((event.target as HTMLElement).closest("a,button")) return;
    suppressClick.current = false;
    drag.current = { x: event.clientX, y: event.clientY, moved: false };
    event.currentTarget.setPointerCapture?.(event.pointerId);
  };
  const onPointerMove = (event: PointerEvent<HTMLDivElement>) => {
    const state = drag.current;
    if (!state) return;
    const dx = event.clientX - state.x;
    const dy = event.clientY - state.y;
    if (!state.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD_PX) return;
    state.moved = true;
    state.x = event.clientX;
    state.y = event.clientY;
    setViewport((vp) => panBy(vp, dx, dy));
  };
  const endDrag = (event: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.moved) suppressClick.current = true;
    drag.current = null;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget) return;
    switch (event.key) {
      case "+":
      case "=":
        return zoomBy(CHART_ZOOM_STEP);
      case "-":
        return zoomBy(1 / CHART_ZOOM_STEP);
      case "0":
        return fit();
      case "ArrowLeft":
        return setViewport((vp) => panBy(vp, PAN_KEY_STEP_PX, 0));
      case "ArrowRight":
        return setViewport((vp) => panBy(vp, -PAN_KEY_STEP_PX, 0));
      case "ArrowUp":
        return setViewport((vp) => panBy(vp, 0, PAN_KEY_STEP_PX));
      case "ArrowDown":
        return setViewport((vp) => panBy(vp, 0, -PAN_KEY_STEP_PX));
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <TreeToolbar tree={tree}>
        <Button
          variant="ghost"
          size="xs"
          onClick={tree.expandAll}
          disabled={overCap}
          className="text-muted-foreground"
        >
          <ChevronsUpDown className="size-3.5" />
          <span className="hidden md:inline">{t(($) => $.tree.chart_expand_level)}</span>
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={tree.collapseAll}
          className="text-muted-foreground"
        >
          <ChevronsDownUp className="size-3.5" />
          <span className="hidden md:inline">{t(($) => $.tree.collapse_all)}</span>
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={tree.resetChart}
          className="text-muted-foreground"
        >
          <RotateCcw className="size-3.5" />
          <span className="hidden md:inline">{t(($) => $.tree.chart_reset)}</span>
        </Button>
      </TreeToolbar>
      {overCap ? (
        <TooManyNodes count={issueCount} tree={tree} />
      ) : (
        <div className="relative min-h-0 flex-1">
          <div
            ref={viewRef}
            tabIndex={0}
            role="group"
            aria-label={t(($) => $.tree.chart_aria_label)}
            data-slot="tree-chart"
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={endDrag}
            onPointerCancel={endDrag}
            onClickCapture={(event) => {
              if (!suppressClick.current) return;
              suppressClick.current = false;
              event.preventDefault();
              event.stopPropagation();
            }}
            onKeyDown={onKeyDown}
            className="absolute inset-0 cursor-grab touch-none select-none overflow-hidden bg-background outline-none focus-visible:ring-2 focus-visible:ring-ring/50 active:cursor-grabbing"
          >
            <div
              data-slot="tree-chart-canvas"
              className="absolute left-0 top-0 origin-top-left"
              style={{
                width: layout.width,
                height: layout.height,
                transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.k})`,
              }}
            >
              <svg
                aria-hidden
                data-slot="tree-chart-edges"
                width={layout.width}
                height={layout.height}
                className="absolute left-0 top-0 text-faint-foreground"
              >
                {layout.edges.map((edge) => (
                  <path
                    key={edge.id}
                    d={edge.path}
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={1.5}
                    strokeLinejoin="round"
                  />
                ))}
              </svg>
              {layout.nodes.map((placed) => {
                const { node } = placed;
                if (node.kind === "root") {
                  return (
                    <ProjectChartNode key={placed.id} placed={placed} projectId={projectId} />
                  );
                }
                const row = node.row;
                if (!row) return null;
                if (row.kind === "issue") {
                  return (
                    <IssueChartNode
                      key={placed.id}
                      row={row}
                      placed={placed}
                      childProgress={childProgressMap.get(row.issue.id)}
                      onToggle={tree.toggle}
                      onFocus={tree.focus}
                    />
                  );
                }
                return (
                  <PlaceholderChartNode
                    key={placed.id}
                    row={row}
                    placed={placed}
                    onLoadMore={tree.loadMore}
                    onRetry={tree.retry}
                  />
                );
              })}
            </div>
          </div>
          <div className="absolute bottom-3 right-3 flex items-center gap-1 rounded-lg border bg-card p-1 shadow-xs">
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t(($) => $.tree.chart_zoom_out)}
              title={t(($) => $.tree.chart_zoom_out)}
              onClick={() => zoomBy(1 / CHART_ZOOM_STEP)}
            >
              <ZoomOut className="size-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t(($) => $.tree.chart_zoom_in)}
              title={t(($) => $.tree.chart_zoom_in)}
              onClick={() => zoomBy(CHART_ZOOM_STEP)}
            >
              <ZoomIn className="size-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t(($) => $.tree.chart_fit)}
              title={t(($) => $.tree.chart_fit)}
              onClick={fit}
            >
              <Maximize className="size-3.5" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
