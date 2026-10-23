// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", () => ({
  api: {},
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

// The detail page's own queries are not under test; stub them empty.
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

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (sel: (s: { user: { id: string } }) => unknown) =>
    sel({ user: { id: "user-me" } }),
}));

vi.mock("@multica/core/runtimes", () => ({
  isRuntimeUsableForUser: () => true,
  deriveRuntimeHealth: () => "online",
  runtimeDisplayName: (rt: { name: string }) => rt.name,
  runtimeProfileListOptions: (wsId: string) => ({
    queryKey: ["runtime-profiles", wsId],
  }),
  parseRuntimeProfileBoundConflict: () => null,
  useDeleteRuntimeProfile: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/agents", () => ({
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    runtimes: () => "/runtimes",
    agentDetail: () => "/agents",
  }),
}));

vi.mock("@multica/core/runtimes/mutations", () => ({
  useUpdateRuntime: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteRuntime: () => ({ mutate: vi.fn(), isPending: false }),
  useUnbindAgentsAndDeleteRuntime: () => ({
    mutate: vi.fn(),
    isPending: false,
  }),
}));

vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("./usage-section", () => ({ UsageSection: () => null }));
vi.mock("./revoke-runtime-access-dialog", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./revoke-runtime-access-dialog")>()),
  RevokeRuntimeAccessDialog: () => null,
}));
vi.mock("./shared", () => ({ HealthBadge: () => null }));
vi.mock("../../agents/presence", () => ({
  availabilityConfig: { offline: { dotClass: "", textClass: "" } },
  workloadConfig: { idle: { icon: () => null, textClass: "" } },
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("../../navigation", () => ({
  AppLink: ({ children }: { children: ReactNode }) => <>{children}</>,
  useNavigation: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import { RuntimeDetail } from "./runtime-detail";

function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Local Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "host.local",
    metadata: {},
    owner_id: "user-me",
    visibility: "private",
    last_seen_at: "2026-04-27T11:59:50Z",
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

function renderDetail(runtime: AgentRuntime) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <RuntimeDetail runtime={runtime} />
    </QueryClientProvider>,
  );
}

describe("AG patch: RuntimeDetail drain badge", () => {
  it("AG patch: shows Draining with the running count on the detail page", () => {
    renderDetail(
      makeRuntime({
        metadata: { ag_drain: { draining: true, idle: false, active_tasks: 3 } },
      }),
    );
    expect(screen.getByText("Draining · 3 running")).toBeInTheDocument();
  });

  it("AG patch: shows Idle on the detail page once drained and empty", () => {
    renderDetail(
      makeRuntime({
        metadata: { ag_drain: { draining: true, idle: true, active_tasks: 0 } },
      }),
    );
    expect(screen.getByText("Idle")).toBeInTheDocument();
  });

  it("AG patch: shows no drain badge for a runtime that is not draining", () => {
    renderDetail(makeRuntime({ metadata: { cli_version: "0.3.17" } }));
    expect(screen.queryByText(/Draining/)).not.toBeInTheDocument();
    expect(screen.queryByText("Idle")).not.toBeInTheDocument();
  });

  it("AG patch: a malformed drain value does not break the detail page", () => {
    renderDetail(makeRuntime({ metadata: { ag_drain: ["draining"] } }));
    expect(screen.getAllByText("Local Runtime").length).toBeGreaterThan(0);
    expect(screen.queryByText(/Draining/)).not.toBeInTheDocument();
  });
});
