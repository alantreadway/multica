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
import { GripVertical } from "@multica/ui/icons";
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

/** Makes the rows inside it sortable; renders no element of its own. */
export function AgSortableLabelList({
  reorder,
  labels,
  children,
}: {
  reorder: ReturnType<typeof useAgLabelReorder>;
  labels: Label[];
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
        {children}
      </SortableContext>
    </DndContext>
  );
}

/**
 * One sortable label row. `children` receives the drag handle, which goes in
 * the row's name cell, and returns the catalog row.
 */
export function AgSortableLabelRow({
  label,
  canReorder,
  children,
}: {
  label: Label;
  canReorder: boolean;
  children: (handle: ReactNode) => ReactNode;
}) {
  const { t } = useT("ag");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: label.id,
    disabled: !canReorder,
  });
  const handle = canReorder ? (
    <button
      type="button"
      aria-label={t(($) => $.settings.labels.actions.reorder, { name: label.name })}
      className="-ml-1.5 flex size-6 shrink-0 touch-none items-center justify-center rounded-md cursor-grab text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring active:cursor-grabbing [@media(pointer:coarse)]:size-11"
      {...attributes}
      {...listeners}
    >
      <GripVertical className="size-4" />
    </button>
  ) : null;
  return (
    <div
      ref={setNodeRef}
      data-ag-label-row
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={`bg-card motion-reduce:transition-none! ${isDragging ? "relative z-10 shadow-[var(--surface-shadow)]" : ""}`}
    >
      {children(handle)}
    </div>
  );
}
