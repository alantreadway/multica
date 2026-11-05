"use client";

import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  agDecisionPlace,
  visibleAGDecisionIntent,
  type AGDecisionAction,
  type AGDecisionCard,
  type AGDecisionPlace,
} from "@multica/core/decisions/ag-decisions";
import {
  agDecisionListOptions,
  useCancelAGDecisionIntent,
  useCreateAGDecisionIntent,
} from "@multica/core/decisions/ag-decisions-queries";
import { useT } from "../i18n";
import { ActionDialog, errorText } from "./decision-action-dialog";

/** A click the server has accepted while the list still shows the card as it was: the intent it showed then, and what was clicked. */
interface Departure {
  intentBefore: string | null;
  action: string;
}

const TOAST_MS = 6000;

/**
 * Clicks on the Decisions page: the second-tap dialog, sending, and undo. A sent card leaves the queue as soon as the
 * server has the intent, before the list refetch shows it, so the next card is on screen without waiting.
 */
export function useDecisionActions({ wsId, onSent }: { wsId: string; onSent: (card: AGDecisionCard) => void }) {
  const { t } = useT("ag");
  const qc = useQueryClient();
  const create = useCreateAGDecisionIntent();
  const cancel = useCancelAGDecisionIntent();
  const [departures, setDepartures] = useState<ReadonlyMap<string, Departure>>(() => new Map());
  const [busy, setBusy] = useState<{ cardId: string; actionId: string } | null>(null);
  const [dialog, setDialog] = useState<{ card: AGDecisionCard; action: AGDecisionAction } | null>(null);
  const lastSent = useRef<string | null>(null);

  /** The departure still ahead of the list: once the card shows a newer intent, that intent says where it is. */
  const departureOf = (card: AGDecisionCard) => {
    const departure = departures.get(card.id);
    return departure && departure.intentBefore === (card.latestIntent?.id ?? null) ? departure : null;
  };

  const placeOf = (card: AGDecisionCard): AGDecisionPlace => {
    const place = agDecisionPlace(card);
    return place === "queue" && departureOf(card) ? "activity" : place;
  };

  const undo = async (cardId: string) => {
    const pendingIntent = (cards: AGDecisionCard[] | undefined) => {
      const card = cards?.find((c) => c.id === cardId);
      const intent = card ? visibleAGDecisionIntent(card) : null;
      return intent?.status === "pending" ? intent : null;
    };
    try {
      // Straight after a send the cached list predates the intent, so read it fresh.
      const intent =
        pendingIntent(qc.getQueryData(agDecisionListOptions(wsId).queryKey)?.cards) ??
        pendingIntent((await qc.fetchQuery({ ...agDecisionListOptions(wsId), staleTime: 0 })).cards);
      if (!intent) {
        toast(t(($) => $.decisions.actions.too_late));
        return;
      }
      await cancel.mutateAsync(intent.id);
    } catch (e) {
      toast.error(t(($) => $.decisions.actions.queue_error, { message: errorText(e) }));
    }
  };

  const send = async (card: AGDecisionCard, action: AGDecisionAction, note = "", overrideReason = "") => {
    setBusy({ cardId: card.id, actionId: action.id });
    try {
      await create.mutateAsync({
        cardId: card.id,
        intent: { revision: card.revision, action_id: action.id, note, override_reason: overrideReason },
      });
    } finally {
      setBusy(null);
    }
    setDepartures((prev) =>
      new Map(prev).set(card.id, { intentBefore: card.latestIntent?.id ?? null, action: action.label }),
    );
    lastSent.current = card.id;
    onSent(card);
    toast.success(
      t(($) => $.decisions.actions.queued_toast, { action: action.label, ref: card.issueRef || card.title }),
      {
        duration: TOAST_MS,
        action: { label: t(($) => $.decisions.actions.undo), onClick: () => void undo(card.id) },
      },
    );
  };

  const reasonsFor = (card: AGDecisionCard, action: AGDecisionAction) =>
    card.recommendedAction && action.id !== card.recommendedAction ? card.overrideReasons : [];

  const request = (card: AGDecisionCard, action: AGDecisionAction) => {
    if (busy) return;
    if (action.confirm || action.note || reasonsFor(card, action).length > 0) {
      setDialog({ card, action });
      return;
    }
    send(card, action).catch((e: unknown) =>
      toast.error(t(($) => $.decisions.actions.queue_error, { message: errorText(e) })),
    );
  };

  const dialogElement = dialog && (
    <ActionDialog
      title={dialog.action.label}
      action={dialog.action}
      reasons={reasonsFor(dialog.card, dialog.action)}
      onCancel={() => setDialog(null)}
      onConfirm={async ({ note, overrideReason }) => {
        await send(dialog.card, dialog.action, note, overrideReason);
        setDialog(null);
      }}
    />
  );

  return {
    placeOf,
    /** What a card that has left the queue was sent to do, while the list does not show its intent yet. */
    departingAction: (card: AGDecisionCard) => departureOf(card)?.action ?? null,
    request,
    undo: (cardId: string) => void undo(cardId),
    undoLast: () => {
      if (lastSent.current) void undo(lastSent.current);
    },
    busy,
    dialog: dialogElement,
    dialogOpen: dialog !== null,
  };
}
