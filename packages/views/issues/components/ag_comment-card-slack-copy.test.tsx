import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { forwardRef, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { TimelineEntry } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

// Copy (Slack) end to end through CommentCard: only the clipboard and the toast
// are mocked, so the real renderer, mrkdwn fallback and copyRich run on
// `entry.content`.

const toastSuccess = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { success: toastSuccess, error: vi.fn() } }));

vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: { uploadFile: vi.fn() },
  dispatchReasonCode: () => undefined,
  errorCode: () => undefined,
}));

vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/acme/issues",
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

// useAppOrigin reads the adapter through ./context, not the barrel mocked above.
vi.mock("../../navigation/context", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation/context")>()),
  useOptionalNavigation: () => ({ getShareableUrl: (p: string) => `https://app.example${p}` }),
}));

vi.mock("@multica/core/workspace/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/hooks")>()),
  useActorName: () => ({ getActorName: () => "Ada" }),
}));

vi.mock("../../common/actor-avatar", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../common/actor-avatar")>()),
  ActorAvatar: () => null,
}));

vi.mock("../hooks/use-comment-trigger-preview", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../hooks/use-comment-trigger-preview")>()),
  useCommentTriggerPreview: () => ({ agents: [], blocked: [] }),
}));

vi.mock("../../editor", async () => ({
  ...(await vi.importActual<typeof import("../../editor/use-upload-gate")>("../../editor/use-upload-gate")),
  ...(await vi.importActual<typeof import("../../editor/use-lazy-editor")>("../../editor/use-lazy-editor")),
  ...(await vi.importActual<typeof import("../../editor/use-composer-submit")>("../../editor/use-composer-submit")),
  useEditorUpload: () => ({ uploadWithToast: vi.fn(), upload: vi.fn(), uploading: false }),
  useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
  FileDropOverlay: () => null,
  ReadonlyContent: ({ content }: { content: string }) => <div>{content}</div>,
  Attachment: () => null,
  AttachmentDownloadProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  ContentEditor: forwardRef(function MockContentEditor() {
    return <textarea data-testid="editor" />;
  }),
}));

import { CommentCard } from "./comment-card";

class FakeClipboardItem {
  constructor(public items: Record<string, Blob | Promise<Blob>>) {}
}

const CONTENT = [
  "**Bold** and [docs](/acme/docs) plus [@Ada](mention://member/m-1)",
  "",
  "- one",
  "  - nested",
].join("\n");

function entry(content: string): TimelineEntry {
  return {
    type: "comment",
    id: "root",
    actor_type: "member",
    actor_id: "user-1",
    content,
    parent_id: null,
    comment_type: "comment",
    reactions: [],
    attachments: [],
    created_at: "2026-09-11T07:00:00Z",
    updated_at: "2026-09-11T07:00:00Z",
    revision: 1,
  };
}

function renderCard(root: TimelineEntry, replies: TimelineEntry[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <CommentCard
        issueId="issue-1"
        entry={root}
        replies={replies}
        currentUserId="user-1"
        onReply={vi.fn().mockResolvedValue(true)}
        onEdit={vi.fn().mockResolvedValue(undefined)}
        onDelete={vi.fn()}
        onToggleReaction={vi.fn()}
        onCopyLink={vi.fn()}
        onCreateSubIssue={vi.fn()}
      />
    </QueryClientProvider>,
  );
}

async function clickCopySlack(menuIndex: number) {
  fireEvent.click(screen.getAllByRole("button", { name: "Comment actions" })[menuIndex]!);
  fireEvent.click(await screen.findByRole("menuitem", { name: "Copy (Slack)" }));
}

async function writtenBlobs(write: ReturnType<typeof vi.fn>) {
  await waitFor(() => expect(write).toHaveBeenCalledTimes(1));
  const [item] = write.mock.calls[0]![0] as FakeClipboardItem[];
  const entries = item!.items;
  expect(Object.keys(entries).sort()).toEqual(["text/html", "text/plain"]);
  return {
    html: await (await entries["text/html"]!).text(),
    text: await (await entries["text/plain"]!).text(),
  };
}

describe("AG patch: CommentCard Copy (Slack)", () => {
  let write: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal("ClipboardItem", FakeClipboardItem);
    write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { write }, configurable: true });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
  });

  it("writes text/html and a text/plain mrkdwn fallback built from entry.content, then toasts", async () => {
    renderCard(entry(CONTENT));
    await clickCopySlack(0);

    const { html, text } = await writtenBlobs(write);
    expect(html).toContain("<strong>Bold</strong>");
    // Relative link made absolute against the app origin; the member mention is plain text.
    expect(html).toContain('<a href="https://app.example/acme/docs">docs</a>');
    expect(html).toContain("@Ada");
    expect(html).not.toContain("mention://");
    expect(html).toMatch(/<ul>[\s\S]*<li>[\s\S]*<ul>[\s\S]*nested[\s\S]*<\/ul>/);

    expect(text).toContain("*Bold*");
    expect(text).toContain("<https://app.example/acme/docs|docs>");
    expect(text).toContain("@Ada");
    expect(text).not.toContain("**");

    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith("Copied"));
  });

  it("copies from a reply menu as well as the root menu", async () => {
    const reply = { ...entry("reply **text**"), id: "reply", parent_id: "root" };
    renderCard(entry("root **text**"), [reply]);

    // Menus render in order: root first, then the reply.
    await clickCopySlack(1);
    const { html, text } = await writtenBlobs(write);
    expect(html).toContain("reply <strong>text</strong>");
    expect(text).toBe("reply *text*");
  });

  it("shows no success toast when the clipboard write is refused", async () => {
    write.mockRejectedValue(new Error("denied"));
    Object.defineProperty(document, "execCommand", {
      value: vi.fn(() => false),
      configurable: true,
      writable: true,
    });
    renderCard(entry(CONTENT));
    await clickCopySlack(0);

    await waitFor(() => expect(write).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(toastSuccess).not.toHaveBeenCalled();
  });
});
