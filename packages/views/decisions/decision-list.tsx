"use client";

import type { ReactNode } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Check, Clock } from "@multica/ui/icons";
import {
  visibleAGDecisionIntent,
  type AGDecisionCard,
  type AGDecisionGroup,
} from "@multica/core/decisions/ag-decisions";
import { Button } from "@multica/ui/components/ui/button";
import { Kbd } from "@multica/ui/components/ui/kbd";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { cn } from "@multica/ui/lib/utils";
import { UI_EASE_OUT, UI_MOTION_DURATION } from "@multica/ui/lib/motion";
import { useTimeAgo } from "../i18n/use-time-ago";
import { useT } from "../i18n";
import { GroupBatchActions } from "./decision-action-dialog";

const TONE_DOT = { warn: "bg-warning", bad: "bg-destructive" } as const;

const heading = (card: AGDecisionCard) => card.title || card.issueRef || card.cardType;

/** A row that fades in where it lands and collapses out of the list it leaves. */
function AnimatedRow({ children }: { children: ReactNode }) {
  const reduced = useReducedMotion();
  const duration = reduced ? 0 : UI_MOTION_DURATION.standard;
  return (
    <motion.li
      layout={reduced ? false : "position"}
      initial={{ opacity: 0 }}
      animate={{ opacity: 1, height: "auto" }}
      exit={{ opacity: 0, height: 0, x: reduced ? 0 : -16 }}
      transition={{ duration, ease: UI_EASE_OUT }}
      className="overflow-hidden"
    >
      {children}
    </motion.li>
  );
}

function QueueRow({
  card,
  selected,
  retry,
  onSelect,
}: {
  card: AGDecisionCard;
  selected: boolean;
  retry: boolean;
  onSelect: () => void;
}) {
  const { t } = useT("ag");
  const timeAgo = useTimeAgo();
  const status = retry ? visibleAGDecisionIntent(card)?.status : undefined;
  const statusLabel =
    status === "refused"
      ? t(($) => $.decisions.status.not_done)
      : status === "expired"
        ? t(($) => $.decisions.status.expired)
        : status
          ? t(($) => $.decisions.status.failed)
          : null;
  const dots = card.chips.filter((chip) => chip.tone === "warn" || chip.tone === "bad");

  return (
    <button
      type="button"
      aria-current={selected || undefined}
      onClick={onSelect}
      className={cn(
        "flex w-full min-w-0 flex-col gap-0.5 rounded-md border-l-2 border-transparent px-2.5 py-2 text-left transition-colors hover:bg-surface-hover",
        retry && "border-warning bg-warning/5",
        selected && "border-brand bg-surface-selected hover:bg-surface-selected",
      )}
    >
      <span className="flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
        {card.issueRef && <span className="font-mono">{card.issueRef}</span>}
        {dots.map((chip, n) => (
          <span
            key={`${chip.label}-${n}`}
            title={chip.label}
            className={cn("size-1.5 shrink-0 rounded-full", TONE_DOT[chip.tone as keyof typeof TONE_DOT])}
          />
        ))}
        <span className="flex-1" />
        {statusLabel ? (
          <span className="shrink-0 text-warning">{statusLabel}</span>
        ) : (
          card.waitingSince && <span className="shrink-0">{timeAgo(card.waitingSince)}</span>
        )}
      </span>
      <span className="truncate text-body">{heading(card)}</span>
    </button>
  );
}

function ActivityRow({
  card,
  departingAction,
  selected,
  onSelect,
  onUndo,
}: {
  card: AGDecisionCard;
  departingAction: string | null;
  selected: boolean;
  onSelect: () => void;
  onUndo: () => void;
}) {
  const { t } = useT("ag");
  const intent = departingAction ? null : visibleAGDecisionIntent(card);
  const action = departingAction ?? card.actions.find((a) => a.id === intent?.actionId)?.label ?? intent?.actionId ?? "";
  const pending = departingAction !== null || intent?.status === "pending";
  const running = intent?.status === "running";

  return (
    <div className={cn("flex min-w-0 items-center gap-1 rounded-md", selected && "bg-surface-selected")}>
      <button
        type="button"
        aria-current={selected || undefined}
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-caption hover:bg-surface-hover"
      >
        <span className="flex size-3.5 shrink-0 items-center justify-center">
          {pending ? (
            <Clock className="size-3.5 text-brand" />
          ) : running ? (
            <Spinner className="size-3" />
          ) : (
            <Check className="size-3.5 text-success" />
          )}
        </span>
        {card.issueRef && <span className="shrink-0 font-mono text-muted-foreground">{card.issueRef}</span>}
        <span className={cn("truncate", !pending && "text-muted-foreground")}>
          {pending
            ? t(($) => $.decisions.actions.queued, { action })
            : running
              ? t(($) => $.decisions.actions.working, { action })
              : intent?.message || action}
        </span>
      </button>
      {pending && (
        <Button size="xs" variant="ghost" className="shrink-0" onClick={onUndo}>
          {t(($) => $.decisions.actions.undo)}
        </Button>
      )}
    </div>
  );
}

