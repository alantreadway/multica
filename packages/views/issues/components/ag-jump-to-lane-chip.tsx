import { ListTree } from "lucide-react";
import { useT } from "../../i18n";

/** Stops pointerdown so pressing the chip doesn't start the card drag. */
export function JumpToLaneChip({ onJump }: { onJump: () => void }) {
  const { t } = useT("ag");
  return (
    <div className="mt-1 flex justify-end">
      <button
        type="button"
        onPointerDown={(e) => e.stopPropagation()}
        onClick={onJump}
        className="inline-flex items-center gap-1 rounded-full bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <ListTree className="size-3" aria-hidden />
        {t(($) => $.issues.swimlane.jump_to_lane)}
      </button>
    </div>
  );
}
