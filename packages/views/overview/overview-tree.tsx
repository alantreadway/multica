"use client";

import { AlertTriangle, X } from "@multica/ui/icons";
import {
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

// AG patch: Overview dashboard, one pinned work tree: known scope (units, stage
// strips, ETA for known units only) beside the facts that say more is coming.

const DAY = 24 * 60 * 60 * 1000;

function StageStrip({ unit }: { unit: AGOverviewUnit }) {
  const { t } = useT("ag");
  const status = useStatusLabel();
  return (
    <ol className="flex gap-1" aria-label={t(($) => $.overview.stages)}>
      {agOverviewStages(unit).map((stage) => (
        <li
          key={stage.name}
          aria-current={stage.state === "current" ? "step" : undefined}
          className={cn(
            "rounded border px-1.5 text-micro whitespace-nowrap",
            stage.state === "done" && "text-muted-foreground",
            stage.state === "todo" && "border-dashed text-muted-foreground",
            stage.state === "current" && "border-brand text-foreground",
          )}
        >
          <span aria-hidden>{stage.state === "done" ? "✓ " : stage.state === "current" ? "● " : ""}</span>
          {stage.status ? status(stage.status) : stage.name}
          {stage.round ? ` ${t(($) => $.overview.round, { round: stage.round })}` : ""}
        </li>
      ))}
    </ol>
  );
}

function PRCell({ unit }: { unit: AGOverviewUnit }) {
  const { t } = useT("ag");
  if (!unit.pr) return null;
  const { pr } = unit;
  const failed = pr.checks === "FAILURE" || pr.checks === "ERROR";
  return (
    <span className={cn("whitespace-nowrap tabular-nums", failed ? "text-destructive" : "text-muted-foreground")}>
      #{pr.number}
      {pr.state === "draft" && ` ${t(($) => $.overview.pr_draft)}`}
      {pr.checks === "SUCCESS" && ` · ${t(($) => $.overview.ci_passed)}`}
      {failed && ` · ${t(($) => $.overview.ci_failed)}`}
    </span>
  );
}

function UnitRow({ unit, clock }: { unit: AGOverviewUnit; clock: number }) {
  const { t } = useT("ag");
  const age = useAge();
  const status = useStatusLabel();
  const waitingLabel = useWaitingOnLabel();
  const waiting = unit.status === "backlog" || unit.status === "todo";
  return (
    <tr className="border-t align-middle">
      <td className="py-1 pr-2">
        <IssueRefButton issue={unit} />
      </td>
      <td className="w-full max-w-0 py-1 pr-2">
        <span className="block truncate" title={unit.title}>
          {unit.title}
        </span>
      </td>
      <td className="py-1 pr-2 whitespace-nowrap">
        {waiting ? <span className="text-caption text-muted-foreground">{status(unit.status)}</span> : <StageStrip unit={unit} />}
      </td>
      <td className="py-1 pr-2 text-caption">
        <PRCell unit={unit} />
      </td>
      <td className={cn("py-1 pr-2 text-caption whitespace-nowrap tabular-nums", unit.ageing ? "text-warning" : "text-muted-foreground")}>
        {!waiting && unit.inStateSince && (
          <>
            {age(unit.inStateSince, clock)}
            {unit.ageing && (
              <AlertTriangle aria-label={t(($) => $.overview.past_usual)} className="ml-1 inline size-3.5" />
            )}
          </>
        )}
      </td>
      <td className="py-1">{unit.waitingOn && <Chip>{waitingLabel(unit.waitingOn)}</Chip>}</td>
    </tr>
  );
}

function OpenWave({ wave, clock }: { wave: AGOverviewWave; clock: number }) {
  const { t } = useT("ag");
  const open = agOverviewOpenUnits(wave);
  const done = wave.units.filter((u) => u.status === "done").length;
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
          <table className="mt-1 w-full text-label">
            <tbody>
              {open.map((unit) => (
                <UnitRow key={unit.id} unit={unit} clock={clock} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function KnownScope({ tree }: { tree: AGOverviewTree }) {
  const { t } = useT("ag");
  const range = useDateRange();
  const { units, done, eta, etaHidden } = tree.known;
  const segments = Math.min(units, 40);
  const filled = units === 0 ? 0 : Math.round((done / units) * segments);
  return (
    <section aria-label={t(($) => $.overview.known)} className="flex min-w-0 flex-col gap-1.5">
      <h4 className="text-caption font-medium text-muted-foreground">{t(($) => $.overview.known)}</h4>
      <div className="flex items-center gap-3">
        <span className="text-label tabular-nums">{t(($) => $.overview.units_done, { done, units })}</span>
        {units > 0 && (
          <div
            role="img"
            aria-label={t(($) => $.overview.units_done, { done, units })}
            className="flex h-2 w-40 gap-0.5"
          >
            {Array.from({ length: segments }, (_, i) => (
              <span key={i} className={cn("flex-1 rounded-sm", i < filled ? "bg-brand" : "bg-muted")} />
            ))}
          </div>
        )}
      </div>
      {eta ? (
        <div>
          <p className="text-label font-medium">{range(eta.p50, eta.p85)}</p>
          <p className="text-caption text-muted-foreground">{t(($) => $.overview.eta_label)}</p>
        </div>
      ) : (
        etaHidden !== "no_open_units" && (
          <p className="text-caption text-muted-foreground">
            {etaHidden === "few_samples" ? t(($) => $.overview.eta_few_samples) : t(($) => $.overview.eta_none)}
          </p>
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
    <section aria-label={t(($) => $.overview.unknown)} className="flex min-w-0 flex-col gap-1.5">
      <h4 className="flex items-center gap-2 text-caption font-medium text-muted-foreground">
        {t(($) => $.overview.unknown)}
        <Chip tone={scopeOpen ? "warn" : "good"}>
          {scopeOpen ? t(($) => $.overview.scope_open) : t(($) => $.overview.scope_complete)}
        </Chip>
      </h4>
      <ul className="flex flex-col gap-1 text-caption">
        {unbrokenCount > 0 && (
          <li>
            {t(($) => $.overview.unbroken, { count: unbrokenCount })}
            <span className="ml-1 inline-flex flex-wrap gap-x-2">
              {unbroken.map((issue) => (
                <span key={issue.id} className="inline-flex items-center gap-1">
                  <IssueRefButton issue={issue} />
                  <span className="text-muted-foreground">{status(issue.status)}</span>
                </span>
              ))}
            </span>
          </li>
        )}
        {recentUnits > 0 && <li>{t(($) => $.overview.recent_units, { count: recentUnits, days: recentDays })}</li>}
        {pastGrowth.length > 0 && (
          <li className="text-muted-foreground">
            {t(($) => $.overview.past_growth, {
              growth: pastGrowth.map((g) => `${agOverviewGrowth(g)}×`).join(", "),
            })}
          </li>
        )}
      </ul>
    </section>
  );
}

export function OverviewTree({
  tree,
  clock,
  onUnpin,
  unpinning,
}: {
  tree: AGOverviewTree;
  clock: number;
  onUnpin: () => void;
  unpinning: boolean;
}) {
  const { t } = useT("ag");
  const doneWaves = tree.waves.filter((w) => w.done && w.units.length + w.issueCount > 0);
  const openWaves = tree.waves.filter((w) => !w.done);
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
          <div className="mt-3 grid gap-4 md:grid-cols-2">
            <KnownScope tree={tree} />
            <UnknownScope tree={tree} />
          </div>
          {openWaves.map((wave) => (
            <OpenWave key={wave.root.id} wave={wave} clock={clock} />
          ))}
          {doneWaves.length > 0 && (
            <p className="mt-3 border-t pt-3 text-caption text-muted-foreground">
              {t(($) => $.overview.done_waves, {
                list: doneWaves
                  .map((w) =>
                    t(($) => $.overview.done_wave, {
                      id: w.root.identifier,
                      count: w.issueCount,
                      days: Math.max(
                        1,
                        Math.ceil(((w.finishedAt ? new Date(w.finishedAt).getTime() : clock) - new Date(w.startedAt).getTime()) / DAY),
                      ),
                    }),
                  )
                  .join(", "),
              })}
            </p>
          )}
        </>
      )}
    </article>
  );
}
