"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout } from "react-resizable-panels";
import {
  agDecisionCardIsActionable,
  agDecisionKeyedActions,
  countAGDecisionsByWaitingOn,
  groupAGDecisionCards,
  isAGDecisionEngineStale,
  isAGDecisionWorkerOffline,
  WAITING_ON_ALAN,
  type AGDecisionCard,
} from "@multica/core/decisions/ag-decisions";
import { agDecisionListOptions } from "@multica/core/decisions/ag-decisions-queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { isEditableShortcutTarget, isPortalLayerShortcutTarget } from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { Button } from "@multica/ui/components/ui/button";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@multica/ui/components/ui/resizable";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { useTimeAgo } from "../i18n/use-time-ago";
import { useT } from "../i18n";
import { IssuePeekHost } from "../issues/components/issue-peek";
import { PAGE_GUTTER, PAGE_TOOLBAR, PageHeader } from "../layout/page-header";
import { DecisionDetail } from "./decision-detail";
import { DecisionList } from "./decision-list";
import { useDecisionActions } from "./use-decision-actions";

const CLOCK_TICK_MS = 30_000;
const LIST_DEFAULT_SIZE = 280;
const LIST_MIN_SIZE = 240;
const LIST_MAX_SIZE = 420;

function useWaitingOnLabel() {
  const { t } = useT("ag");
  return (waitingOn: string): string => {
    switch (waitingOn) {
      case "alan":
        return t(($) => $.decisions.waiting_on.alan);
      case "author":
        return t(($) => $.decisions.waiting_on.author);
      case "colleague":
        return t(($) => $.decisions.waiting_on.colleague);
      case "agent":
        return t(($) => $.decisions.waiting_on.agent);
      case "nobody":
        return t(($) => $.decisions.waiting_on.nobody);
      default:
        // The engine can add a holder without a web build.
        return waitingOn;
    }
  };
}

/** The card after `id` in `order`, else the one before it: where the eye goes once `id` leaves the list. */
const neighbourOf = (order: readonly AGDecisionCard[], id: string) => {
  const at = order.findIndex((card) => card.id === id);
  return at < 0 ? null : (order[at + 1] ?? order[at - 1] ?? null);
};

