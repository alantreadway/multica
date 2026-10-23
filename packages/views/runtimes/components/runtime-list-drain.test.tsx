// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { renderWithI18n } from "../../test/i18n";

// The row's cells query workspace data; none of it feeds the drain badge.
vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>(
      "@tanstack/react-query",
    );
  return {
    ...actual,
    useQuery: vi.fn(() => ({ data: [], isLoading: false })),
  };
});

vi.mock("@multica/core/runtimes/mutations", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes/mutations")>()),
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false }),
  useUnbindAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("@multica/core/runtimes", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes")>()),
  deriveRuntimeHealth: () => "online",
  isRuntimeUsableForUser: () => true,
  runtimeUsageOptions: () => ({ kind: "usage" }),
  runtimeProfileListOptions: () => ({ kind: "runtime-profiles" }),
  parseRuntimeProfileBoundConflict: () => null,
  useDeleteRuntimeProfile: () => ({ mutate: vi.fn(), isPending: false }),
  useCreateRuntimeProfile: () => ({ isPending: false }),
  useUpdateRuntimeProfile: () => ({ isPending: false }),
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  deriveWorkload: () => "idle",
  useWorkspacePresenceMap: () => ({ byAgent: new Map(), loading: false }),
  agentTaskSnapshotOptions: () => ({ kind: "task-snapshot" }),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    runtimeDetail: (id: string) => `/ws-1/runtimes/${id}`,
    runtimes: () => "/ws-1/runtimes",
  }),
}));

vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  agentListOptions: () => ({ kind: "agents" }),
  memberListOptions: () => ({ kind: "members" }),
}));

vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: {},
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("../../common/use-viewing-timezone", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../common/use-viewing-timezone")>()),
  useViewingTimezone: () => "UTC",
}));

vi.mock("./provider-logo", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./provider-logo")>()),
  ProviderLogo: () => null,
}));
vi.mock("./shared", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./shared")>()),
  HealthIcon: () => null,
  useHealthLabel: () => () => "Online",
}));

import { RuntimeList } from "./runtime-list";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "rt",
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

function makeAdapter(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/ws-1/runtimes",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (p) => p,
  };
}

function renderList(runtimes: AgentRuntime[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <NavigationProvider value={makeAdapter()}>
      <QueryClientProvider client={qc}>
        <RuntimeList
          runtimes={runtimes}
          now={Date.parse("2026-01-01T00:00:30Z")}
        />
      </QueryClientProvider>
    </NavigationProvider>,
  );
}

describe("AG patch: RuntimeList drain badge", () => {
  it("AG patch: shows Draining with the running count on the draining row only", () => {
    renderList([
      makeRuntime({
        id: "rt-drain",
        name: "Draining Mac",
        metadata: { ag_drain: { draining: true, idle: false, active_tasks: 2 } },
      }),
      makeRuntime({ id: "rt-plain", name: "Plain Mac", metadata: {} }),
    ]);
    expect(screen.getAllByText("Draining · 2 running")).toHaveLength(1);
    expect(screen.queryByText("Idle")).not.toBeInTheDocument();
    expect(screen.getByText("Plain Mac")).toBeInTheDocument();
  });

  it("AG patch: shows Idle on a drained, empty row", () => {
    renderList([
      makeRuntime({
        metadata: { ag_drain: { draining: true, idle: true, active_tasks: 0 } },
      }),
    ]);
    expect(screen.getByText("Idle")).toBeInTheDocument();
  });

  it("AG patch: an offline row shows no drain badge", () => {
    renderList([
      makeRuntime({
        status: "offline",
        metadata: { ag_drain: { draining: true, idle: true, active_tasks: 0 } },
      }),
    ]);
    expect(screen.queryByText("Idle")).not.toBeInTheDocument();
    expect(screen.queryByText(/Draining/)).not.toBeInTheDocument();
  });

  it("AG patch: a malformed drain value leaves the row rendering without a badge", () => {
    renderList([
      makeRuntime({ name: "Odd Mac", metadata: { ag_drain: { draining: "yes" } } }),
    ]);
    expect(screen.getByText("Odd Mac")).toBeInTheDocument();
    expect(screen.queryByText(/Draining/)).not.toBeInTheDocument();
  });
});
