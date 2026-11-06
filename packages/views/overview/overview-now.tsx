"use client";

import { AlertTriangle } from "@multica/ui/icons";
import type { AGOverview, AGOverviewIssueRef } from "@multica/core/overview/ag-overview";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useIssuePeekActions } from "../issues/surface/peek-context";
import { AppLink } from "../navigation";
import { useAge, useStatusLabel, useWaitingOnLabel } from "./overview-format";
import { OwnerDot, ownerFill, ownerTone, sortByOwner } from "./overview-owner";

// AG patch: Overview dashboard, the "Now" cards. Read-only: decisions happen on
// the Decisions page.

const HOUR = 3_600_000;
/** The ageing plot runs from 1× to 10× a state's usual (p90) time, on a log scale. */
const AGEING_MAX_RATIO = 10;
const AGEING_TICKS = [1, 2, 5, 10];
/** Labels closer than this (percent of the width) go on the other lane. */
const AGEING_LABEL_GAP = 9;

export function IssueRefButton({ issue, className }: { issue: AGOverviewIssueRef; className?: string }) {
  const peek = useIssuePeekActions();
  return (
    <Button
      variant="link"
      size="xs"
      className={cn("h-auto shrink-0 p-0 font-mono text-caption", className)}
      title={issue.title}
      onClick={() => peek?.open(issue.id)}
    >
      {issue.identifier}
    </Button>
  );
}