export function DecisionsPage() {
  const { t } = useT("ag");
  const { t: tLayout } = useT("layout");
  const timeAgo = useTimeAgo();
  const label = useWaitingOnLabel();
  const wsId = useWorkspaceId();
  const isCompact = useIsCompact();
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({ id: "multica_ag_decisions_layout" });
  const { data, isPending, isError } = useQuery(agDecisionListOptions(wsId));
  const [holder, setHolder] = useState(WAITING_ON_ALAN);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [compactDetail, setCompactDetail] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
    return () => clearInterval(timer);
  }, []);

  const cards = useMemo(() => data?.cards ?? [], [data]);
  // Read by onSent, which runs after the click that changed the list.
  const queueRef = useRef<AGDecisionCard[]>([]);
  const actions = useDecisionActions({
    wsId,
    onSent: (card) => {
      setSelectedId(neighbourOf(queueRef.current, card.id)?.id ?? null);
      setCompactDetail(false);
    },
  });

  const placed = cards.map((card) => ({ card, place: actions.placeOf(card) }));
  const holders = countAGDecisionsByWaitingOn(placed.filter((p) => p.place !== "activity").map((p) => p.card));
  const mine = placed.filter((p) => p.card.waitingOn === holder);
  const retry = mine.filter((p) => p.place === "retry").map((p) => p.card);
  const groups = groupAGDecisionCards(mine.filter((p) => p.place === "queue").map((p) => p.card));
  const activity = placed.filter((p) => p.place === "activity").map((p) => p.card);
  const queue = [...retry, ...groups.flatMap((group) => group.cards)];
  queueRef.current = queue;
  const navigable = [...queue, ...activity];
  const selected = navigable.find((card) => card.id === selectedId) ?? queue[0] ?? null;

  const lastSyncAt = data?.lastSyncAt ?? null;
  const stale = isAGDecisionEngineStale(lastSyncAt, now);
  const workerOffline = lastSyncAt !== null && isAGDecisionWorkerOffline(data?.workerSeenAt ?? null, now);

  const select = (id: string) => {
    setSelectedId(id);
    setCompactDetail(true);
  };

  // J/K move, 1-9 act on the open card, Z undoes the last send. Read through a ref so the listener is attached once.
  const keys = useRef<(event: KeyboardEvent) => void>(() => {});
  keys.current = (event) => {
    if (event.metaKey || event.ctrlKey || event.altKey || event.repeat || isImeComposing(event)) return;
    if (isEditableShortcutTarget(event.target) || isPortalLayerShortcutTarget(event.target) || actions.dialogOpen) return;
    const key = event.key.toLowerCase();
    const at = selected ? navigable.indexOf(selected) : -1;
    if (key === "j" || key === "k") {
      const next = navigable[key === "j" ? at + 1 : Math.max(at - 1, 0)];
      if (!next) return;
      event.preventDefault();
      setSelectedId(next.id);
    } else if (key === "z") {
      event.preventDefault();
      actions.undoLast();
    } else if (/^[1-9]$/.test(key) && selected && agDecisionCardIsActionable(selected)) {
      const action = agDecisionKeyedActions(selected)[Number(key) - 1];
      if (!action) return;
      event.preventDefault();
      actions.request(selected, action);
    }
  };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => keys.current(event);
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const emptyText = holder === WAITING_ON_ALAN ? t(($) => $.decisions.empty) : t(($) => $.decisions.nothing_here);
  const notice = isPending ? null : isError ? (
    <p role="alert" className="text-muted-foreground">
      {t(($) => $.decisions.load_error)}
    </p>
  ) : !lastSyncAt ? (
    <p className="text-muted-foreground">{t(($) => $.decisions.never_synced)}</p>
  ) : null;

  // Keyed by holder: switching tabs swaps the list, it does not animate cards out of it.
  const list = (
    <DecisionList
      key={holder}
      retry={retry}
      groups={groups}
      activity={activity}
      selectedId={selected?.id ?? null}
      onSelect={select}
      departingAction={actions.departingAction}
      onUndo={actions.undo}
    />
  );
  const detail = selected ? (
    <DecisionDetail
      key={selected.id}
      card={selected}
      busy={actions.busy}
      onAction={(action) => actions.request(selected, action)}
      onBack={isCompact ? () => setCompactDetail(false) : undefined}
    />
  ) : (
    <div className="flex h-full items-center justify-center p-6 text-muted-foreground">{emptyText}</div>
  );

  return (
    <IssuePeekHost>
      <div className="flex h-full min-h-0 flex-col">
        <PageHeader>
          <h1 className="min-w-0 flex-1 truncate font-medium">{tLayout(($) => $.nav.decisions)}</h1>
          {lastSyncAt && !stale && (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.decisions.updated, { time: timeAgo(lastSyncAt) })}
            </span>
          )}
        </PageHeader>
        <div className={cn(PAGE_TOOLBAR, "justify-start gap-1 border-b")}>
          {holders.map(({ waitingOn, count }) => (
            <Button
              key={waitingOn}
              size="sm"
              variant={waitingOn === holder ? "brand" : "ghost"}
              aria-pressed={waitingOn === holder}
              onClick={() => {
                setHolder(waitingOn);
                setSelectedId(null);
              }}
            >
              {label(waitingOn)}{" "}
              <span
                className={cn(
                  "rounded-full px-1.5 text-caption tabular-nums",
                  waitingOn === holder ? "bg-brand-foreground/20" : "bg-muted text-muted-foreground",
                )}
              >
                {count}
              </span>
            </Button>
          ))}
        </div>
        {(stale || workerOffline || notice) && (
          <div className={cn(PAGE_GUTTER, "flex flex-col gap-1 border-b py-2")}>
            {stale && lastSyncAt && (
              <p role="status" className="text-warning">
                {t(($) => $.decisions.stale, { time: timeAgo(lastSyncAt) })}
              </p>
            )}
            {workerOffline && (
              <p role="status" className="text-warning">
                {t(($) => $.decisions.actions.worker_offline)}
              </p>
            )}
            {notice}
          </div>
        )}
        {isPending ? (
          <div className={cn(PAGE_GUTTER, "py-4")}>
            <Skeleton className="h-24 w-full" />
          </div>
        ) : isCompact ? (
          <div className="flex min-h-0 flex-1 flex-col">
            {compactDetail && selected ? detail : queue.length + activity.length > 0 ? list : detail}
          </div>
        ) : (
          <ResizablePanelGroup
            orientation="horizontal"
            className="min-h-0 flex-1"
            defaultLayout={defaultLayout}
            onLayoutChanged={onLayoutChanged}
          >
            <ResizablePanel
              id="list"
              defaultSize={LIST_DEFAULT_SIZE}
              minSize={LIST_MIN_SIZE}
              maxSize={LIST_MAX_SIZE}
              groupResizeBehavior="preserve-pixel-size"
            >
              <div className="h-full border-r">{list}</div>
            </ResizablePanel>
            <ResizableHandle />
            <ResizablePanel id="detail" minSize="40%">
              {detail}
            </ResizablePanel>
          </ResizablePanelGroup>
        )}
      </div>
      {actions.dialog}
    </IssuePeekHost>
  );
}
