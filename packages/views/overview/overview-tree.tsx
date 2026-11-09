"use client";

import { AlertTriangle, CheckCircle2, X, XCircle } from "@multica/ui/icons";
import {
  agOverviewBoardColumns,
  agOverviewGrowth,
  agOverviewOpenUnits,
  agOverviewStages,
  type AGOverviewTree,
  type AGOverviewUnit,
  type AGOverviewWave,
} from "@multica/core/overview/ag-overview";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useAge, useDateRange, useStatusLabel, useWaitingOnLabel } from "./overview-format";
import { Chip, IssueRefButton } from "./overview-now";
import { OwnerDot, ownerOutline, ownerTone, sortByOwner } from "./overview-owner";

// AG patch: Overview dashboard, one pinned work tree: known scope (a stage board
// of open units, ETA for known units only) beside the facts that say more is coming.

const DAY = 24 * 60 * 60 * 1000;

/** Where a unit is now: owner as the outline, then status, round, and time in this state. */
function StageToken({ unit, running, clock }: { unit: AGOverviewUnit; running: boolean; clock: number }) {
  const { t } = useT("ag");
  const age = useAge();
  const status = useStatusLabel();
  const waitingLabel = useWaitingOnLabel();
  const waiting = unit.status === "backlog" || unit.status === "todo";
  const stage = agOverviewStages(unit).find((s) => s.state === "current");
  const tone = ownerTone(unit.waitingOn);
  return (
    <span
      title={unit.waitingOn ? waitingLabel(unit.waitingOn) : undefined}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border bg-background px-1.5 py-0.5 text-micro whitespace-nowrap",
        waiting ? "border-dashed text-muted-foreground" : ownerOutline(tone),
      )}
    >
      {running && <span aria-hidden className="size-1.5 rounded-full bg-owner-agent motion-safe:animate-pulse" />}
      {waiting ? (
        status(unit.status)
      ) : (
        <>
          {stage?.status && <span>{status(stage.status)}</span>}
          {stage?.round ? <span>{t(($) => $.overview.round_short, { round: stage.round })}</span> : null}
          {unit.inStateSince && (
            <span className={cn("tabular-nums", unit.ageing ? "text-warning" : "text-muted-foreground")}>
              {age(unit.inStateSince, clock)}
            </span>
          )}
          {unit.ageing && <AlertTriangle aria-label={t(($) => $.overview.past_usual)} className="size-3 text-warning" />}
        </>
      )}
      {unit.waitingOn && <span className="sr-only">{waitingLabel(unit.waitingOn)}</span>}
    </span>
  );
}

function PRCell({ unit }: { unit: AGOverviewUnit }) {
  const { t } = useT("ag");
  if (!unit.pr) return null;
  const { pr } = unit;
  const failed = pr.checks === "FAILURE" || pr.checks === "ERROR";
  const passed = pr.checks === "SUCCESS";
  return (
    <span className={cn("inline-flex items-center gap-1 whitespace-nowrap tabular-nums", failed ? "text-destructive" : "text-muted-foreground")}>
      #{pr.number}
      {pr.state === "draft" && <span className="text-muted-foreground">{t(($) => $.overview.pr_draft)}</span>}
      {failed && <XCircle aria-label={t(($) => $.overview.ci_failed)} className="size-3.5" />}
      {passed && <CheckCircle2 aria-label={t(($) => $.overview.ci_passed)} className="size-3.5 text-success" />}
    </span>
  );
}

function BoardRow({
  unit,
  columns,
  running,
  clock,
}: {
  unit: AGOverviewUnit;
  columns: string[];
  running: boolean;
  clock: number;
}) {
  const { t } = useT("ag");
  const stages = new Map(agOverviewStages(unit).map((stage) => [stage.name, stage]));
  const waiting = unit.status === "backlog" || unit.status === "todo";
  return (
    <tr className="border-t align-middle">
      <td className="py-1 pr-2">
        <IssueRefButton issue={unit} />
      </td>
      <td className="max-w-0 py-1 pr-3">
        <span className="block truncate" title={unit.title}>
          {unit.title}
        </span>
      </td>
      {columns.map((name, i) => {
        const stage = stages.get(name);
        const current = stage?.state === "current" || (waiting && i === 0);
        return (
          <td key={name} className="px-1 py-1">
            {current ? (
              <StageToken unit={unit} running={running} clock={clock} />
            ) : stage?.state === "done" ? (
              <span className="block h-0.5 rounded-full bg-muted-foreground/40">
                <span className="sr-only">{t(($) => $.overview.status.done)}</span>
              </span>
            ) : null}
          </td>
        );
      })}
      <td className="py-1 pl-2 text-caption">
        <PRCell unit={unit} />
      </td>
    </tr>
  );
}