export function Chip({ children, tone }: { children: React.ReactNode; tone?: "warn" | "bad" | "good" }) {
  return (
    <span
      className={cn(
        "shrink-0 rounded-full border px-1.5 text-micro",
        tone === "warn" && "border-warning/40 text-warning",
        tone === "bad" && "border-destructive/40 text-destructive",
        tone === "good" && "border-success/40 text-success",
        !tone && "text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}

function Card({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1 rounded-lg border bg-card p-3">
      <h3 className="mb-1 flex items-center justify-between text-label font-medium">
        {title}
        <span className="tabular-nums text-muted-foreground">{count}</span>
      </h3>
      {children}
    </section>
  );
}

function Row({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("flex min-w-0 items-center gap-2 text-caption", className)}>{children}</div>;
}

/** Every open Desk card by who holds it, you included, as one bar. */
function HolderBar({ counts }: { counts: [string, number][] }) {
  const { t } = useT("ag");
  const waitingLabel = useWaitingOnLabel();
  const total = counts.reduce((sum, [, count]) => sum + count, 0);
  if (total === 0) return null;
  return (
    <div className="mt-2 flex flex-col gap-1.5">
      <div role="img" aria-label={t(($) => $.overview.holders, { count: total })} className="flex h-2.5 gap-0.5">
        {counts.map(([holder, count]) => (
          <span
            key={holder}
            title={`${waitingLabel(holder)} ${count}`}
            style={{ flexGrow: count }}
            className={cn("rounded-sm", ownerFill(ownerTone(holder)))}
          />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-3 gap-y-0.5 text-caption text-muted-foreground">
        {counts.map(([holder, count]) => (
          <li key={holder} className="flex items-center gap-1.5">
            <OwnerDot tone={ownerTone(holder)} />
            <span className="tabular-nums">
              {waitingLabel(holder)} {count}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function ageingRatio(item: AGOverview["now"]["ageing"][number], clock: number) {
  const hours = Math.max(0, clock - new Date(item.inStateSince).getTime()) / HOUR;
  return item.p90Hours > 0 ? hours / item.p90Hours : AGEING_MAX_RATIO;
}

/** Percent along the axis for a multiple of the usual time. */
function ageingX(ratio: number) {
  const clamped = Math.min(Math.max(ratio, 1), AGEING_MAX_RATIO);
  return (Math.log10(clamped) / Math.log10(AGEING_MAX_RATIO)) * 100;
}

/** Each ageing unit as a dot on "how many times its usual time", so the worst stands out. */
function AgeingPlot({ items, clock }: { items: AGOverview["now"]["ageing"]; clock: number }) {
  const { t } = useT("ag");
  const age = useAge();
  const status = useStatusLabel();
  const waitingLabel = useWaitingOnLabel();
  const peek = useIssuePeekActions();
  const placed = items
    .map((item) => ({ item, x: ageingX(ageingRatio(item, clock)) }))
    .sort((a, b) => a.x - b.x);
  const lastX = [-Infinity, -Infinity];
  const dots = placed.map(({ item, x }) => {
    const lane = x - lastX[0]! >= AGEING_LABEL_GAP ? 0 : x - lastX[1]! >= AGEING_LABEL_GAP ? 1 : 0;
    lastX[lane] = x;
    return { item, x, lane };
  });
  const worst = placed[placed.length - 1]?.item;
  const describe = (item: AGOverview["now"]["ageing"][number]) =>
    [
      item.issue.identifier,
      t(($) => $.overview.past_p90, { status: status(item.status), age: age(item.inStateSince, clock), p90: item.p90Hours }),
      item.waitingOn ? waitingLabel(item.waitingOn) : null,
    ]
      .filter(Boolean)
      .join(" · ");

  return (
    <>
      {/* Inset by half a dot so the ends of the scale stay inside the card. */}
      <div className="relative mx-2 mt-1 h-16">
        <span aria-hidden className="absolute inset-x-0 top-10 h-px bg-border" />
        {AGEING_TICKS.map((tick) => (
          <span
            key={tick}
            aria-hidden
            style={{ left: `${ageingX(tick)}%` }}
            className="absolute top-12 -translate-x-1/2 text-micro whitespace-nowrap text-muted-foreground"
          >
            {tick === 1
              ? t(($) => $.overview.ratio_usual, { ratio: tick })
              : tick === AGEING_MAX_RATIO
                ? t(($) => $.overview.ratio_max, { ratio: tick })
                : t(($) => $.overview.ratio, { ratio: tick })}
          </span>
        ))}
        <ul className="contents">
          {dots.map(({ item, x, lane }) => (
            <li key={item.issue.id} className="contents">
              <button
                type="button"
                title={describe(item)}
                aria-label={describe(item)}
                onClick={() => peek?.open(item.issue.id)}
                style={{ left: `${x}%`, top: lane === 0 ? 18 : 2 }}
                className="group absolute flex -translate-x-1/2 flex-col items-center gap-0.5 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="font-mono text-micro text-muted-foreground group-hover:text-foreground">
                  {item.issue.number || item.issue.identifier}
                </span>
                {lane === 1 && <span aria-hidden className="h-3.5 w-px bg-border" />}
                <OwnerDot tone={ownerTone(item.waitingOn)} className="size-2.5 ring-2 ring-card" />
              </button>
            </li>
          ))}
        </ul>
      </div>
      {worst && (
        <Row className="mt-1">
          <AlertTriangle aria-hidden className="size-3.5 shrink-0 text-warning" />
          <IssueRefButton issue={worst.issue} />
          <span className="min-w-0 flex-1 truncate text-muted-foreground">
            {t(($) => $.overview.past_p90, {
              status: status(worst.status),
              age: age(worst.inStateSince, clock),
              p90: worst.p90Hours,
            })}
          </span>
        </Row>
      )}
    </>
  );
}

export function OverviewNow({ now: data, clock }: { now: AGOverview["now"]; clock: number }) {
  const { t } = useT("ag");
  const age = useAge();
  const paths = useWorkspacePaths();
  const oldestQueued = data.queued[0];
  const holders = sortByOwner([
    ...(data.waitingOnYou.length > 0 ? ([["alan", data.waitingOnYou.length]] as [string, number][]) : []),
    ...Object.entries(data.elsewhere).filter(([holder, count]) => holder !== "alan" && count > 0),
  ]);

  return (
    <div className="grid gap-3 md:grid-cols-3">
      <Card title={t(($) => $.overview.running)} count={data.running.length}>
        {data.running.length === 0 && <p className="text-caption text-muted-foreground">{t(($) => $.overview.nothing_running)}</p>}
        {data.running.map((task) => (
          <Row key={task.id}>
            <span aria-hidden className="size-2 shrink-0 rounded-full bg-owner-agent motion-safe:animate-pulse" />
            <span className="max-w-32 shrink-0 truncate">{task.agentName}</span>
            {task.issue ? (
              <>
                <IssueRefButton issue={task.issue} />
                <span className="min-w-0 flex-1 truncate text-muted-foreground">{task.issue.title}</span>
              </>
            ) : (
              <span className="min-w-0 flex-1 truncate text-muted-foreground">{t(($) => $.overview.no_issue)}</span>
            )}
            {task.reviewRound ? <Chip>{t(($) => $.overview.round, { round: task.reviewRound })}</Chip> : null}
            <span className="shrink-0 tabular-nums text-muted-foreground">{age(task.since, clock)}</span>
          </Row>
        ))}
        {oldestQueued && (
          <Row className="mt-1 text-muted-foreground">
            <span className="min-w-0 truncate">
              {t(($) => $.overview.queued, {
                count: data.queued.length,
                age: age(oldestQueued.since, clock),
                agent: oldestQueued.agentName,
              })}
            </span>
            {oldestQueued.issue && <IssueRefButton issue={oldestQueued.issue} />}
          </Row>
        )}
      </Card>

      <Card title={t(($) => $.overview.waiting_on_you)} count={data.waitingOnYou.length}>
        {data.waitingOnYou.length === 0 && (
          <p className="text-caption text-muted-foreground">{t(($) => $.overview.nothing_waiting)}</p>
        )}
        {data.waitingOnYou.map((card) => (
          <Row key={card.id}>
            {card.cardType && <Chip>{card.cardType.replace(/_/g, " ")}</Chip>}
            {card.issue && (
              <>
                <IssueRefButton issue={card.issue} />
                <span className="min-w-0 flex-1 truncate text-muted-foreground">{card.issue.title}</span>
              </>
            )}
          </Row>
        ))}
        <Row>
          <AppLink href={paths.decisions()} className="text-brand hover:underline">
            {t(($) => $.overview.open_decisions)}
          </AppLink>
        </Row>
        <HolderBar counts={holders} />
      </Card>

      <Card title={t(($) => $.overview.ageing)} count={data.ageing.length}>
        {data.ageing.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.overview.nothing_ageing)}</p>
        ) : (
          <AgeingPlot items={data.ageing} clock={clock} />
        )}
      </Card>
    </div>
  );
}
