// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import enAgents from "../../locales/en/agents.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes, agents: enAgents },
};

// Stub the workspace queries the columns reach into. None of them feed the
// row menu directly, but `createRuntimeColumns` wires CliCell + CostCell
// against the same query client, so we still need useQuery to resolve.
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

vi.mock("@multica/core/runtimes/mutations", () => ({
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false, mutateAsync: vi.fn() }),
  useUnbindAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: vi.fn(),
  }),
}));

vi.mock("@multica/core/runtimes", () => ({
  deriveRuntimeHealth: () => "online",
  isRuntimeUsableForUser: () => true,
  runtimeUsageOptions: () => ({ kind: "usage" }),
  runtimeProfileListOptions: () => ({ kind: "runtime-profiles" }),
  parseRuntimeProfileBoundConflict: () => null,
  useDeleteRuntimeProfile: () => ({
    mutate: vi.fn(),
    isPending: false,
    mutateAsync: vi.fn(),
  }),
  useCreateRuntimeProfile: () => ({
    isPending: false,
    mutateAsync: vi.fn(),
  }),
  useUpdateRuntimeProfile: () => ({
    isPending: false,
    mutateAsync: vi.fn(),
  }),
}));

vi.mock("@multica/core/agents", () => ({
  deriveWorkload: () => "idle",
  useWorkspacePresenceMap: () => ({ byAgent: new Map(), loading: false }),
  agentTaskSnapshotOptions: () => ({ kind: "task-snapshot" }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runtimeDetail: (id: string) => `/ws-1/runtimes/${id}`,
    runtimes: () => "/ws-1/runtimes",
  }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({ kind: "agents" }),
  memberListOptions: () => ({ kind: "members" }),
}));

// The unified DeleteRuntimeDialog the kebab now opens reaches into auth +
// the api singleton. The dialog never renders in these tests (`open=false`
// throughout) but its hooks still mount; stub them so module init is clean.
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    deleteRuntime: vi.fn(),
    unbindAgentsAndDeleteRuntime: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));

vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("./shared", () => ({
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
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={makeAdapter()}>
        <QueryClientProvider client={qc}>
          <RuntimeList
            runtimes={runtimes}
            now={Date.parse("2026-01-01T00:00:30Z")}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

describe("AG patch: RuntimeList drain badge", () => {
  beforeEach(() => vi.clearAllMocks());

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
