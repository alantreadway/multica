// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const { updateSettings, toastError } = vi.hoisted(() => ({
  updateSettings: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({
  api: { updateAGRuntimeSettings: updateSettings },
  ApiError: class ApiError extends Error {},
}));
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

import { QuotaSection } from "./quota-section";

function makeRuntime(
  quota: Record<string, unknown> | null,
  overrides: Partial<AgentRuntime> = {},
  settings?: Record<string, unknown>,
): AgentRuntime {
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
    metadata: {
      ...(quota ? { ag_quota: quota } : {}),
      ...(settings ? { ag_settings: settings } : {}),
    },
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const at = (h: number, m: number) => new Date(2026, 9, 18, h, m).toISOString();
const clock = (h: number, m: number) =>
  `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;

const backup = (b: Record<string, unknown>) => ({
  detected: true,
  valid: true,
  reason: "",
  enabled: false,
  max_concurrent: 2,
  ...b,
});

const base = {
  account: "primary",
  effective_cap: 3,
  max: 5,
  windows: {
    five_hour: { utilization: 0.84, resets_at: at(21, 10) },
    seven_day: { utilization: 0.4 },
  },
  backup: backup({}),
};

function renderSection(runtime: AgentRuntime, canEdit = true) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <QuotaSection runtime={runtime} canEdit={canEdit} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  updateSettings.mockReset().mockResolvedValue({});
  toastError.mockReset();
});
afterEach(cleanup);

describe("AG patch: QuotaSection", () => {
  it("AG patch: renders nothing without a quota report", () => {
    renderSection(makeRuntime(null));
    expect(screen.queryByTestId("ag-quota-section")).not.toBeInTheDocument();
    cleanup();
    renderSection(makeRuntime({ windows: 1 } as never, { metadata: { ag_quota: "bad" } }));
    expect(screen.queryByTestId("ag-quota-section")).not.toBeInTheDocument();
  });

  it("AG patch: shows both windows with utilisation and reset time", () => {
    renderSection(makeRuntime(base));
    expect(screen.getByRole("progressbar", { name: "5-hour" })).toHaveAttribute(
      "aria-valuenow",
      "84",
    );
    expect(screen.getByText(`84% · resets ${clock(21, 10)}`)).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "7-day" })).toHaveAttribute(
      "aria-valuenow",
      "40",
    );
    expect(screen.getByText("40%")).toBeInTheDocument();
  });

  it("AG patch: shows a dash for a window the daemon has not seen", () => {
    renderSection(makeRuntime({ ...base, windows: {} }));
    expect(screen.getAllByText("—")).toHaveLength(2);
    expect(screen.getByRole("progressbar", { name: "5-hour" })).not.toHaveAttribute(
      "aria-valuenow",
    );
  });

  it("AG patch: shows the effective cap", () => {
    renderSection(makeRuntime(base));
    expect(screen.getByText("Running up to 3 of 5")).toBeInTheDocument();
    expect(screen.queryByText(/On hold/)).not.toBeInTheDocument();
  });

  it("AG patch: shows a hold with its end time", () => {
    renderSection(
      makeRuntime({
        ...base,
        effective_cap: 0,
        hold: { until: at(21, 10), window: "five_hour" },
      }),
    );
    expect(screen.getByText(`On hold until ${clock(21, 10)}`)).toBeInTheDocument();
    expect(screen.getByText("Running up to 0 of 5")).toBeInTheDocument();
  });

  it("AG patch: hides the hold and cap on an offline runtime but keeps the backup", () => {
    renderSection(
      makeRuntime(
        { ...base, hold: { until: at(21, 10) } },
        { status: "offline" },
      ),
    );
    expect(screen.queryByText(/On hold/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Running up to/)).not.toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  it("AG patch: marks a runtime running on the backup account", () => {
    renderSection(makeRuntime({ ...base, account: "backup" }));
    expect(screen.getByText("Running on backup")).toBeInTheDocument();
    cleanup();
    renderSection(makeRuntime(base));
    expect(screen.queryByText("Running on backup")).not.toBeInTheDocument();
  });

  it("AG patch: reports a backup that is not found, and disables the setting", () => {
    renderSection(makeRuntime({ ...base, backup: backup({ detected: false, valid: false }) }));
    expect(screen.getByText("Not found")).toBeInTheDocument();
    expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("spinbutton")).toBeDisabled();
  });

  it("AG patch: a disabled toggle does not save", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, backup: backup({ valid: false, reason: "not_logged_in" }) }));
    await user.click(screen.getByRole("switch"));
    expect(updateSettings).not.toHaveBeenCalled();
    expect(screen.getByRole("switch")).not.toBeChecked();
  });

  it("AG patch: reports an invalid backup with its reason, and disables the setting", () => {
    const cases: [string, string][] = [
      ["not_logged_in", "Not logged in"],
      ["same_account", "Same account as the primary"],
      ["check_failed", "Check failed"],
      ["", "Unknown reason"],
      ["from_the_future", "Unknown reason"],
    ];
    for (const [reason, text] of cases) {
      const { unmount } = renderSection(
        makeRuntime({ ...base, backup: backup({ valid: false, reason }) }),
      );
      expect(screen.getByText(`Found but invalid · ${text}`)).toBeInTheDocument();
      expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
      unmount();
    }
  });

  it("AG patch: a ready backup enables the toggle but keeps the input off until it is on", () => {
    renderSection(makeRuntime(base));
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.getByRole("switch")).not.toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("spinbutton")).toBeDisabled();
    expect(screen.getByText("Only urgent issues run on the backup.")).toBeInTheDocument();
  });

  it("AG patch: the input is enabled once the backup is on", () => {
    renderSection(makeRuntime({ ...base, backup: backup({ enabled: true, max_concurrent: 3 }) }));
    expect(screen.getByRole("switch")).toBeChecked();
    expect(screen.getByRole("spinbutton")).toBeEnabled();
    expect(screen.getByRole("spinbutton")).toHaveValue(3);
  });

  it("AG patch: a read-only viewer cannot change the setting", () => {
    renderSection(makeRuntime(base), false);
    expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Read-only")).toBeInTheDocument();
  });

  it("AG patch: toggling sends both fields", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime(base));
    await user.click(screen.getByRole("switch"));
    await waitFor(() =>
      expect(updateSettings).toHaveBeenCalledWith("rt-1", {
        backup_enabled: true,
        backup_max_concurrent: 2,
      }),
    );
  });

  it("AG patch: committing the limit sends both fields", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, backup: backup({ enabled: true }) }));
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "4");
    await user.tab();
    await waitFor(() =>
      expect(updateSettings).toHaveBeenCalledWith("rt-1", {
        backup_enabled: true,
        backup_max_concurrent: 4,
      }),
    );
  });

  it("AG patch: Enter commits the limit", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, backup: backup({ enabled: true }) }));
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "5{Enter}");
    await waitFor(() => expect(updateSettings).toHaveBeenCalledTimes(1));
  });

  it("AG patch: an out-of-range or unchanged limit is not sent and reverts", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, backup: backup({ enabled: true }) }));
    const input = screen.getByRole("spinbutton");
    for (const bad of ["0", "6", "2.5", ""]) {
      await user.clear(input);
      if (bad) await user.type(input, bad);
      await user.tab();
      expect(input).toHaveValue(2);
    }
    await user.click(input);
    await user.tab();
    expect(updateSettings).not.toHaveBeenCalled();
  });

  it("AG patch: the input is bounded by 1..100 when max is unknown", async () => {
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, max: undefined, backup: backup({ enabled: true }) }));
    expect(screen.getByRole("spinbutton")).toHaveAttribute("max", "100");
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "50");
    await user.tab();
    await waitFor(() => expect(updateSettings).toHaveBeenCalledTimes(1));
  });

  it("AG patch: the stored setting wins over the daemon's echo", () => {
    renderSection(
      makeRuntime(base, {}, { backup_enabled: true, backup_max_concurrent: 4 }),
    );
    expect(screen.getByRole("switch")).toBeChecked();
    expect(screen.getByRole("spinbutton")).toHaveValue(4);
  });

  it("AG patch: a failed save toasts and shows the server state again", async () => {
    updateSettings.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    renderSection(makeRuntime({ ...base, backup: backup({ enabled: true }) }));
    const input = screen.getByRole("spinbutton");
    await user.clear(input);
    await user.type(input, "4");
    await user.tab();
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(toastError).toHaveBeenCalledWith("Couldn't save the backup setting");
    await waitFor(() => expect(input).toHaveValue(2));
    expect(screen.getByRole("switch")).toBeChecked();
  });

  it("AG patch: the toggle shows server state, not the click, when the save fails", async () => {
    updateSettings.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    renderSection(makeRuntime(base));
    await user.click(screen.getByRole("switch"));
    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(screen.getByRole("switch")).not.toBeChecked();
  });
});
