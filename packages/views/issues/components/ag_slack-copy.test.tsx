import { describe, expect, it } from "vitest";
import {
  formatMrkdwn,
  renderSlackHtml,
  slackPlainText,
  type SlackCopyContext,
} from "./ag_slack-copy";

const ctx: SlackCopyContext = { origin: "https://app.example", workspaceSlug: "acme" };
const bare: SlackCopyContext = { origin: null, workspaceSlug: null };

async function render(md: string, c: SlackCopyContext = ctx): Promise<HTMLElement> {
  const root = document.createElement("div");
  root.innerHTML = await renderSlackHtml(md, c);
  return root;
}

describe("AG patch: Copy (Slack) HTML renderer", () => {
  it("renders bold, italic, strike, inline code and quotes as formatted HTML", async () => {
    const root = await render("**b** *i* ~~s~~ `c`\n\n> quoted");
    expect(root.querySelector("strong")?.textContent).toBe("b");
    expect(root.querySelector("em")?.textContent).toBe("i");
    expect(root.querySelector("s, del, strike")?.textContent).toBe("s");
    expect(root.querySelector("code")?.textContent).toBe("c");
    expect(root.querySelector("blockquote")?.textContent).toContain("quoted");
  });

  it("keeps single newlines as line breaks", async () => {
    const root = await render("line one\nline two");
    expect(root.querySelector("br")).not.toBeNull();
  });

  it("renders nested lists as nested ul/ol", async () => {
    const root = await render("- a\n  - a1\n  - a2\n- b\n\n1. one\n2. two");
    const outer = root.querySelector("ul")!;
    expect(outer.querySelectorAll(":scope > li")).toHaveLength(2);
    expect(outer.querySelector("li > ul")).not.toBeNull();
    expect(outer.querySelectorAll("li > ul > li")).toHaveLength(2);
    expect(root.querySelectorAll("ol > li")).toHaveLength(2);
  });

  it("renders task list items as checkbox glyphs, not input elements", async () => {
    const root = await render("- [x] done\n- [ ] todo");
    expect(root.querySelector("input")).toBeNull();
    expect(root.textContent).toContain("☑");
    expect(root.textContent).toContain("☐");
  });

  it.each(["", "ts", "mermaid", "html"])("keeps a ```%s fence as a code block", async (lang) => {
    const root = await render("```" + lang + "\n<b>x</b> & y\n```");
    const code = root.querySelector("pre code");
    expect(code).not.toBeNull();
    // Content is shown literally, not interpreted as markup.
    expect(code!.textContent).toContain("<b>x</b> & y");
    expect(root.querySelector("pre b")).toBeNull();
  });

  it.each([1, 2, 3, 4, 5, 6])("renders h%i as a bold paragraph", async (n) => {
    const root = await render(`${"#".repeat(n)} Title`);
    expect(root.querySelector(`h${n}`)).toBeNull();
    expect(root.querySelector("p > strong")?.textContent).toBe("Title");
  });

  it("renders a table as a code block of pipe-table text", async () => {
    const root = await render("| A | B |\n| - | - |\n| 1 | 2 |\n| 3 | x\\|y |");
    expect(root.querySelector("table")).toBeNull();
    const text = root.querySelector("pre code")!.textContent!;
    expect(text.split("\n")).toEqual([
      "| A | B |",
      "| --- | --- |",
      "| 1 | 2 |",
      "| 3 | x\\|y |",
    ]);
  });

  describe("links and mentions", () => {
    it("keeps absolute links", async () => {
      const root = await render("[docs](https://x.com/a?b=1)");
      const a = root.querySelector("a")!;
      expect(a.getAttribute("href")).toBe("https://x.com/a?b=1");
      expect(a.textContent).toBe("docs");
    });

    it("absolutises relative links against the app origin", async () => {
      const root = await render("[page](/acme/projects/p1)");
      expect(root.querySelector("a")!.getAttribute("href")).toBe(
        "https://app.example/acme/projects/p1",
      );
    });

    it("absolutises relative attachment links", async () => {
      const root = await render("[report.pdf](/api/attachments/abc/download)");
      expect(root.querySelector("a")!.getAttribute("href")).toBe(
        "https://app.example/api/attachments/abc/download",
      );
    });

    it("leaves relative links as they are when the origin is unknown", async () => {
      const root = await render("[page](/acme/projects/p1)", bare);
      expect(root.querySelector("a")!.getAttribute("href")).toBe("/acme/projects/p1");
    });

    it("turns images into links labelled with their alt text, absolutised", async () => {
      const root = await render("![diagram](/uploads/d.png)");
      expect(root.querySelector("img")).toBeNull();
      const a = root.querySelector("a")!;
      expect(a.getAttribute("href")).toBe("https://app.example/uploads/d.png");
      expect(a.textContent).toBe("diagram");
    });

    it("labels an image without alt text with its URL", async () => {
      const root = await render("![](https://x.com/i.png)");
      expect(root.querySelector("a")!.textContent).toBe("https://x.com/i.png");
    });

    it("renders an editor attachment (!file syntax) as an absolute link without the prefix", async () => {
      const root = await render(
        "!file[report.pdf](/api/attachments/0b0b6f3e-1a2b-4c3d-8e4f-5a6b7c8d9e0f/download)",
      );
      const a = root.querySelector("a")!;
      expect(a.textContent).toBe("report.pdf");
      expect(a.getAttribute("href")).toBe(
        "https://app.example/api/attachments/0b0b6f3e-1a2b-4c3d-8e4f-5a6b7c8d9e0f/download",
      );
      expect(root.textContent).not.toContain("!file");
    });

    it("renders member mentions as plain @Name text", async () => {
      const root = await render("hi [@Ada](mention://member/u-1) there");
      expect(root.querySelector("a")).toBeNull();
      expect(root.textContent).toBe("hi @Ada there");
    });

    it("renders agent mentions as plain @Name text", async () => {
      const root = await render("ping [@Builder](mention://agent/a-1)");
      expect(root.querySelector("a")).toBeNull();
      expect(root.textContent).toBe("ping @Builder");
    });

    it("renders legacy [@ id label] shortcodes as plain @Name text", async () => {
      const root = await render('hi [@ id="u-1" label="Ada"]');
      expect(root.querySelector("a")).toBeNull();
      expect(root.textContent).toBe("hi @Ada");
    });

    it("renders issue mentions as absolute links labelled with the identifier", async () => {
      const root = await render("see [MUL-12](mention://issue/iss-9)");
      const a = root.querySelector("a")!;
      expect(a.getAttribute("href")).toBe("https://app.example/acme/issues/iss-9");
      expect(a.textContent).toBe("MUL-12");
    });

    it("falls back to the identifier text when an issue link can't be built", async () => {
      for (const c of [bare, { origin: "https://app.example", workspaceSlug: null }]) {
        const root = await render("see [MUL-12](mention://issue/iss-9)", c);
        expect(root.querySelector("a")).toBeNull();
        expect(root.textContent).toBe("see MUL-12");
      }
    });
  });

  it("does not emit script elements or javascript: links from hostile input", async () => {
    const root = await render("<script>alert(1)</script>\n\n[x](javascript:alert(1))");
    expect(root.querySelector("script")).toBeNull();
    const href = root.querySelector("a")?.getAttribute("href") ?? "";
    expect(href.toLowerCase().startsWith("javascript:")).toBe(false);
  });
});

