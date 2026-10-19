import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { copyRich } from "@multica/ui/lib/ag_clipboard-rich";

const html = "<p><strong>hi</strong> there</p>";
const text = "*hi* there";

class FakeClipboardItem {
  constructor(public items: Record<string, Blob>) {}
}

function stubClipboard(write: ReturnType<typeof vi.fn> | undefined) {
  Object.defineProperty(navigator, "clipboard", {
    value: write ? { write } : undefined,
    configurable: true,
  });
}

function stubExecCommand(impl: () => boolean) {
  const fn = vi.fn(impl);
  Object.defineProperty(document, "execCommand", { value: fn, configurable: true, writable: true });
  return fn;
}

describe("AG patch: copyRich", () => {
  beforeEach(() => {
    vi.stubGlobal("ClipboardItem", FakeClipboardItem);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    stubClipboard(undefined);
    document.body.innerHTML = "";
  });

  describe("ClipboardItem path", () => {
    it("writes one item holding text/html and text/plain blobs", async () => {
      const write = vi.fn().mockResolvedValue(undefined);
      stubClipboard(write);
      const exec = stubExecCommand(() => true);

      await expect(copyRich({ html, text })).resolves.toBe(true);

      expect(write).toHaveBeenCalledTimes(1);
      const items = write.mock.calls[0]![0] as FakeClipboardItem[];
      expect(items).toHaveLength(1);
      const blobs = items[0]!.items;
      expect(Object.keys(blobs).sort()).toEqual(["text/html", "text/plain"]);
      expect(blobs["text/html"]!.type).toBe("text/html");
      expect(blobs["text/plain"]!.type).toBe("text/plain");
      expect(await blobs["text/html"]!.text()).toBe(html);
      expect(await blobs["text/plain"]!.text()).toBe(text);
      // The fallback must not run when the async API succeeds.
      expect(exec).not.toHaveBeenCalled();
    });

    it("falls back to execCommand when the write rejects", async () => {
      stubClipboard(vi.fn().mockRejectedValue(new DOMException("denied", "NotAllowedError")));
      const exec = stubExecCommand(() => true);

      await expect(copyRich({ html, text })).resolves.toBe(true);
      expect(exec).toHaveBeenCalledWith("copy");
    });
  });

  describe("execCommand fallback", () => {
    it("is used when ClipboardItem is unavailable", async () => {
      vi.stubGlobal("ClipboardItem", undefined);
      const write = vi.fn();
      stubClipboard(write);
      const exec = stubExecCommand(() => true);

      await expect(copyRich({ html, text })).resolves.toBe(true);
      expect(write).not.toHaveBeenCalled();
      expect(exec).toHaveBeenCalledWith("copy");
    });

    it("is used when navigator.clipboard is unavailable (plain http)", async () => {
      stubClipboard(undefined);
      const exec = stubExecCommand(() => true);

      await expect(copyRich({ html, text })).resolves.toBe(true);
      expect(exec).toHaveBeenCalledWith("copy");
    });

    it("selects a contenteditable holding the HTML while copying, then removes it", async () => {
      stubClipboard(undefined);
      let seen: { editable: string | null; innerHTML: string; selected: string } | null = null;
      stubExecCommand(() => {
        // jsdom doesn't reflect the contentEditable property to an attribute.
        const holder = document.body.lastElementChild as HTMLElement | null;
        seen = {
          editable: holder?.contentEditable ?? null,
          innerHTML: holder?.innerHTML ?? "",
          selected: document.getSelection()?.toString() ?? "",
        };
        return true;
      });

      await copyRich({ html, text });

      expect(seen).toEqual({
        editable: "true",
        innerHTML: html,
        selected: "hi there",
      });
      expect(document.body.children).toHaveLength(0);
    });

    it("restores the previously focused element and selection", async () => {
      stubClipboard(undefined);
      stubExecCommand(() => true);
      const input = document.createElement("input");
      document.body.appendChild(input);
      input.focus();
      const para = document.createElement("p");
      para.textContent = "keep me selected";
      document.body.appendChild(para);
      const range = document.createRange();
      range.selectNodeContents(para);
      const selection = document.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);

      await copyRich({ html, text });

      expect(document.activeElement).toBe(input);
      expect(selection.toString()).toBe("keep me selected");
    });

    it("returns false when execCommand reports failure", async () => {
      stubClipboard(undefined);
      stubExecCommand(() => false);

      await expect(copyRich({ html, text })).resolves.toBe(false);
      expect(document.body.children).toHaveLength(0);
    });

    it("returns false and cleans up when execCommand throws", async () => {
      stubClipboard(undefined);
      stubExecCommand(() => {
        throw new Error("blocked");
      });

      await expect(copyRich({ html, text })).resolves.toBe(false);
      expect(document.body.children).toHaveLength(0);
    });

    it("returns false when both paths fail", async () => {
      stubClipboard(vi.fn().mockRejectedValue(new Error("nope")));
      stubExecCommand(() => false);

      await expect(copyRich({ html, text })).resolves.toBe(false);
    });
  });
});
