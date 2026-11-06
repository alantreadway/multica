"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agOverviewOptions,
  useCreateAGOverviewPin,
  useDeleteAGOverviewPin,
} from "@multica/core/overview/ag-overview-queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useTimeAgo } from "../i18n/use-time-ago";
import { IssuePeekHost } from "../issues/components/issue-peek";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";
import { OverviewNow } from "./overview-now";
import { OverviewTree } from "./overview-tree";

// AG patch: Overview dashboard. Read-only: what runs now, what waits on Alan,
// and how far each pinned work tree has to go. Pinning is the only write.

const CLOCK_TICK_MS = 30_000;

function PinForm({ wsId }: { wsId: string }) {
  const { t } = useT("ag");
  const [ref, setRef] = useState("");
  const pin = useCreateAGOverviewPin(wsId);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    const value = ref.trim();
    if (!value || pin.isPending) return;
    pin.mutate(value, { onSuccess: () => setRef("") });
  };
  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
      <Input
        value={ref}
        onChange={(event) => {
          setRef(event.target.value);
          if (pin.isError) pin.reset();
        }}
        placeholder={t(($) => $.overview.pin_placeholder)}
        aria-label={t(($) => $.overview.pin_placeholder)}
        aria-invalid={pin.isError || undefined}
        className="h-7 w-56"
      />
      <Button type="submit" size="sm" variant="outline" disabled={!ref.trim() || pin.isPending} aria-busy={pin.isPending}>
        {t(($) => $.overview.pin)}
      </Button>
      {pin.isError && (
        <span role="alert" className="text-caption text-destructive">
          {t(($) => $.overview.pin_error)}
        </span>
      )}
    </form>
  );
}

export function OverviewPage() {
  const { t } = useT("ag");
  const { t: tLayout } = useT("layout");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const { data, isPending, isError } = useQuery(agOverviewOptions(wsId));
  const unpin = useDeleteAGOverviewPin(wsId);
  const [clock, setClock] = useState(() => Date.now());
  const runningIssueIds = useMemo(
    () => new Set(data?.now.running.flatMap((task) => (task.issue ? [task.issue.id] : [])) ?? []),
    [data],
  );

  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), CLOCK_TICK_MS);
    return () => clearInterval(timer);
  }, []);

  return (
    <IssuePeekHost>
      <div className="flex h-full min-h-0 flex-col">
        <PageHeader>
          <h1 className="min-w-0 flex-1 truncate font-medium">{tLayout(($) => $.nav.overview)}</h1>
          {data?.generatedAt && (
            <span className="shrink-0 text-caption text-muted-foreground">
              {t(($) => $.overview.updated, { time: timeAgo(data.generatedAt) })}
            </span>
          )}
        </PageHeader>
        <div className={cn(PAGE_GUTTER, "flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto py-4")}>
          {isPending ? (
            <Skeleton className="h-32 w-full" />
          ) : isError || !data ? (
            <p role="alert" className="text-muted-foreground">
              {t(($) => $.overview.load_error)}
            </p>
          ) : (
            <>
              <section aria-labelledby="ag-overview-now" className="flex flex-col gap-2">
                <h2 id="ag-overview-now" className="text-label font-medium text-muted-foreground">
                  {t(($) => $.overview.now)}
                </h2>
                <OverviewNow now={data.now} clock={clock} />
              </section>
              <section aria-labelledby="ag-overview-trees" className="flex flex-col gap-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h2 id="ag-overview-trees" className="text-label font-medium text-muted-foreground">
                    {t(($) => $.overview.trees)}
                  </h2>
                  <PinForm wsId={wsId} />
                </div>
                {data.trees.length === 0 && (
                  <p className="text-caption text-muted-foreground">{t(($) => $.overview.no_pins)}</p>
                )}
                {data.trees.map((tree) => (
                  <OverviewTree
                    key={tree.pin.id}
                    tree={tree}
                    clock={clock}
                    runningIssueIds={runningIssueIds}
                    unpinning={unpin.isPending && unpin.variables === tree.pin.id}
                    onUnpin={() => unpin.mutate(tree.pin.id)}
                  />
                ))}
              </section>
            </>
          )}
        </div>
      </div>
    </IssuePeekHost>
  );
}
