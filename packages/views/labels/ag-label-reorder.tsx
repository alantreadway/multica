"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "lucide-react";
import { toast } from "sonner";
import { useReorderLabels } from "@multica/core/labels";
import type { Label, LabelResourceType } from "@multica/core/types";
import { useT } from "../i18n";

/**
 * AG patch: drag to reorder labels in the label manager. `labels` must be a
 * stable reference while unchanged: `order` resyncs from it in an effect.
 */
export function useAgLabelReorder(
  labels: Label[],
  resourceType: LabelResourceType,
  query: string,
) {
  const { t } = useT("ag");
  // Local order so a drop reads as instant before the optimistic cache write
  // settles; resynced whenever the server list changes.
  const [order, setOrder] = useState(labels);
  useEffect(() => setOrder(labels), [labels]);
  const reorder = useReorderLabels();
  const saving = useRef(false);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  // A filtered list hides labels, so a drop there could not describe the whole
  // order. Reordering is only offered on the full list.
  const canReorder = !query.trim() && order.length > 1;
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id || saving.current) return;
    const from = order.findIndex((l) => l.id === active.id);
    const to = order.findIndex((l) => l.id === over.id);
    if (from < 0 || to < 0) return;
    const next = arrayMove(order, from, to);
    saving.current = true;
    setOrder(next);
    reorder.mutate(
      { resource_type: resourceType, label_ids: next.map((l) => l.id) },
      {
        onError: (error) => {
          setOrder(labels);
          toast.error(
            error instanceof Error
              ? error.message
              : t(($) => $.settings.labels.reorder_failed),
          );
        },
        onSettled: () => {
          saving.current = false;
        },
      },
    );
  };

  return { order, canReorder, sensors, onDragEnd };
}

/** The label list's container, made sortable. */
export function AgSortableLabelList({
  reorder,
  labels,
  className,
  children,
}: {
  reorder: ReturnType<typeof useAgLabelReorder>;
  labels: Label[];
  className: string;
  children: ReactNode;
}) {
  return (
    <DndContext
      sensors={reorder.sensors}
      collisionDetection={closestCenter}
      onDragEnd={reorder.onDragEnd}
    >
      <SortableContext
        items={labels.map((l) => l.id)}
        strategy={verticalListSortingStrategy}
      >
        <div className={className}>{children}</div>
      </SortableContext>
    </DndContext>
  );
}

/** One label row with its drag handle; `children` are the row's cells. */
export function AgSortableLabelRow({
  label,
  canReorder,
  children,
}: {
  label: Label;
  canReorder: boolean;
  children: ReactNode;
}) {
  const { t } = useT("ag");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: label.id,
    disabled: !canReorder,
  });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={`flex items-center gap-2 bg-card py-3 pl-2 pr-4 motion-reduce:transition-none! ${isDragging ? "relative z-10 shadow-[var(--surface-shadow)]" : ""}`}
    >
      {canReorder ? (
        <button
          type="button"
          aria-label={t(($) => $.settings.labels.actions.reorder, { name: label.name })}
          className="flex size-6 shrink-0 touch-none items-center justify-center rounded-md cursor-grab text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring active:cursor-grabbing [@media(pointer:coarse)]:size-11"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-4" />
        </button>
      ) : (
        <span className="size-6 shrink-0 [@media(pointer:coarse)]:size-11" />
      )}
      <div className="grid min-w-0 flex-1 gap-2 md:grid-cols-[minmax(11rem,1fr)_minmax(12rem,1.4fr)_6rem_7rem_2rem] md:items-center md:gap-4">
        {children}
      </div>
    </div>
  );
}
