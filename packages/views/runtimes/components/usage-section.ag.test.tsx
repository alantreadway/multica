// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, fireEvent } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

// The viewer's tz (Viewing layer) drives the day boundaries.
const VIEWER_TZ = "Asia/Tokyo";

const runtimeUsageByAgentOptions = vi.hoisted(() =>
  vi.fn((..._args: unknown[]) => ({ kind: "by-agent" as const })),
);

vi.mock("../../common/use-viewing-timezone", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../common/use-viewing-timezone")>()),
  useViewingTimezone: () => VIEWER_TZ,
}));

vi.mock("@multica/core/runtimes/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes/queries")>()),
  runtimeUsageOptions: () => ({ kind: "usage" as const }),
  runtimeUsageByAgentOptions,
}));

vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  agentListOptions: () => ({ kind: "agents" as const }),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

// Each test sets its own usage rows.
const usageOverride = vi.hoisted(() => ({ rows: [] as unknown[] }));

// useQuery is mocked so the component renders synchronously with canned
// data — the `kind` tag on each query-options object routes the response.
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (opts: { kind?: string }) => ({
    data: opts?.kind === "usage" ? usageOverride.rows : [],
    isLoading: false,
  }),
}));

// Charts are recharts-heavy; stub them.
vi.mock("./charts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./charts")>()),
  DailyCostChart: () => <div data-testid="daily-cost-chart" />,
  DailyTokensChart: () => <div data-testid="daily-tokens-chart" />,
  WeeklyCostChart: () => <div data-testid="weekly-cost-chart" />,
  WeeklyTokensChart: () => <div data-testid="weekly-tokens-chart" />,
  ActivityHeatmap: () => <div data-testid="heatmap" />,
}));

import { UsageSection } from "./usage-section";
import { addDaysIso, todayIso } from "../utils";

const RUNTIME: AgentRuntime = {
  id: "r-1",
  workspace_id: "ws-1",
  daemon_id: null,
  name: "test-runtime",
  runtime_mode: "cloud",
  provider: "claude",
  launch_header: "",
  status: "online",
  device_info: "",
  metadata: {},
  owner_id: null,
  visibility: "private",
  last_seen_at: null,
  created_at: "2026-05-01T00:00:00Z",
  updated_at: "2026-05-01T00:00:00Z",
};

describe("AG patch: 1d and Yesterday periods", () => {
  const rowOn = (daysAgo: number, input: number) => ({
    runtime_id: "r-1",
    date: addDaysIso(todayIso(VIEWER_TZ), -daysAgo),
    provider: "anthropic",
    model: "claude-sonnet-4-6",
    input_tokens: input,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
  });

  beforeEach(() => {
    usageOverride.rows = [rowOn(0, 1_000), rowOn(1, 4_000), rowOn(5, 9_000)];
    runtimeUsageByAgentOptions.mockClear();
  });

  afterEach(() => {
    usageOverride.rows = [];
  });

  it("AG patch: offers the same ranges as Analytics plus Yesterday", () => {
    renderWithI18n(<UsageSection runtime={RUNTIME} />);
    for (const name of ["1d", "Yesterday", "7d", "30d", "90d"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  it("AG patch: 1d counts today only", () => {
    renderWithI18n(<UsageSection runtime={RUNTIME} />);
    fireEvent.click(screen.getByRole("button", { name: "1d" }));

    expect(screen.getByText("Tokens · 1D")).toBeInTheDocument();
    const flows = Array.from(document.querySelectorAll("number-flow-react"));
    expect(flows.at(-1)).toHaveAttribute("aria-label", "1K");
  });

  it("AG patch: Yesterday counts yesterday only and says so", () => {
    renderWithI18n(<UsageSection runtime={RUNTIME} />);
    fireEvent.click(screen.getByRole("button", { name: "Yesterday" }));

    expect(screen.getByText("Cost · Yesterday")).toBeInTheDocument();
    expect(screen.getByText("Tokens · Yesterday")).toBeInTheDocument();
    const flows = Array.from(document.querySelectorAll("number-flow-react"));
    expect(flows.at(-1)).toHaveAttribute("aria-label", "4K");
  });

  it("AG patch: points to Analytics for per-agent cost on a single day", () => {
    renderWithI18n(<UsageSection runtime={RUNTIME} />);
    expect(
      screen.queryByText("Per-agent cost for a single day is on the Analytics page."),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Yesterday" }));

    expect(
      screen.getByText("Per-agent cost for a single day is on the Analytics page."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/agents? on this runtime/)).not.toBeInTheDocument();
  });

  it("AG patch: switching to Weekly from Yesterday falls back to the weekly default", () => {
    renderWithI18n(<UsageSection runtime={RUNTIME} />);
    fireEvent.click(screen.getByRole("button", { name: "Yesterday" }));
    fireEvent.click(screen.getByRole("button", { name: "Weekly" }));

    expect(screen.queryByRole("button", { name: "Yesterday" })).not.toBeInTheDocument();
    expect(screen.getByText("Cost · 90D")).toBeInTheDocument();
  });
});
