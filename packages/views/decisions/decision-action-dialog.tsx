"use client";

import { useState } from "react";
import {
  agDecisionBatchActions,
  type AGDecisionAction,
  type AGDecisionCard,
} from "@multica/core/decisions/ag-decisions";
import { useCreateAGDecisionIntent } from "@multica/core/decisions/ag-decisions-queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";

export const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** The second tap: why it needs one, the text it needs, or why the recommendation was not taken. */
export function ActionDialog({
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
          <Button variant="default" disabled={busy || missingNote} onClick={() => void submit()}>
            {t(($) => $.decisions.actions.confirm)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function GroupBatchActions({ cards }: { cards: AGDecisionCard[] }) {
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