describe("AG patch: Copy (Slack) mrkdwn fallback", () => {
  // Same behaviour table as server/internal/integrations/slack/mrkdwn_test.go.
  it.each([
    ["plain unchanged", "just a reply", "just a reply"],
    ["empty", "", ""],
    ["bold", "**bold**", "*bold*"],
    ["italic star to underscore", "*italic*", "_italic_"],
    ["underscore italic kept", "_italic_", "_italic_"],
    ["bold italic", "***both***", "*_both_*"],
    ["strikethrough", "~~gone~~", "~gone~"],
    ["header to bold", "## Title", "*Title*"],
    ["header strips inner bold", "### **Big**", "*Big*"],
    ["markdown link", "see [docs](https://x.com/a)", "see <https://x.com/a|docs>"],
    ["image link untouched", "![alt](https://x.com/i.png)", "![alt](https://x.com/i.png)"],
    ["inline code protected", "use `**not bold**` here", "use `**not bold**` here"],
    ["existing slack mention untouched", "hi <@U123>", "hi <@U123>"],
    ["ampersand and angles escaped", "a & b < c > d", "a &amp; b &lt; c &gt; d"],
    ["blockquote preserved", "> quoted line", "> quoted line"],
  ])("formatMrkdwn: %s", (_name, input, want) => {
    expect(formatMrkdwn(input)).toBe(want);
  });

  it("protects fenced code while converting text outside it", () => {
    expect(
      formatMrkdwn("before\n```\n**stars** and [x](y) stay literal\n```\nafter **bold**"),
    ).toBe("before\n```\n**stars** and [x](y) stay literal\n```\nafter *bold*");
  });

  it("converts a link nested inside bold", () => {
    expect(formatMrkdwn("**see [docs](https://x.com)**")).toBe("*see <https://x.com|docs>*");
  });

  it("does not double-escape already-escaped entities", () => {
    expect(formatMrkdwn("a &amp; b")).toBe("a &amp; b");
  });

  it("keeps nested list markers and indentation", () => {
    expect(formatMrkdwn("- a\n  - a1\n- b")).toBe("- a\n  - a1\n- b");
  });

  describe("slackPlainText", () => {
    it("writes member and agent mentions as their label", () => {
      expect(
        slackPlainText("hi [@Ada](mention://member/u-1) and [@Bot](mention://agent/a-1)", ctx),
      ).toBe("hi @Ada and @Bot");
    });

    it("writes an editor attachment (!file syntax) as a Slack link without the prefix", () => {
      expect(
        slackPlainText(
          "!file[report.pdf](/api/attachments/0b0b6f3e-1a2b-4c3d-8e4f-5a6b7c8d9e0f/download)",
          ctx,
        ),
      ).toBe(
        "<https://app.example/api/attachments/0b0b6f3e-1a2b-4c3d-8e4f-5a6b7c8d9e0f/download|report.pdf>",
      );
    });

    it("writes legacy mention shortcodes as their label", () => {
      expect(slackPlainText('hi [@ id="u-1" label="Ada"]', ctx)).toBe("hi @Ada");
    });

    it("writes an issue mention as a Slack link to the absolute issue URL", () => {
      expect(slackPlainText("see [MUL-12](mention://issue/iss-9)", ctx)).toBe(
        "see <https://app.example/acme/issues/iss-9|MUL-12>",
      );
    });

    it("writes the bare identifier for an issue mention when no URL can be built", () => {
      expect(slackPlainText("see [MUL-12](mention://issue/iss-9)", bare)).toBe("see MUL-12");
    });

    it("absolutises relative and attachment links", () => {
      expect(
        slackPlainText("[a](/acme/projects/p) [f](/api/attachments/x/download)", ctx),
      ).toBe(
        "<https://app.example/acme/projects/p|a> <https://app.example/api/attachments/x/download|f>",
      );
    });

    it("leaves relative links alone when the origin is unknown", () => {
      expect(slackPlainText("[a](/acme/projects/p)", bare)).toBe("</acme/projects/p|a>");
    });

    it("doesn't absolutise a protocol-relative link or an image", () => {
      expect(slackPlainText("[a](//cdn.example/x)", ctx)).toBe("<//cdn.example/x|a>");
      expect(slackPlainText("![i](/uploads/i.png)", ctx)).toBe("![i](/uploads/i.png)");
    });

    it("keeps formatting, headings and quotes in the mrkdwn output", () => {
      expect(slackPlainText("## Plan\n**bold** and ~~gone~~\n> quote", ctx)).toBe(
        "*Plan*\n*bold* and ~gone~\n> quote",
      );
    });
  });
});
