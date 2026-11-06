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

// AG patch: Overview dashboard, the "Now" cards. Read-only: decisions happen on
// the Decisions page.

const WAITING_ORDER = ["author", "colleague", "agent", "nobody"];

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

export function OverviewNow({ now: data, clock }: { now: AGOverview["now"]; clock: number }) {
  const { t } = useT("ag");
  const age = useAge();
  const status = useStatusLabel();
  const waitingLabel = useWaitingOnLabel();
  const paths = useWorkspacePaths();
  const oldestQueued = data.queued[0];
  const elsewhere = Object.entries(data.elsewhere).sort(
    ([a], [b]) => (WAITING_ORDER.indexOf(a) + 1 || 99) - (WAITING_ORDER.indexOf(b) + 1 || 99),
  );

  return (
    <div className="grid gap-3 md:grid-cols-3">
      <Card title={t(($) => $.overview.running)} count={data.running.length}>
        {data.running.length === 0 && <p className="text-caption text-muted-foreground">{t(($) => $.overview.nothing_running)}</p>}
        {data.running.map((task) => (
          <Row key={task.id}>
            <span aria-hidden className="size-2 shrink-0 rounded-full bg-brand" />
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
        {elsewhere.length > 0 && (
          <Row className="mt-1 flex-wrap text-muted-foreground">
            <span>{t(($) => $.overview.elsewhere)}</span>
            {elsewhere.map(([holder, count]) => (
              <span key={holder} className="tabular-nums">
                {waitingLabel(holder)} {count}
              </span>
            ))}
          </Row>
        )}
      </Card>

      <Card title={t(($) => $.overview.ageing)} count={data.ageing.length}>
        {data.ageing.length === 0 && <p className="text-caption text-muted-foreground">{t(($) => $.overview.nothing_ageing)}</p>}
        {data.ageing.map((item) => (
          <Row key={item.issue.id}>
            <AlertTriangle aria-hidden className="size-3.5 shrink-0 text-warning" />
            <IssueRefButton issue={item.issue} />
            <span className="min-w-0 flex-1 truncate text-muted-foreground">
              {t(($) => $.overview.past_p90, {
                status: status(item.status),
                age: age(item.inStateSince, clock),
                p90: item.p90Hours,
              })}
            </span>
            {item.waitingOn && <Chip>{waitingLabel(item.waitingOn)}</Chip>}
          </Row>
        ))}
      </Card>
    </div>
  );
}
