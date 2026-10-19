// formatMrkdwn below is a TypeScript port of server/internal/integrations/slack/mrkdwn.go,
// itself a Go port of the Markdown-to-mrkdwn converter (format_message) from
// Nous Research's Hermes Agent, used under the MIT License. Source:
// https://github.com/NousResearch/hermes-agent/blob/main/plugins/platforms/slack/adapter.py
//
// Copyright (c) 2025 Nous Research
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// AG patch: "Copy (Slack)" comment menu item. Renders a comment's Markdown as
// HTML that Slack's composer turns into rich text on paste, plus a mrkdwn
// plain-text fallback. See ag-patches/PATCHES.md.

import type { ReactNode } from "react";
import ReactMarkdown, {
  defaultUrlTransform,
  type Components,
} from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { preprocessMentionShortcodes } from "@multica/ui/markdown/mentions";

export interface SlackCopyContext {
  /** Public origin of the app, used to absolutise relative links. */
  origin: string | null;
  /** Current workspace slug, used to build issue links. */
  workspaceSlug: string | null;
}

// --- mrkdwn plain-text fallback ---------------------------------------------

const reFenced = /(```(?:[^\n]*\n)?[\s\S]*?```)/g;
const reInlineCode = /(`[^`]+`)/g;
const reMdLink = /(!?)\[([^\]]+)\]\(([^()]*(?:\([^()]*\)[^()]*)*)\)/g;
const reSlackEntity = /(<(?:[@#!]|(?:https?|mailto|tel):)[^>\n]+>)/g;
const reBlockquote = /^(>+\s)/gm;
const reHeader = /^#{1,6}\s+(.+)$/gm;
const reInnerBold = /\*\*(.+?)\*\*/g;
const reBoldItalic = /\*\*\*(.+?)\*\*\*/g;
const reBold = /\*\*(.+?)\*\*/g;
const reItalic = /\*(\S(?:[^*\n]*?\S)?)\*/g;
const reStrike = /~~(.+?)~~/g;

/** Converts standard Markdown to Slack mrkdwn. */
export function formatMrkdwn(content: string): string {
  if (content === "") return content;

  const values = new Map<string, string>();
  const order: string[] = [];
  const stash = (v: string): string => {
    const key = `\x00SL${order.length}\x00`;
    values.set(key, v);
    order.push(key);
    return key;
  };

  let text = content;

  // 1) Protect fenced code blocks, then 2) inline code.
  text = text.replace(reFenced, (m) => stash(m));
  text = text.replace(reInlineCode, (m) => stash(m));

  // 3) Markdown links [text](url) -> <url|text>; image links (![..]) are left
  //    untouched (Slack does not render inline images from markdown).
  text = text.replace(
    reMdLink,
    (m: string, bang: string, label: string, rawUrl: string) => {
      if (bang === "!") return m;
      let url = rawUrl.trim();
      if (url.startsWith("<") && url.endsWith(">")) {
        url = url.slice(1, -1).trim();
      }
      return stash(`<${url}|${label}>`);
    },
  );

  // 4) Protect existing Slack entities / manual links, 5) blockquote markers.
  text = text.replace(reSlackEntity, (m) => stash(m));
  text = text.replace(reBlockquote, (m) => stash(m));

  // 6) Escape Slack control chars (unescape first so input isn't double-escaped).
  text = text.replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">");
  text = text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

  // 7) Headers (## Title) -> *Title* (strip redundant bold inside).
  text = text.replace(reHeader, (_m: string, rawInner: string) => {
    const inner = rawInner.trim().replace(reInnerBold, "$1");
    return stash(`*${inner}*`);
  });

  // 8) ***bold italic*** -> *_text_*, 9) **bold** -> *bold*,
  // 10) *italic* -> _italic_, 11) ~~strike~~ -> ~strike~.
  text = text.replace(reBoldItalic, (_m: string, inner: string) => stash(`*_${inner}_*`));
  text = text.replace(reBold, (_m: string, inner: string) => stash(`*${inner}*`));
  text = text.replace(reItalic, (_m: string, inner: string) => stash(`_${inner}_`));
  text = text.replace(reStrike, (_m: string, inner: string) => stash(`~${inner}~`));

  // 13) Restore placeholders in reverse insertion order (nested ones resolve).
  for (let i = order.length - 1; i >= 0; i--) {
    const key = order[i]!;
    text = text.split(key).join(values.get(key)!);
  }
  return text;
}

// --- Mentions and URLs -------------------------------------------------------

const MENTION_RE = /\[([^\]]*)\]\(mention:\/\/([a-z_]+)\/([^)\s]*)\)/g;

/** Absolute issue link for a `mention://issue/<id>` target, or null. */
function issueUrl(id: string, ctx: SlackCopyContext): string | null {
  if (!ctx.origin || !ctx.workspaceSlug) return null;
  return `${ctx.origin}/${encodeURIComponent(ctx.workspaceSlug)}/issues/${encodeURIComponent(id)}`;
}

