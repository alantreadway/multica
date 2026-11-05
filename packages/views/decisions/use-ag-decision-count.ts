import { useQuery } from "@tanstack/react-query";
import { agDecisionCountOptions } from "@multica/core/decisions/ag-decisions-queries";

/** Cards waiting on Alan, for the sidebar badge. Zero on a stock backend, which answers 404. */
export function useAGDecisionCount(wsId: string | undefined | null): number {
  const { data } = useQuery({ ...agDecisionCountOptions(wsId ?? ""), enabled: !!wsId });
  return data ?? 0;
}
