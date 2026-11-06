import { useT } from "../i18n";
import { useLocale } from "../i18n";

// AG patch: Overview dashboard. Short labels shared by the page's sections.

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** "18m", "27h", "3d": how long something has been going, in the reader's language. */
export function useAge() {
  const { t } = useT("ag");
  return (since: string, now: number): string => {
    const ms = Math.max(0, now - new Date(since).getTime());
    if (ms < HOUR) return t(($) => $.overview.age.minutes, { count: Math.floor(ms / MINUTE) });
    if (ms < 2 * DAY) return t(($) => $.overview.age.hours, { count: Math.floor(ms / HOUR) });
    return t(($) => $.overview.age.days, { count: Math.floor(ms / DAY) });
  };
}

export function useStatusLabel() {
  const { t } = useT("ag");
  return (status: string): string => {
    switch (status) {
      case "backlog":
        return t(($) => $.overview.status.backlog);
      case "todo":
        return t(($) => $.overview.status.todo);
      case "in_progress":
        return t(($) => $.overview.status.in_progress);
      case "in_review":
        return t(($) => $.overview.status.in_review);
      case "handoff":
        return t(($) => $.overview.status.handoff);
      case "blocked":
        return t(($) => $.overview.status.blocked);
      case "done":
        return t(($) => $.overview.status.done);
      case "cancelled":
        return t(($) => $.overview.status.cancelled);
      default:
        // A workspace can add statuses.
        return status.replace(/_/g, " ");
    }
  };
}

export function useWaitingOnLabel() {
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
        return waitingOn;
    }
  };
}

/** "Mon 9 Nov – Wed 11 Nov", in the reader's locale. */
export function useDateRange() {
  const locale = useLocale();
  return (from: string, to: string): string => {
    const format = new Intl.DateTimeFormat(locale, { weekday: "short", day: "numeric", month: "short" });
    const a = new Date(from);
    const b = new Date(to);
    if (format.format(a) === format.format(b)) return format.format(a);
    return format.formatRange ? format.formatRange(a, b) : `${format.format(a)} – ${format.format(b)}`;
  };
}