/** Resolves a relative or attachment URL against the app origin. */
function absoluteUrl(href: string, ctx: SlackCopyContext): string {
  if (!ctx.origin || /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//")) {
    return href;
  }
  try {
    return new URL(href, ctx.origin).toString();
  } catch {
    return href;
  }
}

const RELATIVE_LINK_RE = /(^|[^!])\[([^\]]+)\]\((\/(?!\/)[^)\s]*)\)/gm;

/**
 * Rewrites links so the plain-text fallback reads well: member and agent
 * mentions become their label, issue mentions and relative links absolute links.
 */
function flattenMentions(content: string, ctx: SlackCopyContext): string {
  return preprocessMentionShortcodes(content)
    .replace(
      RELATIVE_LINK_RE,
      (_m: string, lead: string, label: string, href: string) =>
        `${lead}[${label}](${absoluteUrl(href, ctx)})`,
    )
    .replace(
      MENTION_RE,
      (_m: string, label: string, type: string, id: string) => {
        if (type === "issue") {
          const url = issueUrl(id, ctx);
          return url ? `[${label}](${url})` : label;
        }
        return label;
      },
    );
}

/** The `text/plain` mrkdwn rendering of a comment. */
export function slackPlainText(content: string, ctx: SlackCopyContext): string {
  return formatMrkdwn(flattenMentions(content, ctx));
}

// --- HTML renderer -----------------------------------------------------------

interface HastNode {
  type?: string;
  tagName?: string;
  value?: string;
  children?: HastNode[];
}

function hastText(node: HastNode | undefined): string {
  if (!node) return "";
  if (node.type === "text") return node.value ?? "";
  if (node.tagName === "br") return "\n";
  return (node.children ?? []).map(hastText).join("");
}

function findAll(node: HastNode, tagName: string, out: HastNode[] = []): HastNode[] {
  for (const child of node.children ?? []) {
    if (child.tagName === tagName) out.push(child);
    else findAll(child, tagName, out);
  }
  return out;
}

/** GFM pipe-table text for a `<table>` node. */
function tableToPipeText(table: HastNode): string {
  const rows = findAll(table, "tr").map((tr) =>
    (tr.children ?? [])
      .filter((c) => c.tagName === "th" || c.tagName === "td")
      .map((c) => hastText(c).replace(/\s*\n\s*/g, " ").replace(/\|/g, "\\|").trim()),
  );
  if (rows.length === 0) return "";
  const width = Math.max(...rows.map((r) => r.length));
  const line = (r: string[]) =>
    `| ${Array.from({ length: width }, (_, i) => r[i] ?? "").join(" | ")} |`;
  const out = [line(rows[0]!), `| ${Array.from({ length: width }, () => "---").join(" | ")} |`];
  for (const r of rows.slice(1)) out.push(line(r));
  return out.join("\n");
}

function slackComponents(ctx: SlackCopyContext): Components {
  const heading = ({ children }: { children?: ReactNode }) => (
    <p>
      <strong>{children}</strong>
    </p>
  );
  return {
    a: ({ href, children }) => {
      const target = href ?? "";
      const mention = /^mention:\/\/([a-z_]+)\/(.*)$/.exec(target);
      if (mention) {
        if (mention[1] === "issue") {
          const url = issueUrl(mention[2]!, ctx);
          return url ? <a href={url}>{children}</a> : <>{children}</>;
        }
        // Members, agents and anything else: the label as plain text.
        return <>{children}</>;
      }
      return <a href={absoluteUrl(target, ctx)}>{children}</a>;
    },
    img: ({ src, alt }) => {
      const url = absoluteUrl(typeof src === "string" ? src : "", ctx);
      const label = alt || url;
      return url ? <a href={url}>{label}</a> : <>{label}</>;
    },
    h1: heading,
    h2: heading,
    h3: heading,
    h4: heading,
    h5: heading,
    h6: heading,
    del: ({ children }) => <s>{children}</s>,
    input: ({ checked }) => <>{checked ? "☑" : "☐"}</>,
    table: ({ node }) => (
      <pre>
        <code>{tableToPipeText(node as HastNode)}</code>
      </pre>
    ),
  };
}

function urlTransform(url: string): string {
  // The default transform drops the `mention:` scheme, which the `a` component
  // needs to tell mentions from ordinary links.
  return url.startsWith("mention://") ? url : defaultUrlTransform(url);
}

/**
 * Renders a comment's Markdown as an HTML string for Slack's composer. Loads
 * `react-dom/server` lazily so it stays out of the main bundle; call
 * `preloadSlackRenderer` ahead of the click to keep the copy inside the
 * user-gesture window (Safari rejects clipboard writes outside it).
 */
export async function renderSlackHtml(
  content: string,
  ctx: SlackCopyContext,
): Promise<string> {
  const { renderToStaticMarkup } = await import("react-dom/server");
  return renderToStaticMarkup(
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkBreaks]}
      components={slackComponents(ctx)}
      urlTransform={urlTransform}
    >
      {preprocessMentionShortcodes(content)}
    </ReactMarkdown>,
  );
}

export function preloadSlackRenderer(): void {
  void import("react-dom/server");
}