function OpenWave({ wave, runningIssueIds, clock }: { wave: AGOverviewWave; runningIssueIds: Set<string>; clock: number }) {
  const { t } = useT("ag");
  const open = agOverviewOpenUnits(wave);
  const done = wave.units.filter((u) => u.status === "done").length;
  const columns = agOverviewBoardColumns(open);
  const inColumn = (name: string, i: number) =>
    open.filter((unit) => {
      if (unit.status === "backlog" || unit.status === "todo") return i === 0;
      return agOverviewStages(unit).some((stage) => stage.name === name && stage.state === "current");
    }).length;
  return (
    <div className="mt-3 border-t pt-3">
      <div className="flex min-w-0 items-center gap-2 text-label">
        <IssueRefButton issue={wave.root} />
        <span className="min-w-0 flex-1 truncate">{wave.root.title}</span>
        <span className="shrink-0 tabular-nums text-muted-foreground">
          {t(($) => $.overview.units_done, { done, units: wave.units.length })}
        </span>
      </div>
      {open.length > 0 && (
        <div className="overflow-x-auto">
          <table className="mt-2 w-full min-w-[48rem] table-fixed text-label" aria-label={t(($) => $.overview.stages)}>
            <colgroup>
              <col className="w-20" />
              <col className="w-56" />
              {columns.map((name) => (
                <col key={name} />
              ))}
              <col className="w-32" />
            </colgroup>
            <thead>
              <tr className="text-micro text-muted-foreground">
                <th colSpan={2} />
                {columns.map((name, i) => {
                  const count = inColumn(name, i);
                  return (
                    <th key={name} scope="col" className="px-1 pb-1 text-left font-normal">
                      <span className="flex items-center justify-between rounded-sm bg-muted/60 px-1.5 py-0.5 capitalize">
                        {name}
                        {count > 0 && <span className="tabular-nums">{count}</span>}
                      </span>
                    </th>
                  );
                })}
                <th scope="col" className="pb-1 pl-2 text-left font-normal">
                  {t(($) => $.overview.pr)}
                </th>
              </tr>
            </thead>
            <tbody>
              {open.map((unit) => (
                <BoardRow key={unit.id} unit={unit} columns={columns} running={runningIssueIds.has(unit.id)} clock={clock} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/** The holders present on this tree's open units, so colour is never the only label. */
function OwnerLegend({ units }: { units: AGOverviewUnit[] }) {
  const waitingLabel = useWaitingOnLabel();
  const counts = new Map<string, number>();
  for (const unit of units) {
    if (unit.waitingOn) counts.set(unit.waitingOn, (counts.get(unit.waitingOn) ?? 0) + 1);
  }
  if (counts.size === 0) return null;
  return (
    <ul className="flex flex-wrap gap-x-3 text-caption text-muted-foreground">
      {sortByOwner([...counts.entries()]).map(([holder, count]) => (
        <li key={holder} className="flex items-center gap-1.5">
          <OwnerDot tone={ownerTone(holder)} />
          <span className="tabular-nums">
            {waitingLabel(holder)} {count}
          </span>
        </li>
      ))}
    </ul>
  );
}

function KnownScope({ tree }: { tree: AGOverviewTree }) {
  const { t } = useT("ag");
  const range = useDateRange();
  const { units, done, eta, etaHidden } = tree.known;
  const segments = Math.min(units, 40);
  const filled = units === 0 ? 0 : Math.round((done / units) * segments);
  return (
    <section aria-label={t(($) => $.overview.known)} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <span className="text-label tabular-nums">{t(($) => $.overview.units_done, { done, units })}</span>
      {units > 0 && (
        <div aria-hidden className="flex h-2 w-48 gap-0.5">
          {Array.from({ length: segments }, (_, i) => (
            <span key={i} className={cn("flex-1 rounded-sm", i < filled ? "bg-muted-foreground" : "bg-muted")} />
          ))}
        </div>
      )}
      {eta ? (
        <span className="flex items-baseline gap-2">
          <span className="text-label font-medium">{range(eta.p50, eta.p85)}</span>
          <span className="text-caption text-muted-foreground">{t(($) => $.overview.eta_label)}</span>
        </span>
      ) : (
        etaHidden !== "no_open_units" && (
          <span className="text-caption text-muted-foreground">
            {etaHidden === "few_samples" ? t(($) => $.overview.eta_few_samples) : t(($) => $.overview.eta_none)}
          </span>
        )
      )}
    </section>
  );
}

function UnknownScope({ tree }: { tree: AGOverviewTree }) {
  const { t } = useT("ag");
  const status = useStatusLabel();
  const { scopeOpen, unbroken, unbrokenCount, recentUnits, recentDays, pastGrowth } = tree.unknown;
  return (
    <section
      aria-label={t(($) => $.overview.unknown)}
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-caption"
    >
      <Chip tone={scopeOpen ? "warn" : "good"}>
        {scopeOpen ? t(($) => $.overview.scope_open) : t(($) => $.overview.scope_complete)}
      </Chip>
      {unbrokenCount > 0 && (
        <span className="inline-flex flex-wrap items-center gap-x-1.5">
          {t(($) => $.overview.unbroken, { count: unbrokenCount })}
          {unbroken.map((issue) => (
            <span key={issue.id} className="inline-flex items-center gap-1">
              <IssueRefButton issue={issue} />
              <span className="text-muted-foreground">{status(issue.status)}</span>
            </span>
          ))}
        </span>
      )}
      {recentUnits > 0 && <span>{t(($) => $.overview.recent_units, { count: recentUnits, days: recentDays })}</span>}
      {pastGrowth.length > 0 && (
        <span className="text-muted-foreground">
          {t(($) => $.overview.past_growth, {
            growth: pastGrowth.map((g) => `${agOverviewGrowth(g)}×`).join(", "),
          })}
        </span>
      )}
    </section>
  );
}

export function OverviewTree({
  tree,
  clock,
  runningIssueIds,
  onUnpin,
  unpinning,
}: {
  tree: AGOverviewTree;
  clock: number;
  runningIssueIds: Set<string>;
  onUnpin: () => void;
  unpinning: boolean;
}) {
  const { t } = useT("ag");
  const doneWaves = tree.waves.filter((w) => w.done && w.units.length + w.issueCount > 0);
  const openWaves = tree.waves.filter((w) => !w.done);
  const openUnits = openWaves.flatMap(agOverviewOpenUnits);
  const weeks = tree.startedAt ? Math.floor((clock - new Date(tree.startedAt).getTime()) / (7 * DAY)) : 0;
  const days = tree.startedAt ? Math.floor((clock - new Date(tree.startedAt).getTime()) / DAY) : 0;
  const meta = [
    tree.waves.length > 0 ? t(($) => $.overview.waves, { count: tree.waves.length }) : null,
    tree.startedAt ? (weeks >= 2 ? t(($) => $.overview.weeks, { count: weeks }) : t(($) => $.overview.days, { count: days })) : null,
    tree.runHours > 0 ? t(($) => $.overview.run_hours, { count: Math.round(tree.runHours) }) : null,
  ].filter(Boolean);

  return (
    <article aria-label={tree.pin.label} className="rounded-lg border bg-card p-3">
      <header className="flex min-w-0 items-baseline gap-3">
        <h3 className="shrink-0 font-medium">{tree.pin.label}</h3>
        {tree.title && <span className="min-w-0 truncate">{tree.title}</span>}
        <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground">{meta.join(" · ")}</span>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label={t(($) => $.overview.unpin, { label: tree.pin.label })}
          disabled={unpinning}
          aria-busy={unpinning}
          onClick={onUnpin}
        >
          <X />
        </Button>
      </header>
      {tree.issueCount === 0 ? (
        <p className="mt-2 text-caption text-muted-foreground">{t(($) => $.overview.no_issues)}</p>
      ) : (
        <>
          <div className="mt-2 flex flex-col gap-1.5">
            <KnownScope tree={tree} />
            <UnknownScope tree={tree} />
          </div>
          {openWaves.map((wave) => (
            <OpenWave key={wave.root.id} wave={wave} runningIssueIds={runningIssueIds} clock={clock} />
          ))}
          {(openUnits.length > 0 || doneWaves.length > 0) && (
            <footer className="mt-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t pt-3">
              {doneWaves.length > 0 ? (
                <p className="min-w-0 text-caption text-muted-foreground">
                  {t(($) => $.overview.done_waves, {
                    list: doneWaves
                      .map((w) =>
                        t(($) => $.overview.done_wave, {
                          id: w.root.identifier,
                          count: w.issueCount,
                          days: Math.max(
                            1,
                            Math.ceil(
                              ((w.finishedAt ? new Date(w.finishedAt).getTime() : clock) - new Date(w.startedAt).getTime()) / DAY,
                            ),
                          ),
                        }),
                      )
                      .join(", "),
                  })}
                </p>
              ) : (
                <span />
              )}
              <OwnerLegend units={openUnits} />
            </footer>
          )}
        </>
      )}
    </article>
  );
}
