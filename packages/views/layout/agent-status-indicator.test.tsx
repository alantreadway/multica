// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { SidebarProvider } from "@multica/ui/components/ui/sidebar";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { renderWithI18n } from "../test/i18n";

type Presence = { runningCount: number; queuedCount: number };

const { presence, runtimeFetch } = vi.hoisted(() => ({
  runtimeFetch: vi.fn(() => new Promise(() => {})),
  presence: {
    current: { byAgent: new Map(), loading: false } as {
      byAgent: Map<string, { runningCount: number; queuedCount: number }>;
      loading: boolean;
    },
  },
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useWorkspacePresenceMap: () => presence.current,
}));

vi.mock("@multica/core/runtimes/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes/queries")>()),
  runtimeListOptions: () => ({
    queryKey: ["runtimes"],
    queryFn: runtimeFetch,
  }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    agents: () => "/acme/agents",
    runtimes: () => "/acme/runtimes",
  }),
}));

vi.mock("../issues/components/workspace-agent-working-chip", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../issues/components/workspace-agent-working-chip")>()),
  WorkingAgentsHoverContent: ({
    agents,
  }: {
    agents: { id: string; running_task_count: number }[] | undefined;
  }) => (
    <div data-testid="working-hover">
      {agents === undefined
        ? "unresolved"
        : agents.map((a) => `${a.id}:${a.running_task_count}`).join(",") || "nobody"}
    </div>
  ),
}));

import { AgentStatusIndicator } from "./agent-status-indicator";

function setPresence(agents: Record<string, Presence>, loading = false) {
  presence.current = { byAgent: new Map(Object.entries(agents)), loading };
}

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "mac-mini",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const draining = (active: number | null, idle = false) => ({
  ag_drain: { draining: true, idle, active_tasks: active },
});

function renderIndicator(runtimes: AgentRuntime[] = [], wsId: string | null = "ws-1") {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(["runtimes"], runtimes);
  const adapter: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p) => p,
  };
  // A fresh element per call: rerendering the same element reference is a no-op.
  const ui = () => (
    <NavigationProvider value={adapter}>
      <QueryClientProvider client={qc}>
        <SidebarProvider>
          <AgentStatusIndicator wsId={wsId ?? undefined} />
        </SidebarProvider>
      </QueryClientProvider>
    </NavigationProvider>
  );
  return { qc, ui, ...renderWithI18n(ui()) };
}

beforeEach(() => {
  setPresence({});
  runtimeFetch.mockClear();
});
afterEach(cleanup);

