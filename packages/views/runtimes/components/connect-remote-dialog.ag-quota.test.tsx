import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import { ConnectRemoteDialog } from "./connect-remote-dialog";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-test",
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  paths: { workspace: () => ({ agents: () => "/agents", runtimeDetail: () => "/r" }) },
  useWorkspaceSlug: () => "workspace-test",
}));

const wsEventState = vi.hoisted(() => ({
  handler: null as ((payload: unknown) => void) | null,
}));
vi.mock("@multica/core/realtime", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/realtime")>()),
  useWSEvent: (_event: string, handler: (payload: unknown) => void) => {
    wsEventState.handler = handler;
  },
}));
vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  useNavigation: () => ({ push: vi.fn() }),
}));

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <ConnectRemoteDialog onClose={vi.fn()} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("AG patch: ConnectRemoteDialog ignores AG refreshes", () => {
  beforeEach(() => {
    wsEventState.handler = null;
  });

  it("AG patch: a quota, drain or settings refresh does not complete the connection", async () => {
    const { baseElement } = renderDialog();
    for (const action of ["quota", "drain", "ag_settings"]) {
      act(() => {
        wsEventState.handler?.({ action, runtime_id: "rt-1" });
      });
    }
    // The success step mounts after an animation frame; give it time to appear.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 300));
    });
    expect(baseElement).toHaveTextContent("multica setup");
    expect(screen.queryByText("Computer connected")).not.toBeInTheDocument();
  });

  it("AG patch: a real registration still completes it after AG refreshes", async () => {
    renderDialog();
    act(() => {
      wsEventState.handler?.({ action: "quota", runtime_id: "rt-1" });
      wsEventState.handler?.({ runtime_id: "rt-2" });
    });
    await waitFor(() =>
      expect(screen.getByText("Computer connected")).toBeInTheDocument(),
    );
  });
});
