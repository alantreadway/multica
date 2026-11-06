"use client";

import { Fragment } from "react";
import { ArrowLeft, ArrowUpRight, Sparkles } from "lucide-react";
import {
  agDecisionCardIsActionable,
  agDecisionKeyedActions,
  isAGDecisionOption,
  splitAGDecisionLine,
  visibleAGDecisionIntent,
  type AGDecisionAction,
  type AGDecisionCard,
  type AGDecisionChipTone,
} from "@multica/core/decisions/ag-decisions";
import { useCancelAGDecisionIntent } from "@multica/core/decisions/ag-decisions-queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Kbd } from "@multica/ui/components/ui/kbd";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { cn } from "@multica/ui/lib/utils";
import { useTimeAgo } from "../i18n/use-time-ago";
import { useT } from "../i18n";
import { useIssuePeekActions } from "../issues/surface/peek-context";

const CHIP_TONE: Record<AGDecisionChipTone, string> = {
  neutral: "",
  good: "text-success",
  warn: "text-warning",
  bad: "text-destructive",
};

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

export function DecisionDetail({
  card,
  busy,
  onAction,
  onBack,
}: {
  card: AGDecisionCard;
  busy: { cardId: string; actionId: string } | null;
  onAction: (action: AGDecisionAction) => void;
  /** Compact layouts show the detail instead of the list, so they need a way back. */
  onBack?: () => void;
}) {
  const { t } = useT("ag");
  const timeAgo = useTimeAgo();
  const peek = useIssuePeekActions();
  const actionable = agDecisionCardIsActionable(card);
  const keyed = agDecisionKeyedActions(card);
  const options = keyed.filter(isAGDecisionOption);
  const others = keyed.filter((action) => !isAGDecisionOption(action));
  const shortcut = (action: AGDecisionAction) => keyed.indexOf(action) + 1;
  const isRecommended = (action: AGDecisionAction) =>
    card.recommendedAction ? action.id === card.recommendedAction : action.primary;
  const lines = card.lines.map((line) => ({ line, field: splitAGDecisionLine(line) }));
  const fields = lines.flatMap(({ field }) => (field ? [field] : []));
  const prose = lines.filter(({ field }) => !field).map(({ line }) => line);
  // Said in words only when no button carries it.
  const recommendationText =
    card.recommended && !card.actions.some((action) => action.id === card.recommendedAction) ? card.recommended : "";
  const sending = (action: AGDecisionAction) => busy?.cardId === card.id && busy.actionId === action.id;
  const keycap = (action: AGDecisionAction, className?: string) =>
    shortcut(action) <= 9 && (
      <Kbd aria-hidden className={className}>
        {shortcut(action)}
      </Kbd>
    );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-6 py-5">
          {onBack && (
            <Button variant="ghost" size="sm" className="self-start" onClick={onBack}>
              <ArrowLeft />
              {t(($) => $.decisions.actions.back)}
            </Button>
          )}
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-caption text-muted-foreground">
            {card.issueId && card.issueRef ? (
              <Button
                variant="link"
                size="xs"
                className="h-auto p-0 font-mono text-caption"
                onClick={() => peek?.open(card.issueId as string)}
              >
                {card.issueRef}
              </Button>
            ) : (
              card.issueRef && <span className="font-mono">{card.issueRef}</span>
            )}
            {card.group && <span>· {card.group}</span>}
            {card.waitingSince && (
              <span>· {t(($) => $.decisions.waiting, { time: timeAgo(card.waitingSince as string) })}</span>
            )}
            <span className="flex-1" />
            {card.links.map((link) => (
              <a
                key={link.url}
                href={link.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-0.5 rounded-sm bg-muted px-1.5 text-caption text-muted-foreground hover:text-foreground"
              >
                {link.label}
                <ArrowUpRight className="size-3" />
              </a>
            ))}
          </div>
          <h2 className="text-title-sm font-medium break-words">{card.title || card.issueRef || card.cardType}</h2>
          {card.chips.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {card.chips.map((chip, n) => (
                <Badge key={`${chip.label}-${n}`} variant="outline" className={CHIP_TONE[chip.tone]}>
                  {chip.label}
                </Badge>
              ))}
            </div>
          )}
          <IntentStatus card={card} />
          {fields.length > 0 && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
              {fields.map((field, n) => (
                <Fragment key={n}>
                  <dt className="pt-0.5 text-caption tracking-wide text-muted-foreground uppercase">{field.label}</dt>
                  <dd className="break-words">{field.value}</dd>
                </Fragment>
              ))}
            </dl>
          )}
          {prose.map((line, n) => (
            <p key={n} className="break-words">
              {line}
            </p>
          ))}
          {options.length > 0 && (
            <div className="flex flex-col gap-2">
              {options.map((option) => (
                <button
                  key={option.id}
                  type="button"
                  disabled={!actionable || busy !== null}
                  aria-keyshortcuts={String(shortcut(option))}
                  onClick={() => onAction(option)}
                  className={cn(
                    "flex items-start gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors hover:bg-surface-hover focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none disabled:pointer-events-none disabled:opacity-60",
                    isRecommended(option) && "border-brand/45 bg-brand/5",
                  )}
                >
                  {keycap(option, "mt-0.5")}
                  <span className="min-w-0 flex-1 break-words">{option.label}</span>
                  {sending(option) ? (
                    <Spinner className="mt-0.5 size-3.5" />
                  ) : (
                    isRecommended(option) && <Sparkles aria-hidden className="mt-0.5 size-3.5 text-brand" />
                  )}
                </button>
              ))}
            </div>
          )}
          {card.bullets.length > 0 && (
            <ul className="list-disc pl-4 text-muted-foreground">
              {card.bullets.map((bullet, n) => (
                <li key={n} className="break-words">
                  {bullet}
                </li>
              ))}
            </ul>
          )}
          {card.blockers.length > 0 && (
            <p className="break-words text-caption text-muted-foreground">
              {t(($) => $.decisions.blocked, { reason: card.blockers.join("; ") })}
            </p>
          )}
        </div>
      </div>
      {((actionable && others.length > 0) || recommendationText) && (
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t px-6 py-3">
          {recommendationText && (
            <span className="mr-auto text-caption text-muted-foreground">
              {t(($) => $.decisions.recommended, { action: recommendationText })}
            </span>
          )}
          {actionable &&
            others.map((action) => (
              <Button
                key={action.id}
                size="sm"
                variant={isRecommended(action) ? "default" : "outline"}
                disabled={busy !== null}
                aria-busy={sending(action) || undefined}
                aria-keyshortcuts={String(shortcut(action))}
                onClick={() => onAction(action)}
              >
                {sending(action) ? <Spinner /> : isRecommended(action) && <Sparkles aria-hidden />}
                {action.label}
                {keycap(action, isRecommended(action) ? "bg-primary-foreground/15 text-primary-foreground" : undefined)}
              </Button>
            ))}
        </div>
      )}
    </div>
  );
}
