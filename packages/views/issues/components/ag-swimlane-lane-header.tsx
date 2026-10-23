"use client";

/** AG patch: tree-ordered parent lanes. Lane header parts. */
import { useT } from "../../i18n";
import type { SwimlaneTreeInfo } from "./swimlane-tree";

/** `↳ PARENT` before a sub-lane's title; the tooltip holds the full path. */
export function AgLaneAncestorPath({ tree }: { tree?: SwimlaneTreeInfo }) {
  const ancestorPath = tree && tree.path.length > 1 ? tree.path : null;
  if (!ancestorPath) return null;
  const pathText = ancestorPath.map((p) => p.identifier).join(" > ");
  const parentIdentifier = ancestorPath[ancestorPath.length - 2]?.identifier ?? "";
  return (
    <span
      title={pathText}
      className="min-w-0 max-w-[45%] shrink truncate text-micro font-medium tabular-nums text-muted-foreground"
    >
      {`↳ ${parentIdentifier}`}
    </span>
  );
}

/** How many sub-lanes a collapsed lane hides. */
export function AgSubLanesHidden({
  tree,
  isCollapsed,
}: {
  tree?: SwimlaneTreeInfo;
  isCollapsed: boolean;
}) {
  const { t } = useT("ag");
  const subLanes = tree?.descendantCount ?? 0;
  if (!isCollapsed || subLanes === 0) return null;
  return (
    <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
      {t(($) => $.issues.swimlane.sub_lanes_hidden, { count: subLanes })}
    </span>
  );
}
