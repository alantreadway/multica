import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  copyRich: vi.fn(),
  copyText: vi.fn(),
  toastSuccess: vi.fn(),
  renderSlackHtml: vi.fn(),
  slackPlainText: vi.fn(),
  preloadSlackRenderer: vi.fn(),
}));

vi.mock("@multica/ui/components/ui/dropdown-menu", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/ui/components/ui/dropdown-menu")>()),
  DropdownMenuItem: ({ children, onClick }: { children: ReactNode; onClick?: () => void }) => (
    <button type="button" role="menuitem" onClick={onClick}>
      {children}
    </button>
  ),
}));
vi.mock("@multica/ui/lib/ag_clipboard-rich", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/ui/lib/ag_clipboard-rich")>()),
  copyRich: mocks.copyRich,
}));
vi.mock("@multica/ui/lib/clipboard", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/ui/lib/clipboard")>()),
  copyText: mocks.copyText,
}));
vi.mock("sonner", () => ({ toast: { success: mocks.toastSuccess } }));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspaceSlug: () => "acme",
}));
vi.mock("../../navigation/use-app-origin", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation/use-app-origin")>()),
  useAppOrigin: () => "https://app.example",
}));
vi.mock("./ag_slack-copy", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./ag_slack-copy")>()),
  renderSlackHtml: mocks.renderSlackHtml,
  slackPlainText: mocks.slackPlainText,
  preloadSlackRenderer: mocks.preloadSlackRenderer,
}));

import { CopySlackMenuItem } from "./ag_comment-share-items";

const ctx = { origin: "https://app.example", workspaceSlug: "acme" };

// copyRich reports false for a rejected html promise, as the real one does.
function rejectingCopyRich() {
  mocks.copyRich.mockImplementation(async ({ html }: { html: Promise<string> }) => {
    try {
      await html;
      return true;
    } catch {
      return false;
    }
  });
}

function click() {
  fireEvent.click(screen.getByRole("menuitem", { name: "Copy (Slack)" }));
}

describe("AG patch: CopySlackMenuItem", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.renderSlackHtml.mockResolvedValue("<p>html</p>");
    mocks.slackPlainText.mockReturnValue("mrkdwn");
    mocks.copyRich.mockResolvedValue(true);
    mocks.copyText.mockResolvedValue(true);
  });

  it("renders a localised Copy (Slack) label", () => {
    renderWithI18n(<CopySlackMenuItem content="**x**" />);
    expect(screen.getByRole("menuitem", { name: "Copy (Slack)" })).toBeTruthy();
  });

  it("copies the HTML with the mrkdwn fallback and toasts on success", async () => {
    renderWithI18n(<CopySlackMenuItem content="**x**" />);
    click();

    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.renderSlackHtml).toHaveBeenCalledWith("**x**", ctx);
    expect(mocks.slackPlainText).toHaveBeenCalledWith("**x**", ctx);
    const arg = mocks.copyRich.mock.calls[0]![0] as { html: Promise<string>; text: string };
    expect(arg.text).toBe("mrkdwn");
    await expect(arg.html).resolves.toBe("<p>html</p>");
    expect(mocks.copyText).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Copied");
  });

  it("calls copyRich inside the click, before the render promise resolves", () => {
    mocks.renderSlackHtml.mockReturnValue(new Promise(() => {}));
    renderWithI18n(<CopySlackMenuItem content="x" />);
    click();

    expect(mocks.copyRich).toHaveBeenCalledTimes(1);
  });

  it("shows no toast when every clipboard path fails", async () => {
    mocks.copyRich.mockResolvedValue(false);
    renderWithI18n(<CopySlackMenuItem content="x" />);
    click();

    await waitFor(() => expect(mocks.copyRich).toHaveBeenCalled());
    await Promise.resolve();
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  it("falls back to copying the mrkdwn text when HTML rendering throws", async () => {
    mocks.renderSlackHtml.mockRejectedValue(new Error("render failed"));
    rejectingCopyRich();
    renderWithI18n(<CopySlackMenuItem content="x" />);
    click();

    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledTimes(1));
    expect(mocks.copyText).toHaveBeenCalledWith("mrkdwn");
  });

  it("falls back to the raw Markdown when the mrkdwn conversion throws too", async () => {
    mocks.slackPlainText.mockImplementation(() => {
      throw new Error("convert failed");
    });
    mocks.renderSlackHtml.mockRejectedValue(new Error("render failed"));
    rejectingCopyRich();
    renderWithI18n(<CopySlackMenuItem content="**raw**" />);
    click();

    await waitFor(() => expect(mocks.copyText).toHaveBeenCalledWith("**raw**"));
  });

  it("shows no toast when the render fallback copy fails as well", async () => {
    mocks.renderSlackHtml.mockRejectedValue(new Error("render failed"));
    mocks.copyText.mockResolvedValue(false);
    rejectingCopyRich();
    renderWithI18n(<CopySlackMenuItem content="x" />);
    click();

    await waitFor(() => expect(mocks.copyText).toHaveBeenCalled());
    await Promise.resolve();
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  it("preloads the renderer when the menu item mounts", () => {
    renderWithI18n(<CopySlackMenuItem content="x" />);
    expect(mocks.preloadSlackRenderer).toHaveBeenCalledTimes(1);
  });
});
