/**
 * Copy rich text (`text/html` with a `text/plain` fallback) to the clipboard.
 *
 * Prefers one `ClipboardItem` through the async Clipboard API, which needs a
 * secure context (`https://` or `localhost`). Where `ClipboardItem` is missing
 * or the write throws (plain `http://`, permission denied), it selects a hidden
 * `contenteditable` holding the HTML and runs `document.execCommand('copy')`,
 * which carries the HTML too. Focus is restored afterwards, as `copyText` does.
 *
 * `html` may be a pending string: the write then starts synchronously inside
 * the click and the item resolves when the HTML is ready. Safari rejects a
 * clipboard write that happens after an await (see `copyImage`).
 *
 * @returns `true` on success, `false` on failure (including a rejected `html`).
 */
export async function copyRich({
  html,
  text,
}: {
  html: string | Promise<string>;
  text: string;
}): Promise<boolean> {
  if (
    typeof navigator !== "undefined" &&
    navigator.clipboard?.write &&
    typeof ClipboardItem !== "undefined"
  ) {
    try {
      await navigator.clipboard.write([
        new ClipboardItem({
          "text/html": Promise.resolve(html).then(
            (h) => new Blob([h], { type: "text/html" }),
          ),
          "text/plain": new Blob([text], { type: "text/plain" }),
        }),
      ]);
      return true;
    } catch {
      // Fall through to the selection-based copy below.
    }
  }

  if (typeof document === "undefined") return false;

  let resolvedHtml: string;
  try {
    resolvedHtml = await html;
  } catch {
    return false;
  }

  const holder = document.createElement("div");
  holder.contentEditable = "true";
  holder.innerHTML = resolvedHtml;
  // Keep it out of layout and view, but selectable (opacity 0, not display: none).
  holder.style.position = "fixed";
  holder.style.top = "0";
  holder.style.left = "0";
  holder.style.opacity = "0";
  holder.style.pointerEvents = "none";
  holder.style.whiteSpace = "pre-wrap";

  const previouslyFocused =
    document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const selection = document.getSelection();
  const previousRanges: Range[] = [];
  if (selection) {
    for (let i = 0; i < selection.rangeCount; i++) {
      previousRanges.push(selection.getRangeAt(i));
    }
  }

  document.body.appendChild(holder);
  try {
    holder.focus();
    const range = document.createRange();
    range.selectNodeContents(holder);
    selection?.removeAllRanges();
    selection?.addRange(range);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    selection?.removeAllRanges();
    for (const range of previousRanges) selection?.addRange(range);
    document.body.removeChild(holder);
    previouslyFocused?.focus();
  }
}
