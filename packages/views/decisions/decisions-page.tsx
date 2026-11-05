"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  countAGDecisionsByWaitingOn,
  groupAGDecisionCards,
  isAGDecisionEngineStale,
  WAITING_ON_ALAN,
  type AGDecisionCard,
  type AGDecisionChipTone,
} from "@multica/core/decisions/ag-decisions";
import { agDecisionListOptions } from "@multica/core/decisions/ag-decisions-queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useTimeAgo } from "../i18n/use-time-ago";
import { useT } from "../i18n";
import { IssuePeekHost } from "../issues/components/issue-peek";
import { useIssuePeekActions } from "../issues/surface/peek-context";
import { PAGE_GUTTER, PAGE_RAIL, PAGE_TOOLBAR, PageHeader } from "../layout/page-header";

const CLOCK_TICK_MS = 30_000;

const CHIP_TONE: Record<AGDecisionChipTone, string> = {
  neutral: "",
  good: "text-success",
  warn: "text-warning",
  bad: "text-destructive",
};

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

function DecisionCardRow({ card }: { card: AGDecisionCard }) {
  const { t } = useT("ag");
  const timeAgo = useTimeAgo();
  const peek = useIssuePeekActions();
  const heading = card.title || card.issueRef || card.cardType;

  return (
    <li className="flex flex-col gap-1.5 border-b px-1 py-3 last:border-b-0">
      <div className="flex min-w-0 items-center gap-2">
        {card.issueId && card.issueRef && (
          <Button
            variant="link"
            size="xs"
            className="h-auto shrink-0 p-0 text-caption"
            onClick={() => peek?.open(card.issueId as string)}
          >
            {card.issueRef}
          </Button>
        )}
        <span className="min-w-0 flex-1 truncate font-medium">{heading}</span>
        {card.firstSeenAt && (
          <span className="shrink-0 text-caption text-muted-foreground">
            {t(($) => $.decisions.waiting, { time: timeAgo(card.firstSeenAt as string) })}
          </span>
        )}
      </div>
      {card.chips.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {card.chips.map((chip, n) => (
            <Badge key={`${chip.label}-${n}`} variant="outline" className={CHIP_TONE[chip.tone]}>
              {chip.label}
            </Badge>
          ))}
        </div>
      )}
      {card.lines.map((line, n) => (
        <p key={n} className="break-words text-muted-foreground">
          {line}
        </p>
      ))}
      {card.recommended && (
        <p className="break-words">{t(($) => $.decisions.recommended, { action: card.recommended })}</p>
      )}
      {card.blockers.length > 0 && (
        <p className="break-words text-caption text-muted-foreground">
          {t(($) => $.decisions.blocked, { reason: card.blockers.join("; ") })}
        </p>
      )}
      {card.links.length > 0 && (
        <div className="flex flex-wrap gap-3">
          {card.links.map((link) => (
            <a
              key={link.url}
              href={link.url}
              target="_blank"
              rel="noreferrer"
              className="text-caption text-muted-foreground underline underline-offset-2 hover:text-foreground"
            >
              {link.label}
            </a>
          ))}
        </div>
      )}
    </li>
  );
}

export function DecisionsPage() {
  const { t } = useT("ag");
  const { t: tLayout } = useT("layout");
  const timeAgo = useTimeAgo();
  const label = useWaitingOnLabel();
  const wsId = useWorkspaceId();
  const { data, isPending, isError } = useQuery(agDecisionListOptions(wsId));
  const [holder, setHolder] = useState(WAITING_ON_ALAN);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
    return () => clearInterval(timer);
  }, []);

  const cards = useMemo(() => data?.cards ?? [], [data]);
  const holders = useMemo(() => countAGDecisionsByWaitingOn(cards), [cards]);
  const groups = useMemo(
    () => groupAGDecisionCards(cards.filter((card) => card.waitingOn === holder)),
    [cards, holder],
  );
  const lastSyncAt = data?.lastSyncAt ?? null;
  const stale = isAGDecisionEngineStale(lastSyncAt, now);

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
        <div className={cn(PAGE_TOOLBAR, "justify-start")}>
          {holders.map(({ waitingOn, count }) => (
            <Button
              key={waitingOn}
              size="sm"
              variant={waitingOn === holder ? "brand" : "ghost"}
              aria-pressed={waitingOn === holder}
              onClick={() => setHolder(waitingOn)}
            >
              {label(waitingOn)} {count}
            </Button>
          ))}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className={cn(PAGE_RAIL, PAGE_GUTTER, "flex flex-col gap-4 pb-6")}>
            {stale && lastSyncAt && (
              <p role="status" className="text-warning">
                {t(($) => $.decisions.stale, { time: timeAgo(lastSyncAt) })}
              </p>
            )}
            {isPending && <Skeleton className="h-24 w-full" />}
            {isError && <p role="alert" className="text-muted-foreground">{t(($) => $.decisions.load_error)}</p>}
            {!isPending && !isError && !lastSyncAt && (
              <p className="text-muted-foreground">{t(($) => $.decisions.never_synced)}</p>
            )}
            {!isPending && !isError && lastSyncAt && groups.length === 0 && (
              <p className="text-muted-foreground">
                {holder === WAITING_ON_ALAN ? t(($) => $.decisions.empty) : t(($) => $.decisions.nothing_here)}
              </p>
            )}
            {groups.map((group) => (
              <section key={group.name} aria-label={group.name || undefined}>
                {group.name && (
                  <h2 className="text-caption font-medium text-muted-foreground">
                    {group.name} ({group.cards.length})
                  </h2>
                )}
                <ul>
                  {group.cards.map((card) => (
                    <DecisionCardRow key={card.id} card={card} />
                  ))}
                </ul>
              </section>
            ))}
          </div>
        </div>
      </div>
    </IssuePeekHost>
  );
}
