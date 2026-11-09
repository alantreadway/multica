import { cn } from "@multica/ui/lib/utils";

// AG patch: Overview dashboard. Who holds a piece of work, as colour. Every
// colour sits next to a text label; nobody is a dashed outline, not a hue.

export type OwnerTone = "you" | "agent" | "colleague" | "nobody" | "other";

/** Display order for anything grouped by holder. */
export const OWNER_ORDER = ["alan", "author", "colleague", "agent", "nobody"];

export function ownerTone(waitingOn: string | null | undefined): OwnerTone {
  switch (waitingOn) {
    case "alan":
      return "you";
    case "agent":
      return "agent";
    // The author of a colleague's PR is a colleague too.
    case "author":
    case "colleague":
      return "colleague";
    case "nobody":
      return "nobody";
    default:
      return "other";
  }
}

const FILL: Record<OwnerTone, string> = {
  you: "bg-owner-you",
  agent: "bg-owner-agent",
  colleague: "bg-owner-colleague",
  nobody: "border border-dashed border-muted-foreground",
  other: "bg-muted-foreground",
};

const OUTLINE: Record<OwnerTone, string> = {
  you: "border-owner-you",
  agent: "border-owner-agent",
  colleague: "border-owner-colleague",
  nobody: "border-dashed border-muted-foreground",
  other: "border-border",
};

export function ownerFill(tone: OwnerTone) {
  return FILL[tone];
}

export function ownerOutline(tone: OwnerTone) {
  return OUTLINE[tone];
}

export function OwnerDot({ tone, className }: { tone: OwnerTone; className?: string }) {
  return <span aria-hidden className={cn("inline-block size-2 shrink-0 rounded-full", FILL[tone], className)} />;
}

export function sortByOwner<T>(entries: [string, T][]): [string, T][] {
  const rank = (holder: string) => OWNER_ORDER.indexOf(holder) + 1 || 99;
  return [...entries].sort(([a], [b]) => rank(a) - rank(b));
}
