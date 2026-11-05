"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  agDecisionBatchActions,
  agDecisionCardIsActionable,
  countAGDecisionsByWaitingOn,
  groupAGDecisionCards,
  isAGDecisionEngineStale,
  isAGDecisionWorkerOffline,
  visibleAGDecisionIntent,
  WAITING_ON_ALAN,
  type AGDecisionAction,
  type AGDecisionCard,
  type AGDecisionChipTone,
} from "@multica/core/decisions/ag-decisions";
import {
  agDecisionListOptions,
  useCancelAGDecisionIntent,
  useCreateAGDecisionIntent,
} from "@multica/core/decisions/ag-decisions-queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useTimeAgo } from "../i18n/use-time-ago";
import { useT } from "../i18n";
import { IssuePeekHost } from "../issues/components/issue-peek";
import { useIssuePeekActions } from "../issues/surface/peek-context";
import { PAGE_GUTTER, PAGE_TOOLBAR, PageHeader } from "../layout/page-header";

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

const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** The second tap: why it needs one, the text it needs, or why the recommendation was not taken. */
function ActionDialog({
  title,
  action,
  reasons,
  onCancel,
  onConfirm,
}: {
  title: string;
  action: AGDecisionAction;
  reasons: string[];
  onCancel: () => void;
  onConfirm: (input: { note: string; overrideReason: string }) => Promise<void>;
}) {
  const { t } = useT("ag");
  const [note, setNote] = useState("");
  const [overrideReason, setOverrideReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const missingNote = Boolean(action.note?.required) && !note.trim();

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await onConfirm({ note, overrideReason });
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onCancel()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {action.confirm && <DialogDescription>{action.confirm}</DialogDescription>}
        </DialogHeader>
        {action.note && (
          <Textarea
            value={note}
            placeholder={action.note.placeholder}
            aria-label={action.note.placeholder || action.label}
            onChange={(event) => setNote(event.target.value)}
          />
        )}
        {reasons.length > 0 && (
          <div role="group" aria-label={t(($) => $.decisions.actions.override_reason)} className="flex flex-col gap-1.5">
            <p className="text-caption text-muted-foreground">{t(($) => $.decisions.actions.override_reason)}</p>
            <div className="flex flex-wrap gap-1">
              {reasons.map((reason) => (
                <Button
                  key={reason}
                  size="xs"
                  variant={reason === overrideReason ? "brand" : "outline"}
                  aria-pressed={reason === overrideReason}
                  onClick={() => setOverrideReason(reason === overrideReason ? "" : reason)}
                >
                  {reason}
                </Button>
              ))}
            </div>
          </div>
        )}
        {error && (
          <p role="alert" className="text-destructive">
            {t(($) => $.decisions.actions.queue_error, { message: error })}
          </p>
        )}
        <DialogFooter>
          <Button variant="ghost" disabled={busy} onClick={onCancel}>
            {t(($) => $.decisions.actions.back)}
          </Button>
          <Button variant="brand" disabled={busy || missingNote} onClick={() => void submit()}>
            {t(($) => $.decisions.actions.confirm)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function IntentStatus({ card }: { card: AGDecisionCard }) {
  const { t } = useT("ag");
  const cancel = useCancelAGDecisionIntent();
  const intent = visibleAGDecisionIntent(card);
  if (!intent) return null;
  const action = card.actions.find((a) => a.id === intent.actionId)?.label ?? intent.actionId;
  switch (intent.status) {
    case "pending":
      return (
        <div role="status" className="flex items-center gap-2 text-muted-foreground">
          <span>{t(($) => $.decisions.actions.queued, { action })}</span>
          <Button size="xs" variant="ghost" disabled={cancel.isPending} onClick={() => cancel.mutate(intent.id)}>
            {t(($) => $.decisions.actions.cancel)}
          </Button>
        </div>
      );
    case "running":
      return (
        <p role="status" className="text-muted-foreground">
          {t(($) => $.decisions.actions.working, { action })}
        </p>
      );
    case "succeeded":
      return (
        <p role="status" className="break-words text-success">
          {intent.message || action}
        </p>
      );
    case "refused":
      return (
        <p role="alert" className="break-words text-warning">
          {t(($) => $.decisions.actions.not_done, { message: intent.message })}
        </p>
      );
    case "expired":
      return (
        <p role="alert" className="break-words text-warning">
          {intent.message || t(($) => $.decisions.actions.expired)}
        </p>
      );
    default:
      return (
        <p role="alert" className="break-words text-destructive">
          {t(($) => $.decisions.actions.failed, { message: intent.message })}
        </p>
      );
  }
}

function CardActions({ card }: { card: AGDecisionCard }) {
  const { t } = useT("ag");
  const create = useCreateAGDecisionIntent();
  const [open, setOpen] = useState<AGDecisionAction | null>(null);
  const [error, setError] = useState("");
  if (!agDecisionCardIsActionable(card) || card.actions.length === 0) return null;

  const send = (action: AGDecisionAction, note = "", overrideReason = "") =>
    create.mutateAsync({
      cardId: card.id,
      intent: { revision: card.revision, action_id: action.id, note, override_reason: overrideReason },
    });
  const reasonsFor = (action: AGDecisionAction) =>
    card.recommendedAction && action.id !== card.recommendedAction ? card.overrideReasons : [];
  const click = (action: AGDecisionAction) => {
    setError("");
    if (action.confirm || action.note || reasonsFor(action).length > 0) setOpen(action);
    else send(action).catch((e: unknown) => setError(errorText(e)));
  };

  return (
    <>
      <div className="flex flex-wrap gap-1.5">
        {card.actions.map((action) => (
          <Button
            key={action.id}
            size="sm"
            variant={action.primary ? "brand" : "outline"}
            disabled={create.isPending}
            onClick={() => click(action)}
          >
            {action.label}
          </Button>
        ))}
      </div>
      {error && (
        <p role="alert" className="break-words text-destructive">
          {t(($) => $.decisions.actions.queue_error, { message: error })}
        </p>
      )}
      {open && (
        <ActionDialog
          title={open.label}
          action={open}
          reasons={reasonsFor(open)}
          onCancel={() => setOpen(null)}
          onConfirm={async ({ note, overrideReason }) => {
            await send(open, note, overrideReason);
            setOpen(null);
          }}
        />
      )}
    </>
  );
}

function GroupBatchActions({ cards }: { cards: AGDecisionCard[] }) {
  const { t } = useT("ag");
  const create = useCreateAGDecisionIntent();
  const [open, setOpen] = useState<{ action: AGDecisionAction; cards: AGDecisionCard[] } | null>(null);
  const batches = agDecisionBatchActions(cards);
  if (batches.length === 0) return null;

  const run = async ({ action, cards: targets }: { action: AGDecisionAction; cards: AGDecisionCard[] }) => {
    const results = await Promise.allSettled(
      targets.map((card) =>
        create.mutateAsync({
          cardId: card.id,
          intent: { revision: card.revision, action_id: action.id, note: "", override_reason: "" },
        }),
      ),
    );
    const failed = results.filter((result) => result.status === "rejected").length;
    if (failed > 0) {
      throw new Error(t(($) => $.decisions.actions.batch_partial, { failed, total: targets.length }));
    }
    setOpen(null);
  };

  return (
    <>
      {batches.map((batch) => (
        <Button key={batch.action.id} size="xs" variant="outline" onClick={() => setOpen(batch)}>
          {t(($) => $.decisions.actions.batch_title, { action: batch.action.label, total: batch.cards.length })}
        </Button>
      ))}
      {open && (
        <ActionDialog
          title={t(($) => $.decisions.actions.batch_title, { action: open.action.label, total: open.cards.length })}
          action={open.action}
          reasons={[]}
          onCancel={() => setOpen(null)}
          onConfirm={() => run(open)}
        />
      )}
    </>
  );
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
        {card.waitingSince && (
          <span className="shrink-0 text-caption text-muted-foreground">
            {t(($) => $.decisions.waiting, { time: timeAgo(card.waitingSince as string) })}
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
      {card.bullets.length > 0 && (
        <ul className="list-disc pl-4 text-muted-foreground">
          {card.bullets.map((bullet, n) => (
            <li key={n} className="break-words">
              {bullet}
            </li>
          ))}
        </ul>
      )}
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
      <IntentStatus card={card} />
      <CardActions card={card} />
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
  const workerOffline = lastSyncAt !== null && isAGDecisionWorkerOffline(data?.workerSeenAt ?? null, now);

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
          <div className={cn(PAGE_GUTTER, "flex flex-col gap-4 pb-6")}>
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
                <div className="flex flex-wrap items-center gap-2">
                  {group.name && (
                    <h2 className="text-caption font-medium text-muted-foreground">
                      {group.name} ({group.cards.length})
                    </h2>
                  )}
                  <GroupBatchActions cards={group.cards} />
                </div>
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