describe("AG patch: AgentStatusIndicator", () => {
  it("AG patch: shows agents, running and queued counts with a pulsing dot", () => {
    setPresence({
      a1: { runningCount: 2, queuedCount: 1 },
      a2: { runningCount: 1, queuedCount: 1 },
      a3: { runningCount: 1, queuedCount: 0 },
      idle: { runningCount: 0, queuedCount: 0 },
    });
    renderIndicator();
    expect(screen.getByText("3 agents · 4 running · 2 queued")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-dot")).toHaveClass("animate-pulse");
    expect(screen.queryByTestId("agent-status-drain")).not.toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/agents");
  });

  it("AG patch: drops the queued part when nothing is queued and singularises", () => {
    setPresence({ a1: { runningCount: 1, queuedCount: 0 } });
    renderIndicator();
    expect(screen.getByText("1 agent · 1 running")).toBeInTheDocument();
  });

  it("AG patch: shows the muted empty state and keeps the row", () => {
    setPresence({ a1: { runningCount: 0, queuedCount: 0 } });
    renderIndicator();
    expect(screen.getByText("No agents running")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-dot")).not.toHaveClass("animate-pulse");
  });

  it("AG patch: tones the summary muted unless agents are working", () => {
    setPresence({ a1: { runningCount: 1, queuedCount: 0 } });
    const { unmount } = renderIndicator();
    const working = screen.getByTestId("agent-status-summary");
    expect(working).toHaveClass("text-sidebar-foreground");
    expect(working).not.toHaveClass("text-muted-foreground");
    unmount();

    setPresence({ a1: { runningCount: 0, queuedCount: 0 } });
    const empty = renderIndicator();
    expect(screen.getByTestId("agent-status-summary")).toHaveClass("text-muted-foreground");
    expect(screen.getByTestId("agent-status-summary")).not.toHaveClass("text-sidebar-foreground");
    empty.unmount();

    setPresence({}, true);
    renderIndicator();
    expect(screen.getByTestId("agent-status-summary")).toHaveClass("text-muted-foreground");
  });

  it("AG patch: lets the summary wrap instead of inheriting the button's nowrap truncation", () => {
    setPresence({ a1: { runningCount: 4, queuedCount: 2 } });
    renderIndicator();
    // SidebarMenuButton sets nowrap on its last child span; the summary must opt back in
    // itself, because that selector outranks a whitespace utility on the wrapper.
    expect(screen.getByTestId("agent-status-summary")).toHaveClass("whitespace-normal");
  });

  it("AG patch: shows queued work when nothing is running yet", () => {
    setPresence({ a1: { runningCount: 0, queuedCount: 2 } });
    renderIndicator();
    expect(screen.getByText("No agents running · 2 queued")).toBeInTheDocument();
  });

  it("AG patch: shows a dash, never 0, while loading", () => {
    setPresence({}, true);
    renderIndicator();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.queryByText(/0/)).not.toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAccessibleName("Agent status not loaded");
  });

  it("AG patch: renders nothing without a workspace", () => {
    renderIndicator([], null);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByTestId("agent-status-dot")).not.toBeInTheDocument();
  });

  it("AG patch: adds the drain marker beside the agent counts and links to Runtimes", () => {
    setPresence({ a1: { runningCount: 2, queuedCount: 0 } });
    renderIndicator([
      makeRuntime({ id: "r1", metadata: draining(2) }),
      makeRuntime({ id: "r2", metadata: draining(1) }),
      makeRuntime({ id: "r3", metadata: {} }),
    ]);
    expect(screen.getByText("1 agent · 2 running")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent("Draining · 3 running");
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/runtimes");
  });

  it("AG patch: shows Drained once every draining runtime is idle", () => {
    renderIndicator([
      makeRuntime({ id: "r1", metadata: draining(0, true) }),
      makeRuntime({ id: "r2", metadata: draining(0, true) }),
    ]);
    expect(screen.getByText("No agents running")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent(
      "Drained · safe to restart",
    );
  });

  it("AG patch: one busy draining runtime keeps the marker on Draining", () => {
    renderIndicator([
      makeRuntime({ id: "r1", metadata: draining(0, true) }),
      makeRuntime({ id: "r2", metadata: draining(1) }),
    ]);
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent("Draining · 1 running");
  });

  it("AG patch: an unknown drain count shows Draining without a number", () => {
    renderIndicator([makeRuntime({ metadata: draining(null) })]);
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent(/^Draining$/);
  });

  it("AG patch: ignores offline runtimes with a stale drain report", () => {
    renderIndicator([makeRuntime({ status: "offline", metadata: draining(0, true) })]);
    expect(screen.queryByTestId("agent-status-drain")).not.toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/agents");
  });

  it("AG patch: hover lists the working agents and each draining runtime", async () => {
    setPresence({
      a1: { runningCount: 2, queuedCount: 0 },
      a2: { runningCount: 1, queuedCount: 0 },
      idle: { runningCount: 0, queuedCount: 1 },
    });
    renderIndicator([
      makeRuntime({ id: "r1", name: "mac-mini", metadata: draining(2) }),
      makeRuntime({ id: "r2", name: "laptop", metadata: draining(0, true) }),
    ]);
    await userEvent.hover(screen.getByRole("link"));
    expect(await screen.findByTestId("working-hover")).toHaveTextContent("a1:2,a2:1");
    expect(screen.getByText("Draining runtimes")).toBeInTheDocument();
    expect(screen.getByText("mac-mini · 2 running")).toBeInTheDocument();
    expect(screen.getByText("laptop · drained")).toBeInTheDocument();
  });

  it("AG patch: hover leaves the roster unresolved while loading", async () => {
    setPresence({}, true);
    renderIndicator();
    await userEvent.hover(screen.getByRole("link"));
    expect(await screen.findByTestId("working-hover")).toHaveTextContent("unresolved");
  });

  it("AG patch: drained still links to Runtimes", () => {
    renderIndicator([makeRuntime({ metadata: draining(0, true) })]);
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/runtimes");
  });

  it("AG patch: queued-only work shows beside the drain marker", () => {
    setPresence({ a1: { runningCount: 0, queuedCount: 3 } });
    renderIndicator([makeRuntime({ metadata: draining(0) })]);
    expect(screen.getByText("No agents running · 3 queued")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent("Draining · 0 running");
  });

  it("AG patch: a drain marker still shows next to the dash while presence loads", () => {
    setPresence({}, true);
    renderIndicator([makeRuntime({ metadata: draining(1) })]);
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-drain")).toHaveTextContent("Draining · 1 running");
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/runtimes");
  });

  it("AG patch: no runtimes means no drain marker and no hover runtime list", async () => {
    setPresence({ a1: { runningCount: 1, queuedCount: 0 } });
    renderIndicator([makeRuntime({ metadata: {} })]);
    expect(screen.queryByTestId("agent-status-drain")).not.toBeInTheDocument();
    await userEvent.hover(screen.getByRole("link"));
    expect(await screen.findByTestId("working-hover")).toHaveTextContent("a1:1");
    expect(screen.queryByText("Draining runtimes")).not.toBeInTheDocument();
  });

  it("AG patch: hover names a draining runtime whose task count is unknown", async () => {
    renderIndicator([makeRuntime({ name: "mac-mini", metadata: draining(null) })]);
    await userEvent.hover(screen.getByRole("link"));
    expect(await screen.findByText("mac-mini · draining")).toBeInTheDocument();
  });

  it("AG patch: updates when the cached runtime list changes, without refetching", async () => {
    const { qc } = renderIndicator([]);
    expect(screen.queryByTestId("agent-status-drain")).not.toBeInTheDocument();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/agents");

    await act(async () => {
      qc.setQueryData(["runtimes"], [makeRuntime({ metadata: draining(2) })]);
    });
    await waitFor(() =>
      expect(screen.getByTestId("agent-status-drain")).toHaveTextContent("Draining · 2 running"),
    );
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/runtimes");

    await act(async () => {
      qc.setQueryData(["runtimes"], [makeRuntime({ metadata: draining(0, true) })]);
    });
    await waitFor(() =>
      expect(screen.getByTestId("agent-status-drain")).toHaveTextContent(
        "Drained · safe to restart",
      ),
    );

    await act(async () => {
      qc.setQueryData(["runtimes"], [makeRuntime({ metadata: {} })]);
    });
    await waitFor(() =>
      expect(screen.queryByTestId("agent-status-drain")).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("link")).toHaveAttribute("href", "/acme/agents");
    expect(runtimeFetch).not.toHaveBeenCalled();
  });

  it("AG patch: updates when presence changes from loading to working to empty", () => {
    setPresence({}, true);
    const { rerender, ui } = renderIndicator();
    expect(screen.getByText("—")).toBeInTheDocument();

    setPresence({ a1: { runningCount: 1, queuedCount: 0 } });
    rerender(ui());
    expect(screen.getByText("1 agent · 1 running")).toBeInTheDocument();

    setPresence({ a1: { runningCount: 0, queuedCount: 0 } });
    rerender(ui());
    expect(screen.getByText("No agents running")).toBeInTheDocument();
    expect(screen.getByTestId("agent-status-dot")).not.toHaveClass("animate-pulse");
  });
});
