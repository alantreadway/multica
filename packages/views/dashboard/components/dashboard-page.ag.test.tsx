import { describe, it, expect, beforeEach, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider } from "../../navigation";
import type { NavigationAdapter } from "../../navigation";

// Capture every queryKey passed to useQuery. queryOptions() inside the
// dashboard options builders runs for real, so the key is the production key.
const queryKeys = vi.hoisted(() => [] as unknown[][]);

function todayIso() {
  return new Date().toISOString().slice(0, 10);
}

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  // The page reads the client only to invalidate the dashboard keys from
  // the refresh button; there is no provider in these renders.
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: (opts: { queryKey: unknown[] }) => {
    queryKeys.push(opts.queryKey);
    if (opts.queryKey[0] === "workspaces" && opts.queryKey[2] === "agents") {
      return {
        data: [{ id: "agent-1", name: "Agent One" }],
        isLoading: false,
        isSuccess: true,
      };
    }
    // Only today's daily rows: a window that ends yesterday sees none.
    const data =
      opts.queryKey[2] === "daily"
        ? [
            {
              date: todayIso(),
              provider: "anthropic",
              model: "claude-sonnet-4-6",
              input_tokens: 1_000,
              output_tokens: 2_000,
              cache_read_tokens: 0,
              cache_write_tokens: 0,
              task_count: 2,
            },
          ]
        : [];
    return { data, isLoading: false, isSuccess: true };
  },
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/acme/agents/${id}`,
  }),
}));

vi.mock("@multica/core/auth", async (importOriginal) => {
  type AuthState = { user: { timezone: string | null } | null };
  const state = (): AuthState => ({ user: { timezone: "UTC" } });
  return {
    ...(await importOriginal<typeof import("@multica/core/auth")>()),
    useAuthStore: Object.assign(
      (sel?: (s: AuthState) => unknown) => (sel ? sel(state()) : state()),
      { getState: state },
    ),
  };
});

import { DashboardPage } from "./dashboard-page";

function renderDashboard() {
  const adapter: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/usage",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => `https://example.test${path}`,
  };
  return renderWithI18n(
    <NavigationProvider value={adapter}>
      <DashboardPage />
    </NavigationProvider>,
  );
}

describe("AG patch: Yesterday period", () => {
  beforeEach(() => {
    queryKeys.length = 0;
    cleanup();
  });
  async function pickPeriod(
    user: ReturnType<typeof userEvent.setup>,
    name: string,
  ) {
    await user.click(screen.getByRole("button", { name: "Period" }));
    await user.click(await screen.findByRole("menuitemradio", { name }));
  }

  it("AG patch: offers Yesterday right after 1d", async () => {
    const user = userEvent.setup();
    renderDashboard();
    await user.click(screen.getByRole("button", { name: "Period" }));
    const items = (await screen.findAllByRole("menuitemradio")).map(
      (item) => item.textContent,
    );
    expect(items).toEqual(["1d", "Yesterday", "7d", "30d", "90d", "180d"]);
  });

  it("AG patch: labels the KPIs Yesterday and drops today's rows", async () => {
    const user = userEvent.setup();
    const { container } = renderDashboard();
    expect(screen.getByText("Cost · 30D")).toBeInTheDocument();

    await pickPeriod(user, "Yesterday");

    expect(screen.getByText("Cost · Yesterday")).toBeInTheDocument();
    expect(screen.getByText("Runs · Yesterday")).toBeInTheDocument();
    // The daily fixture only has rows dated today.
    const flows = Array.from(container.querySelectorAll("number-flow-react"));
    expect(flows[0]).toHaveAttribute("aria-label", "$0.00");
  });

  it("AG patch: asks the per-agent rollups for a window that ends yesterday", async () => {
    const user = userEvent.setup();
    renderDashboard();
    queryKeys.length = 0;

    await pickPeriod(user, "Yesterday");

    const windowPart = (kind: string) =>
      queryKeys
        .filter((k) => k[0] === "dashboard" && k[2] === kind)
        .map((k) => k[3])
        .at(-1);
    expect(windowPart("by-agent")).toBe("1-1");
    expect(windowPart("agent-runtime")).toBe("1-1");
    expect(windowPart("failures-by-agent")).toBe("1-1");
  });

  it("AG patch: keeps 1d on today's rows", async () => {
    const user = userEvent.setup();
    renderDashboard();

    await pickPeriod(user, "1d");

    expect(screen.getByText("Cost · 1D")).toBeInTheDocument();
    const byAgent = queryKeys
      .filter((k) => k[0] === "dashboard" && k[2] === "by-agent")
      .at(-1);
    expect(byAgent?.[3]).toBe(1);
  });
});