function SectionHeading({ children, tone }: { children: ReactNode; tone?: "warn" }) {
  return (
    <h2
      className={cn(
        "flex min-w-0 flex-wrap items-center gap-2 px-2.5 pt-3 pb-1 text-micro font-medium tracking-wide text-muted-foreground uppercase",
        tone === "warn" && "text-warning",
      )}
    >
      {children}
    </h2>
  );
}

export function DecisionList({
  retry,
  groups,
  activity,
  selectedId,
  onSelect,
  departingAction,
  onUndo,
}: {
  retry: AGDecisionCard[];
  groups: AGDecisionGroup[];
  activity: AGDecisionCard[];
  selectedId: string | null;
  onSelect: (cardId: string) => void;
  departingAction: (card: AGDecisionCard) => string | null;
  onUndo: (cardId: string) => void;
}) {
  const { t } = useT("ag");

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {retry.length > 0 && (
          <section aria-label={t(($) => $.decisions.needs_retry)}>
            <SectionHeading tone="warn">
              {t(($) => $.decisions.needs_retry)} · {retry.length}
            </SectionHeading>
            <ul>
              <AnimatePresence initial={false}>
                {retry.map((card) => (
                  <AnimatedRow key={card.id}>
                    <QueueRow card={card} retry selected={card.id === selectedId} onSelect={() => onSelect(card.id)} />
                  </AnimatedRow>
                ))}
              </AnimatePresence>
            </ul>
          </section>
        )}
        {groups.map((group) => (
          <section key={group.name} aria-label={group.name || undefined}>
            <SectionHeading>
              {group.name && (
                <span>
                  {group.name} · {group.cards.length}
                </span>
              )}
              <GroupBatchActions cards={group.cards} />
            </SectionHeading>
            <ul>
              <AnimatePresence initial={false}>
                {group.cards.map((card) => (
                  <AnimatedRow key={card.id}>
                    <QueueRow
                      card={card}
                      retry={false}
                      selected={card.id === selectedId}
                      onSelect={() => onSelect(card.id)}
                    />
                  </AnimatedRow>
                ))}
              </AnimatePresence>
            </ul>
          </section>
        ))}
      </div>
      {activity.length > 0 && (
        <section aria-label={t(($) => $.decisions.activity)} className="max-h-[40%] shrink-0 overflow-y-auto border-t px-2 pb-1">
          <SectionHeading>{t(($) => $.decisions.activity)}</SectionHeading>
          <ul>
            <AnimatePresence initial={false}>
              {activity.map((card) => (
                <AnimatedRow key={card.id}>
                  <ActivityRow
                    card={card}
                    departingAction={departingAction(card)}
                    selected={card.id === selectedId}
                    onSelect={() => onSelect(card.id)}
                    onUndo={() => onUndo(card.id)}
                  />
                </AnimatedRow>
              ))}
            </AnimatePresence>
          </ul>
        </section>
      )}
      <div className="flex shrink-0 flex-wrap items-center gap-1 border-t px-4 py-2 text-caption text-muted-foreground">
        <Kbd>J</Kbd>
        <Kbd>K</Kbd>
        <span className="mr-2">{t(($) => $.decisions.keys.move)}</span>
        <Kbd>1</Kbd>
        <span>–</span>
        <Kbd>9</Kbd>
        <span className="mr-2">{t(($) => $.decisions.keys.act)}</span>
        <Kbd>Z</Kbd>
        <span>{t(($) => $.decisions.keys.undo)}</span>
      </div>
    </div>
  );
}
